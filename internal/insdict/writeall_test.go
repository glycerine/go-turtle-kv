package insdict

import (
	"fmt"
	"iter"
	"math/rand"
	"slices"
	"testing"
)

func TestSlowWriteAllOverlappingPullIterators(t *testing.T) {
	for _, first := range []int{0, 1} {
		for _, exit := range []string{"stop", "exhaust"} {
			for _, rebuild := range []string{"growth", "pack"} {
				t.Run(fmt.Sprintf("first=%d/%s/%s", first, exit, rebuild), func(t *testing.T) {
					d := NewDict[int, int]()
					for k := 0; k < 5; k++ {
						d.Put(k, k)
					}
					// Calls are serialized, but iterator lifetimes can overlap
					// without ending in reverse order of their starts.
					nextA, stopA := iter.Pull2(d.SlowWriteAll())
					defer stopA()
					nextB, stopB := iter.Pull2(d.SlowWriteAll())
					defer stopB()
					next := []func() (int, int, bool){nextA, nextB}
					stop := []func(){stopA, stopB}
					for i, advance := range next {
						if k, v, ok := advance(); !ok || k != 0 || v != 0 {
							t.Fatalf("iterator %d first yield = (%d, %d, %v)", i, k, v, ok)
						}
					}
					if exit == "stop" {
						stop[first]()
					} else {
						for k := 1; k < 5; k++ {
							if got, v, ok := next[first](); !ok || got != k || v != k {
								t.Fatalf("draining iterator: got (%d, %d, %v), want key %d", got, v, ok, k)
							}
						}
						if _, _, ok := next[first](); ok {
							t.Fatal("drained iterator did not terminate")
						}
					}

					d.Del(0) // a hole before the surviving iterator's cursor
					want := []int{1, 2, 3, 4}
					if rebuild == "growth" {
						d.Put(5, 5) // initial entry budget is full; forces a rebuild
						want = append(want, 5)
					} else {
						d.Pack(true) // must remain a no-op while one iterator is active
					}
					var got []int
					for {
						k, v, ok := next[1-first]()
						if !ok {
							break
						}
						if v != k {
							t.Errorf("key %d has value %d", k, v)
						}
						got = append(got, k)
					}
					if !slices.Equal(got, want) {
						t.Errorf("surviving iterator yielded %v, want %v", got, want)
					}
					// Stopping completed iterators must not release protection twice.
					stopA()
					stopB()
					d.Del(1)
					d.Pack(true)
					if len(d.entries) != d.Len() {
						t.Error("Pack did not remove tombstones after both iterators completed")
					}
					checkInvariants(t, d)
				})
			}
		}
	}
}

func TestSlowWriteAllOverlappingClearAndClone(t *testing.T) {
	for _, first := range []int{0, 1} {
		t.Run(fmt.Sprintf("first=%d", first), func(t *testing.T) {
			d := NewDict[int, int]()
			for k := 0; k < 5; k++ {
				d.Put(k, k)
			}
			nextA, stopA := iter.Pull2(d.SlowWriteAll())
			defer stopA()
			nextB, stopB := iter.Pull2(d.SlowWriteAll())
			defer stopB()
			nextA()
			nextB()
			d.Del(0)
			clone := d.Clone()
			clone.Pack(true)
			if len(clone.entries) != clone.Len() {
				t.Error("clone inherited the original's iterator protection")
			}
			if len(d.entries) != 5 {
				t.Fatal("packing clone changed the original's entries")
			}
			d.Clear()
			next := []func() (int, int, bool){nextA, nextB}
			for _, i := range []int{first, 1 - first} {
				if _, _, ok := next[i](); ok {
					t.Fatalf("iterator %d continued after Clear", i)
				}
			}
			// Reuse only after both iterations finish, as Clear's contract requires.
			d.Put(10, 10)
			d.Del(10)
			d.Pack(true)
			if len(d.entries) != 0 {
				t.Error("Pack remained disabled after Clear and iterator shutdown")
			}
			checkInvariants(t, d)
			checkInvariants(t, clone)
		})
	}
}

// The oracle is a map plus a queue of keys awaiting their turn. Deletion
// removes a key from the queue; a fresh insertion appends it. It never reads
// the dictionary's entries or iterator position to decide what comes next.
func TestSlowWriteAllRandomized(t *testing.T) {
	for _, hashMode := range []string{"default", "collisions", "zero"} {
		for seed := int64(0); seed < 64; seed++ {
			t.Run(fmt.Sprintf("%s/seed=%d", hashMode, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))
				d := NewDict[int, int]()
				switch hashMode {
				case "collisions":
					d = NewDictFunc[int, int](func(k int) uint64 { return uint64(k % 4) })
				case "zero":
					d = NewDictFunc[int, int](func(int) uint64 { return 0 })
				}
				m := newModel[int, int]()
				var pending []int
				put := func(k, v int) {
					_, exists := m.m[k]
					if got := d.Put(k, v); got != !exists {
						t.Fatalf("Put(%d) newlyAdded=%v, want %v", k, got, !exists)
					}
					if !exists {
						pending = append(pending, k)
					}
					m.put(k, v)
				}
				del := func(k int) {
					want := m.del(k)
					if got := d.Del(k); got != want {
						t.Fatalf("Del(%d)=%v, want %v", k, got, want)
					}
					if i := slices.Index(pending, k); i >= 0 {
						pending = slices.Delete(pending, i, i+1)
					}
				}
				for k := 0; k < 64; k++ {
					put(k, k)
				}
				// Begin with holes, including holes before the first yielded key.
				for k := 0; k < 64; k++ {
					if rng.Intn(3) == 0 {
						del(k)
					}
				}
				checkAgainst(t, d, m)
				remaining := 600 // finite budget so newly appended keys eventually drain
				visits := 0
				for k, v := range d.SlowWriteAll() {
					if len(pending) == 0 || k != pending[0] {
						t.Fatalf("visit %d: unexpected key %d; pending=%v", visits, k, pending)
					}
					if want := m.m[k]; v != want {
						t.Fatalf("visit %d: key %d value=%d, want %d", visits, k, v, want)
					}
					pending = pending[1:]
					visits++
					// Some advances have no mutations, others have multiple bursts.
					burst := min(rng.Intn(17), remaining)
					for j := 0; j < burst; j++ {
						remaining--
						target := rng.Intn(128)
						switch rng.Intn(6) {
						case 0, 1:
							put(target, rng.Int())
						case 2:
							del(target)
						case 3:
							// Reinsert both visited and not-yet-visited keys.
							del(target)
							put(target, rng.Int())
						case 4:
							del(k)
							put(k, rng.Int())
						case 5:
							d.Pack(rng.Intn(2) == 0)
						}
						checkAgainst(t, d, m)
					}
				}
				if len(pending) != 0 {
					t.Fatalf("iteration skipped keys: %v", pending)
				}
				if d.activeWriteIters != 0 {
					t.Fatal("packing remains disabled after iteration")
				}
				d.Pack(true)
				if len(d.entries) != d.Len() {
					t.Fatal("Pack did not remove tombstones after iteration")
				}
				checkAgainst(t, d, m)
			})
		}
	}
}

func TestSlowWriteAllRetainedTombstones(t *testing.T) {
	d := NewDictFunc[int, int](func(int) uint64 { return 0 })
	// Exactly fill the initial entry budget. Keep one key as the iterator's
	// starting point, then create far more tombstones than live entries.
	for k := 0; k < 5; k++ {
		d.Put(k, k)
	}
	for k := 0; k < 4; k++ {
		d.Del(k)
	}
	var got []int
	for k := range d.SlowWriteAll() {
		got = append(got, k)
		if k == 4 {
			for next := 5; next < 100; next++ {
				d.Put(next, next)
				d.Del(next)
				checkInvariants(t, d)
				if _, found := d.Get2(0); found {
					t.Fatal("rebuild indexed a deleted entry as key zero")
				}
			}
			before := len(d.indices)
			d.Pack(true)
			if len(d.indices) != before {
				t.Fatal("Pack rebuilt the table during iteration")
			}
			if !d.DelPackMaybe(4) {
				t.Fatal("DelPackMaybe did not delete the current key")
			}
			if len(d.entries) != 100 {
				t.Fatal("DelPackMaybe compacted entries during iteration")
			}
			d.Put(100, 100)
		}
	}
	if !slices.Equal(got, []int{4, 100}) {
		t.Fatalf("yielded %v, want [4 100]", got)
	}
	checkInvariants(t, d)
}

func TestSlowWriteAllRestoresState(t *testing.T) {
	for _, exit := range []string{"complete", "break", "panic"} {
		t.Run(exit, func(t *testing.T) {
			d := NewDict[int, int]()
			for k := 0; k < 5; k++ {
				d.Put(k, k)
			}
			var got []int
			for k := range d.SlowWriteAll() {
				got = append(got, k)
				if k != 0 {
					continue
				}
				func() {
					if exit == "panic" {
						defer func() {
							if p := recover(); p != "test panic" {
								t.Fatalf("recovered %v, want test panic", p)
							}
						}()
					}
					for range d.SlowWriteAll() {
						switch exit {
						case "break":
							return
						case "panic":
							panic("test panic")
						}
					}
				}()
				if d.activeWriteIters != 1 {
					t.Fatal("nested iteration removed outer iterator's protection")
				}
				d.Del(0)
				d.Put(5, 5) // forces growth with a hole before the outer cursor
			}
			if !slices.Equal(got, []int{0, 1, 2, 3, 4, 5}) {
				t.Fatalf("yielded %v, want [0 1 2 3 4 5]", got)
			}
			if d.activeWriteIters != 0 {
				t.Fatal("packing remains disabled after outer iteration")
			}
			checkInvariants(t, d)
		})
	}
}

func TestSlowWriteAllEarlyExit(t *testing.T) {
	for _, panicking := range []bool{false, true} {
		d := NewDict[int, int]()
		d.Put(1, 1)
		func() {
			if panicking {
				defer func() {
					if p := recover(); p != "test panic" {
						t.Fatalf("recovered %v, want test panic", p)
					}
				}()
			}
			for k := range d.SlowWriteAll() {
				d.Del(k)
				if panicking {
					panic("test panic")
				}
				break
			}
		}()
		if d.activeWriteIters != 0 {
			t.Fatal("packing remains disabled after early exit")
		}
		d.Pack(true)
		if len(d.entries) != 0 {
			t.Fatal("Pack did not remove tombstones after early exit")
		}
		checkInvariants(t, d)
	}
}
