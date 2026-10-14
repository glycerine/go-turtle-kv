package bench

import (
	"math"
	"testing"

	"github.com/glycerine/go-turtle-kv"
)

// The benchmark adapter only discards Get's presence bit and Scan's count,
// matching the Tidwall adapter. Keys and values stay native uint64 throughout.
type benchTurtle struct{ store *turtle.Map[uint64, uint64] }

func newBenchTurtle() *benchTurtle {
	return &benchTurtle{store: turtle.NewMap[uint64, uint64](turtleOptions())}
}
func (m *benchTurtle) Get(k uint64) uint64 { v, _ := m.store.Get(k); return v }
func (m *benchTurtle) Put(k, v uint64)     { m.store.Put(k, v) }
func (m *benchTurtle) Len() int            { return m.store.Len() }
func (m *benchTurtle) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	m.store.Scan(start, length, visit)
}

func TestTurtleUint64Values(t *testing.T) {
	m := newBenchTurtle()
	keys := []uint64{0, 1, 255, 256, 1 << 32, 1 << 63, math.MaxUint64}
	for _, k := range keys {
		m.Put(k, ^k)
	}
	for _, k := range keys {
		if got := m.Get(k); got != ^k {
			t.Fatalf("Get(%d) = %d, want %d", k, got, ^k)
		}
	}
	pos := 0
	m.Scan(0, len(keys), func(k, v uint64) bool {
		if pos >= len(keys) || k != keys[pos] || v != ^k {
			t.Fatalf("incorrect numeric scan order/value at position %d: %d/%d", pos, k, v)
		}
		pos++
		return true
	})
	if pos != len(keys) {
		t.Fatalf("scan returned %d keys, want %d", pos, len(keys))
	}
}
