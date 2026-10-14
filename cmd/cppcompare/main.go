// cppcompare matches cppbench/main.cpp's inputs and fixed operation counts.
package main

import (
	"flag"
	"fmt"
	"slices"
	"time"

	turtle "github.com/glycerine/go-turtle-kv"
	"github.com/glycerine/go-turtle-kv/internal/benchdata"
	"github.com/glycerine/go-turtle-kv/internal/insdict"
)

func main() {
	n := flag.Int("n", 65536, "initial keys")
	count := flag.Int("count", 262144, "operations per point phase")
	scans := flag.Int("scans", 4096, "queries per scan length")
	traceBits := flag.Uint("trace-bits", 20, "log2 trace length")
	noLocks := flag.Bool("no-locks", false, "disable map locking")
	flag.Parse()
	if *n < 2 || *count < 1 || *scans < 1 || *traceBits > 24 {
		panic("invalid arguments")
	}
	const value uint64 = 0x0001020304050607
	const update uint64 = 0x0101020304050607
	keys := make([]uint64, *n)
	sorted := make([]uint64, *n)
	trace := make([]int, 1<<*traceBits)
	var fingerprint uint64
	for i := range trace {
		trace[i] = int(insdict.Mix64(uint64(i)+2023) % uint64(*n))
		fingerprint = fingerprint*0x9e3779b97f4a7c15 + uint64(trace[i])
	}
	for i := range keys {
		keys[i] = benchdata.Key(i)
	}
	copy(sorted, keys)
	slices.Sort(sorted)
	fmt.Printf("Go config N=%d count=%d scans=%d value_bytes=8 M=16384 L=65536 F=16 NoLocks=%v trace_bits=%d\nTrace checksum=%x\n", *n, *count, *scans, *noLocks, *traceBits, fingerprint)
	newMap := func() *turtle.Map[uint64, uint64] {
		return turtle.NewMap[uint64, uint64](turtle.Options{NoLocks: *noLocks})
	}
	elapsed := func(start time.Time, count int) float64 {
		return float64(time.Since(start).Nanoseconds()) / float64(count)
	}
	m := newMap()
	start := time.Now()
	for _, k := range keys {
		m.Put(k, value)
	}
	fmt.Printf("Go/TreeInsert ns/key=%g\n", elapsed(start, *n))
	for _, k := range keys {
		v, ok := m.Get(k)
		if !ok || v != value {
			panic("load validation")
		}
	}
	var sum uint64
	start = time.Now()
	for i := 0; i < *count; i++ {
		v, ok := m.Get(keys[trace[i%len(trace)]])
		if !ok {
			panic("hit")
		}
		sum += v & 255
	}
	fmt.Printf("Go/TreeHit ns/op=%g checksum=%d\n", elapsed(start, *count), sum)
	start = time.Now()
	for i := 0; i < *count; i++ {
		if _, ok := m.Get(keys[trace[i%len(trace)]] | 1); ok {
			panic("miss")
		}
	}
	fmt.Printf("Go/TreeMiss ns/op=%g\n", elapsed(start, *count))
	start = time.Now()
	for i := 0; i < *count; i++ {
		m.Put(keys[trace[i%len(trace)]], update)
	}
	fmt.Printf("Go/TreeUpdate ns/op=%g\n", elapsed(start, *count))
	updated := make(map[uint64]bool)
	for i := 0; i < min(*count, len(trace)); i++ {
		updated[keys[trace[i]]] = true
	}
	for _, k := range keys {
		want := value
		if updated[k] {
			want = update
		}
		v, ok := m.Get(k)
		if !ok || v != want {
			panic("update validation")
		}
	}
	scanMap := m
	m = newMap()
	for _, k := range keys {
		m.Put(k, value)
	}
	sum = 0
	start = time.Now()
	for i := 0; i < *count; i++ {
		k := keys[trace[i%len(trace)]]
		if i&1 == 0 {
			m.Put(k, value)
		} else {
			v, ok := m.Get(k)
			if !ok {
				panic("mixed")
			}
			sum += v & 255
		}
	}
	fmt.Printf("Go/MemTreeMixed50 ns/op=%g checksum=%d\n", elapsed(start, *count), sum)
	for _, k := range keys {
		v, ok := m.Get(k)
		if !ok || v != value {
			panic("mixed validation")
		}
	}
	fmt.Printf("MemTable/tree full validation passed N=%d\n", *n)
	start = time.Now()
	for _, k := range keys {
		m.Put(k|1, value)
	}
	fmt.Printf("Go/FreshPut ns/key=%g\n", elapsed(start, *n))
	for _, k := range keys {
		for _, key := range []uint64{k, k | 1} {
			v, ok := m.Get(key)
			if !ok || v != value {
				panic("growth validation")
			}
		}
	}
	fmt.Printf("Fresh growth validation passed N=%d\n", *n)

	verify := func(k uint64, limit int) {
		pos, _ := slices.BinarySearch(sorted, k)
		wantCount := min(limit, len(sorted)-pos)
		visited := scanMap.Scan(k, limit, func(k, v uint64) bool {
			if pos >= len(sorted) || k != sorted[pos] {
				panic("scan key")
			}
			want := value
			if updated[k] {
				want = update
			}
			if v != want {
				panic("scan value")
			}
			pos++
			return true
		})
		if visited != wantCount {
			panic("scan count")
		}
	}
	verify(sorted[0], *n)
	fmt.Printf("Full scan validation passed N=%d\n", *n)
	for _, length := range []int{1, 100, 1000} {
		for i := 0; i < 32; i++ {
			verify(keys[trace[i]], length)
		}
		verify(sorted[len(sorted)-1], length)
		expected := 0
		for i := 0; i < *scans; i++ {
			pos, _ := slices.BinarySearch(sorted, keys[trace[i%len(trace)]])
			expected += min(length, len(sorted)-pos)
		}
		sum = 0
		visited := 0
		start = time.Now()
		for i := 0; i < *scans; i++ {
			visited += scanMap.Scan(keys[trace[i%len(trace)]], length, func(k, v uint64) bool { sum += v & 255; return true })
		}
		ns := elapsed(start, *scans)
		if visited != expected {
			panic("timed scan count")
		}
		fmt.Printf("Go/Scan length=%d ns/scan=%g visited=%d checksum=%d\n", length, ns, visited, sum)
	}
}
