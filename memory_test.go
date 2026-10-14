package turtle

import (
	"github.com/glycerine/go-turtle-kv/internal/benchdata"
	"github.com/glycerine/go-turtle-kv/internal/insdict"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// Explicit sustained-write diagnostic. Samples retain normal pool/cache storage.
func TestUpdateMemoryFootprint(t *testing.T) {
	text := os.Getenv("TURTLE_MEMORY_UPDATES")
	if text == "" {
		t.Skip("set TURTLE_MEMORY_UPDATES")
	}
	updates, err := strconv.Atoi(text)
	if err != nil || updates < 8 {
		t.Fatal("invalid update count")
	}
	n := benchdata.N()
	s := NewMap[uint64, uint64](Options{})
	for i := 0; i < n; i++ {
		s.Put(benchdata.Key(i), uint64(i))
	}
	last := make([]uint64, n)
	for i := range last {
		last[i] = uint64(i)
	}
	for phase := 0; phase < 8; phase++ {
		for i := phase * updates / 8; i < (phase+1)*updates/8; i++ {
			rank := int(insdict.Mix64(uint64(i&65535)+2023) % uint64(n))
			last[rank] = uint64(i)
			s.Put(benchdata.Key(rank), uint64(i))
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("updates=%d heap=%d", (phase+1)*updates/8, m.HeapAlloc)
	}
	for i, want := range last {
		got, ok := s.Get(benchdata.Key(i))
		if !ok || got != want {
			t.Fatalf("bad final value at %d", i)
		}
	}
	runtime.KeepAlive(s)
}
