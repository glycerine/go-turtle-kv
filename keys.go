package turtle

import (
	"cmp"
	"math"
	"reflect"
	"unsafe"

	"github.com/glycerine/go-turtle-kv/internal/insdict"
)

// K is ordered, so its representation is a string or a numeric scalar. Select
// operations once per map. Representation reads support named scalar types
// without boxing or reflecting on each lookup; no pointer escapes these calls.
func keyHash[K cmp.Ordered]() func(K) uint64 {
	switch reflect.TypeFor[K]().Kind() {
	case reflect.String:
		return func(k K) uint64 { return insdict.EasyHashString(*(*string)(unsafe.Pointer(&k))) }
	case reflect.Float32:
		return func(k K) uint64 { return insdict.EasyHashFloat32(*(*float32)(unsafe.Pointer(&k))) }
	case reflect.Float64:
		return func(k K) uint64 { return insdict.EasyHashFloat64(*(*float64)(unsafe.Pointer(&k))) }
	}
	switch unsafe.Sizeof(*new(K)) {
	case 1:
		return func(k K) uint64 { return insdict.Mix64(uint64(*(*uint8)(unsafe.Pointer(&k)))) }
	case 2:
		return func(k K) uint64 { return insdict.Mix64(uint64(*(*uint16)(unsafe.Pointer(&k)))) }
	case 4:
		return func(k K) uint64 { return insdict.Mix64(uint64(*(*uint32)(unsafe.Pointer(&k)))) }
	default:
		return func(k K) uint64 { return insdict.Mix64(*(*uint64)(unsafe.Pointer(&k))) }
	}
}

// Numeric order bits support radix sorting. Strings use the generic comparator.
// Signed integers flip their sign bit; floats normalize zero and NaNs, matching
// cmp.Compare's total ordering (all NaNs equal and before other values).
func keyOrderBits[K cmp.Ordered]() func(K) uint64 {
	kind := reflect.TypeFor[K]().Kind()
	switch kind {
	case reflect.String:
		return nil
	case reflect.Float32:
		return func(k K) uint64 {
			f := *(*float32)(unsafe.Pointer(&k))
			if f != f {
				return 0
			}
			if f == 0 {
				f = 0
			}
			b := math.Float32bits(f)
			if b>>31 != 0 {
				return uint64(^b)
			}
			return uint64(b ^ (1 << 31))
		}
	case reflect.Float64:
		return func(k K) uint64 {
			f := *(*float64)(unsafe.Pointer(&k))
			if f != f {
				return 0
			}
			if f == 0 {
				f = 0
			}
			b := math.Float64bits(f)
			if b>>63 != 0 {
				return ^b
			}
			return b ^ (uint64(1) << 63)
		}
	}
	signed := kind >= reflect.Int && kind <= reflect.Int64
	switch unsafe.Sizeof(*new(K)) {
	case 1:
		var sign uint8
		if signed {
			sign = 1 << 7
		}
		return func(k K) uint64 { return uint64(*(*uint8)(unsafe.Pointer(&k)) ^ sign) }
	case 2:
		var sign uint16
		if signed {
			sign = 1 << 15
		}
		return func(k K) uint64 { return uint64(*(*uint16)(unsafe.Pointer(&k)) ^ sign) }
	case 4:
		var sign uint32
		if signed {
			sign = 1 << 31
		}
		return func(k K) uint64 { return uint64(*(*uint32)(unsafe.Pointer(&k)) ^ sign) }
	default:
		var sign uint64
		if signed {
			sign = 1 << 63
		}
		return func(k K) uint64 { return *(*uint64)(unsafe.Pointer(&k)) ^ sign }
	}
}

func typeHasPointers(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array:
		return typeHasPointers(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if typeHasPointers(t.Field(i).Type) {
				return true
			}
		}
		return false
	case reflect.String, reflect.Slice, reflect.Map, reflect.Pointer, reflect.UnsafePointer, reflect.Interface, reflect.Func, reflect.Chan:
		return true
	default:
		return false
	}
}
