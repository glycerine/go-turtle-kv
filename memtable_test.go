package turtle

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestMemTableSortedDelta(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 1024, LeafSize: 23, Fanout: 4})
	model := map[string]string{}
	for i := 0; i < 71; i++ {
		key := fmt.Sprintf("common-prefix-%03d", i)
		s.Put(key, "initial")
		model[key] = "initial"
	}
	checkModel(t, s, model)
	for round := 0; round < 40; round++ {
		for j := 0; j < 32; j++ {
			key := fmt.Sprintf("common-prefix-%03d", (j*17+round)%101)
			if j%5 == 0 {
				s.Delete(key)
				delete(model, key)
			} else {
				value := fmt.Sprintf("%d/%d", round, j)
				s.Put(key, value)
				model[key] = value
			}
		}
		checkModel(t, s, model)
		if len(s.mem.dirty) > 32 {
			t.Fatal("incorporated delta was not reused")
		}
	}
	for i := 0; i < 600; i++ {
		s.Put("common-prefix-000", fmt.Sprint(i))
	}
	model["common-prefix-000"] = "599"
	if len(s.mem.dirty) != 0 || s.mem.sorted.Load() != nil {
		t.Fatal("large delta must fall back to a full sort")
	}
	checkModel(t, s, model)

	checkModel(t, s, model)
	s.Flush()
	checkModel(t, s, model)
}

func TestConcurrentReadersMergeMemTableDelta(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 128})
	model := map[string]string{}
	for i := 0; i < 65; i++ {
		key := fmt.Sprint(i)
		s.Put(key, "old")
		model[key] = "old"
	}
	checkModel(t, s, model)
	for i := 0; i < 32; i++ {
		key := fmt.Sprint(i)
		s.Put(key, "new")
		model[key] = "new"
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				checkModel(t, s, model)
			}
		}()
	}
	wg.Wait()
}

func TestMemTableShortScanDeltaAccumulation(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 512})
	model := map[string]string{}
	for i := 0; i < 80; i++ {
		key := fmt.Sprintf("%03d", i)
		s.Put(key, "old")
		model[key] = "old"
	}
	s.Scan("", 7, func(_, _ string) bool { return true })
	for round := 0; round < 40; round++ {
		for j := 0; j < 8; j++ {
			key := fmt.Sprintf("%03d", (round*8+j)%240)
			if j%4 == 0 {
				s.Delete(key)
				delete(model, key)
			} else {
				value := fmt.Sprintf("%d/%d", round, j)
				s.Put(key, value)
				model[key] = value
			}
		}
		keys := make([]string, 0, len(model))
		for key := range model {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		start := fmt.Sprintf("%03d", round)
		first, _ := slices.BinarySearch(keys, start)
		want := keys[first:min(first+7, len(keys))]
		var got []string
		s.Scan(start, 7, func(k, v string) bool {
			if model[string(k)] != string(v) {
				t.Fatal("stale delta value")
			}
			got = append(got, string(k))
			return true
		})
		if !slices.Equal(got, want) {
			t.Fatalf("delta scan: got %v want %v", got, want)
		}
		if len(s.mem.sorted.Load().delta) > 128 {
			t.Fatal("lazy delta grew beyond bound")
		}
	}
	if len(s.mem.sorted.Load().delta) == 0 {
		t.Fatal("expected unmaterialized delta")
	}
	s.Scan("", 512, func(_, _ string) bool { return true })
	if len(s.mem.sorted.Load().delta) != 0 {
		t.Fatal("long scan must materialize delta")
	}
	checkModel(t, s, model)
}

func FuzzMemTableScanCache(f *testing.F) {
	f.Add([]byte{1, 2, 3, 34, 35, 36, 128, 1, 2, 255, 64, 65, 66, 1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			data = data[:4096]
		}
		s := NewMap[string, string](Options{MemTableSize: 128, LeafSize: 7, Fanout: 4})
		model := map[string]string{}
		for i, b := range data {
			key := string([]byte{b & 31})
			if b&128 != 0 {
				s.Delete(key)
				delete(model, key)
			} else {
				v := fmt.Sprint(i)
				s.Put(key, v)
				model[key] = v
			}
			if i%3 == 0 {
				start := string([]byte{(b >> 3) & 31})
				limit := int(b%17) + 1
				if b&64 != 0 {
					limit = 256
				}
				keys := make([]string, 0, len(model))
				for k := range model {
					if k >= start {
						keys = append(keys, k)
					}
				}
				slices.Sort(keys)
				keys = keys[:min(len(keys), limit)]
				var got []string
				s.Scan(start, limit, func(k, v string) bool {
					if model[string(k)] != string(v) {
						t.Fatal("stale cached value")
					}
					got = append(got, string(k))
					return true
				})
				if !slices.Equal(got, keys) {
					t.Fatalf("cached scan got %v want %v", got, keys)
				}
			}
			if i%97 == 0 {

			}
		}
		checkModel(t, s, model)
		s.Flush()
		checkModel(t, s, model)
	})
}

func TestLargeMemTableAdaptiveScanDelta(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 32768})
	model := map[string]string{}
	for i := 0; i < 8192; i++ {
		key := fmt.Sprintf("%05d", i)
		s.Put(key, "initial")
		model[key] = "initial"
	}
	maxDelta := 0
	for round := 0; round < 22; round++ {
		for j := 0; j < 32; j++ {
			key := fmt.Sprintf("%05d", round*32+j)
			value := fmt.Sprint(round)
			if j%11 == 0 {
				s.Delete(key)
				delete(model, key)
			} else {
				s.Put(key, value)
				model[key] = value
			}
		}
		want := make([]string, 0, len(model))
		for key := range model {
			want = append(want, key)
		}
		slices.Sort(want)
		count := 0
		if n := s.Scan("", 1000, func(k, v string) bool {
			if string(k) != want[count] || string(v) != model[string(k)] {
				t.Fatal("large-table delta lost ordering or newest version")
			}
			count++
			return true
		}); n != 1000 {
			t.Fatalf("large-table scan returned %d", n)
		}
		delta := len(s.mem.sorted.Load().delta)
		if delta > 512 {
			t.Fatal("large-table pending delta exceeded bound")
		}
		maxDelta = max(maxDelta, delta)
	}
	if maxDelta != 512 {
		t.Fatalf("fixture did not exercise maximum delta: %d", maxDelta)
	}
	for i := 0; i < 32; i++ {
		key := fmt.Sprintf("%05d", i*197)
		s.Put(key, "concurrent")
		model[key] = "concurrent"
	}
	// Mix ordinary scans with Len's forced materialization, so readers may
	// race to publish different valid representations of the same snapshot.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkModel(t, s, model)
		}()
	}
	wg.Wait()
	s.Scan("", 1<<20, func(_, _ string) bool { return true })
	if len(s.mem.sorted.Load().delta) != 0 {
		t.Fatal("full scan did not materialize large-table delta")
	}
}
