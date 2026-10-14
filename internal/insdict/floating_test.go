package insdict

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
)

func TestFloatingKeysAgainstCanonicalMap(t *testing.T) {
	f32 := []float32{0, math.Float32frombits(1 << 31), 1, -1, 1.5,
		math.SmallestNonzeroFloat32, -math.SmallestNonzeroFloat32,
		math.MaxFloat32, -math.MaxFloat32, float32(math.Inf(1)), float32(math.Inf(-1)),
		math.Float32frombits(0x7fc00001), math.Float32frombits(0x7fc00002),
		math.Float32frombits(0xffc00001), math.Float32frombits(0x7f800001)}
	f64 := []float64{0, math.Float64frombits(1 << 63), 1, -1, 1.5,
		math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64,
		math.MaxFloat64, -math.MaxFloat64, math.Inf(1), math.Inf(-1),
		math.Float64frombits(0x7ff8000000000001), math.Float64frombits(0x7ff8000000000002),
		math.Float64frombits(0xfff8000000000001), math.Float64frombits(0x7ff0000000000001)}
	var c64 []complex64
	for _, x := range f32 {
		c64 = append(c64, complex(x, 0), complex(0, x), complex(x, x), complex(x, f32[1]), complex(f32[1], x))
	}
	var c128 []complex128
	for _, x := range f64 {
		c128 = append(c128, complex(x, 0), complex(0, x), complex(x, x), complex(x, f64[1]), complex(f64[1], x))
	}
	t.Run("float32", func(t *testing.T) {
		auditFloatingKeys(t, f32, EasyHashFloat32, func(k float32) [2]uint64 {
			return [2]uint64{uint64(math.Float32bits(k))}
		})
	})
	t.Run("float64", func(t *testing.T) {
		auditFloatingKeys(t, f64, EasyHashFloat64, func(k float64) [2]uint64 {
			return [2]uint64{math.Float64bits(k)}
		})
	})
	t.Run("complex64", func(t *testing.T) {
		auditFloatingKeys(t, c64, EasyHashComplex64, func(k complex64) [2]uint64 {
			return [2]uint64{uint64(math.Float32bits(real(k))), uint64(math.Float32bits(imag(k)))}
		})
	})
	t.Run("complex128", func(t *testing.T) {
		auditFloatingKeys(t, c128, EasyHashComplex128, func(k complex128) [2]uint64 {
			return [2]uint64{math.Float64bits(real(k)), math.Float64bits(imag(k))}
		})
	})
}

// A linear list supplies insertion order and original key bits. A Go map keyed
// by independently canonicalized bits supplies the requested NaN equivalence.
// Neither oracle uses the dictionary's hash or equality helpers.
func auditFloatingKeys[K comparable](t *testing.T, pool []K, easyHash func(K) uint64, bits func(K) [2]uint64) {
	t.Helper()
	canonical := func(k K) [2]uint64 {
		b := bits(k)
		switch x := any(k).(type) {
		case float32:
			if math.IsNaN(float64(x)) {
				return [2]uint64{0x7fc00000}
			}
			if x == 0 {
				b[0] = 0
			}
		case float64:
			if math.IsNaN(x) {
				return [2]uint64{0x7ff8000000000000}
			}
			if x == 0 {
				b[0] = 0
			}
		case complex64:
			if math.IsNaN(float64(real(x))) || math.IsNaN(float64(imag(x))) {
				return [2]uint64{0x7fc00000, 0x7fc00000}
			}
			if real(x) == 0 {
				b[0] = 0
			}
			if imag(x) == 0 {
				b[1] = 0
			}
		case complex128:
			if math.IsNaN(real(x)) || math.IsNaN(imag(x)) {
				return [2]uint64{0x7ff8000000000000, 0x7ff8000000000000}
			}
			if real(x) == 0 {
				b[0] = 0
			}
			if imag(x) == 0 {
				b[1] = 0
			}
		}
		return b
	}
	for _, a := range pool {
		for _, b := range pool {
			if canonical(a) == canonical(b) && (easyHash(a) != easyHash(b) || defaultHash(a) != defaultHash(b)) {
				t.Fatalf("equal keys %v and %v have different hashes", a, b)
			}
		}
	}
	for _, mode := range []string{"default", "EasyHash", "constant"} {
		for seed := int64(0); seed < 8; seed++ {
			t.Run(fmt.Sprintf("%s/seed=%d", mode, seed), func(t *testing.T) {
				d := NewDict[K, int]()
				if mode == "EasyHash" {
					d = NewDictFunc[K, int](easyHash)
				} else if mode == "constant" {
					d = NewDictFunc[K, int](func(K) uint64 { return 0 })
				}
				rng := rand.New(rand.NewSource(seed))
				std := make(map[[2]uint64]int)
				type pair struct {
					key K
					val int
				}
				var order []pair
				var pending []pair
				iterating := false
				nextValue := 0
				put := func(k K) {
					nextValue++
					_, exists := std[canonical(k)]
					if added := d.Put(k, nextValue); added != !exists {
						t.Fatalf("Put(%v) newlyAdded=%v, want %v", k, added, !exists)
					}
					std[canonical(k)] = nextValue
					p := pair{k, nextValue}
					if !exists {
						order = append(order, p)
						if iterating {
							pending = append(pending, p)
						}
					} else {
						for i := range order {
							if canonical(order[i].key) == canonical(k) {
								order[i].val = nextValue
							}
						}
						for i := range pending {
							if canonical(pending[i].key) == canonical(k) {
								pending[i].val = nextValue
							}
						}
					}
				}
				del := func(k K) {
					_, exists := std[canonical(k)]
					if found := d.Del(k); found != exists {
						t.Fatalf("Del(%v)=%v, want %v", k, found, exists)
					}
					delete(std, canonical(k))
					order = slices.DeleteFunc(order, func(p pair) bool { return canonical(p.key) == canonical(k) })
					pending = slices.DeleteFunc(pending, func(p pair) bool { return canonical(p.key) == canonical(k) })
				}
				check := func() {
					t.Helper()
					checkInvariants(t, d)
					if d.Len() != len(std) || d.Len() != len(order) {
						t.Fatalf("lengths: Dict=%d, map=%d, order=%d", d.Len(), len(std), len(order))
					}
					for _, k := range pool {
						want, exists := std[canonical(k)]
						if got, found := d.Get2(k); got != want || found != exists {
							t.Fatalf("Get2(%v)=(%d,%v), map=(%d,%v)", k, got, found, want, exists)
						}
						if got := d.Get(k); got != want {
							t.Fatalf("Get(%v)=%d, map=%d", k, got, want)
						}
					}
					byValue := make(map[int][2]uint64)
					for k, v := range std {
						byValue[v] = k
					}
					i := 0
					for k, v := range d.All() {
						if i >= len(order) || v != order[i].val || bits(k) != bits(order[i].key) {
							t.Fatalf("iteration index %d: unexpected key bits %x, value %d", i, bits(k), v)
						}
						mk, exists := byValue[v]
						if !exists || canonical(k) != mk {
							t.Fatalf("ranged map lacks entry (%v, %d)", k, v)
						}
						i++
					}
					if i != len(order) {
						t.Fatalf("yielded %d entries, want %d", i, len(order))
					}
				}
				// Include repeated identical NaNs, distinct payloads, and mixed
				// finite keys before growth, compaction, and cloning are exercised.
				for repeat := 0; repeat < 3; repeat++ {
					for _, k := range pool {
						put(k)
					}
				}
				check()
				for step := 0; step < 400; step++ {
					k := pool[rng.Intn(len(pool))]
					switch rng.Intn(8) {
					case 0, 1, 2:
						put(k)
					case 3:
						del(k)
					case 4:
						d.Pack(rng.Intn(2) == 0)
					case 5:
						original := d
						d = d.Clone()
						check()
						original.Clear()
					case 6:
						pending = slices.Clone(order)
						iterating = true
						budget := 24
						for key, val := range d.SlowWriteAll() {
							if len(pending) == 0 || bits(key) != bits(pending[0].key) || val != pending[0].val {
								t.Fatalf("SlowWriteAll yielded unexpected entry (%v, %d)", key, val)
							}
							pending = pending[1:]
							burst := min(4, budget)
							for j := 0; j < burst; j++ {
								budget--
								target := pool[rng.Intn(len(pool))]
								if rng.Intn(2) == 0 {
									put(target)
								} else {
									del(target)
								}
							}
							d.Pack(true)
						}
						iterating = false
						if len(pending) != 0 {
							t.Fatalf("SlowWriteAll missed %d entries", len(pending))
						}
					case 7:
						if step%31 == 0 {
							if step%2 == 0 {
								d.Clear()
							} else {
								d.DeleteAll()
							}
							clear(std)
							order = nil
						}
					}
					check()
				}
			})
		}
	}
}

func TestNamedNaNKeys(t *testing.T) {
	type f32 float32
	type f64 float64
	type c64 complex64
	type c128 complex128
	t.Run("float32", func(t *testing.T) {
		d := NewDictFunc[f32, int](func(k f32) uint64 { return EasyHashFloat32(float32(k)) })
		checkSingleNaNKey(t, d, []f32{f32(math.Float32frombits(0x7fc00001)), f32(math.Float32frombits(0xffc00002))}, f32(1))
	})
	t.Run("float64", func(t *testing.T) {
		d := NewDictFunc[f64, int](func(k f64) uint64 { return EasyHashFloat64(float64(k)) })
		checkSingleNaNKey(t, d, []f64{f64(math.NaN()), f64(math.Float64frombits(0xfff8000000000002))}, f64(1))
	})
	t.Run("complex64", func(t *testing.T) {
		d := NewDictFunc[c64, int](func(k c64) uint64 { return EasyHashComplex64(complex64(k)) })
		nan := float32(math.NaN())
		checkSingleNaNKey(t, d, []c64{c64(complex(nan, 1)), c64(complex(2, nan))}, c64(1+2i))
	})
	t.Run("complex128", func(t *testing.T) {
		d := NewDictFunc[c128, int](func(k c128) uint64 { return EasyHashComplex128(complex128(k)) })
		checkSingleNaNKey(t, d, []c128{c128(complex(math.NaN(), 1)), c128(complex(2, math.NaN()))}, c128(1+2i))
	})
}

func TestNaNInterfaceKeyTypes(t *testing.T) {
	type namedFloat float64
	type namedComplex complex128
	// Deliberately collide all types to verify dynamic type identity is
	// preserved even when their canonical hashes happen to match.
	for _, mode := range []string{"default", "constant"} {
		t.Run(mode, func(t *testing.T) {
			d := NewDict[any, int]()
			if mode == "constant" {
				d = NewDictFunc[any, int](func(any) uint64 { return 0 })
			}
			pairs := [][2]any{
				{float32(math.NaN()), math.Float32frombits(0xffc00002)},
				{math.NaN(), math.Float64frombits(0xfff8000000000002)},
				{complex(float32(math.NaN()), float32(1)), complex(float32(2), float32(math.NaN()))},
				{complex(math.NaN(), 1), complex(2, math.NaN())},
			}
			if mode == "constant" {
				pairs = append(pairs,
					[2]any{namedFloat(math.NaN()), namedFloat(math.Float64frombits(0xfff8000000000002))},
					[2]any{namedComplex(complex(math.NaN(), 1)), namedComplex(complex(2, math.NaN()))})
			}
			for i, p := range pairs {
				if !d.Put(p[0], i+1) || d.Put(p[1], i+100) {
					t.Fatalf("type %T did not maintain exactly one NaN key", p[0])
				}
			}
			if d.Len() != len(pairs) {
				t.Fatalf("Len=%d, want %d distinct dynamic types", d.Len(), len(pairs))
			}
			for i, p := range pairs {
				if v, ok := d.Get2(p[0]); !ok || v != i+100 {
					t.Fatalf("Get2(%T)=(%d,%v), want (%d,true)", p[0], v, ok, i+100)
				}
			}
			checkInvariants(t, d)
			for _, p := range pairs {
				if !d.Del(p[1]) || d.Del(p[0]) {
					t.Fatalf("deletion of type %T did not remove exactly one key", p[0])
				}
			}
			checkInvariants(t, d)
		})
	}
}
