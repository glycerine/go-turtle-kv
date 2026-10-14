package turtle

import (
	"cmp"
	"math"
	"math/rand"
	"slices"
	"testing"
	"unsafe"
)

type signedKey int64
type unsignedKey uint64
type floatKey float64
type stringKey string

func testOrderedKeys[K cmp.Ordered](t *testing.T, keys []K) {
	t.Helper()
	s := NewMap[K, int](Options{MemTableSize: 512, LeafSize: 37, Fanout: 5})
	for i, k := range keys {
		s.Put(k, i)
	}
	want := slices.Clone(keys)
	slices.SortFunc(want, cmp.Compare[K])
	want = slices.CompactFunc(want, func(a, b K) bool { return cmp.Compare(a, b) == 0 })
	verify := func() {
		var got []K
		for k, v := range s.All() {
			got = append(got, k)
			value, ok := s.Get(k)
			if !ok || value != v {
				t.Fatal("Get differs from All")
			}
			// Validate last-write-wins for duplicate keys, including NaNs and signed zero.
			for i := len(keys) - 1; i >= 0; i-- {
				if cmp.Compare(keys[i], k) == 0 {
					if v != i {
						t.Fatalf("wrong latest value: %v", k)
					}
					break
				}
			}
		}
		if !slices.EqualFunc(got, want, func(a, b K) bool { return cmp.Compare(a, b) == 0 }) {
			t.Fatalf("ordered iteration: got %d keys, want %d", len(got), len(want))
		}
		if s.Len() != len(want) {
			t.Fatal("Len")
		}
		if len(want) > 0 {
			start := want[len(want)/2]
			pos := len(want) / 2
			count := s.Scan(start, 17, func(k K, v int) bool {
				if cmp.Compare(k, want[pos]) != 0 {
					t.Fatal("Scan start/order")
				}
				pos++
				return true
			})
			if count != min(17, len(want)-len(want)/2) {
				t.Fatal("Scan count")
			}
		}
	}
	verify()
	s.Flush()
	verify()
	s.Compact()
	verify()
	for _, k := range want {
		s.Delete(k)
	}
	s.Flush()
	if s.Len() != 0 {
		t.Fatal("delete all")
	}
	s.Reset()
	if s.Len() != 0 {
		t.Fatal("Reset")
	}
}

func TestGenericOrderedTypes(t *testing.T) {
	rng := rand.New(rand.NewSource(2023))
	const n = 2048
	signed := make([]signedKey, n)
	unsigned := make([]unsignedKey, n)
	floats := make([]floatKey, n)
	small := make([]int8, n)
	medium := make([]int16, n)
	wide := make([]int32, n)
	u32 := make([]uint32, n)
	f32 := make([]float32, n)
	for i := 0; i < n; i++ {
		x := rng.Uint64()
		signed[i] = signedKey(x)
		unsigned[i] = unsignedKey(x)
		floats[i] = floatKey(int64(x)) / 1e6
		small[i] = int8(x)
		medium[i] = int16(x)
		wide[i] = int32(x)
		u32[i] = uint32(x)
		f32[i] = float32(int32(x))
	}
	floats[0] = floatKey(math.NaN())
	floats[1] = floatKey(math.Float64frombits(0xfff8000000000012))
	floats[2] = floatKey(math.Copysign(0, -1))
	floats[3] = 0
	floats[4] = floatKey(math.Inf(-1))
	floats[5] = floatKey(math.Inf(1))
	f32[0] = float32(math.NaN())
	f32[1] = float32(math.NaN())
	f32[2] = float32(math.Copysign(0, -1))
	f32[3] = 0
	t.Run("signed", func(t *testing.T) { testOrderedKeys(t, signed) })
	t.Run("unsigned", func(t *testing.T) { testOrderedKeys(t, unsigned) })
	t.Run("float64", func(t *testing.T) { testOrderedKeys(t, floats) })
	t.Run("float32", func(t *testing.T) { testOrderedKeys(t, f32) })
	t.Run("int8", func(t *testing.T) { testOrderedKeys(t, small) })
	t.Run("int16", func(t *testing.T) { testOrderedKeys(t, medium) })
	t.Run("int32", func(t *testing.T) { testOrderedKeys(t, wide) })
	t.Run("uint32", func(t *testing.T) { testOrderedKeys(t, u32) })
	t.Run("string", func(t *testing.T) {
		testOrderedKeys(t, []stringKey{"z", "", "a", "a", "\x00", "a\x00", "longer-than-eight-bytes"})
	})
	t.Run("int", func(t *testing.T) { testOrderedKeys(t, []int{-4, -1, 0, 1, 10, math.MaxInt, math.MinInt}) })
	t.Run("uint", func(t *testing.T) { testOrderedKeys(t, []uint{0, 1, ^uint(0), 1 << 31}) })
	t.Run("uint8", func(t *testing.T) { testOrderedKeys(t, []uint8{0, 1, 255, 128}) })
	t.Run("uint16", func(t *testing.T) { testOrderedKeys(t, []uint16{0, 1, 65535, 32768}) })
	t.Run("uintptr", func(t *testing.T) { testOrderedKeys(t, []uintptr{0, 1, ^uintptr(0)}) })
}

func TestNativeValuesAndReferenceClearing(t *testing.T) {
	type value struct {
		array [100]byte
		ref   *int
		slice []byte
	}
	s := NewMap[int, value](Options{MemTableSize: 4, LeafSize: 7, Fanout: 3})
	x := 7
	v := value{ref: &x, slice: []byte{3}}
	v.array[0] = 9
	s.Put(-1, v)
	v.array[0] = 2
	x = 8
	v.slice[0] = 4
	got, ok := s.Get(-1)
	if !ok || got.array[0] != 9 || *got.ref != 8 || got.slice[0] != 4 {
		t.Fatal("values must use Go assignment semantics")
	}
	for i := 0; i < 120; i++ {
		s.Put(i, v)
	}
	for i := -1; i < 120; i++ {
		s.Delete(i)
	}
	s.Flush()
	if s.Len() != 0 {
		t.Fatal("delete/compact")
	}
	for _, r := range s.mem.entries[:cap(s.mem.entries)] {
		if r.value.ref != nil || r.value.slice != nil {
			t.Fatal("MemTable retains cleared references")
		}
	}
	for _, r := range s.sortScratch {
		if r.value.ref != nil || r.value.slice != nil {
			t.Fatal("sort scratch retains references")
		}
	}
	for _, class := range s.recordPool.buffers {
		for _, run := range class {
			for _, r := range run[:cap(run)] {
				if r.value.ref != nil || r.value.slice != nil {
					t.Fatal("record pool retains references")
				}
			}
		}
	}
	s.Compact()
	if s.Len() != 0 {
		t.Fatal("compact")
	}
	// Native uint64 storage needs no per-entry key/value pointers or padding flag.
	if unsafe.Sizeof(record[uint64, uint64]{}) != 24 {
		t.Fatal("unexpected native record size")
	}
	var zero Map[int, int]
	if v, ok := zero.Get(-1); ok || v != 0 || zero.Len() != 0 {
		t.Fatal("zero map")
	}
	zero.Put(-1, 0)
	if v, ok := zero.Get(-1); !ok || v != 0 {
		t.Fatal("present zero value")
	}
}

func FuzzNativeMap(f *testing.F) {
	f.Add([]byte{0, 1, 255, 0, 1, 3, 7, 2, 17, 1, 128, 255, 128, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		s := NewMap[uint64, uint64](Options{MemTableSize: 7, LeafSize: 11, Fanout: 3})
		model := map[uint64]uint64{}
		for i, b := range data {
			k := uint64(b&63) * 0x9e3779b97f4a7c15
			switch b >> 6 {
			case 0:
				s.Put(k, uint64(i))
				model[k] = uint64(i)
			case 1:
				s.Delete(k)
				delete(model, k)
			case 2:
				s.Flush()
			case 3:
				if v, ok := s.Get(k); v != model[k] || ok != func() bool { _, ok := model[k]; return ok }() {
					t.Fatal("Get model")
				}
			}
		}
		verify := func() {
			keys := make([]uint64, 0, len(model))
			for k := range model {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			pos := 0
			for k, v := range s.All() {
				if pos >= len(keys) || k != keys[pos] || v != model[k] {
					t.Fatal("All model")
				}
				pos++
			}
			if pos != len(keys) || s.Len() != len(keys) {
				t.Fatal("count")
			}
		}
		verify()
		s.Flush()
		verify()
		s.Compact()
		verify()
	})
}
