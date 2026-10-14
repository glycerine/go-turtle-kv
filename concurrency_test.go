package turtle

import (
	"fmt"
	"sync"
	"testing"
)

// Small buffers force writer flushes and structural changes while readers run.
func TestConcurrentReadersAndWriters(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprintf("zero=%v", zero), func(t *testing.T) {
			var zeroMap Map[int, int]
			s := &zeroMap
			if !zero {
				s = NewMap[int, int](Options{MemTableSize: 7, LeafSize: 11, Fanout: 3})
			}
			const writers, count = 4, 200
			start := make(chan struct{})
			var wg sync.WaitGroup
			for w := 0; w < writers; w++ {
				wg.Go(func() {
					<-start
					for i := 0; i < count; i++ {
						k := w*count + i
						s.Put(k, k)
						if i%3 == 0 {
							s.Delete(k)
							s.Put(k, k)
						}
						if i%31 == 0 {
							s.Flush()
						}
						if i%73 == 0 {
							s.Compact()
						}
					}
				})
			}
			for r := 0; r < 4; r++ {
				wg.Go(func() {
					<-start
					for i := 0; i < 100; i++ {
						if v, ok := s.Get(i); ok && v != i {
							t.Errorf("Get(%d)=%d", i, v)
						}
						s.Len()
						prev := -1
						s.Scan(0, 17, func(k, v int) bool {
							if k <= prev || k != v {
								t.Errorf("invalid scan: %d/%d after %d", k, v, prev)
							}
							prev = k
							return true
						})
						prev = -1
						for k, v := range s.All() {
							if k <= prev || k != v {
								t.Errorf("invalid All: %d/%d after %d", k, v, prev)
							}
							prev = k
						}
					}
				})
			}
			close(start)
			wg.Wait()
			if n := s.Len(); n != writers*count {
				t.Fatalf("Len=%d", n)
			}
			for k := 0; k < writers*count; k++ {
				if v, ok := s.Get(k); !ok || v != k {
					t.Fatalf("Get(%d)=%d,%v", k, v, ok)
				}
			}
		})
	}
}

func TestConcurrentReset(t *testing.T) {
	s := NewMap[int, int](Options{MemTableSize: 7, LeafSize: 11, Fanout: 3})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Go(func() {
			for i := 0; i < 100; i++ {
				switch worker {
				case 0:
					s.Reset()
				case 1:
					s.Put(i, i)
					s.Delete(i - 1)
				case 2:
					s.Compact()
					s.Flush()
				case 3:
					s.Get(i)
					s.Len()
					for range s.All() {
						break
					}
				}
			}
		})
	}
	wg.Wait()
	s.Reset()
	s.Put(42, 42)
	if v, ok := s.Get(42); !ok || v != 42 || s.Len() != 1 {
		t.Fatal("map unusable after concurrent Reset")
	}
}

func TestTraversalLockLifetime(t *testing.T) {
	for _, noLocks := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoLocks=%v", noLocks), func(t *testing.T) {
			s := NewMap[int, int](Options{NoLocks: noLocks, MemTableSize: 7, LeafSize: 11, Fanout: 3})
			s.Put(1, 1)
			check := func(int, int) bool {
				locked := s.mu.TryLock()
				if locked {
					s.mu.Unlock()
				}
				if locked != noLocks {
					t.Errorf("exclusive lock available inside traversal=%v", locked)
				}
				return false
			}
			unlocked := func() {
				if !s.mu.TryLock() {
					t.Fatal("lock retained after traversal")
				}
				s.mu.Unlock()
			}
			seq := s.All()
			unlocked() // Iterator construction must not retain a lock.
			s.Scan(0, 10, check)
			unlocked()
			seq(check)
			unlocked()
			for range s.All() {
				break
			}
			unlocked()
			for _, run := range []func(){
				func() { s.Scan(0, 10, func(int, int) bool { panic("stop") }) },
				func() {
					for range s.All() {
						panic("stop")
					}
				},
			} {
				func() {
					defer func() {
						if recover() == nil {
							t.Error("expected callback panic")
						}
					}()
					run()
				}()
				unlocked()
			}
			s.Compact()
			s.Reset()
			if s.opts.NoLocks != noLocks {
				t.Fatal("locking configuration changed")
			}
			s.Put(1, 1)
			s.Scan(0, 10, check)
			unlocked()
		})
	}
}
