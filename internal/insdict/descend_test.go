package insdict

import (
	"math"
	"slices"
	"testing"
)

func descendKeys[K comparable, V any](d *Dict[K, V], piv ...K) (keys []K) {
	for k := range d.Descend(piv...) {
		keys = append(keys, k)
	}
	return
}

func TestDescendBounds(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{3, 7, 1, 5} {
		d.Put(k, 10*k)
	}
	seq := d.Descend()
	if d.sorted != nil {
		t.Fatal("iterator construction allocated index")
	}
	for pass := 0; pass < 2; pass++ {
		var got []int
		for k, v := range seq {
			got = append(got, k)
			if v != 10*k {
				t.Fatal(k, v)
			}
		}
		if !slices.Equal(got, []int{7, 5, 3, 1}) {
			t.Fatal(got)
		}
	}
	cache, backing := d.sorted, &d.sorted.sidx[0]
	for _, tc := range []struct{ piv, want []int }{
		{nil, []int{7, 5, 3, 1}},
		{[]int{5}, []int{5, 3, 1}},
		{[]int{6}, []int{5, 3, 1}},
		{[]int{0}, nil},
		{[]int{8}, []int{7, 5, 3, 1}},
		{[]int{7, 3}, []int{7, 5}},
		{[]int{6, 2}, []int{5, 3}},
		{[]int{3, 3}, nil},
		{[]int{3, 7}, nil},
		{[]int{10, 8}, nil},
		{[]int{0, -3}, nil},
	} {
		if got := descendKeys(&d, tc.piv...); !slices.Equal(got, tc.want) {
			t.Fatalf("piv %v got %v want %v", tc.piv, got, tc.want)
		}
	}
	ascendKeys(&d)
	if d.sorted != cache || &d.sorted.sidx[0] != backing || !d.cleanSort {
		t.Fatal("index not shared")
	}
	calls := 0
	for range d.Descend() {
		calls++
		break
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if allocs := testing.AllocsPerRun(100, func() {
		for range d.Descend(6, 2) {
		}
	}); allocs != 0 {
		t.Fatal(allocs)
	}
	d.Put(6, 60)
	if got := descendKeys(&d, 6, 3); !slices.Equal(got, []int{6, 5}) {
		t.Fatal(got)
	}
	d.Del(5)
	d.Pack(true)
	if got := descendKeys(&d, 6, 3); !slices.Equal(got, []int{6}) {
		t.Fatal(got)
	}
	d.Clear()
	if got := descendKeys(&d); len(got) != 0 {
		t.Fatal(got)
	}
	var nilDict *Dict[int, int]
	if got := descendKeys(nilDict); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestDescendDeleteDuringScan(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{3, 7, 1, 5} {
		d.Put(k, k)
	}
	piv := []int{7, 1}
	seq := d.Descend(piv...)
	piv[0], piv[1] = 0, 10
	var got []int
	for k := range seq {
		got = append(got, k)
		d.Del(k + 2)
		d.Del(k)
	}
	if !slices.Equal(got, []int{7, 5, 3}) || d.cleanSort || len(d.sorted.sidx) != 4 {
		t.Fatal(got)
	}
	if got := descendKeys(&d); !slices.Equal(got, []int{1}) {
		t.Fatal(got)
	}
}

func TestDescendFloatAndCustomOrder(t *testing.T) {
	var d Dict[float64, int]
	for _, k := range []float64{0, math.Inf(-1), math.NaN(), math.Inf(1)} {
		d.Put(k, 0)
	}
	got := descendKeys(&d)
	if len(got) != 4 || !math.IsNaN(got[0]) || !math.IsInf(got[1], 1) || got[2] != 0 || !math.IsInf(got[3], -1) {
		t.Fatal(got)
	}
	if got := descendKeys(&d, math.Inf(1), math.Inf(-1)); !slices.Equal(got, []float64{math.Inf(1), 0}) {
		t.Fatal(got)
	}
	if got := descendKeys(&d, math.NaN(), math.Inf(1)); len(got) != 1 || !math.IsNaN(got[0]) {
		t.Fatal(got)
	}
	if got := descendKeys(&d, math.NaN(), math.NaN()); len(got) != 0 {
		t.Fatal(got)
	}
	type key struct{ n int }
	custom := NewDictFunc[key, int](func(k key) uint64 { return Mix64(uint64(k.n)) }, func(a, b key) int { return EasyCompare(a.n, b.n) })
	for i := 0; i < 4; i++ {
		custom.Put(key{i}, i)
	}
	if got := descendKeys(custom, key{3}, key{1}); !slices.Equal(got, []key{{3}, {2}}) {
		t.Fatal(got)
	}
	// Comparator-equivalent distinct keys must all be included at the upper bound.
	equivalent := NewDictFunc[int, int](EasyHashInt, func(a, b int) int { return EasyCompare(a/10, b/10) })
	for _, k := range []int{10, 11, 12, 20} {
		equivalent.Put(k, k)
	}
	gotInt := descendKeys(equivalent, 15, 0)
	slices.Sort(gotInt)
	if !slices.Equal(gotInt, []int{10, 11, 12}) {
		t.Fatal(gotInt)
	}
}

func TestDescendTooManyPivots(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	var d Dict[int, int]
	d.Descend(1, 2, 3)
}
