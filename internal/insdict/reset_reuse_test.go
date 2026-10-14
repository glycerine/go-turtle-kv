package insdict

import (
	"testing"
)

func TestResetReuse(t *testing.T) {
	for _, collide := range []bool{false, true} {
		hash := func(k int) uint64 {
			if collide {
				return 0
			}
			return Mix64(uint64(k))
		}
		d := NewDictFuncSize[int, *int](hash, 257)
		for generation := 0; generation < 30; generation++ {
			indexBase, tagsBase := &d.indices[0], &d.tags[0]
			for i := 0; i < 257; i++ {
				v := generation + i
				d.Put(i, &v)
			}
			entryBase := &d.entries[0]
			d.Del(13) // Include a tombstone and cached ordering in the reset.
			d.Sort(true)
			for range d.Ascend() {
			}
			retained := d.entries[:cap(d.entries)]
			d.ResetReuse()
			if d.Len() != 0 || &d.indices[0] != indexBase || &d.tags[0] != tagsBase || &d.entries[:cap(d.entries)][0] != entryBase {
				t.Fatal("reset failed to empty and retain storage")
			}
			for _, e := range retained {
				if e.val != nil {
					t.Fatal("retained value reference")
				}
			}
			for i := 0; i < 258; i++ {
				if _, ok := d.Get2(i); ok {
					t.Fatal("stale key after reset")
				}
			}
			for range d.All() {
				t.Fatal("stale iteration")
			}
			// Reuse slot zero and verify it cannot make old probe chains visible.
			v := generation
			d.Put(1000+generation, &v)
			if got, ok := d.Get2(1000 + generation); !ok || *got != v {
				t.Fatal("missing new generation")
			}
			if _, ok := d.Get2(0); ok {
				t.Fatal("stale index became visible")
			}
			d.ResetReuse()
		}
	}
}

func TestResetReuseZeroAndNil(t *testing.T) {
	var nilDict *Dict[int, int]
	nilDict.ResetReuse()
	var d Dict[int, int]
	d.ResetReuse()
	d.Put(1, 2)
	d.ResetReuse()
	d.Put(3, 4)
	if d.Get(3) != 4 || d.Get(1) != 0 {
		t.Fatal("zero-value reset")
	}
}

func TestResetReuseDuringWriteIteration(t *testing.T) {
	d := NewDict[int, int]()
	d.Put(1, 2)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		for range d.SlowWriteAll() {
			d.ResetReuse()
		}
	}()
	if !panicked || d.Get(1) != 2 || d.activeWriteIters != 0 {
		t.Fatal("reset must reject active iterator without changing entries")
	}
	d.ResetReuse()
	if d.Len() != 0 {
		t.Fatal("reset after iterator panic")
	}
}
