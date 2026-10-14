package turtle

import (
	"cmp"
	"math/bits"
	"reflect"
	"slices"
	"sync"
)

// Options controls locking and the write-memory trade-off. Sizes count records.
// Zero fields select defaults: locking enabled, MemTableSize 16384,
// LeafSize 65536, Fanout 16.
type Options struct {
	// NoLocks disables internal locking. The caller must then synchronize
	// writers against all operations; concurrent read-only operations are safe.
	NoLocks      bool
	MemTableSize int
	LeafSize     int
	Fanout       int
}

// The low sequence bit marks a tombstone, avoiding a separate flag and padding.
// uint64 keys/values produce 24-byte, pointer-free records.
type record[K cmp.Ordered, V any] struct {
	key   K
	value V
	seq   uint64
}

func (r record[K, V]) deleted() bool { return r.seq&1 != 0 }

type node[K cmp.Ordered, V any] struct {
	items    []record[K, V]
	children []uint32
	pivots   []K
	levels   [][]record[K, V]
}

// Map stores native ordered keys and values. The zero value is ready to use.
// Operations are protected by an RWMutex unless Options.NoLocks is set.
// Scan and All hold read locks throughout callbacks; ScanMut and AllMut hold
// write locks and allow deletion through MutableEntry.Delete. Callbacks must
// not call methods on this map, since nested locking can deadlock.
// Values have Go assignment semantics; referenced data is shared, not copied.
// NaNs compare equal and sort before other keys, following cmp.Compare.
// A Map must not be copied after first use.
type Map[K cmp.Ordered, V any] struct {
	mu          sync.RWMutex
	opts        Options
	mem         *memTable[K, V]
	nodes       []node[K, V]
	root        uint32
	seq         uint64
	sortScratch []record[K, V]
	recordPool  recordPool[K, V]
	orderBits   func(K) uint64
	references  bool
}

// NewMap constructs a map; zero option fields select the defaults.
func NewMap[K cmp.Ordered, V any](opts Options) *Map[K, V] {
	s := &Map[K, V]{opts: opts}
	s.init()
	return s
}
func (s *Map[K, V]) init() {
	if s.mem != nil {
		return
	}
	if s.opts.MemTableSize == 0 {
		s.opts.MemTableSize = 16384
	}
	if s.opts.LeafSize == 0 {
		s.opts.LeafSize = 65536
	}
	if s.opts.Fanout == 0 {
		s.opts.Fanout = 16
	}
	if s.opts.MemTableSize < 1 || s.opts.LeafSize < 2 || s.opts.Fanout < 3 {
		panic("turtle: invalid options")
	}
	s.references = typeHasPointers(reflect.TypeFor[K]()) || typeHasPointers(reflect.TypeFor[V]())
	s.orderBits = keyOrderBits[K]()
	s.recordPool.clearValues = s.references
	s.mem = newMemTable[K, V](s.opts.MemTableSize, s.references)
	s.nodes = append(s.nodes, node[K, V]{})
}
func (s *Map[K, V]) compare(a, b record[K, V]) int { return cmp.Compare(a.key, b.key) }
func (s *Map[K, V]) lower(items []record[K, V], key K) int {
	lo, hi := 0, len(items)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if cmp.Less(items[mid].key, key) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
func (s *Map[K, V]) child(n *node[K, V], key K) int {
	lo, hi := 0, len(n.pivots)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if cmp.Compare(key, n.pivots[mid]) >= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Put inserts or replaces a value, storing keys and values by assignment.
func (s *Map[K, V]) Put(key K, value V) {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	s.write(key, value, false)
}

// Delete records a deletion, including when the key is absent.
func (s *Map[K, V]) Delete(key K) {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	var zero V
	s.write(key, zero, true)
}
func (s *Map[K, V]) write(key K, value V, deleted bool) {
	s.init()
	if s.seq >= ^uint64(0)-1 {
		panic("turtle: sequence exhausted")
	}
	s.seq += 2
	r := record[K, V]{key: key, value: value, seq: s.seq}
	if deleted {
		r.seq |= 1
	}
	s.mem.set(r)
	if s.mem.Len() >= s.opts.MemTableSize {
		s.flush()
	}
}
func (s *Map[K, V]) find(key K) (record[K, V], bool) {
	if s.mem == nil {
		return record[K, V]{}, false
	}
	if r, ok := s.mem.get(key); ok {
		return r, true
	}
	id := s.root
	for {
		n := &s.nodes[id]
		for _, run := range n.levels {
			pos := s.lower(run, key)
			if pos < len(run) && cmp.Compare(run[pos].key, key) == 0 {
				return run[pos], true
			}
		}
		if len(n.children) == 0 {
			pos := s.lower(n.items, key)
			if pos < len(n.items) && cmp.Compare(n.items[pos].key, key) == 0 {
				return n.items[pos], true
			}
			return record[K, V]{}, false
		}
		id = n.children[s.child(n, key)]
	}
}

// Get returns the value and whether the key is present.
func (s *Map[K, V]) Get(key K) (V, bool) {
	if !s.opts.NoLocks {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	r, ok := s.find(key)
	if !ok || r.deleted() {
		var zero V
		return zero, false
	}
	return r.value, true
}

// Flush applies the MemTable as a sorted batch. Interior-node buffers remain
// buffered; Flush does not force every update down to a leaf.
func (s *Map[K, V]) Flush() {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	s.flush()
}

func (s *Map[K, V]) flush() {
	if s.mem == nil || s.mem.Len() == 0 {
		return
	}
	batch := s.recordPool.take(s.mem.Len())
	for _, entry := range s.mem.entries {
		batch = append(batch, entry)
	}
	s.sortBatch(batch)
	s.installRoot(s.apply(s.root, batch))
	s.mem.reset()
}

func (s *Map[K, V]) addNode(n node[K, V]) uint32 {
	if uint64(len(s.nodes)) >= 1<<32 {
		panic("turtle: node[K,V] index exhausted")
	}
	id := uint32(len(s.nodes))
	s.nodes = append(s.nodes, n)
	return id
}

// merge performs sorted last-write-wins compaction. Sequence numbers also make
// this safe when batches have been partitioned or moved during node splits.
func (s *Map[K, V]) merge(a, b []record[K, V], bottom bool) []record[K, V] {
	return s.mergeInto(s.recordPool.take(len(a)+len(b)), a, b, bottom)
}

// mergeInto only reads records and writes its exclusively owned output.
// Concurrent scan readers use it with their own arrays, without the writer pool.
func (s *Map[K, V]) mergeInto(out, a, b []record[K, V], bottom bool) []record[K, V] {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		var r record[K, V]
		c := s.compare(a[i], b[j])
		if c < 0 {
			r = a[i]
			i++
		} else if c > 0 {
			r = b[j]
			j++
		} else {
			r = a[i]
			if b[j].seq > r.seq {
				r = b[j]
			}
			i++
			j++
		}
		if !bottom || !r.deleted() {
			out = append(out, r)
		}
	}
	if !bottom {
		// In particular, copying the first child-flush run needs no per-record
		// merge work. Output ownership remains exclusive even for an empty side.
		out = append(out, a[i:]...)
		out = append(out, b[j:]...)
	} else {
		for _, r := range a[i:] {
			if !r.deleted() {
				out = append(out, r)
			}
		}
		for _, r := range b[j:] {
			if !r.deleted() {
				out = append(out, r)
			}
		}
	}
	return out
}

// mergeLeaf updates existing keys in place. Record slices never escape the
// store, and leaf siblings have disjoint capacity-limited slices. If a batch
// introduces a new key, merge handles the remaining structural work; any
// preceding replacements are already valid last-write-wins updates.
func (s *Map[K, V]) mergeLeaf(a, b []record[K, V]) []record[K, V] {
	deleted := false
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		c := s.compare(a[i], b[j])
		if c < 0 {
			i++
			continue
		}
		if c > 0 {
			return s.merge(a, b, true)
		}
		if b[j].seq > a[i].seq {
			a[i] = b[j]
			deleted = deleted || b[j].deleted()
		}
		i++
		j++
	}
	if j < len(b) {
		return s.merge(a, b, true)
	}
	if !deleted {
		return a
	}
	// Tombstones are discarded only at the bottom of the tree.
	out := a[:0]
	for _, r := range a {
		if !r.deleted() {
			out = append(out, r)
		}
	}
	if s.references {
		clear(a[len(out):])
	}
	return out
}

type branch[K cmp.Ordered, V any] struct {
	id  uint32
	min K
}

func (s *Map[K, V]) installRoot(parts []branch[K, V]) {
	for len(parts) > 1 {
		next := make([]branch[K, V], 0, (len(parts)+s.opts.Fanout-1)/s.opts.Fanout)
		count := (len(parts) + s.opts.Fanout - 1) / s.opts.Fanout
		for group := 0; group < count; group++ {
			start, end := group*len(parts)/count, (group+1)*len(parts)/count
			n := node[K, V]{}
			for i, p := range parts[start:end] {
				n.children = append(n.children, p.id)
				if i != 0 {
					n.pivots = append(n.pivots, p.min)
				}
			}
			next = append(next, branch[K, V]{s.addNode(n), parts[start].min})
		}
		parts = next
	}
	s.root = parts[0].id
}

// apply returns a replacement child list; its first element reuses id. No node
// pointers are held over recursive calls, which may grow and move s.nodes.
func (s *Map[K, V]) apply(id uint32, batch []record[K, V]) []branch[K, V] {
	n := s.nodes[id]
	if len(n.children) == 0 {
		items := s.mergeLeaf(n.items, batch)
		s.recordPool.give(batch)
		if len(items) <= s.opts.LeafSize {
			s.nodes[id].items = items
			var first K
			if len(items) != 0 {
				first = items[0].key
			}
			return []branch[K, V]{{id, first}}
		}
		// Balance leaf splits instead of leaving a nearly empty right sibling.
		count := (len(items) + s.opts.LeafSize - 1) / s.opts.LeafSize
		width := (len(items) + count - 1) / count
		parts := make([]branch[K, V], 0, count)
		for start := 0; start < len(items); start += width {
			end := min(start+width, len(items))
			leaf := node[K, V]{items: items[start:end:end]}
			child := id
			if start == 0 {
				s.nodes[id] = leaf
			} else {
				child = s.addNode(leaf)
			}
			parts = append(parts, branch[K, V]{child, items[start].key})
		}
		return parts
	}
	// Binary carry over at most ceil(log2(child count)) levels. A run is
	// contiguous in this in-memory port; the C++ tree divides runs into segments.
	// An overflowing top level is merged in place and then drained below.
	levelCount := bits.Len(uint(len(n.children) - 1))
	level := 0
	for ; ; level++ {
		for len(n.levels) <= level {
			n.levels = append(n.levels, nil)
		}
		if len(n.levels[level]) == 0 {
			n.levels[level] = batch
			break
		}
		oldRun, oldBatch := n.levels[level], batch
		batch = s.merge(oldRun, oldBatch, false)
		s.recordPool.give(oldRun)
		s.recordPool.give(oldBatch)
		n.levels[level] = nil
		if level == levelCount-1 {
			n.levels[level] = batch
			break
		}
	}
	s.nodes[id] = n
	// Bound total buffered records by approximately (fanout-1) leaf batches,
	// flushing the child with the largest backlog as in the TurtleTree policy.
	for s.pending(id) >= s.opts.LeafSize*max(1, len(s.nodes[id].children)-1) {
		s.flushChild(id, s.busiest(id))
	}
	if len(s.nodes[id].children) <= s.opts.Fanout {
		return []branch[K, V]{{id, *new(K)}}
	}
	// Drain before a structural split so no buffered versions straddle siblings.
	for s.pending(id) != 0 {
		s.flushChild(id, s.busiest(id))
	}
	n = s.nodes[id]
	count := (len(n.children) + s.opts.Fanout - 1) / s.opts.Fanout
	parts := make([]branch[K, V], 0, count)
	for group := 0; group < count; group++ {
		start, end := group*len(n.children)/count, (group+1)*len(n.children)/count
		part := node[K, V]{children: slices.Clone(n.children[start:end]), pivots: slices.Clone(n.pivots[start : end-1])}
		child := id
		if start == 0 {
			s.nodes[id] = part
		} else {
			child = s.addNode(part)
		}
		var first K
		if start != 0 {
			first = n.pivots[start-1]
		}
		parts = append(parts, branch[K, V]{child, first})
	}
	return parts
}

func (s *Map[K, V]) pending(id uint32) int {
	count := 0
	for _, run := range s.nodes[id].levels {
		count += len(run)
	}
	return count
}
func (s *Map[K, V]) busiest(id uint32) int {
	n := &s.nodes[id]
	counts := make([]int, len(n.children))
	for _, run := range n.levels {
		prev := 0
		for i, pivot := range n.pivots {
			end := s.lower(run, pivot)
			counts[i] += end - prev
			prev = end
		}
		counts[len(counts)-1] += len(run) - prev
	}
	best := 0
	for i := range counts {
		if counts[i] > counts[best] {
			best = i
		}
	}
	return best
}
func (s *Map[K, V]) flushChild(id uint32, pos int) {
	n := s.nodes[id]
	var batch []record[K, V]
	for level, run := range n.levels {
		start, end := 0, len(run)
		if pos != 0 {
			start = s.lower(run, n.pivots[pos-1])
		}
		if pos < len(n.pivots) {
			end = s.lower(run, n.pivots[pos])
		}
		if start == end {
			continue
		}
		oldBatch := batch
		batch = s.merge(oldBatch, run[start:end], false)
		s.recordPool.give(oldBatch)
		// merge owns its output, so it is now safe to compact this private run
		// in place. No record slices escape the store, and writers are exclusive.
		// C++ avoids moving survivors using per-pivot flush cursors instead.
		remaining := len(run) - (end - start)
		if remaining == 0 {
			s.recordPool.give(run)
			n.levels[level] = nil
		} else {
			copy(run[start:], run[end:])
			if s.references {
				clear(run[remaining:])
			}
			n.levels[level] = run[:remaining]
		}
	}
	parts := s.apply(n.children[pos], batch)
	if len(parts) == 1 {
		// apply reuses the original child id when there is no split.
		s.nodes[id] = n
		return
	}
	children := make([]uint32, 0, len(n.children)+len(parts)-1)
	children = append(children, n.children[:pos]...)
	for _, p := range parts {
		children = append(children, p.id)
	}
	children = append(children, n.children[pos+1:]...)
	pivots := make([]K, 0, len(children)-1)
	pivots = append(pivots, n.pivots[:pos]...)
	for _, p := range parts[1:] {
		pivots = append(pivots, p.min)
	}
	pivots = append(pivots, n.pivots[pos:]...)
	n.children, n.pivots = children, pivots
	s.nodes[id] = n
}

// Reset drops all entries and releases retained storage. Configuration is preserved.
func (s *Map[K, V]) Reset() {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	s.replaceData(&Map[K, V]{})
	s.init()
}

// replaceData preserves the destination mutex and immutable locking option.
// The caller holds exclusive access; copying a live mutex would lose waiters.
func (s *Map[K, V]) replaceData(next *Map[K, V]) {
	s.mem = next.mem
	s.nodes = next.nodes
	s.root = next.root
	s.seq = next.seq
	s.sortScratch = next.sortScratch
	s.recordPool = next.recordPool
	s.orderBits = next.orderBits
	s.references = next.references
}
