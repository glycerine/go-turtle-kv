package bench

import (
	"fmt"
	"os"
	"slices"
	"testing"

	turtle "github.com/glycerine/go-turtle-kv"
)

// Large, opt-in model check for the exact study populations and all tuning
// combinations. Verify before/after growth and after forcing the pending batch
// into the tree; benchmarks must not mistake missing records for fast reads.
func TestReadTuningConfigurations(t *testing.T) {
	if os.Getenv("TURTLE_TUNING_CHECK") == "" {
		t.Skip("set TURTLE_TUNING_CHECK=1 for the tuning matrix model check")
	}
	n := benchLoadSize()
	for _, cfg := range readTuningConfigs() {
		t.Run(fmt.Sprintf("M%d/L%d/F%d", cfg.m, cfg.l, cfg.f), func(t *testing.T) {
			s := turtle.NewMap[uint64, uint64](turtle.Options{NoLocks: true, MemTableSize: cfg.m, LeafSize: cfg.l, Fanout: cfg.f})
			model := make(map[uint64]uint64, 2*n)
			check := func() {
				t.Helper()
				for k, want := range model {
					if got, ok := s.Get(k); !ok || got != want {
						t.Fatalf("Get(%d)=%d,%v want %d", k, got, ok, want)
					}
				}
				keys := make([]uint64, 0, len(model))
				for k := range model {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				pos := 0
				for k, v := range s.All() {
					if pos >= len(keys) || k != keys[pos] || v != model[k] {
						t.Fatalf("invalid scan entry %d: %d/%d", pos, k, v)
					}
					pos++
				}
				if pos != len(keys) {
					t.Fatalf("scan count=%d want %d", pos, len(keys))
				}
			}
			for i := 0; i < n; i++ {
				k := benchKey(i)
				s.Put(k, uint64(i))
				model[k] = uint64(i)
			}
			check()
			for i := 0; i < n; i++ {
				k := benchKey(i) | 1
				s.Put(k, uint64(n+i))
				model[k] = uint64(n + i)
			}
			check()
			s.Flush()
			check()
		})
	}
}
