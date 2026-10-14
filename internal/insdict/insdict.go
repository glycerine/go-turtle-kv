package insdict

import (
	"cmp"
	"fmt"
	"iter"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unsafe"

	"github.com/cespare/xxhash/v2"
)

const (
	slotEmpty int  = -1
	slotDummy int  = -2
	minSize        = 8 // must be a power of two and evenly divisible by 8.
	tagEmpty  byte = 0
	tagDummy  byte = 1
	tagUsed   byte = 0x80
)

type sortedIndex struct {
	sidx []int
}

type entry[K comparable, V any] struct {
	hash uint64
	key  K
	val  V
	live bool
}

// Dict is an insertion-ordered hash map modeled on CPython's 3.7+ compact dict.
//
// For built-in comparable key types, the zero value Dict
// is perfectly usable and needs no NewDict() call. The built-in
// comparable key types are: string, int, int8, int16,
// int32, int64, uint, uint8, uint16, uint32, uint64, uintptr,
// float32, float64, complex64, complex128, bool, byte, rune. Other
// key types need the user to supply the hash function, and so require
// a call to NewDictFunc to set up. The internal defaultHash() panics to enforce this.
//
// NaNs of a given floating-point type compare equal as keys, regardless of sign or payload.
// All NaN-containing values of a given complex type also compare equal.
// Updating a key preserves its original insertion position and stored key.
//
// Pre-allocating with NewDictSize or NewDictFuncSize can save
// time and memory by avoiding table rebuilds on growth; benchmark your use.
//
// Just like the built-in Go map, we are not safe for concurrent use by default,
// and require external synchronization when a writer can race with readers.
// Sort, Ascend, and Descend may modify their sorted cache and require exclusive access.
// Other readers do not modify the data structure and so do not race with each other.
// Any number of read-only goroutines can access a Dict concurrently (those
// that do no Put/Set, no Del, no Pack, and no SlowWriteAll; only Get, Get2, Len, or All).
type Dict[K comparable, V any] struct {
	cleanSort bool
	compare   func(K, K) int // nil => default ordering
	sorted    *sortedIndex

	hash    func(K) uint64 // nil => defaultHash
	indices []int          // slotEmpty, slotDummy, or index into entries
	entries []entry[K, V]  // dense, insertion-ordered, may contain holes
	live    int            // live entry count
	mask    uint64
	tags    []byte // empty, dummy, or tagUsed | high 7 hash bits

	// Active SlowWriteAll iterations preserve entry positions across rebuilds.
	activeWriteIters int
}

func NewDict[K comparable, V any]() *Dict[K, V] {
	return &Dict[K, V]{}
}

// NewDictFunc lets callers supply a hash for key types the default doesn't know.
// Equal keys must have identical hashes, including all NaN representations for
// floating-point keys and all NaN-containing values of a given complex type.
// The EasyHashFloat* and EasyHashComplex* helpers satisfy this rule.
// An optional comparator defines Ascend order: negative for a < b, zero for
// equivalent keys, positive for a > b. It must define a consistent total order.
// EasyCompare[K] provides natural ordering for ordered types, with NaNs last.
func NewDictFunc[K comparable, V any](hash func(K) uint64, compare ...func(K, K) int) *Dict[K, V] {
	if len(compare) > 1 {
		panic("insdict: at most one comparator is allowed")
	}
	d := &Dict[K, V]{hash: hash}
	if len(compare) == 1 {
		d.compare = compare[0]
	}
	return d
}

// NewDictSize returns a Dict with room for hint entries, so inserting up to
// hint distinct keys triggers no rebuild and no reallocation.
func NewDictSize[K comparable, V any](hint int) *Dict[K, V] {
	d := &Dict[K, V]{}
	if hint > 0 {
		d.initSize(hint)
	}
	return d
}

// NewDictFuncSize is NewDictFunc with a capacity hint. A nil hash selects the
// default hash. The optional comparator defines Ascend order, as in NewDictFunc.
func NewDictFuncSize[K comparable, V any](hash func(K) uint64, hint int, compare ...func(K, K) int) *Dict[K, V] {
	d := NewDictFunc[K, V](hash, compare...)
	if hint > 0 {
		d.initSize(hint)
	}
	return d
}

// Keep the constructor small enough to inline, so a local Dict can stay on
// the stack. Entry capacity follows the requested hint rather than spare slots.
func (d *Dict[K, V]) initSize(hint int) {
	d.rebuildEntries(presizeFor(hint), hint)
}

// presizeFor returns the smallest power-of-two table size whose usable entry
// capacity (2/3 of the table) holds n entries.
func presizeFor(n int) int {
	size := minSize
	for size*2/3 < n {
		if size <= 0 || size > (math.MaxInt>>1) {
			panic("insdict: capacity hint exceeds maximum table size")
		}
		size <<= 1
	}
	return size
}

func (d *Dict[K, V]) hashOf(k K) (h uint64) {
	if d.hash != nil {
		h = d.hash(k)
	} else if iv, ok := any(k).(int); ok {
		// Keep the common integer hash inline instead of dispatching through
		// hashOf and the full defaultHash type switch on every lookup.
		h = Mix64(uint64(iv))
	} else {
		h = defaultHash(k)
	}
	return
}

// Mix64 is the splitmix64 finalizer: a good, cheap integer mixer.
// Exported so custom hash functions can use it to combine fields.
func Mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func defaultHash[K comparable](k K) uint64 {
	switch v := any(k).(type) {
	case string:
		return xxhash.Sum64String(v)
	case int:
		return Mix64(uint64(v))
	case int8:
		return Mix64(uint64(v))
	case int16:
		return Mix64(uint64(v))
	case int32: // covers rune
		return Mix64(uint64(v))
	case int64:
		return Mix64(uint64(v))
	case uint:
		return Mix64(uint64(v))
	case uint8: // covers byte
		return Mix64(uint64(v))
	case uint16:
		return Mix64(uint64(v))
	case uint32:
		return Mix64(uint64(v))
	case uint64:
		return Mix64(v)
	case uintptr:
		return Mix64(uint64(v))
	case float32:
		return EasyHashFloat32(v)
	case float64:
		return EasyHashFloat64(v)
	case complex64:
		return EasyHashComplex64(v)
	case complex128:
		return EasyHashComplex128(v)
	case bool:
		if v {
			return Mix64(1)
		}
		return Mix64(0)
	case nil:
		// allows any/interface{} as key type. this is legal Go 1.20+;
		// any is comparable at compile time. (You just get a runtime
		// panic if the any ends up holding a non-comparable).
		return 0
	}
	panic(fmt.Sprintf("insdict: no default hash for key type '%T'; use NewDictFunc", k))
}

// EasyHashString provides a default hash for strings. Currently this
// is based on cespare/xxhash, but this is subject to change if I find
// something even better.
func EasyHashString(key string) uint64 { return xxhash.Sum64String(key) }

// EasyHash* functions are a set of convenience hash functions
// to make it easy for users to call NewDictFunc
// for a given key type. It is probably faster to set the d.hash function once
// rather than to dispatch to defaultHash on every Get and doing
// reflection. After all, the type of the key is known and fixed.
// .
func EasyHashBool(key bool) uint64 {
	if key {
		return Mix64(1)
	}
	return Mix64(0)
}

func EasyHashInt(key int) uint64     { return Mix64(uint64(key)) }
func EasyHashInt8(key int8) uint64   { return Mix64(uint64(key)) }
func EasyHashInt16(key int16) uint64 { return Mix64(uint64(key)) }
func EasyHashInt32(key int32) uint64 { return Mix64(uint64(key)) }
func EasyHashInt64(key int64) uint64 { return Mix64(uint64(key)) }

func EasyHashUint(key uint) uint64     { return Mix64(uint64(key)) }
func EasyHashUint8(key uint8) uint64   { return Mix64(uint64(key)) }
func EasyHashUint16(key uint16) uint64 { return Mix64(uint64(key)) }
func EasyHashUint32(key uint32) uint64 { return Mix64(uint64(key)) }
func EasyHashUint64(key uint64) uint64 { return Mix64(key) }

func EasyHashUintptr(key uintptr) uint64 { return Mix64(uint64(key)) }

func EasyHashByte(key byte) uint64 { return Mix64(uint64(key)) }
func EasyHashRune(key rune) uint64 { return Mix64(uint64(key)) }

// float32KeyBits normalizes signed zero and all NaN representations.
func float32KeyBits(key float32) uint32 {
	if key == 0 {
		return 0
	}
	if key != key {
		return 0x7fc00000
	}
	return math.Float32bits(key)
}

func float64KeyBits(key float64) uint64 {
	if key == 0 {
		return 0
	}
	if key != key {
		return 0x7ff8000000000000
	}
	return math.Float64bits(key)
}

// EasyHashFloat32 normalizes signed zero and all NaN representations so
// equivalent dictionary keys have identical hashes.
func EasyHashFloat32(key float32) uint64 {
	return Mix64(uint64(float32KeyBits(key)))
}

// EasyHashFloat64 normalizes signed zero and all NaN representations.
func EasyHashFloat64(key float64) uint64 {
	return Mix64(float64KeyBits(key))
}

// equalNaNKeys handles floating-point and complex keys that Go's == cannot match.
// Call only after ordinary equality fails and both keys are non-reflexive.
func equalNaNKeys[K comparable](a, b K) bool {
	switch any(a).(type) {
	case float32:
		_, ok := any(b).(float32)
		return ok
	case float64:
		_, ok := any(b).(float64)
		return ok
	case complex64:
		_, ok := any(b).(complex64)
		return ok
	case complex128:
		_, ok := any(b).(complex128)
		return ok
	default:
		// Named floating-point and complex types require a custom hash but share the
		// same NaN equality. Preserve dynamic type identity for interface keys.
		x, y := reflect.ValueOf(a), reflect.ValueOf(b)
		if x.Type() != y.Type() {
			return false
		}
		return x.Kind() == reflect.Float32 || x.Kind() == reflect.Float64 ||
			x.Kind() == reflect.Complex64 || x.Kind() == reflect.Complex128
	}
}

// EasyHashComplex64 maps all NaN-containing values to one hash.
// Otherwise it normalizes zero components, packs real
// and imaginary 32-bit float bits into uint64, and mixes with Mix64.
func EasyHashComplex64(key complex64) uint64 {
	if key != key {
		return Mix64(0x7fc000007fc00000)
	}
	r := real(key)
	if r == 0 {
		r = 0
	}
	im := imag(key)
	if im == 0 {
		im = 0
	}
	return Mix64((uint64(math.Float32bits(r)) << 32) | uint64(math.Float32bits(im)))
}

// EasyHashComplex128 maps all NaN-containing values to one hash.
// Otherwise it normalizes zero components and
// mixes real and imaginary 64-bit float bits with Mix64.
func EasyHashComplex128(key complex128) uint64 {
	if key != key {
		return Mix64(0x7ff8000000000000)
	}
	r := real(key)
	if r == 0 {
		r = 0
	}
	im := imag(key)
	if im == 0 {
		im = 0
	}
	return Mix64(Mix64(math.Float64bits(r)) ^ math.Float64bits(im))
}

// Len returns the number of live entries.
func (d *Dict[K, V]) Len() int {
	if d == nil {
		return 0
	}
	return d.live
}

// Get returns the value for k, or the zero value if absent.
func (d *Dict[K, V]) Get(k K) (v V) {
	v, _ = d.Get2(k)
	return
}

// Get2 returns the value for k and whether it was present.
func (d *Dict[K, V]) Get2(k K) (v V, found bool) {
	if d == nil || d.live == 0 {
		return
	}
	var h uint64
	if d.hash != nil {
		h = d.hash(k)
	} else if iv, ok := any(k).(int); ok {
		// Keep the common integer hash inline instead of dispatching through
		// hashOf and the full defaultHash type switch on every lookup.
		h = Mix64(uint64(iv))
	} else {
		h = defaultHash(k)
	}
	tag := hashTag(h)
	i, perturb := h&d.mask, h
	for {
		// Check compact metadata first: most misses never read either array.
		ctrl := d.tags[i]
		if ctrl == tag {
			ix := d.indices[i]
			e := &d.entries[ix]
			if e.hash == h && (e.key == k || (e.key != e.key && k != k && equalNaNKeys(e.key, k))) {
				return e.val, true
			}
		} else if ctrl == tagEmpty {
			return
		}
		perturb >>= 5
		i = (i*5 + perturb + 1) & d.mask
	}
}

// On the linear congruential sequence above; llm audit reported:
//
// Summary of Correctness Verification
//
// The following core mechanisms were verified and found sound:
//
// Probe Sequence & Invariants: The CPython-style linear
// congruential sequence (i*5 + perturb + 1) & mask visits
// all slots. Load factor <= 2/3 guarantees at least 1/3 empty slots,
// preventing infinite probe cycles.
//
// Tombstone Re-use: freeSlot reuses tagDummy slots only after
// find has proven key absence. Reused slots never break
// existing probe chains.
//
// SlowWriteAll: Nested iteration, early break, mid-loop
// insertion/growth, and panics correctly preserve and
// release their active iteration count via defer.
//
// Memory & GC Cleanliness: Unused slots in compacted entries
// and deleted entries are explicitly cleared, leaving no
// stale pointers in underlying arrays.

// Occupied tags use the high bit to distinguish them from empty and dummy
// slots. Use hash bits independent of the low bits selecting the initial slot.
func hashTag(h uint64) byte { return tagUsed | byte(h>>57) }

// find returns the indices slot and entries index for k, or ix == -1 and the
// empty slot where the probe ended if k is absent. Requires d.indices != nil
// and tag == hashTag(h). Passing the tag keeps this probe loop inlineable.
func (d *Dict[K, V]) find(k K, h uint64, tag byte) (slot, ix int) {
	i, perturb := h&d.mask, h
	for d.tags[i] != tagEmpty {
		if d.tags[i] == tag {
			ix = d.indices[i]
			e := &d.entries[ix]
			if e.hash == h && (e.key == k || (e.key != e.key && k != k && equalNaNKeys(e.key, k))) {
				return int(i), ix
			}
		}
		perturb >>= 5
		i = (i*5 + perturb + 1) & d.mask
	}
	return int(i), -1
}

// usable is the maximum entry count (holes included) before a table rebuild.
func (d *Dict[K, V]) usable() int { return len(d.indices) * 2 / 3 }

// first empty-or-dummy slot on h's probe path (only call when the key is known absent)
func (d *Dict[K, V]) freeSlot(h uint64) uint64 {
	i, perturb := h&d.mask, h
	for d.tags[i] >= tagUsed {
		perturb >>= 5
		i = (i*5 + perturb + 1) & d.mask
	}
	return i
}

// Rebuild the table to size slots, compacting holes and preserving order.
// Same-size compaction reuses existing storage when capacity permits.
func (d *Dict[K, V]) rebuild(size int) {
	d.rebuildEntries(size, size*2/3)
}

func (d *Dict[K, V]) rebuildEntries(size, capacity int) {
	d.cleanSort = false
	old := d.entries
	reuse := size == len(d.indices)
	if reuse {
		clear(d.tags)
	} else {
		// Sizes are powers of two >= 8, divisible by the int width (4 or 8 bytes).
		// Allocate int indexes followed by exactly size control bytes in one
		// pointer-free, aligned allocation.
		// Limit the index slice's capacity so it cannot overlap the controls.
		const bytesPerInt = int(unsafe.Sizeof(int(0)))
		storage := make([]int, size+size/bytesPerInt) // works because size is the number of ints.
		d.indices = storage[:size:size]
		d.tags = unsafe.Slice((*byte)(unsafe.Pointer(&storage[size])), size)
	}
	for i := range d.indices {
		d.indices[i] = slotEmpty
	}
	d.mask = uint64(size - 1)
	reuseEntries := reuse && cap(old) >= capacity
	if reuseEntries {
		d.entries = old[:0]
	} else {
		d.entries = make([]entry[K, V], 0, capacity)
	}
	for i := range old {
		if !old[i].live {
			if d.activeWriteIters != 0 {
				// Preserve the position without indexing a deleted entry.
				d.entries = append(d.entries, old[i])
			}
			continue
		}
		slot := d.freeSlot(old[i].hash)
		d.indices[slot] = len(d.entries)
		d.tags[slot] = hashTag(old[i].hash)
		d.entries = append(d.entries, old[i])
	}
	if reuseEntries {
		// Compaction can leave duplicate pointer-bearing entries past the new
		// length. Clear them so later deletions can release their keys/values.
		clear(old[len(d.entries):])
	}
}

// size such that live entries fill at most ~1/3 of total table slots,
// which corresponds to ~1/2 of usable capacity (since usable capacity is 2/3 of total slots)
func sizeFor(live int) int {
	if live > (math.MaxInt-1)/3 {
		panic("insdict: entry count exceeds maximum table capacity")
	}
	size := minSize
	for size < live*3 {
		if size <= 0 || size > (math.MaxInt>>1) {
			panic("insdict: table size overflow")
		}
		size <<= 1
	}
	return size
}

// Put associates key k with value v. Updating the value associated with
// an existing key keeps the key's original insertion order.
// Put may rebuild and re-pack the underlying array. Interleaving Put with
// range All() iteration is not recommended, as it may make iteration miss elements. See
// the All docs for more information.
func (d *Dict[K, V]) Put(k K, v V) (newlyAdded bool) {
	if d.indices == nil {
		d.rebuild(minSize)
	}
	var h uint64
	if d.hash != nil {
		h = d.hash(k)
	} else if iv, ok := any(k).(int); ok {
		h = Mix64(uint64(iv))
	} else {
		h = defaultHash(k)
	}
	tag := hashTag(h)

	// existing key: overwrite in place, order position unchanged
	slot, ix := d.find(k, h, tag)
	if ix >= 0 {
		d.entries[ix].val = v
		return false
	}

	// New key: rebuild when the table's entry budget is full (holes included).
	if len(d.entries) >= d.usable() {
		count := d.live
		if d.activeWriteIters != 0 {
			// Retained tombstones consume the entry budget too.
			count = len(d.entries)
		}
		d.rebuild(sizeFor(count))
		slot = int(d.freeSlot(h))
	} else if d.live != len(d.entries) {
		// Deletions leave tombstones: prefer the first one on the probe path.
		// Without holes, find already returned the insertion slot.
		slot = int(d.freeSlot(h))
	}

	ix = len(d.entries)
	d.entries = append(d.entries, entry[K, V]{hash: h, key: k, val: v, live: true})
	d.indices[slot] = ix
	d.tags[slot] = tag
	d.live++
	d.cleanSort = false

	return true
}

// Set is the same as Put. Included for backward compatibility.
func (d *Dict[K, V]) Set(k K, v V) (newlyAdded bool) {
	return d.Put(k, v)
}

// DeleteAll quickly deletes all elements from the dictionary.
func (d *Dict[K, V]) DeleteAll() {
	if d == nil {
		return
	}
	d.cleanSort = false
	d.sorted = nil
	d.indices = nil
	d.entries = nil
	d.live = 0
	d.mask = 0
	d.tags = nil
	// Preserve activeWriteIters until the active iterations exit.
}

// Clear is the same as DeleteAll. It quickly deletes all elements
// from the dictionary.
func (d *Dict[K, V]) Clear() {
	d.DeleteAll()
}

// ResetReuse deletes all entries while retaining the table's backing arrays.
// It clears keys and values so pointer-bearing types release their references.
// Hash and comparison functions are preserved. ResetReuse must not be called
// during a SlowWriteAll iteration because it reuses entry positions.
func (d *Dict[K, V]) ResetReuse() {
	if d == nil {
		return
	}
	if d.activeWriteIters != 0 {
		panic("insdict: ResetReuse during SlowWriteAll")
	}
	d.cleanSort = false
	d.sorted = nil
	clear(d.entries)
	d.entries = d.entries[:0]
	clear(d.tags)
	// Empty tags terminate probes before indices are read. Each insertion
	// overwrites its index, so stale indices need no clearing.
	d.live = 0
}

// Del removes k and reports whether it was present.
//
// Del will not automatically re-pack the underlying table, even
// if many tombstones are present, and thus it is safe to delete
// with Del during an All iteration.
//
// After many deletions, to vacuum tombstones, you should call Pack
// manually; or you could just regularly use DelPackMaybe instead of Del.
//
// DelPackMaybe is an alternative to Del, a user convenience method
// that will automatically compact based on heuristics. It is provided for
// those times when you don't want to think very hard about when to Pack,
// but still want your tombstones cleaned up at some point.
func (d *Dict[K, V]) Del(k K) (found bool) {
	if d == nil || d.live == 0 {
		return false
	}
	h := d.hashOf(k)

	slot, ix := d.find(k, h, hashTag(h))
	if ix < 0 {
		return false
	}

	d.indices[slot] = slotDummy
	d.tags[slot] = tagDummy
	d.entries[ix] = entry[K, V]{} // zero it so the GC can release K and V
	d.live--
	d.cleanSort = false

	return true
}

// DelPackMaybe is a convenience wrapper that calls
// Del(k) followed by Pack(force=false).
// As a replacement for Del, it can save the user from having
// to think too hard about when to Pack away their tombstones.
// However it cannot be intermixed with All iteration safely
// as it calls Pack; see the comments on All.
func (d *Dict[K, V]) DelPackMaybe(k K) (found bool) {
	found = d.Del(k)
	d.Pack(false)
	return
}

// Pack may vacuum and re-pack the underlying array, removing tombstones.
// If force is false then heuristics are used, currently 75% tombstones,
// to decide whether to re-pack. If force is true then we always repack
// if there are any tombstones at all. If there are no tombstones then
// Pack is always a very fast no-op, no matter what force is. This enables preparing for
// Put during iteration (an uncommon pattern) with Pack(true):
//
// You must call Pack(true) to eliminate all tombstones before doing a
// range All() in the special case of interleaving Put calls with iteration -- otherwise
// your iteration may miss keys after a Put grows the table and
// shrinks the indexes of keys that had tombstones before them.
// Do not do both Put and Del during All iteration unless you can
// tolerate skipping over some keys unknowingly. Use SlowWriteAll
// instead of All here. See the All and SlowWriteAll docs for more.
func (d *Dict[K, V]) Pack(force bool) {
	if d == nil || d.activeWriteIters != 0 {
		return
	}
	if d.live == len(d.entries) {
		// no tombstones, do nothing.
		return
	}
	if !force && len(d.entries) > 32 && d.live < len(d.entries)/4 {
		force = true
	}
	if force {
		d.rebuild(sizeFor(d.live))
	}
}

// EasyCompare compares ordered keys, placing NaNs after all other values.
// It also supports named types and can be supplied to NewDictFunc or NewDictFuncSize.
func EasyCompare[K cmp.Ordered](a, b K) int {
	if a != a {
		if b != b {
			return 0
		}
		return 1
	}
	if b != b {
		return -1
	}
	return cmp.Compare(a, b)
}

func defaultCompare[K comparable]() func(K, K) int {
	var zero K
	switch any(zero).(type) {
	case string:
		return any(EasyCompare[string]).(func(K, K) int)
	case int:
		return any(EasyCompare[int]).(func(K, K) int)
	case int8:
		return any(EasyCompare[int8]).(func(K, K) int)
	case int16:
		return any(EasyCompare[int16]).(func(K, K) int)
	case int32:
		return any(EasyCompare[int32]).(func(K, K) int)
	case int64:
		return any(EasyCompare[int64]).(func(K, K) int)
	case uint:
		return any(EasyCompare[uint]).(func(K, K) int)
	case uint8:
		return any(EasyCompare[uint8]).(func(K, K) int)
	case uint16:
		return any(EasyCompare[uint16]).(func(K, K) int)
	case uint32:
		return any(EasyCompare[uint32]).(func(K, K) int)
	case uint64:
		return any(EasyCompare[uint64]).(func(K, K) int)
	case uintptr:
		return any(EasyCompare[uintptr]).(func(K, K) int)
	case float32:
		return any(EasyCompare[float32]).(func(K, K) int)
	case float64:
		return any(EasyCompare[float64]).(func(K, K) int)
	}
	// Named ordered types retain a reflection fallback unless a comparator is supplied.
	var compare func(K, K) int
	switch reflect.TypeFor[K]().Kind() {
	case reflect.String:
		compare = func(a, b K) int { return strings.Compare(reflect.ValueOf(a).String(), reflect.ValueOf(b).String()) }
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		compare = func(a, b K) int { return cmp.Compare(reflect.ValueOf(a).Int(), reflect.ValueOf(b).Int()) }
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		compare = func(a, b K) int { return cmp.Compare(reflect.ValueOf(a).Uint(), reflect.ValueOf(b).Uint()) }
	case reflect.Float32, reflect.Float64:
		compare = func(a, b K) int {
			x, y := reflect.ValueOf(a).Float(), reflect.ValueOf(b).Float()
			if math.IsNaN(x) {
				if math.IsNaN(y) {
					return 0
				}
				return 1
			}
			if math.IsNaN(y) {
				return -1
			}
			return cmp.Compare(x, y)
		}
	default:
		panic("insdict: Ascend requires string, integer, or floating-point keys")
	}
	return compare
}

// Sort controls the cached sorted index. Sort(true) materializes the index
// and brings it up to date if dirty. Sort(false) releases the index and marks
// it dirty; a later Sort(true), Ascend, or Descend rebuilds it on demand.
// Sort does not change insertion order. It requires exclusive access and must
// not be called during a sorted iteration. A nil receiver is a no-op.
// Sort(true) requires the same key ordering or custom comparator as Ascend.
func (d *Dict[K, V]) Sort(on bool) {
	if d == nil {
		return
	}
	if on {
		d.prepareSorted()
	} else {
		d.sorted = nil
		d.cleanSort = false
	}
}

// prepareSorted materializes the shared ascending index only when needed.
func (d *Dict[K, V]) prepareSorted() func(K, K) int {
	if d.sorted == nil {
		d.sorted = &sortedIndex{}
		d.cleanSort = false
	}
	compare := d.compare
	if compare == nil {
		compare = defaultCompare[K]()
		d.compare = compare
	}
	if !d.cleanSort {
		idx := d.sorted.sidx[:0]
		if cap(idx) < d.live {
			idx = make([]int, 0, d.live)
		}
		for i := range d.entries {
			if d.entries[i].live {
				idx = append(idx, i)
			}
		}
		d.sorted.sidx = idx
		slices.SortFunc(idx, func(a, b int) int { return compare(d.entries[a].key, d.entries[b].key) })
		d.cleanSort = true
	}

	return compare
}

// Ascend returns an iterator over live entries in ascending key order.
// With no pivots it visits all keys; with one it visits keys >= piv[0];
// with two it visits piv[0] <= key < piv[1]. More than two pivots panic.
// Pivots need not be present. Equal or reversed bounds yield no entries.
// A range break stops iteration.
// By default keys must be strings, integers, or floats (including named types);
// other key types require a custom comparator. NaNs sort last by default.
// A nil receiver is empty.
//
// The sorted index is allocated lazily and reused until keys or entry positions
// change. Value updates do not require sorting again. Ascend requires exclusive
// access because it may update the cache, even when the loop body only reads.
//
// PRE: the user must guarantee exclusive access to the Dict before
// Ascend starts, and that exclusion must continue until it finishes.
//
// The loop body may call Del on keys <= the current key without changing the
// active index. Inserting keys, packing, clearing, or starting another sorted iteration
// after deletion during a scan is not supported.
func (d *Dict[K, V]) Ascend(piv ...K) iter.Seq2[K, V] {
	if len(piv) > 2 {
		panic("insdict: Ascend accepts at most two pivots")
	}
	// Copy bounds so a caller changing a variadic slice cannot change the iterator.
	n := len(piv)
	var lower, upper K
	if n > 0 {
		lower = piv[0]
	}
	if n > 1 {
		upper = piv[1]
	}
	return func(yield func(K, V) bool) {
		if d == nil {
			return
		}
		compare := d.prepareSorted()

		// Keep positions stable until every active iterator exits, regardless
		// of exit order. The defer also releases protection on panic or Goexit.
		d.activeWriteIters++
		defer func() {
			d.activeWriteIters--
		}()

		start, end := 0, len(d.sorted.sidx)
		search := func(key K) int {
			i, _ := slices.BinarySearchFunc(d.sorted.sidx, key, func(i int, key K) int {
				return compare(d.entries[i].key, key)
			})
			return i
		}
		if n > 0 {
			start = search(lower)
		}
		if n > 1 {
			end = search(upper)
		}
		if start >= end {
			return
		}
		for _, i := range d.sorted.sidx[start:end] {
			e := &d.entries[i]
			if !yield(e.key, e.val) {
				return
			}
		}
	}
}

// Descend returns an iterator over live entries in descending key order.
// With no pivots it visits all keys; with one it visits keys <= piv[0];
// with two it visits piv[0] >= key > piv[1]. More than two pivots panic.
// Pivots need not be present. Equal or reversed bounds yield no entries.
// Ordering follows Ascend's comparator in reverse, so NaNs come first by default.
// A nil receiver is empty. A range break stops iteration.
//
// Descend shares Ascend's lazy sorted index and requires exclusive access.
// The loop body may call Del on keys >= the current key without changing the
// active index. Inserting keys, packing, clearing, or starting another sorted
// iteration after deletion during a scan is not supported.
//
// PRE: the user must guarantee exclusive access to the Dict before
// Descend starts, and that exclusion must continue until it finishes.
func (d *Dict[K, V]) Descend(piv ...K) iter.Seq2[K, V] {
	if len(piv) > 2 {
		panic("insdict: Descend accepts at most two pivots")
	}
	n := len(piv)
	var upper, lower K
	if n > 0 {
		upper = piv[0]
	}
	if n > 1 {
		lower = piv[1]
	}
	return func(yield func(K, V) bool) {
		if d == nil {
			return
		}
		compare := d.prepareSorted()

		// Keep positions stable until every active iterator exits, regardless
		// of exit order. The defer also releases protection on panic or Goexit.
		d.activeWriteIters++
		defer func() {
			d.activeWriteIters--
		}()

		idx := d.sorted.sidx
		// Upper bounds include every key equivalent to the pivot under compare.
		search := func(key K) int {
			return sort.Search(len(idx), func(i int) bool { return compare(d.entries[idx[i]].key, key) > 0 })
		}
		start, end := len(idx), 0
		if n > 0 {
			start = search(upper)
		}
		if n > 1 {
			end = search(lower)
		}
		for j := start - 1; j >= end; j-- {
			e := &d.entries[idx[j]]
			if !yield(e.key, e.val) {
				return
			}
		}
	}
}

// All iterates entries in insertion order; the order in which the
// keys were first added to the Dict.
//
// Assuming you have exclusive write access to the Dict,
// it is safe to call Del() during iteration
// since it does not auto-repack the array, but
// instead only writes a tombstone. (Compaction only happens
// when the user calls Pack manually or when Put
// grows the array and we Pack during the copy over).
//
// Put of new keys during All iteration is not recommended.
// Put could provoke a resize of the underlying array.
// The copy to the new larger array will omit tombstones. This will
// lower the index of elements that were after the tombstones. You risk missing some
// elements during the iteration (without knowing it), if there were
// tombstones present before the current iteration point.
//
// If you really must Put during iteration,
// be sure to call Pack(true) before starting All so as to force vacuuming out of all
// tombstones beforehand; and forbid Del during such iterations (that also Put).
// As noted, a Del followed by a Put can result in an internal copy
// and compaction that will lower the index of all keys that had
// tombstones before them in the array. The iterator's held index integer can
// become too large, causing some Dict entries to be missed. Since
// this is not expected to be a common use pattern, we do not contort the code to
// accommodate it. You have been warned. Update: or use SlowWriteAll instead of All.
//
// A simple alternative approach that will not mysteriously
// skip over any of the original keys while supporting both
// Put and Del during iteration is to Clone the Dict and
// iterate one copy while modifying the other. Update: or use SlowWriteAll.
//
// Note that if you only need to Put (and not Del), then Pack(true)
// once before All suffices to avoid skipped keys and the need to Clone.
// Lacking tombstones, the underlying array can be grown during
// iteration without changing any of the original index positions.
func (d *Dict[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		if d == nil {
			return
		}
		for i := 0; i < len(d.entries); i++ {
			e := &d.entries[i]
			if !e.live {
				continue
			}
			if !yield(e.key, e.val) {
				return
			}
		}
	}
}

// SlowWriteAll initiates a range iteration over all keys.
// This iteration can tolerate interleaved Put and Del without
// the risk of accidentally skipping keys (in contrast to All).
// SlowWriteAll is about 4x slower than All in our benchmarks; this is likely
// due to being less inlinable. See go test -v -run=xxx -bench=Iterate
//
// PRE-REQUISITE: the calling goroutine must ensure (through sync.Mutex.Lock,
// sync.RWMutex.Lock, or the equivalent logical guarantee such as only ever
// allowing a single goroutine to even see the Dict) that they maintain exclusive
// access to the dictionary for the entire SlowWriteAll operation. Since this
// is already a requirement for any Put or Del, this should not be
// any additional burden except to ensure that the exclusive access begins
// strictly before the first iteration of a range over SlowWriteAll.
//
// The skipping keys hazard occurs on Pack or when a Put causes the underlying array
// to grow (see comments on All); normally the copy over to a bigger array
// omits tombstones (keys that have been deleted with Del), automatically
// Pack-ing the array.
//
// SlowWriteAll takes advantage of the knowledge of exclusive Dict
// access to temporarily mark the Dict so that array growth
// (provoked by Put) will copy tombstones (created by Del) to the
// new array rather than vacuum them out. Thus
// we can accurately maintain our iteration index in the face of arbitrarily
// interleaved Put, Del, and iteration advances. Put now creates no hazard
// because it only ever extends the valid index number, never
// shrinking it.
//
// SlowWriteAll does not automatically do a Pack at its beginning or end to
// allow the user fine grain control over when Pack happens. The user
// might wish to do a Pack prior to, or after, a SlowWriteAll in order to optimize
// memory use. Be warned however that *during* a range over SlowWriteAll
// iteration, Pack is a no-op and reclaims no space, preserving
// the accuracy of the iterator's position until the iteration completes.
//
// Nested SlowWriteAll iterations and overlapping iter.Pull2 iterations are
// supported, including when they finish in a different order than they began.
// Access must still be serialized as described above.
//
// Clear and DeleteAll set the loop to terminate after the current
// round finishes, since len(entries) drops to 0.
//
// It is not recommended to Put after a Clear inside a loop; you should
// just break after a Clear or DeleteAll. If a single pass through the loop body
// does a Clear and then some Puts, those Put keys will probably be
// skipped by the currrent SlowWriteAll (but even that is not guaranteed,
// since the subsequent behavior depends on both the number of Puts and the current
// iteration point). In short, after a Clear, you should first break out of
// the loop before doing Puts to get well-defined and repeatable behavior
// with respect to key visibility.
//
// Assuming no Clear is used, newly Put keys are guaranteed to be visible
// and will appear naturally at the tail of the range after all prior keys;
// in insertion order.
//
// This means, for example, that if a loop Puts a new key during every
// iteration, then it will never terminate on its own (it will run out of memory first);
// each new key will be visited after the iteration that added it.
func (d *Dict[K, V]) SlowWriteAll() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		if d == nil || len(d.entries) == 0 {
			return
		}
		// Keep positions stable until every active iterator exits, regardless
		// of exit order. The defer also releases protection on panic or Goexit.
		d.activeWriteIters++
		defer func() {
			d.activeWriteIters--
		}()
		for i := 0; i < len(d.entries); i++ {
			e := &d.entries[i]
			if !e.live {
				continue
			}
			if !yield(e.key, e.val) {
				return
			}
		}
	}
}

// Clone creates and returns an independent and identical copy of d.
// Keys and values are copied shallowly.
//
// So, of course, if K or V contains a pointer then the clone r will contain
// an identical copy of that pointer. This is what it means to say
// that keys and values are shallow copies: referenced (pointed to) data is shared.
//
// The custom hash function (if any) and any state captured by it are also shared.
func (d *Dict[K, V]) Clone() (r *Dict[K, V]) {
	if d == nil {
		return nil
	}
	r = &Dict[K, V]{
		hash:    d.hash,
		compare: d.compare,
		indices: append([]int(nil), d.indices...),
		entries: append([]entry[K, V](nil), d.entries...),
		live:    d.live,
		mask:    d.mask,
		tags:    append([]byte(nil), d.tags...),

		// The clone has no active iterators; omit activeWriteIters.
	}
	return
}
