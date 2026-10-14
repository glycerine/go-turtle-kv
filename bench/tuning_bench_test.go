package bench

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glycerine/go-turtle-kv"
)

func BenchmarkTuning(b *testing.B) {
	n := benchLoadSize()
	keys := make([]uint64, n)
	for i := range keys {
		keys[i] = benchKey(i) | 1
	}
	for _, pair := range [][2]int{{256, 256}, {1024, 1024}, {4096, 4096}, {16384, 4096}, {16384, 16384}, {16384, 65536}, {32768, 65536}} {
		opts := turtle.Options{NoLocks: true, MemTableSize: pair[0], LeafSize: pair[1], Fanout: 16}
		b.Run(fmt.Sprintf("M%d/L%d/FreshPut", pair[0], pair[1]), func(b *testing.B) {
			layout := benchPointLayout{"Turtle", func() benchUint64Points { return &benchTurtle{store: turtle.NewMap[uint64, uint64](opts)} }}
			benchmarkFreshPut(b, layout, n, keys)
		})
	}
}

// Compare existing tuning knobs using exactly the comparison table's point
// loops, including fresh growth from N to 2N. No read fixture is explicitly
// flushed or compacted. Use -benchtime=Nx for a complete fresh-write cycle.
func BenchmarkReadTuning(b *testing.B) {
	n := benchLoadSize()
	for _, cfg := range readTuningConfigs() {
		opts := turtle.Options{NoLocks: true, MemTableSize: cfg.m, LeafSize: cfg.l, Fanout: cfg.f}
		b.Run(fmt.Sprintf("M%d/L%d/F%d", cfg.m, cfg.l, cfg.f), func(b *testing.B) {
			layout := benchPointLayout{"Turtle", func() benchUint64Points { return &benchTurtle{store: turtle.NewMap[uint64, uint64](opts)} }}
			for _, bc := range pinComparisonCases(pointCasesForLayouts(n, []benchPointLayout{layout})) {
				name := strings.TrimPrefix(bc.name, "Turtle/")
				if name == "Update" {
					continue
				}
				b.Run(name, bc.run)
			}
			b.Run("GetHitAfterFresh", func(b *testing.B) {
				pinComparisonCases([]comparisonBenchmark{{"after-fresh", func(b *testing.B) {
					idx := loadComparisonPoints(layout, n)
					defer closeComparison(idx)
					for i := 0; i < n; i++ {
						idx.Put(benchKey(i)|1, uint64(n+i))
					}
					ops := benchTrace(n, 8192, 1)
					var sum uint64
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						sum += idx.Get(ops[i%len(ops)].key | uint64(i&1))
					}
					b.StopTimer()
					benchSink = sum
				}}})[0].run(b)
			})
		})
	}
}

type readTuningConfig struct{ m, l, f int }

func readTuningConfigs() []readTuningConfig {
	return []readTuningConfig{
		{16384, 65536, 16}, // Current defaults.
		{256, 256, 4}, {256, 256, 16},
		{1024, 1024, 4}, {1024, 1024, 16}, {1024, 1024, 64},
		{4096, 4096, 4}, {4096, 4096, 16},
		{16384, 1024, 4}, {16384, 1024, 16},
		{16384, 4096, 4}, {16384, 4096, 16},
		{16384, 16384, 16}, {16384, 16384, 64},
		{4096, 65536, 16}, {65536, 1024, 16},
		{65536, 256, 256}, {16384, 1048576, 16}, {131072, 1024, 16},
		{131072, 65536, 16}, {262144, 65536, 16},
	}
}
