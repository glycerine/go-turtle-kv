package turtle

import (
	"cmp"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
)

func TestMutableTimestampCleanup(t *testing.T) {
	for _, noLocks := range []bool{false, true} {
		for _, all := range []bool{false, true} {
			t.Run(fmt.Sprintf("NoLocks=%v/All=%v", noLocks, all), func(t *testing.T) {
				s := NewMap[int, int](Options{NoLocks: noLocks, MemTableSize: 7, LeafSize: 11, Fanout: 3})
				// Includes multiple snapshot batches, leaves, buffered versions, and a
				// pending MemTable. Deletion forces repeated automatic flushes.
				for i := 599; i >= -100; i-- {
					s.Put(i, i)
					if i%5 == 0 {
						s.Put(i, i+1)
						s.Put(i, i)
					}
				}
				visited := 0
				visit := func(e *MutableEntry[int, int]) bool {
					if e.Key != visited-100 || e.Value != e.Key {
						t.Fatalf("entry %d: %d/%d", visited, e.Key, e.Value)
					}
					visited++
					if e.Key >= 450 {
						return false
					}
					e.Delete()
					e.Delete()
					return true
				}
				if all {
					for e := range s.AllMut() {
						if !visit(e) {
							break
						}
					}
				} else {
					if n := s.ScanMut(-100, 1000, visit); n != 551 {
						t.Fatalf("count=%d", n)
					}
				}
				if visited != 551 || s.Len() != 150 {
					t.Fatalf("visited=%d Len=%d", visited, s.Len())
				}
				for i := -100; i < 600; i++ {
					v, ok := s.Get(i)
					if ok != (i >= 450) || ok && v != i {
						t.Fatalf("Get(%d)=%d,%v", i, v, ok)
					}
				}
				for e := range s.AllMut() {
					e.Delete()
				}
				if s.Len() != 0 {
					t.Fatal("AllMut did not delete everything")
				}
			})
		}
	}
}

func TestMutableScanLimits(t *testing.T) {
	var zero Map[int, int]
	if n := zero.ScanMut(0, 10, func(*MutableEntry[int, int]) bool { t.Fatal("empty callback"); return true }); n != 0 {
		t.Fatal(n)
	}
	for range zero.AllMut() {
		t.Fatal("empty iterator")
	}
	s := NewMap[int, int](Options{MemTableSize: 3, LeafSize: 5, Fanout: 3})
	for i := 0; i < 300; i++ {
		s.Put(i, i)
	}
	for _, limit := range []int{-1, 0, 1, 127, 128, 129} {
		seen := 0
		if n := s.ScanMut(10, limit, func(e *MutableEntry[int, int]) bool {
			if e.Key != 10+seen {
				t.Fatalf("key=%d", e.Key)
			}
			seen++
			return true
		}); n != max(0, limit) || seen != n {
			t.Fatalf("limit=%d count=%d seen=%d", limit, n, seen)
		}
	}
	if n := s.ScanMut(999, 10, func(*MutableEntry[int, int]) bool { t.Fatal("past end"); return true }); n != 0 {
		t.Fatal(n)
	}
	if n := s.ScanMut(10, 200, func(e *MutableEntry[int, int]) bool { e.Delete(); return false }); n != 1 {
		t.Fatal(n)
	}
	if _, ok := s.Get(10); ok {
		t.Fatal("deletion on stopping entry lost")
	}
}

func TestMutableEntryScopeAndLocks(t *testing.T) {
	for _, noLocks := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoLocks=%v", noLocks), func(t *testing.T) {
			s := NewMap[int, int](Options{NoLocks: noLocks})
			s.Put(1, 1)
			s.Put(2, 2)
			var saved *MutableEntry[int, int]
			panics := func(f func()) {
				t.Helper()
				defer func() {
					if recover() == nil {
						t.Error("expected panic")
					}
				}()
				f()
			}
			unlocked := func() {
				t.Helper()
				if !s.mu.TryLock() {
					t.Fatal("lock retained")
				}
				s.mu.Unlock()
			}
			check := func(e *MutableEntry[int, int]) bool {
				// Exclusive traversal must exclude readers as well as writers.
				got := s.mu.TryRLock()
				if got {
					s.mu.RUnlock()
				}
				if got != noLocks {
					t.Errorf("read lock available=%v", got)
				}
				if saved != nil {
					panics(saved.Delete)
				}
				copy := *e
				panics(copy.Delete)
				saved = e
				return true
			}
			seq := s.AllMut()
			unlocked()
			seq(check)
			unlocked()
			panics(saved.Delete)
			s.ScanMut(0, 1, func(e *MutableEntry[int, int]) bool {
				// Changing the exposed copy must not delete a different key.
				e.Key = 2
				e.Value = 999
				e.Delete()
				return false
			})
			if _, ok := s.Get(1); ok {
				t.Fatal("original key not deleted")
			}
			if v, ok := s.Get(2); !ok || v != 2 {
				t.Fatal("exposed copy changed stored data")
			}
			for _, run := range []func(){
				func() {
					s.ScanMut(0, 10, func(e *MutableEntry[int, int]) bool { saved = e; e.Delete(); panic("stop") })
				},
				func() {
					s.Put(2, 2)
					for e := range s.AllMut() {
						saved = e
						e.Delete()
						panic("stop")
					}
				},
			} {
				panics(run)
				unlocked()
				panics(saved.Delete)
			}
			if s.Len() != 0 {
				t.Fatal("panic rolled back deletion")
			}
			var empty MutableEntry[int, int]
			panics(empty.Delete)
		})
	}
}

func TestMutableFloatOrder(t *testing.T) {
	s := NewMap[float64, int](Options{MemTableSize: 3, LeafSize: 5, Fanout: 3})
	keys := []float64{math.NaN(), math.Inf(-1)}
	for i := -200; i < 200; i++ {
		keys = append(keys, float64(i))
	}
	keys = append(keys, math.Inf(1))
	for i, k := range keys {
		s.Put(k, i)
	}
	i := 0
	for e := range s.AllMut() {
		if cmp.Compare(e.Key, keys[i]) != 0 || e.Value != i {
			t.Fatalf("entry %d=%v/%d", i, e.Key, e.Value)
		}
		e.Delete()
		i++
	}
	if i != len(keys) || s.Len() != 0 {
		t.Fatalf("visited=%d Len=%d", i, s.Len())
	}
	// A NaN lower bound must include the NaN entry before all numeric keys.
	for _, k := range keys {
		s.Put(k, 1)
	}
	if n := s.ScanMut(math.NaN(), len(keys), func(e *MutableEntry[float64, int]) bool { e.Delete(); return true }); n != len(keys) {
		t.Fatal(n)
	}
}

func TestMutableScanModel(t *testing.T) {
	for _, noLocks := range []bool{false, true} {
		s := NewMap[int, int](Options{NoLocks: noLocks, MemTableSize: 7, LeafSize: 11, Fanout: 3})
		model := map[int]int{}
		rng := rand.New(rand.NewSource(2026))
		for pass := 0; pass < 100; pass++ {
			for i := 0; i < 50; i++ {
				k := rng.Intn(700) - 350
				s.Put(k, pass)
				model[k] = pass
			}
			start, limit := rng.Intn(700)-350, rng.Intn(300)
			var want []int
			for k := -350; k < 350; k++ {
				if _, ok := model[k]; ok && k >= start && len(want) < limit {
					want = append(want, k)
				}
			}
			pos := 0
			n := s.ScanMut(start, limit, func(e *MutableEntry[int, int]) bool {
				if e.Key != want[pos] || e.Value != model[e.Key] {
					t.Fatalf("pass %d entry %d=%d/%d", pass, pos, e.Key, e.Value)
				}
				if rng.Intn(2) == 0 {
					e.Delete()
					delete(model, e.Key)
				}
				pos++
				return true
			})
			if n != len(want) || s.Len() != len(model) {
				t.Fatalf("pass %d count=%d Len=%d", pass, n, s.Len())
			}
			for k, v := range model {
				if got, ok := s.Get(k); !ok || got != v {
					t.Fatalf("Get(%d)=%d,%v", k, got, ok)
				}
			}
		}
	}
}

func TestConcurrentMutableTraversal(t *testing.T) {
	s := NewMap[int, int](Options{MemTableSize: 7, LeafSize: 11, Fanout: 3})
	for k := -600; k < 600; k++ {
		s.Put(k, k)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for e := range s.AllMut() {
			if e.Key >= 0 {
				break
			}
			e.Delete()
		}
	})
	wg.Go(func() { s.ScanMut(0, 300, func(e *MutableEntry[int, int]) bool { e.Delete(); return true }) })
	wg.Go(func() {
		for k := 600; k < 700; k++ {
			s.Put(k, k)
		}
	})
	wg.Go(func() {
		for i := 0; i < 50; i++ {
			s.Get(i)
			s.Len()
			prev := -1000
			for k, v := range s.All() {
				if k <= prev || v != k {
					t.Error("invalid read")
				}
				prev = k
			}
		}
	})
	wg.Wait()
	for k := -600; k < 700; k++ {
		v, ok := s.Get(k)
		if ok != (k >= 300) || ok && v != k {
			t.Fatalf("Get(%d)=%d,%v", k, v, ok)
		}
	}
}

func TestMutableReferencedValues(t *testing.T) {
	s := NewMap[int, *int](Options{})
	value := 10
	s.Put(1, &value)
	s.ScanMut(0, 1, func(e *MutableEntry[int, *int]) bool {
		*e.Value = 20
		return true
	})
	got, ok := s.Get(1)
	if !ok || *got != 20 {
		t.Fatal("referenced value update lost")
	}
	for e := range s.AllMut() {
		*e.Value = 30
	}
	if value != 30 {
		t.Fatal("iterator referenced value update lost")
	}
}
