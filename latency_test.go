package turtle

import (
	"github.com/glycerine/go-turtle-kv/internal/benchdata"
	"github.com/glycerine/go-turtle-kv/internal/insdict"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Clock every Put, retaining all flushes and one in 64 ordinary writes.
func TestUpdateLatency(t *testing.T) {
	text := os.Getenv("TURTLE_LATENCY_UPDATES")
	if text == "" {
		t.Skip("set TURTLE_LATENCY_UPDATES")
	}
	updates, err := strconv.Atoi(text)
	if err != nil || updates < 1 {
		t.Fatal("invalid update count")
	}
	n := benchdata.N()
	s := NewMap[uint64, uint64](Options{})
	for i := 0; i < n; i++ {
		s.Put(benchdata.Key(i), uint64(i))
	}
	var ordinary, flushes []int64
	for i := 0; i < updates; i++ {
		key := benchdata.Key(int(insdict.Mix64(uint64(i&65535)+2023) % uint64(n)))
		start := time.Now()
		s.Put(key, uint64(i))
		elapsed := time.Since(start).Nanoseconds()
		if s.mem.Len() == 0 {
			flushes = append(flushes, elapsed)
		} else if i&63 == 0 {
			ordinary = append(ordinary, elapsed)
		}
	}
	for _, sample := range []struct {
		name string
		data []int64
	}{{"ordinary-sampled-1-in-64", ordinary}, {"flush-all", flushes}} {
		if len(sample.data) == 0 {
			t.Logf("%s count=0", sample.name)
			continue
		}
		slices.Sort(sample.data)
		at := func(p int) int64 { return sample.data[(len(sample.data)-1)*p/100] }
		t.Logf("%s count=%d p50-ns=%d p99-ns=%d max-ns=%d", sample.name, len(sample.data), at(50), at(99), at(100))
	}
}
