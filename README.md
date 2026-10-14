# Golang Turtle KV: an in-memory key/value store in Go

An in-memory ordered map following the buffered TurtleTree design in
[Astolfi et al 2026 "Dynamic read & write optimization with TurtleKV"](turtle.pdf). 

# benchmarks

The table below compares map[uint64]uint64 trees and hash tables. The first two get lines
reflect randomized searching. The last shows unsorted sequential
scan so the hash tables can really strut. All numbers are the average nanoseconds per key.
Apple silicon ARM64 numbers are shown; AMD64 numbers has similar ratios.

| Operation (report ns/key)  | Wormhole[1] | Turtle[2] | BP-Tree[3] | tidwall/btree[4] | red-black tree[5] |insdict[6]|builtin map|
| -------------------------- | ----------: | --------: | ---------: | ---------------: | --------------: | ---------: | --------: |
| Get hit                    |        18.0 |      57.6 |       51.3 |             56.9 |           102.0 |        5.6 |       4.0 |
| Get miss                   |        18.1 |      61.9 |       61.7 |             56.6 |           129.2 |        5.5 |       4.4 |
| Put fresh                  |        48.4 |      46.1 |       66.4 |             96.8 |           185.8 |       18.8 |      34.4 |
| Ordered scan               |        1.28 |      1.28 |       1.35 |             1.79 |            9.93 |       1.97 |       N/A |
| Unordered scan             |        1.32 |      1.28 |       1.94 |             1.03 |            9.83 |       0.31 |      4.68 |

In these micro benchmarks: 

Compared to the fast in memory B-tree tidwall/btree, Wormhole is 2x as fast at Puts (writing keys; 96.8 / 48.4 == 2.0),
and 3.1x faster at Gets (reading keys; 56.6/18.1 == 3.1).

Although the Turtle design matches Wormhole for writes, its reads are still 3x slower. The BP-Tree does not
come close to Wormhole on either readers or writes, and is restricted to a single goroutine.

Full table scan performance is dominated by the insdict, where pre-fetching sequential array elements makes it
15x faster than the built-in Go map (4.68/0.31 = 15.1). Wormhole and friends are 3x faster than the built in map,
and all support ordered scans and range queries.

[1] Wormhole is: https://github.com/glycerine/go-wormhole-kv

[2] Turtle is this repo; https://github.com/glycerine/go-turtle-kv

[3] BP-tree: https://github.com/glycerine/bufftree

[4] tidwall/btree is: https://github.com/tidwall/btree

[5] the red-black tree used was: https://github.com/glycerine/rbtree

[6] insdict.Dict is: https://github.com/glycerine/insdict


# Go API stores generic keys and values

```go
import turtle "github.com/glycerine/go-turtle-kv"

m := turtle.NewMap[uint64, uint64](turtle.Options{})
m.Put(42, 100)
value, found := m.Get(42)
_, _ = value, found
m.Scan(40, 10, func(key, value uint64) bool {
    return true // false stops iteration after this entry
})
for key, value := range m.All() {
    _, _ = key, value
}
m.Delete(42)
m.Flush()
m.Compact()
```

The zero value `turtle.Map[K,V]` is usable. Keys support `cmp.Ordered`, including
named integer, float, and string types. Ordering follows `cmp.Compare`: NaNs
compare equal and sort before other floats; signed zeros compare equal.
`Scan(start, limit, visit)` visits keys >= start, in order, and returns the
callback count. Nonpositive limits visit nothing. `All` includes every key,
including negative keys and NaNs. `Len` traverses the merged live entries.

Put and Get use normal Go assignment semantics. Integer and array values are
stored inline. Slices, maps, pointers, and other referenced data share their
backing objects; the map does not deep-copy them. Returned values have no
arena-view lifetime restriction. A `sync.RWMutex` protects all operations by
default, including on a zero-value Map: readers run concurrently, and writers
have exclusive access. `Scan` and each `All` iteration hold a read lock throughout
callbacks; callbacks and loop bodies must not call methods on the same map,
because even a nested read can deadlock when a writer is waiting. Creating an
`All` iterator does not lock or take a snapshot; iteration acquires the lock.

For timestamp cleanup, use `ScanMut` or `AllMut`. They hold an exclusive lock
throughout traversal, and provide scoped entries whose `Delete` method uses
that existing lock:

```go
// Here keys are timestamps; remove every entry older than cutoff.
for entry := range m.AllMut() {
    if entry.Key >= cutoff {
        break
    }
    entry.Delete()
}
// Or bound a mutable scan by its starting key and number of visited entries:
m.ScanMut(0, 1000, func(entry *turtle.MutableEntry[uint64, uint64]) bool {
    if entry.Key >= cutoff {
        return false
    }
    entry.Delete()
    return true
})
```

`Delete` takes effect immediately and repeated calls on the current entry are
harmless. Entries may only be used for deletion in their own callback or loop
body; calling `Delete` later panics. Key and Value fields are copies: assigning
to them does not change the stored entry. Callbacks may mutate referenced value
data under the write lock, but must not call Map methods such as `m.Delete`.
Mutable traversal copies at most 128 key/value pairs at a time before callbacks,
then resumes strictly past the last visited key. This allows deletes to flush
or split the tree without invalidating the traversal, including across NaNs
and negative keys. Creating `AllMut` does not lock; iteration acquires the lock.

Use `turtle.Options{NoLocks: true}` when you provide synchronization at a higher
level. In that mode, writes and mutable traversals require exclusive access;
concurrent read-only calls remain safe. Mutating referenced values still needs
caller synchronization. Do not copy a Map after first use.

This replaces the previous byte-oriented Store API. There is no encoding
adapter, byte arena, offset decoding, or architecture-specific prefetch path.
The MemTable and buffered tree remain; this experiment changes representation,
not the ingestion algorithm. For `Map[uint64,uint64]`, each record is 24 bytes:
a key, a value, and a sequence number whose low bit represents a tombstone.
Separators store keys alone. Native pointer-free keys/values produce pointer-free
record/index arrays; reference-bearing generic types are scanned normally by Go.

The MemTable uses a local copy of insdict v0.14.5, with native
keys mapping to integer positions in a dense record slice. ResetReuse retains
its backing storage across flushes. Hash selection happens once per map and
supports named scalar keys without per-operation reflection. Numeric batches
use radix sorting; string batches use full ordered comparisons.

Interior nodes keep sorted runs in binary-carry levels and flush the child with
the most pending entries. Leaf merges resolve versions and remove tombstones;
splits propagate through integer child indexes. Scans lazily open subtrees and
merge sorted runs, using an immutable cached MemTable snapshot with a bounded
write delta. Read-only callers publish snapshots atomically.

Record buffers remain pooled. Only exclusively owned batches/interior runs
enter the pool; shared leaf storage does not. The pool limits retained payload
to less than 32 MiB across its size classes, independently of value size.
Reference-bearing scratch and recycled buffers are cleared to release old
values. Compact rebuilds live entries and discards sparse leaves and obsolete
versions; it temporarily needs both maps in memory. Reset releases storage and
preserves configuration.

Zero option fields select **16,384 MemTable records, 65,536 leaf records, and
fanout 16**. These sizes count records, so large inline values can make batches
and leaves expensive to move. The large-value benchmark shows that a typed
value arena is worth investigating; it is not implemented here. Flushes are
synchronous, sparse leaves are not automatically merged, and durability,
recovery, snapshots, filters, and background compaction are not implemented.

Run library checks from this directory:

```sh
go test ./...
go test -race ./...
go vet ./...
go test -run '^$' -fuzz '^FuzzNativeMap$' -fuzztime=30s -parallel=1
go test -run '^$' -fuzz '^FuzzMemTableScanCache$' -fuzztime=30s -parallel=1
TURTLE_MEMORY_UPDATES=4194304 go test -run TestUpdateMemoryFootprint -v
TURTLE_LATENCY_UPDATES=4194304 go test -run TestUpdateLatency -v
```

The separate [bench module](bench/README.md) contains all competitor dependencies
and the `map[uint64]uint64` comparison suite. From this directory:

```sh
make bench
make memory # 100,000 entries, sorted by retained heap
GOMAXPROCS=1 taskset -c 6 make bench
```

See [BENCHMARKS.md](BENCHMARKS.md) for before/after results, larger-population
reads, large-value tradeoffs, memory measurements, and profiling evidence.


The local comparison now includes Wormhole and insdict. `make bench BENCH_CPU=6`
pins each timed worker to CPU 6 with GOMAXPROCS=1. Existing size options can favor
reads: for example, `TURTLE_BENCH_LEAF=1048576` improves 1M-key tree lookups at
the cost of slower merges on writes. A larger MemTable can keep a small working
set in its hash table, but that advantage changes at flush boundaries.
The [read/write tuning study](BENCHMARKS.md#readwrite-tuning-against-wormhole--2026-10-11)
records the measured tradeoffs; defaults remain unchanged.

---------------
Copyright (C) 2026, Jason E. Aten, Ph.D.

License: Apache2. See the LICENSE file.
