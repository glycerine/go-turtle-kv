package turtle

import (
	"cmp"
	"iter"
)

// MutableEntry is a key/value pair visited by ScanMut or AllMut. Delete removes
// the visited key using the traversal's exclusive access. Key and Value are
// copies with Go assignment semantics; changing these fields does not update
// the map or change which key Delete removes.
//
// An entry must not be copied, shared with another goroutine, or used to delete
// after its callback or loop body returns. Call methods only on the entry, not
// on the traversed Map, whose lock is already held.
type MutableEntry[K cmp.Ordered, V any] struct {
	Key     K
	Value   V
	key     K
	scope   *mutationScope[K, V]
	deleted bool
}

type mutationScope[K cmp.Ordered, V any] struct {
	owner   *Map[K, V]
	current *MutableEntry[K, V]
}

// Delete removes this entry's original key. Repeated calls within the same
// callback are harmless. Calling Delete outside the entry's scope panics.
func (e *MutableEntry[K, V]) Delete() {
	if e.scope == nil || e.scope.current != e {
		panic("turtle: MutableEntry.Delete outside traversal callback")
	}
	if e.deleted {
		return
	}
	var zero V
	e.scope.owner.write(e.key, zero, true)
	e.deleted = true
}

func (scope *mutationScope[K, V]) visit(k K, v V, visit func(*MutableEntry[K, V]) bool) bool {
	entry := &MutableEntry[K, V]{Key: k, Value: v, key: k, scope: scope}
	scope.current = entry
	defer func() { scope.current = nil }()
	return visit(entry)
}

// ScanMut visits up to limit live entries with keys >= start, in key order,
// holding an exclusive lock unless NoLocks is set. Returning false stops after
// the current entry. It returns the callback count; limit <= 0 visits nothing.
// The callback may delete its entry with entry.Delete() and mutate referenced
// value data. It must not call methods on this Map or retain entries for deletion.
func (s *Map[K, V]) ScanMut(start K, limit int, visit func(*MutableEntry[K, V]) bool) int {
	if !s.opts.NoLocks {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return s.visitMut(start, true, limit, visit)
}

// AllMut iterates over every live entry in key order, including negative keys
// and NaNs. Each iteration holds an exclusive lock unless NoLocks is set;
// constructing the iterator does not acquire the lock or take a snapshot.
// The loop body may call entry.Delete() but must not call methods on this Map.
func (s *Map[K, V]) AllMut() iter.Seq[*MutableEntry[K, V]] {
	return func(yield func(*MutableEntry[K, V]) bool) {
		if !s.opts.NoLocks {
			s.mu.Lock()
			defer s.mu.Unlock()
		}
		var zero K
		s.visitMut(zero, false, int(^uint(0)>>1), yield)
	}
}

func (s *Map[K, V]) visitMut(start K, seek bool, limit int, visit func(*MutableEntry[K, V]) bool) int {
	scope := &mutationScope[K, V]{owner: s}
	defer func() { scope.owner = nil }()
	return s.scanMut(start, seek, limit, func(k K, v V) bool { return scope.visit(k, v, visit) })
}

// scanMut snapshots a bounded batch before invoking callbacks. A callback may
// change the tree, including flushing or splitting it, without invalidating an
// active scan cursor. Each new batch seeks strictly past the last visited key.
// The caller holds exclusive access for the entire operation.
func (s *Map[K, V]) scanMut(start K, seek bool, limit int, visit func(K, V) bool) int {
	if limit <= 0 {
		return 0
	}
	type pair struct {
		key   K
		value V
	}
	const batchSize = 128
	batch := make([]pair, 0, min(batchSize, limit))
	count := 0
	resume := false
	for count < limit {
		want := min(batchSize, limit-count)
		s.scan(start, seek, want+1, func(k K, v V) bool {
			if resume && cmp.Compare(k, start) == 0 {
				return true
			}
			batch = append(batch, pair{k, v})
			return len(batch) < want
		})
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			count++
			if !visit(item.key, item.value) {
				return count
			}
		}
		start = batch[len(batch)-1].key
		seek, resume = true, true
		// Values can reference large objects. Keep only this batch's payload alive.
		clear(batch)
		batch = batch[:0]
	}
	return count
}
