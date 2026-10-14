package turtle

import (
	"cmp"
	"math/bits"
	"unsafe"
)

// Recycle only exclusively owned batches and node-buffer runs. Leaf arrays
// can be shared by capacity-limited sibling slices and are never returned here.
// Two buffers per size class bound the number of retained scratch records.
const recordPoolClasses = 19

type recordPool[K cmp.Ordered, V any] struct {
	buffers     [recordPoolClasses][2][]record[K, V]
	clearValues bool
}

func (p *recordPool[K, V]) take(size int) []record[K, V] {
	if size == 0 {
		return nil
	}
	class := bits.Len(uint(size - 1))
	if class >= len(p.buffers) {
		return make([]record[K, V], 0, size)
	}
	out := p.buffers[class][0]
	p.buffers[class][0] = p.buffers[class][1]
	p.buffers[class][1] = nil
	if out == nil {
		out = make([]record[K, V], 0, 1<<class)
	}
	return out[:0]
}
func (p *recordPool[K, V]) give(run []record[K, V]) {
	capacity := cap(run)
	if p.clearValues {
		clear(run[:capacity])
	}
	if capacity == 0 || capacity&(capacity-1) != 0 {
		return
	}
	// Keep large generic values from inflating the pool's byte budget. Across
	// all size classes and two buffers each, retained payload stays below 32 MiB.
	if uint64(capacity) > (8<<20)/uint64(unsafe.Sizeof(record[K, V]{})) {
		return
	}
	class := bits.Len(uint(capacity - 1))
	if class >= len(p.buffers) {
		return
	}
	for i := range p.buffers[class] {
		if p.buffers[class][i] == nil {
			p.buffers[class][i] = run[:0]
			return
		}
	}
}
