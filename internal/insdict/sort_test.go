package insdict

import (
	"slices"
	"testing"
)

func TestSort(t *testing.T) {
	var d Dict[int, int]
	d.Sort(false)
	d.Sort(true)
	if d.sorted == nil || !d.cleanSort || len(d.sorted.sidx) != 0 {
		t.Fatal("empty dictionary did not materialize a clean index")
	}
	for _, k := range []int{3, 1, 2} {
		d.Put(k, k*10)
	}
	check := func(want []int) {
		t.Helper()
		if d.sorted == nil || !d.cleanSort {
			t.Fatal("index is not materialized and clean")
		}
		var got []int
		for _, i := range d.sorted.sidx {
			if !d.entries[i].live {
				t.Fatal("index contains deleted entry")
			}
			got = append(got, d.entries[i].key)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("index keys %v, want %v", got, want)
		}
	}
	d.Sort(true)
	check([]int{1, 2, 3})
	cache, backing := d.sorted, &d.sorted.sidx[0]
	d.Sort(true)
	if d.sorted != cache || &d.sorted.sidx[0] != backing {
		t.Fatal("clean index was not reused")
	}
	d.Del(1)
	d.Put(0, 0)
	d.Sort(true)
	check([]int{0, 2, 3})
	var insertionOrder []int
	for k := range d.All() {
		insertionOrder = append(insertionOrder, k)
	}
	if !slices.Equal(insertionOrder, []int{3, 2, 0}) {
		t.Fatalf("Sort changed insertion order: %v", insertionOrder)
	}
	for range 2 {
		d.Sort(false)
		if d.sorted != nil || d.cleanSort {
			t.Fatal("Sort(false) did not release and invalidate index")
		}
	}
	d.Sort(true)
	check([]int{0, 2, 3})
	d.Sort(false)
	if got := ascendKeys(&d); !slices.Equal(got, []int{0, 2, 3}) {
		t.Fatalf("Ascend after Sort(false): %v", got)
	}
}

func TestSortCustomComparator(t *testing.T) {
	d := NewDictFunc[int, int](nil, func(a, b int) int { return EasyCompare(b, a) })
	d.Put(1, 1)
	d.Put(3, 3)
	d.Put(2, 2)
	d.Sort(true)
	if !d.cleanSort || d.sorted == nil || d.entries[d.sorted.sidx[0]].key != 3 {
		t.Fatal("Sort did not use custom comparator")
	}
}

func TestSortNil(t *testing.T) {
	var d *Dict[int, int]
	d.Sort(true)
	d.Sort(false)
}
