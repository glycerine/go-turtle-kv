// Package benchdata shares deterministic inputs between internal diagnostics
// and the separate comparison-benchmark module.
package benchdata

import (
	"os"
	"strconv"
)

// Same permutation as ~/bufftree/bench: even keys are hits, odd keys misses.
func Key(rank int) uint64 {
	const mask = uint64(1<<63 - 1)
	x := uint64(rank) & mask
	x ^= x >> 30
	x = x * 0x3f58476d1ce4e5b9 & mask
	x ^= x >> 27
	x = x * 0x14d049bb133111eb & mask
	x ^= x >> 31
	return x << 1
}
func N() int {
	if v := os.Getenv("TURTLE_BENCH_N"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 {
			panic("invalid TURTLE_BENCH_N")
		}
		return n
	}
	return 1 << 16
}
