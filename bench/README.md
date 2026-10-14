# Native generic comparison benchmarks

All seven stores implement `map[uint64]uint64` semantics: Wormhole, Turtle,
BPTree v0.0.3, Tidwall v1.8.1, red-black tree v0.2.2, insdict v0.14.6, and the
builtin Go map. The native adapters, fresh-write values, insdict comparison,
column order, and CPU-pinning helper follow `~/wormhole/go/bench`. No store
serializes keys/values. Competitor dependencies stay in this separate module.

Turtle uses `NoLocks: true`, matching Tidwall's unlocked generic `Map` and the
serial workload. Turtle's public default enables an RWMutex. Wormhole uses its
normal `Ref` API and closes each reference after a fixture is discarded.
`BENCH_META_CACHE=true` enables Wormhole's optional metadata cache; it is off
by default, matching the supplied comparison.

The Wormhole module is replaced by the local checkout at
`../../../../wuxb45/wormhole/go` (the checkout linked by `~/wormhole/go` on this
machine). For another layout, set the path with
`go mod edit -replace=github.com/glycerine/wormhole/go=/path/to/wormhole/go`
from this directory. The Turtle replacement remains `..`, so the Turtle column
always measures the current working tree rather than a published release.

The workload and table generator follow bufftree/bench. All columns share the
same even-key population, disjoint odd misses, seed-2023 traces of 8,192 ranks,
and interface dispatch. Default population: 65,536 entries. Fresh inserts add
unique odd keys, growing from N to 2N; fixture reloading is outside timing.
Go map and insdict receive an N-entry capacity hint.

Run from `go/`:

```sh
make bench
make bench BENCH_CPU=6
TURTLE_BENCH_N=1048576 make bench BENCH_CPU=6 BENCH_TIME=300ms BENCH_COUNT=5
```

`make bench` defaults to GOMAXPROCS=1 and pins each timed worker to the first
allowed CPU. `BENCH_CPU=6` chooses CPU 6; each worker verifies and restores its
thread affinity. This pinning requires Linux, matching Wormhole's suite.

Use `make memory` from `go/` for a retained-memory table sorted from largest
to smallest, using 100,000 entries like bufftree's memory target:

```sh
make memory
make memory MEMORY_N=1048576
```

Every store receives the same scrambled `uint64` keys and `uint64` values,
with the same configuration and capacity hints as `make bench`. Each measurement
runs in a fresh process with GOMAXPROCS=1 and reports post-GC retained heap minus
the pre-load baseline, MiB, bytes/key, and extra GC pointer tracing load (bytes).
Turtle's pending writes and retained pools count toward its footprint; ordered scans and explicit
flushes are not run. This measures retained Go heap, not RSS or cumulative
allocations. From this directory, use `make -C .. memory`.

From this directory use `make -C .. bench`, or run individual cases:

```sh
go test ./...
go test -run '^$' -bench 'BenchmarkCompare(Points|Iteration)' -benchmem
TURTLE_BENCH_N=1048576 go test -run '^TestMemoryFootprint$' -v
TURTLE_BENCH_N=8192 go test -run '^$' -bench '^BenchmarkValueSizes$' -benchmem
```

`make bench` prints simplified/detailed Markdown tables using medians of three
100 ms samples by default. Normal tests skip this opt-in measurement. Scan time
is divided by actual visited keys, including short results near the end.
The simplified rows select Get hit, fresh Put, and ordered scans of up to
100,000 entries. Go map has no ordered range scan; its full traversal visits
values in unspecified order while trees use key order.

`TURTLE_BENCH_N` selects population. `TURTLE_BENCH_MEMTABLE`,
`TURTLE_BENCH_LEAF`, and `TURTLE_BENCH_FANOUT` override only Turtle's
record-count options. Defaults are 16,384 / 65,536 / 16. The table omits existing-key
updates to match Wormhole; `BenchmarkComparePoints` still includes that hot-set
workload, whose repeated 8,192-key trace fits in the default MemTable.

## Current make bench output

The seven-store baseline was measured on 2026-10-11 with CPU 6,
GOMAXPROCS=1, AMD Threadripper 3960X, linux/amd64, Go 1.26.4-pont-onethread,
65,536 entries, and medians of three 100 ms samples. Wormhole checkout:
`cd1cce2`, MetaCache=false. Turtle uses its default sizes.
[Raw output](results/read-tuning-baseline-table.txt).

| Operation (showing ns/key)    | Wormhole | Turtle | BP-Tree | tidwall/btree | red-blk tree | insdict | builtin map |
| ----------------------------- | -------: | -----: | ------: | ------------: | -----------: | ------: | ----------: |
| Tree `Get`, hit               |     60.2 |  115.4 |    98.3 |         108.1 |        199.3 |    21.5 |        17.0 |
| Tree `Get`, miss              |     55.4 |  119.5 |    99.0 |         108.5 |        218.5 |    17.7 |        15.1 |
| Tree `Put`, fresh key         |    142.1 |  128.5 |   110.5 |         181.4 |        384.5 |    53.4 |        75.4 |
| Ordered scan, maximum 10,000  |     2.51 |   2.80 |    3.08 |          3.93 |        15.79 |    5.53 |         N/A |
| Ordered scan, maximum 100,000 |     2.49 |   2.78 |    3.07 |          3.92 |        15.53 |    5.42 |         N/A |
| Full traversal                |     2.46 |   2.82 |    3.97 |          2.64 |        15.11 |    0.66 |        9.24 |

## Read/write tuning

`BenchmarkReadTuning` varies only Turtle's existing MemTableSize, LeafSize, and
Fanout options. It shares the comparison's lookup and fresh-write loops.
`GetHitAfterFresh` additionally measures hits after inserting all N fresh keys,
alternating old even keys and new odd keys. Every fresh-write confirmation uses
exactly N timed inserts, so the sample includes the complete growth from N to
2N, including any automatic flush at the end.

```sh
# Run from go/bench; CPU 6 must be allowed on your machine.
GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning$' -benchtime=100ms -bench-cpu=6
GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning/M(16384|131072|262144)$/L65536/F16/FreshPut$' -benchtime=65536x -count=5 -bench-cpu=6
```

The existing knobs help, but no tested setting closes Wormhole's point-read
gap once the data is in the tree. The full
[larger-MemTable table](results/read-tuning-memtable-table.txt), at 64K entries
and M=131072, reports 43.3 ns hit reads and 177.6 ns fresh writes versus
Wormhole's 60.7 ns and 148.9 ns in that same run. At 1,048,576 initial entries, raising LeafSize
from 65,536 to 1,048,576 improved hit reads from 498.2 to 311.0 ns/key (38% less
time), while fresh insertion rose from 168.6 to 297.8 ns/key (77% more time).
Hits after the fresh-write cycle improved from 825.0 to 566.0 ns/key. This
confirmation uses medians of three 200 ms read runs and three full N-insert
write runs; it is separate from the shorter `make bench` samples above.

```sh
# From go/: larger leaves, with writes paying for larger merges.
TURTLE_BENCH_N=1048576 TURTLE_BENCH_LEAF=1048576 make bench BENCH_CPU=6
# Keep a bounded working set in a larger hash MemTable.
TURTLE_BENCH_MEMTABLE=131072 make bench BENCH_CPU=6
```

At 65,536 entries, MemTableSize=131,072 reduced hit reads to 44.8 ns/key but
cost 201.5 ns/key for a complete fresh-write cycle. Those writes fill and flush
the MemTable: subsequent hit reads cost 148.4 ns/key. MemTableSize=262,144 keeps
even the doubled fixture in memory (44.0 ns initial hit, 51.3 ns after growth,
36.9 ns fresh insertion), but hits slow to 148.5 ns when a 131,072-entry fixture
is doubled and reaches that flush threshold. This is a working-set tradeoff,
not a persistent improvement to tree lookup. Defaults remain unchanged.

Full matrices, read-after-write controls, allocation costs, methodology, and
raw-output links are in [BENCHMARKS.md](../BENCHMARKS.md#readwrite-tuning-against-wormhole--2026-10-11).


Validate all tuning configurations against a map model before and after growth
and after an explicit flush (opt-in because it checks the full population):

```sh
TURTLE_TUNING_CHECK=1 go test -run '^TestReadTuningConfigurations$' -count=1
```

## Larger values and profiling

`BenchmarkValueSizes` compares native uint64, `[100]byte`, and `[1024]byte`
values using identical fresh-insertion traces and assignment semantics for
Turtle and Tidwall. There are no byte-API wrappers. Larger inline values are
copied through sorting and merging; the study records where this becomes costly.

Fresh-insert benchmarks label timed phases `phase=fresh-put`, excluding fixture
reloading from CPU profile selection:

```sh
go test -c -o /tmp/turtle-native.test
GOMAXPROCS=1 taskset -c 6 /tmp/turtle-native.test -test.run '^$' -test.bench 'BenchmarkComparePoints/Turtle/FreshPut$' -test.benchtime=3s -test.cpuprofile=/tmp/turtle-native.cpu
go tool pprof -top -relative_percentages -tagfocus=phase=fresh-put /tmp/turtle-native.test /tmp/turtle-native.cpu
perf stat -e cycles,instructions,branches,branch-misses,cache-references,cache-misses env GOMAXPROCS=1 taskset -c 6 /tmp/turtle-native.test -test.run '^$' -test.bench 'BenchmarkComparePoints/Turtle/FreshPut$' -test.benchtime=1000000x
```

CPU/allocation profiles and perf counters include setup unless a supported
phase filter excludes it. Allocation summaries are not filtered by CPU labels.
The custom installed Go toolchain omitted pprof; this session uses a temporary
GOROOT view at `/mnt/oldrog/tmp/turtle-goroot` with a built pprof executable.
Session caches are `/mnt/oldrog/tmp/turtle-modcache` and
`/mnt/oldrog/tmp/turtle-gocache` (GOMODCACHE/GOCACHE).
