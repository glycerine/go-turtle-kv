package bench

import (
	"github.com/glycerine/go-turtle-kv"
	"github.com/tidwall/btree"
	"testing"
)

type nativeValueWriter[V any] interface{ Put(uint64, V) }
type nativeTidwall[V any] struct{ btree.Map[uint64, V] }

func (m *nativeTidwall[V]) Put(k uint64, v V) { m.Set(k, v) }

// Compare inline value copying with identical key traces and assignment
// semantics. No serialization, byte API, or heap payload adapters are involved.
func BenchmarkValueSizes(b *testing.B) {
	b.Run("Uint64", func(b *testing.B) { benchmarkNativeValues[uint64](b) })
	b.Run("Array100", func(b *testing.B) { benchmarkNativeValues[[100]byte](b) })
	b.Run("Array1024", func(b *testing.B) { benchmarkNativeValues[[1024]byte](b) })
}
func benchmarkNativeValues[V any](b *testing.B) {
	n := benchLoadSize()
	var value V
	for _, layout := range []struct {
		name string
		make func() nativeValueWriter[V]
	}{
		{"Turtle", func() nativeValueWriter[V] { return turtle.NewMap[uint64, V](turtleOptions()) }},
		{"Tidwall", func() nativeValueWriter[V] { return &nativeTidwall[V]{} }},
	} {
		b.Run(layout.name, func(b *testing.B) {
			load := func() nativeValueWriter[V] {
				s := layout.make()
				for i := 0; i < n; i++ {
					s.Put(benchKey(i), value)
				}
				return s
			}
			s := load()
			keys := make([]uint64, n)
			for i := range keys {
				keys[i] = benchKey(i) | 1
			}
			inserted := 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if inserted == n {
					b.StopTimer()
					s = load()
					inserted = 0
					b.StartTimer()
				}
				s.Put(keys[inserted], value)
				inserted++
			}
			b.StopTimer()
		})
	}
}
