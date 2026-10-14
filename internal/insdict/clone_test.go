package insdict

import (
	"slices"
	"testing"
)

func TestClone(t *testing.T) {
	for _, tc := range []struct {
		name string
		hint int
		n    int
		del  func(int) bool
	}{
		{name: "zero"},
		{name: "presized_empty", hint: 100},
		{name: "populated", n: 100},
		{name: "tombstones", n: 100, del: func(k int) bool { return k%3 == 0 }},
		{name: "all_deleted", n: 100, del: func(int) bool { return true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDictSize[int, int](tc.hint)
			original := newModel[int, int]()
			for k := 0; k < tc.n; k++ {
				d.Put(k, k+1)
				original.put(k, k+1)
			}
			for k := 0; k < tc.n; k++ {
				if tc.del != nil && tc.del(k) {
					d.Del(k)
					original.del(k)
				}
			}

			clone := d.Clone()
			if clone == d {
				t.Fatal("Clone returned the original dictionary")
			}
			if clone.mask != d.mask || clone.live != d.live ||
				!slices.Equal(clone.indices, d.indices) ||
				!slices.Equal(clone.tags, d.tags) ||
				!slices.Equal(clone.entries, d.entries) {
				t.Fatal("Clone did not preserve table contents and tombstones")
			}
			if len(d.indices) > 0 && (&clone.indices[0] == &d.indices[0] || &clone.tags[0] == &d.tags[0]) {
				t.Fatal("Clone shares index or tag storage")
			}
			if len(d.entries) > 0 && &clone.entries[0] == &d.entries[0] {
				t.Fatal("Clone shares entry storage")
			}
			checkAgainst(t, clone, original)
			copied := newModel[int, int]()
			for _, k := range original.order {
				copied.put(k, original.m[k])
			}

			// Exercise overwrite, deletion, insertion, compaction and growth in
			// the clone; the original must retain its contents and order.
			clone.Put(1, -1)
			copied.put(1, -1)
			clone.Del(2)
			copied.del(2)
			clone.Pack(true)
			for k := 1000; k < 1300; k++ {
				clone.Put(k, k)
				copied.put(k, k)
			}
			checkAgainst(t, clone, copied)
			checkAgainst(t, d, original)

			// Iterate the clone while mutating and rebuilding the original.
			var visited []int
			for k := range clone.All() {
				visited = append(visited, k)
				d.Del(k)
				original.del(k)
				d.Put(k+2000, -k)
				original.put(k+2000, -k)
				d.Pack(true)
			}
			if !slices.Equal(visited, copied.order) {
				t.Fatalf("clone iteration changed during original mutations: got %v, want %v", visited, copied.order)
			}
			checkAgainst(t, clone, copied)
			checkAgainst(t, d, original)
		})
	}
}

func TestCloneCustomHash(t *testing.T) {
	type key struct{ id int }
	// Collisions ensure the cloned hash function and probe chains are used.
	d := NewDictFunc[key, int](func(key) uint64 { return 7 })
	m := newModel[key, int]()
	for i := 0; i < 20; i++ {
		d.Put(key{i}, i)
		m.put(key{i}, i)
	}
	d.Del(key{3})
	m.del(key{3})
	clone := d.Clone()
	if clone.hash == nil {
		t.Fatal("Clone lost the custom hash function")
	}
	checkAgainst(t, clone, m)
	clone.Put(key{30}, 30)
	clone.Del(key{4})
	clone.Pack(true)
	checkAgainst(t, d, m)
	m.put(key{30}, 30)
	m.del(key{4})
	checkAgainst(t, clone, m)
}

func TestCloneShallowValues(t *testing.T) {
	value := 10
	d := NewDict[int, *int]()
	d.Put(1, &value)
	clone := d.Clone()
	if got := clone.Get(1); got != &value {
		t.Fatal("Clone did not preserve the value pointer")
	}
	*clone.Get(1) = 20
	if *d.Get(1) != 20 {
		t.Fatal("referenced data should be shared")
	}
	replacement := 30
	clone.Put(1, &replacement)
	if d.Get(1) != &value || clone.Get(1) != &replacement {
		t.Fatal("replacing a value in the clone changed the original")
	}
	checkInvariants(t, d)
	checkInvariants(t, clone)
}
