package turtle

import (
	"fmt"
	"github.com/glycerine/go-turtle-kv/internal/insdict"
	"math/rand"
	"slices"
	"testing"
)

func checkModel(t testing.TB, s *Map[string, string], model map[string]string) {
	t.Helper()
	checkTree(t, s)
	keys := make([]string, 0, len(model))
	for key, want := range model {
		keys = append(keys, key)
		got, ok := s.Get(key)
		if !ok || string(got) != want {
			t.Fatalf("Get(%q) = %q,%v; want %q", key, got, ok, want)
		}
	}
	slices.Sort(keys)
	var got []string
	s.Scan("", len(model)+1, func(k, v string) bool {
		if want, ok := model[string(k)]; !ok || want != string(v) {
			t.Fatalf("scan(%q) = %q, want %q present %v", k, v, want, ok)
		}
		got = append(got, string(k))
		return true
	})
	if !slices.Equal(keys, got) {
		t.Fatalf("scan keys differ: got %d want %d", len(got), len(keys))
	}
	if n := s.Len(); n != len(model) {
		t.Fatalf("Len = %d want %d", n, len(model))
	}
}

func TestRandomModel(t *testing.T) {
	for _, opts := range []Options{
		{NoLocks: true, MemTableSize: 7, LeafSize: 5, Fanout: 4},
		{MemTableSize: 1, LeafSize: 2, Fanout: 3},
		{MemTableSize: 7, LeafSize: 5, Fanout: 4},
		{MemTableSize: 64, LeafSize: 16, Fanout: 8},
		{},
	} {
		t.Run(fmt.Sprint(opts), func(t *testing.T) {
			s := NewMap[string, string](opts)
			model := map[string]string{}
			rng := rand.New(rand.NewSource(2023))
			for i := 0; i < 20000; i++ {
				key := string([]byte{byte(rng.Intn(64)), byte(rng.Intn(32))})
				switch rng.Intn(10) {
				case 0, 1:
					s.Delete(key)
					delete(model, key)
				default:
					value := fmt.Sprint(i)
					s.Put(key, value)
					model[key] = value
				}
				if i%397 == 0 {
					checkModel(t, s, model)
				}
				if i%613 == 0 {
					s.Flush()
					checkModel(t, s, model)
				}
				if i%991 == 0 {
					for j := 0; j < 30; j++ {
						k := string([]byte{byte(rng.Intn(64)), byte(rng.Intn(32))})
						v, ok := s.Get(k)
						want, found := model[k]
						if ok != found || string(v) != want {
							t.Fatalf("random Get(%q)=%q,%v want %q,%v", k, v, ok, want, found)
						}
					}
				}
			}
			checkModel(t, s, model)
			s.Compact()
			checkModel(t, s, model)
			for k := range model {
				s.Delete(k)
			}
			s.Flush()
			checkModel(t, s, map[string]string{})
			s.Compact()
			checkModel(t, s, map[string]string{})
		})
	}
}

func TestReadAllocations(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 8, LeafSize: 4, Fanout: 4})
	for i := 0; i < 100; i++ {
		s.Put(fmt.Sprintf("%016d", i), string([]byte{byte(i)}))
	}
	treeKey, memKey, missing := fmt.Sprintf("%016d", 20), fmt.Sprintf("%016d", 98), fmt.Sprintf("%016d", 200)
	if n := testing.AllocsPerRun(1000, func() { s.Get(treeKey); s.Get(memKey); s.Get(missing) }); n != 0 {
		t.Fatal("Get allocations", n)
	}
}

func FuzzStore(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 2, 1, 0, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		s := NewMap[string, string](Options{MemTableSize: 3, LeafSize: 4, Fanout: 3})
		model := map[string]string{}
		for i, b := range data {
			key := string([]byte{b % 32})
			if b&128 != 0 {
				s.Delete(key)
				delete(model, key)
			} else {
				v := fmt.Sprint(i)
				s.Put(key, v)
				model[key] = v
			}
		}
		checkModel(t, s, model)
		s.Flush()
		checkModel(t, s, model)
	})
}

func TestPrefixAndLongKeys(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 11, LeafSize: 7, Fanout: 5})
	model := map[string]string{}
	for i := 0; i < 1200; i++ {
		// Exercise long strings sharing a common prefix.
		key := fmt.Sprintf("same-prefix-%05d", (i*311)%1200)
		value := fmt.Sprint(i)
		s.Put(key, value)
		model[key] = value
	}
	for _, key := range []string{"", "\x00", "\x00\x00", "\x00\x00\x01", "same-pre", "same-pre\x00", "same-prefix-"} {
		s.Put(key, key)
		model[key] = key
	}
	checkModel(t, s, model)
	keys := make([]string, 0, len(model))
	for k := range model {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, start := range []string{"", "\x00", "same-prefix-00000", "same-prefix-00319a", "same-prefix-00600", "same-prefix-09999", "z"} {
		for _, limit := range []int{0, 1, 17, 10000} {
			pos, _ := slices.BinarySearch(keys, start)
			want := keys[pos : pos+min(limit, len(keys)-pos)]
			var got []string
			s.Scan(start, limit, func(k, v string) bool { got = append(got, string(k)); return true })
			if !slices.Equal(got, want) {
				t.Fatalf("scan start %q limit %d: got %q want %q", start, limit, got, want)
			}
		}
	}
}

func TestConcurrentReaders(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 17, LeafSize: 13, Fanout: 5})
	for i := 0; i < 2000; i++ {
		s.Put(fmt.Sprint(i), fmt.Sprint(i))
	}
	done := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				k := fmt.Sprint(j)
				v, ok := s.Get(k)
				if !ok || string(v) != k {
					done <- false
					return
				}
				if s.Scan(k, 10, func(k, v string) bool { return k == v }) != 10 {
					done <- false
					return
				}
			}
			done <- true
		}()
	}
	for i := 0; i < 4; i++ {
		if !<-done {
			t.Fatal("concurrent read mismatch")
		}
	}
}

func checkTree(t testing.TB, s *Map[string, string]) {
	t.Helper()
	seen := map[uint32]bool{}
	var walk func(uint32, *string, *string)
	walk = func(id uint32, low, high *string) {
		if seen[id] {
			t.Fatalf("node %d reached twice", id)
		}
		seen[id] = true
		n := &s.nodes[id]
		checkRun := func(run []record[string, string]) {
			for i, r := range run {
				if (low != nil && r.key < *low) || (high != nil && r.key >= *high) {
					t.Fatalf("node %d key out of bounds", id)
				}
				if i > 0 && s.compare(run[i-1], r) >= 0 {
					t.Fatalf("node %d unsorted run", id)
				}
			}
		}
		checkRun(n.items)
		for _, run := range n.levels {
			checkRun(run)
		}
		if len(n.children) == 0 {
			if len(n.items) > s.opts.LeafSize {
				t.Fatal("leaf too large")
			}
			return
		}
		if len(n.children) < 2 || len(n.children) > s.opts.Fanout || len(n.pivots) != len(n.children)-1 {
			t.Fatal("invalid node")
		}
		if !slices.IsSorted(n.pivots) {
			t.Fatal("unsorted pivots")
		}
		for i, child := range n.children {
			lo, hi := low, high
			if i > 0 {
				lo = &n.pivots[i-1]
			}
			if i < len(n.pivots) {
				hi = &n.pivots[i]
			}
			walk(child, lo, hi)
		}
	}
	if s.mem != nil {
		walk(s.root, nil, nil)
	}
}

func TestLeafOverwriteThenInsert(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 100, LeafSize: 20, Fanout: 4})
	model := map[string]string{"a": "old", "c": "old", "e": "old"}
	for _, k := range []string{"a", "c", "e"} {
		s.Put(k, "old")
	}
	s.Flush()
	// The first two entries match existing keys; the third requires allocating
	// a larger merge output after the fast path has changed the old leaf.
	s.Put("a", "new")
	s.Delete("c")
	s.Put("d", "inserted")
	s.Flush()
	model["a"] = "new"
	delete(model, "c")
	model["d"] = "inserted"
	checkModel(t, s, model)
	s.Put("e", "last")
	s.Flush()
	model["e"] = "last"
	checkModel(t, s, model)
}

func TestLargeBatchFullKeyOrdering(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 4096, LeafSize: 127, Fanout: 7})
	model := map[string]string{}
	// Mix equal eight-byte prefixes, variable-length zero prefixes, and
	// different full-width prefixes within the same comparison-sorted batch.
	for i := 0; i < 2200; i++ {
		var key string
		switch i % 3 {
		case 0:
			key = fmt.Sprintf("same-pre%08d", (i*137)%2200)
		case 1:
			key = string([]byte{byte(i >> 8), byte(i), 0})
		case 2:
			key = fmt.Sprintf("%08x/tail", (i*311)%2200)
		}
		value := fmt.Sprint(i)
		s.Put(key, value)
		model[key] = value
	}
	s.Put("", "empty")
	model[""] = "empty"
	s.Put("\x00", "zero")
	model["\x00"] = "zero"
	s.Flush()
	checkModel(t, s, model)
	// Add another batch sharing an even longer common prefix.
	for i := 0; i < 900; i++ {
		key := fmt.Sprintf("same-pre-more-%08d", i)
		s.Put(key, "fallback")
		model[key] = "fallback"
	}
	s.Flush()
	checkModel(t, s, model)
}

func TestDefaultBatchFlushes(t *testing.T) {
	s := NewMap[string, string](Options{})
	const count = 150000 // exceeds the default leaf and exercises node buffering
	model := make(map[string]string, count)
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%08d", (i*7919)%count)
		value := fmt.Sprint(i)
		s.Put(key, value)
		model[key] = value
	}
	for i := 0; i < 5000; i++ {
		key := fmt.Sprintf("%08d", (i*311)%count)
		if i%3 == 0 {
			s.Delete(key)
			delete(model, key)
		} else {
			s.Put(key, "new")
			model[key] = "new"
		}
	}
	checkModel(t, s, model)
	s.Flush()
	checkModel(t, s, model)
}

func TestMemTableHashCollisions(t *testing.T) {
	s := NewMap[string, string](Options{MemTableSize: 13, LeafSize: 7, Fanout: 4})
	// Every string key has the same hash. Flush/reset must retain this hasher.
	s.mem.index = insdict.NewDictFuncSize[string, uint32](func(string) uint64 { return 0 }, s.opts.MemTableSize)
	model := map[string]string{}
	for i := 0; i < 1200; i++ {
		key := fmt.Sprintf("collision-%03d", (i*97)%211)
		switch i % 7 {
		case 0:
			s.Delete(key)
			delete(model, key)
		default:
			value := fmt.Sprint(i)
			s.Put(key, value)
			model[key] = value
		}
		if i%31 == 0 {
			checkModel(t, s, model)
		}
	}
	checkModel(t, s, model)
	s.Flush()
	checkModel(t, s, model)
	if _, ok := s.Get("collision-missing"); ok {
		t.Fatal("hash collision caused a false hit")
	}
}

// A scan must keep traversing later leaves when no ancestor buffer contributes
// an active record stream. This also exercises continuing across tree levels.
func TestScanAcrossUnbufferedLeaves(t *testing.T) {
	for _, long := range []bool{false, true} {
		s := NewMap[string, string](Options{MemTableSize: 64, LeafSize: 4, Fanout: 4})
		model := map[string]string{}
		for i := 0; i < 64; i++ {
			key := string([]byte{byte(i)})
			if long {
				key = fmt.Sprintf("shared-prefix-%03d", i)
			}
			s.Put(key, string([]byte{byte(i)}))
			model[key] = string([]byte{byte(i)})
		}
		for i := range s.nodes {
			if s.pending(uint32(i)) != 0 {
				t.Fatal("fixture must contain no buffered runs")
			}
		}
		checkModel(t, s, model)
		count := 0
		got := s.Scan("", 64, func(_, _ string) bool { count++; return count < 4 })
		if got != 4 || count != 4 {
			t.Fatalf("early callback across leaves: %d/%d", got, count)
		}
	}
}

// Keep three versions of each updated key in separate scan streams, with
// adjacent tombstones and stream ends. Paired heap advances must still consider
// the third version and honor both query limits and early callback stops.
func TestScanOverlappingVersionStreams(t *testing.T) {
	for _, long := range []bool{false, true} {
		keyFor := func(i int) string {
			if long {
				return fmt.Sprintf("shared-prefix-%03d", i)
			}
			return string([]byte{byte(i)})
		}
		s := NewMap[string, string](Options{MemTableSize: 64, LeafSize: 8, Fanout: 8})
		model := map[string]string{}
		for i := 0; i < 64; i++ {
			key := keyFor(i)
			s.Put(key, "initial")
			model[string(key)] = "initial"
		}
		s.opts.MemTableSize = 8
		for round := 0; round < 3; round++ {
			for i := 0; i < 8; i++ {
				key := keyFor(i)
				if round == 2 && i%3 == 0 {
					s.Delete(key)
					delete(model, string(key))
				} else {
					value := fmt.Sprint(round)
					s.Put(key, value)
					model[string(key)] = value
				}
			}
		}
		checkModel(t, s, model)
		for start := 0; start <= 64; start++ {
			var want []string
			for i := start; i < 64; i++ {
				if _, ok := model[string(keyFor(i))]; ok {
					want = append(want, string(keyFor(i)))
				}
			}
			for _, limit := range []int{1, 7, 32, 128} {
				for _, stop := range []int{3, 128} {
					var got []string
					n := s.Scan(keyFor(start), limit, func(k, v string) bool {
						if model[string(k)] != string(v) {
							t.Fatal("scan returned an obsolete version")
						}
						got = append(got, string(k))
						return len(got) < stop
					})
					if n != min(len(want), limit, stop) || !slices.Equal(got, want[:min(len(want), limit, stop)]) {
						t.Fatalf("scan start=%d limit=%d stop=%d returned %d keys", start, limit, stop, n)
					}
				}
			}
		}
	}
}
