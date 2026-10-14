package turtle

import (
	"cmp"
	"github.com/glycerine/go-turtle-kv/internal/insdict"
	"slices"
	"sync/atomic"
)

// The index maps native keys directly to one-based positions in dense records.
// For pointer-free K and V, neither array contains pointers to scan.
type memTable[K cmp.Ordered, V any] struct {
	index      *insdict.Dict[K, uint32]
	entries    []record[K, V]
	sorted     atomic.Pointer[sortedMemTable[K, V]]
	dirty      []record[K, V]
	lastSeq    uint64
	references bool
}

func newMemTable[K cmp.Ordered, V any](capacity int, references bool) *memTable[K, V] {
	return &memTable[K, V]{index: insdict.NewDictFuncSize[K, uint32](keyHash[K](), capacity), entries: make([]record[K, V], 0, capacity), references: references}
}
func (m *memTable[K, V]) Len() int { return len(m.entries) }
func (m *memTable[K, V]) get(key K) (record[K, V], bool) {
	i := m.index.Get(key)
	if i == 0 {
		return record[K, V]{}, false
	}
	return m.entries[i-1], true
}
func (m *memTable[K, V]) set(r record[K, V]) {
	if cached := m.sorted.Load(); cached != nil {
		if len(m.dirty) != 0 && m.dirty[len(m.dirty)-1].seq <= cached.seq {
			if m.references {
				clear(m.dirty)
			}
			m.dirty = m.dirty[:0]
		}
		if len(m.dirty) < 512 {
			m.dirty = append(m.dirty, r)
		} else {
			m.invalidateSorted()
		}
	}
	m.lastSeq = r.seq
	if i := m.index.Get(r.key); i != 0 {
		m.entries[i-1] = r
		return
	}
	if uint64(len(m.entries)) >= 1<<32-1 {
		panic("turtle: MemTable index exhausted")
	}
	m.entries = append(m.entries, r)
	m.index.Put(r.key, uint32(len(m.entries)))
}
func (m *memTable[K, V]) invalidateSorted() {
	if m.sorted.Load() != nil {
		m.sorted.Store(nil)
	}
	if m.references {
		clear(m.dirty)
	}
	m.dirty = m.dirty[:0]
}
func (m *memTable[K, V]) reset() {
	m.invalidateSorted()
	m.lastSeq = 0
	if m.references {
		clear(m.entries)
	}
	m.entries = m.entries[:0]
	m.index.ResetReuse()
}

// Scan readers may publish a sorted cache concurrently. Writes are externally
// exclusive and retain a bounded delta after a published snapshot. A reader
// sorts only that delta, retaining a small separate run for short scans or
// merging into an independently owned snapshot for longer scans. The sequence
// boundary lets the next writer reuse the incorporated write log.
type sortedMemTable[K cmp.Ordered, V any] struct {
	records []record[K, V]
	delta   []record[K, V] // short scans merge this small run lazily
	seq     uint64
}

func (m *memTable[K, V]) sortedRecords(s *Map[K, V], limit int) (base, pending []record[K, V]) {
	if len(m.entries) == 0 {
		return nil, nil
	}
	old := m.sorted.Load()
	// Copying a large base on every small write/scan cycle costs more than
	// retaining a sparse additional stream. Bound that stream to 512 records,
	// and materialize when the query is large relative to the pending table.
	materialize := limit >= 256 && limit/2 >= len(m.entries)
	deltaLimit := min(512, max(128, len(m.entries)/16))
	if old != nil && old.seq == m.lastSeq && (len(old.delta) == 0 || !materialize) {
		return old.records, old.delta
	}
	var records, updates []record[K, V]
	if old == nil {
		records = make([]record[K, V], len(m.entries))
		for i, entry := range m.entries {
			records[i] = entry
		}
		slices.SortFunc(records, s.compare)
	} else {
		records, updates = old.records, old.delta
		if old.seq != m.lastSeq {
			delta := slices.Clone(m.dirty)
			slices.SortFunc(delta, s.compare)
			unique := delta[:0]
			for _, r := range delta {
				if len(unique) != 0 && s.compare(unique[len(unique)-1], r) == 0 {
					if r.seq > unique[len(unique)-1].seq {
						unique[len(unique)-1] = r
					}
				} else {
					unique = append(unique, r)
				}
			}
			updates = unique
			if m.references {
				clear(delta[len(unique):])
			}
			if len(old.delta) != 0 {
				updates = s.mergeInto(make([]record[K, V], 0, len(old.delta)+len(unique)), old.delta, unique, false)
			}
		}
		// The query-size and bounded-delta limits balance copying the base
		// against additional merge work in the scan.
		if len(updates) > deltaLimit || materialize {
			records = s.mergeInto(make([]record[K, V], 0, len(m.entries)), records, updates, false)
			updates = nil
		}
	}
	cached := &sortedMemTable[K, V]{records: records, delta: updates, seq: m.lastSeq}
	if m.sorted.CompareAndSwap(old, cached) {
		return records, updates
	}
	cached = m.sorted.Load()
	return cached.records, cached.delta
}
