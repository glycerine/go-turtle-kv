package turtle

import "slices"

// sortBatch uses numeric key order directly, without a cached byte prefix.
// The stable radix passes preserve full typed records. Strings use cmp ordering.
func (s *Map[K, V]) sortBatch(batch []record[K, V]) {
	if len(batch) < 512 || s.orderBits == nil {
		slices.SortFunc(batch, s.compare)
		return
	}
	var varying uint64
	first := s.orderBits(batch[0].key)
	for _, r := range batch {
		varying |= s.orderBits(r.key) ^ first
	}
	if varying == 0 {
		return
	}
	if cap(s.sortScratch) < len(batch) {
		s.sortScratch = make([]record[K, V], len(batch))
	}
	scratch := s.sortScratch[:len(batch)]
	source, dest := batch, scratch
	var counts [2048]int
	passes := 0
	for shift := uint(0); shift < 64; shift += 11 {
		if varying>>shift&2047 == 0 {
			continue
		}
		clear(counts[:])
		for _, r := range source {
			counts[s.orderBits(r.key)>>shift&2047]++
		}
		total := 0
		for i, count := range counts {
			counts[i] = total
			total += count
		}
		for _, r := range source {
			bucket := s.orderBits(r.key) >> shift & 2047
			dest[counts[bucket]] = r
			counts[bucket]++
		}
		source, dest = dest, source
		passes++
	}
	if passes&1 != 0 {
		copy(batch, source)
	}
	if s.references {
		clear(scratch)
	}
}
