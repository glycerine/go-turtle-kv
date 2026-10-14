package turtle

import (
	"cmp"
	"iter"
)

// A cursor is either a sorted run (run != nil) or an unopened subtree, keyed
// by its inclusive lower bound. Expanding subtrees lazily keeps a short scan
// from touching every leaf to the right of its starting key.
type cursor[K cmp.Ordered, V any] struct {
	run    []record[K, V]
	pos    int
	id     uint32
	parent uint32
	next   uint32
	bound  K
	floor  bool // unbounded left edge for full traversals
}
type scanHeapEntry[K cmp.Ordered] struct {
	key   K
	id    uint32
	floor bool
}

// Scan visits up to limit live entries ordered by key, starting at key >= start.
// Returning false stops after the current entry. limit <= 0 visits nothing.
// Scan returns the callback count. With locking enabled, the read lock spans
// the entire traversal. The callback must not call methods on this map.
func (s *Map[K, V]) Scan(start K, limit int, visit func(K, V) bool) int {
	if !s.opts.NoLocks {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	return s.scan(start, true, limit, visit)
}
func (s *Map[K, V]) scan(start K, seek bool, limit int, visit func(K, V) bool) int {
	if s.mem == nil || limit <= 0 {
		return 0
	}
	// Heap swaps move integer IDs rather than full cursor/slice metadata.
	// Recycle slots as streams finish so full traversals use bounded scratch.
	var slotBuf [16]cursor[K, V]
	var heapBuf [16]scanHeapEntry[K]
	cursors := slotBuf[:0]
	heap := heapBuf[:0]
	freeHead := ^uint32(0)
	less := func(a, b scanHeapEntry[K]) bool {
		if a.floor != b.floor {
			return a.floor
		}
		comparison := cmp.Compare(a.key, b.key)
		return comparison < 0 || (comparison == 0 && cursors[a.id].run == nil && cursors[b.id].run != nil)
	}
	down := func(pos int) {
		for {
			child := pos*2 + 1
			if child >= len(heap) {
				return
			}
			if child+1 < len(heap) && less(heap[child+1], heap[child]) {
				child++
			}
			if !less(heap[child], heap[pos]) {
				return
			}
			heap[pos], heap[child] = heap[child], heap[pos]
			pos = child
		}
	}
	push := func(c cursor[K, V]) {
		var id uint32
		if freeHead != ^uint32(0) {
			id = freeHead
			freeHead = cursors[id].next
			cursors[id] = c
		} else {
			id = uint32(len(cursors))
			cursors = append(cursors, c)
		}
		head := c.bound
		if c.run != nil {
			head = c.run[c.pos].key
		}
		heap = append(heap, scanHeapEntry[K]{key: head, id: id, floor: c.floor})
		for pos := len(heap) - 1; pos > 0; {
			parent := (pos - 1) / 2
			if !less(heap[pos], heap[parent]) {
				break
			}
			heap[pos], heap[parent] = heap[parent], heap[pos]
			pos = parent
		}
	}
	pop := func() cursor[K, V] {
		id := heap[0].id
		c := cursors[id]
		cursors[id] = cursor[K, V]{next: freeHead}
		freeHead = id
		heap[0] = heap[len(heap)-1]
		heap = heap[:len(heap)-1]
		if len(heap) > 0 {
			down(0)
		}
		return c
	}
	add := func(run []record[K, V]) {
		pos := 0
		if seek {
			pos = s.lower(run, start)
		}
		if pos < len(run) {
			push(cursor[K, V]{run: run, pos: pos})
		}
	}
	base, pending := s.mem.sortedRecords(s, limit)
	add(base)
	add(pending)
	push(cursor[K, V]{id: s.root, bound: start, floor: !seek})
	expand := func() {
		for len(heap) > 0 && cursors[heap[0].id].run == nil {
			c := pop()
			// Keep one unopened sibling per ancestor instead of pushing every
			// sibling into the heap. Expanding this child reveals its successor.
			if c.next != 0 {
				parent := &s.nodes[c.parent]
				bound := parent.pivots[c.next-1]
				next := c.next + 1
				if int(next) == len(parent.children) {
					next = 0
				}
				push(cursor[K, V]{id: parent.children[c.next], parent: c.parent, next: next, bound: bound})
			}
			n := &s.nodes[c.id]
			for _, run := range n.levels {
				add(run)
			}
			if len(n.children) == 0 {
				add(n.items)
				continue
			}
			first := 0
			if seek {
				first = s.child(n, start)
			}
			bound := c.bound
			next := uint32(first + 1)
			if int(next) == len(n.children) {
				next = 0
			}
			push(cursor[K, V]{id: n.children[first], parent: c.id, next: next, bound: bound, floor: c.floor})
		}
	}
	take := func() record[K, V] {
		c := &cursors[heap[0].id]
		r := c.run[c.pos]
		c.pos++

		if c.pos == len(c.run) {
			pop()
		} else {
			heap[0].key = c.run[c.pos].key
			down(0)
		}
		return r
	}
	count := 0
	for count < limit {
		expand()
		if len(heap) == 0 {
			break
		}
		// The smallest competing stream is one of the root's children. Emit
		// a consecutive span directly while it precedes that stream's lower bound.
		// Equality stays on the version-merging path below, including tombstones.
		competitor := -1
		if len(heap) > 1 {
			competitor = 1
		}
		if len(heap) > 2 && less(heap[2], heap[1]) {
			competitor = 2
		}
		c := &cursors[heap[0].id]
		first := c.pos
		for c.pos < len(c.run) && count < limit {
			r := c.run[c.pos]
			if competitor >= 0 && (heap[competitor].floor || cmp.Compare(r.key, heap[competitor].key) >= 0) {
				break
			}
			c.pos++

			if r.deleted() {
				continue
			}
			count++
			if !visit(r.key, r.value) {
				return count
			}
		}
		if c.pos != first {
			if c.pos == len(c.run) {
				pop()
			} else {
				heap[0].key = c.run[c.pos].key
				down(0)
			}
			continue
		}
		var r record[K, V]
		paired := false
		if competitor >= 0 {
			other := &cursors[heap[competitor].id]
			r = c.run[c.pos]
			if other.run != nil && c.pos+1 < len(c.run) && other.pos+1 < len(other.run) {
				version := other.run[other.pos]
				if cmp.Compare(r.key, version.key) == 0 {
					// Repair each affected subtree once instead of promoting the
					// same key to the root and taking it again. Stream exhaustion
					// uses the general path, which also recycles cursor slots.
					if version.seq > r.seq {
						r = version
					}
					c.pos++
					other.pos++

					heap[0].key = c.run[c.pos].key
					heap[competitor].key = other.run[other.pos].key
					down(competitor)
					down(0)
					paired = true
				}
			}
		}
		if !paired {
			r = take()
		}
		for {
			expand()
			if len(heap) == 0 || cmp.Compare(r.key, heap[0].key) != 0 {
				break
			}
			other := take()
			if other.seq > r.seq {
				r = other
			}
		}
		if r.deleted() {
			continue
		}
		count++
		if !visit(r.key, r.value) {
			break
		}
	}
	return count
}

// Len counts live entries; writes do not perform an extra tree lookup for counting.
func (s *Map[K, V]) Len() int {
	if !s.opts.NoLocks {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	var zero K
	return s.scan(zero, false, int(^uint(0)>>1), func(K, V) bool { return true })
}

// All iterates over every live entry in key order, including negative keys.
// With locking enabled, each iteration holds a read lock until it ends, including
// its yield calls. The loop body must not call methods on this map.
func (s *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		if !s.opts.NoLocks {
			s.mu.RLock()
			defer s.mu.RUnlock()
		}
		var zero K
		s.scan(zero, false, int(^uint(0)>>1), yield)
	}
}

// Compact rebuilds live data to discard obsolete versions and sparse leaves.
// It temporarily needs both maps in memory. Values retain assignment semantics.
func (s *Map[K, V]) Compact() {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	if s.mem == nil {
		return
	}
	next := NewMap[K, V](s.opts)
	var zero K
	s.scan(zero, false, int(^uint(0)>>1), func(k K, v V) bool {
		next.write(k, v, false)
		return true
	})
	next.flush()
	s.replaceData(next)
}
