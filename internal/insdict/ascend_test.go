package insdict

import (
	"math"
	"slices"
	"testing"
)

func ascendKeys[K comparable, V any](d *Dict[K, V], piv ...K) (keys []K) {
	for k := range d.Ascend(piv...) {
		keys = append(keys, k)
	}
	return
}

func TestAscendCache(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{4, 1, 3, 2} {
		d.Put(k, k*10)
	}
	if d.sorted != nil || d.cleanSort {
		t.Fatal("sorting must be lazy")
	}
	check := func(want []int) {
		t.Helper()
		if got := ascendKeys(&d); !slices.Equal(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	check([]int{1, 2, 3, 4})
	cache, backing := d.sorted, &d.sorted.sidx[0]
	d.Put(2, 99)
	d.Del(100)
	if !d.cleanSort {
		t.Fatal("value update or absent deletion invalidated cache")
	}
	check([]int{1, 2, 3, 4})
	if d.sorted != cache || &d.sorted.sidx[0] != backing {
		t.Fatal("cache not reused")
	}
	if allocs := testing.AllocsPerRun(100, func() {
		for range d.Ascend() {
		}
	}); allocs != 0 {
		t.Fatalf("cached scan allocated %v", allocs)
	}
	d.Del(1)
	if d.cleanSort {
		t.Fatal("deletion did not invalidate")
	}
	check([]int{2, 3, 4})
	d.Pack(true)
	if d.cleanSort {
		t.Fatal("pack did not invalidate")
	}
	check([]int{2, 3, 4})
	for k := 5; k < 100; k++ {
		d.Put(k, k)
	}
	if d.cleanSort {
		t.Fatal("insertion did not invalidate")
	}
	got := ascendKeys(&d)
	if len(got) != 98 || !slices.IsSorted(got) {
		t.Fatal(got)
	}
	clone := d.Clone()
	clone.Del(2)
	if !d.cleanSort {
		t.Fatal("clone modified original cache")
	}
	if got := ascendKeys(clone); len(got) != 97 || got[0] != 3 {
		t.Fatal(got)
	}
	d.Clear()
	if d.cleanSort {
		t.Fatal("clear did not invalidate")
	}
	check(nil)
	d.Put(-1, 1)
	check([]int{-1})
}

func TestAscendDeleteDuringScan(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{5, 3, 1, 4, 2} {
		d.Put(k, k)
	}
	var got []int
	for k := range d.Ascend() {
		got = append(got, k)
		d.Del(k - 1)
		d.Del(k)
	}
	if !slices.Equal(got, []int{1, 2, 3, 4, 5}) || d.Len() != 0 || d.cleanSort {
		t.Fatalf("got %v len %d clean %v", got, d.Len(), d.cleanSort)
	}
	if len(d.sorted.sidx) != 5 {
		t.Fatal("deletion changed active index")
	}
	if got := ascendKeys(&d); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestAscendOrderingAndStop(t *testing.T) {
	var d Dict[string, int]
	d.Put("z", 1)
	d.Put("a", 2)
	d.Put("m", 3)
	if got := ascendKeys(&d, ""); !slices.Equal(got, []string{"a", "m", "z"}) {
		t.Fatal(got)
	}
	calls := 0
	for k, v := range d.Ascend("") {
		calls++
		if k != "a" || v != 2 {
			t.Fatal(k, v)
		}
		break
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	var nilDict *Dict[int, int]
	for range nilDict.Ascend(0) {
		t.Fatal("nil receiver yielded")
	}
	type key uint64
	named := NewDictFunc[key, int](func(k key) uint64 { return Mix64(uint64(k)) })
	named.Put(key(math.MaxUint64), 1)
	named.Put(0, 2)
	if got := ascendKeys(named, 0); !slices.Equal(got, []key{0, key(math.MaxUint64)}) {
		t.Fatal(got)
	}
	var floats Dict[float64, int]
	for _, k := range []float64{math.NaN(), math.Inf(1), -3, 0, math.Inf(-1)} {
		floats.Put(k, 1)
	}
	keys := ascendKeys(&floats)
	if len(keys) != 5 || !math.IsInf(keys[0], -1) || keys[1] != -3 || keys[2] != 0 || !math.IsInf(keys[3], 1) || !math.IsNaN(keys[4]) {
		t.Fatal(keys)
	}
}

func TestAscendUnsupportedKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	var d Dict[bool, int]
	d.Put(true, 1)
	ascendKeys(&d, false)
}

func TestAscendPivot(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{7, 1, 5, 3} {
		d.Put(k, k)
	}
	seq := d.Ascend(4)
	if d.sorted != nil {
		t.Fatal("iterator construction materialized cache")
	}
	for pass := 0; pass < 2; pass++ {
		var got []int
		for k := range seq {
			got = append(got, k)
		}
		if !slices.Equal(got, []int{5, 7}) {
			t.Fatal(got)
		}
	}
	for _, tc := range []struct {
		pivot int
		want  []int
	}{{0, []int{1, 3, 5, 7}}, {3, []int{3, 5, 7}}, {8, nil}} {
		if got := ascendKeys(&d, tc.pivot); !slices.Equal(got, tc.want) {
			t.Fatalf("pivot %d: %v", tc.pivot, got)
		}
	}
	d.Put(4, 4)
	if got := ascendKeys(&d, 4); !slices.Equal(got, []int{4, 5, 7}) {
		t.Fatal(got)
	}
	var f Dict[float64, int]
	f.Put(math.NaN(), 1)
	f.Put(math.Inf(1), 2)
	f.Put(-1, 3)
	if got := ascendKeys(&f, math.NaN()); len(got) != 1 || !math.IsNaN(got[0]) {
		t.Fatal(got)
	}
	if got := ascendKeys(&f, math.Inf(1)); len(got) != 2 || !math.IsInf(got[0], 1) || !math.IsNaN(got[1]) {
		t.Fatal(got)
	}
}

func TestAscendBounds(t *testing.T) {
	var d Dict[int, int]
	for _, k := range []int{7, 1, 5, 3} {
		d.Put(k, k)
	}
	for _, tc := range []struct {
		piv  []int
		want []int
	}{
		{nil, []int{1, 3, 5, 7}},
		{[]int{3}, []int{3, 5, 7}},
		{[]int{3, 7}, []int{3, 5}},
		{[]int{2, 6}, []int{3, 5}},
		{[]int{3, 3}, nil},
		{[]int{7, 3}, nil},
		{[]int{8, 10}, nil},
		{[]int{-3, 0}, nil},
	} {
		if got := ascendKeys(&d, tc.piv...); !slices.Equal(got, tc.want) {
			t.Fatalf("bounds %v: got %v want %v", tc.piv, got, tc.want)
		}
	}
	piv := []int{3, 7}
	seq := d.Ascend(piv...)
	piv[0], piv[1] = 100, 200
	var got []int
	for k := range seq {
		got = append(got, k)
		d.Del(k)
	}
	if !slices.Equal(got, []int{3, 5}) {
		t.Fatal(got)
	}
	if got := ascendKeys(&d); !slices.Equal(got, []int{1, 7}) {
		t.Fatal(got)
	}
	var f Dict[float64, int]
	f.Put(-1, 0)
	f.Put(math.Inf(1), 1)
	f.Put(math.NaN(), 2)
	if got := ascendKeys(&f, math.Inf(-1), math.NaN()); len(got) != 2 || got[0] != -1 || !math.IsInf(got[1], 1) {
		t.Fatal(got)
	}
}

func TestAscendTooManyPivots(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	var d Dict[int, int]
	d.Ascend(1, 2, 3)
}

func TestAscendTypedComparator(t *testing.T) {
	type key int
	d := NewDictFunc[key, int](func(k key) uint64 { return Mix64(uint64(k)) }, EasyCompare[key])
	for _, k := range []key{4, -2, 1} {
		d.Put(k, int(k))
	}
	if got := ascendKeys(d); !slices.Equal(got, []key{-2, 1, 4}) {
		t.Fatal(got)
	}
	if got := ascendKeys(d.Clone(), 1, 4); !slices.Equal(got, []key{1}) {
		t.Fatal(got)
	}
	type record struct{ ID int }
	custom := NewDictFuncSize[record, int](func(k record) uint64 { return Mix64(uint64(k.ID)) }, 3, func(a, b record) int { return EasyCompare(a.ID, b.ID) })
	custom.Put(record{3}, 3)
	custom.Put(record{1}, 1)
	if got := ascendKeys(custom, record{1}, record{3}); !slices.Equal(got, []record{{1}}) {
		t.Fatal(got)
	}
	if EasyCompare(math.NaN(), math.Inf(1)) != 1 || EasyCompare(math.Inf(1), math.NaN()) != -1 || EasyCompare(math.NaN(), math.NaN()) != 0 {
		t.Fatal("NaN ordering")
	}
}

func BenchmarkAscendSortInt(b *testing.B) {
	d := NewDictFuncSize[int, int](EasyHashInt, 1000, EasyCompare[int])
	for i := 0; i < 1000; i++ {
		d.Put((i*997)%1000, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.cleanSort = false
		for range d.Ascend() {
		}
	}
}
