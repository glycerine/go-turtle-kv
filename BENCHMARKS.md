# Native generic representation study — 2026-10-10

The library now stores native generic keys/values and replaces the byte API.
This improves uint64 fresh insertion and scanning substantially. It does not
win every workload: point reads remain slower than Tidwall at 1M keys, and
large inline values make sorting/merging expensive. A typed value arena remains
a useful follow-up experiment for large values.

All pinned runs below use CPU 6, GOMAXPROCS=1, AMD Threadripper 3960X,
linux/amd64, and Go 1.26.4-pont-onethread. Timings ran serially. The earlier ordinary `make bench` output recorded below used no CPU pinning
or explicit worker limit. The current [bench README](bench/README.md) and the
read/write tuning section at the end use pinned workers. Do not interchange
absolute numbers from different runtime settings.

## What changed

`Map[K cmp.Ordered,V any]` stores K and V inline. A uint64 record is 24 bytes,
including its version/tombstone stamp; the byte implementation used a 40-byte
record plus 16 payload bytes for a uint64 key/value pair. Separators now hold
keys only. Native keys go directly into the insdict index, without xxhashing
encoded bytes or resolving keys through arena offsets.

Integer child indexes, pooled record slices, the hash MemTable, numeric radix
sorting, binary-carry interior buffers, and lazy scan merging remain. This
isolates a representation change; it does not remove the MemTable. String keys
use full comparisons. Numeric hashing/radix dispatch supports named scalar
types and is selected once per map. Values follow Go assignment semantics;
pointer-bearing values remain visible to the GC. Recycled reference-bearing
buffers are cleared, and pooled payload is bounded independently of value size.

The byte arena, borrowed-view contract, serialization adapters, and arena-value
prefetch implementation were removed. Durability and snapshots remain outside
this in-memory port.

## Matched before/after table

A saved binary of the previous byte implementation and the generic binary ran
serially with identical 65,536-entry populations, traces, runtime settings,
three 100 ms samples, and default record-count options (16,384 / 65,536 / 16).
The competitors use native uint64 throughout. The byte Turtle column includes
its former encoding adapter; generic Turtle stores integers directly.

| Operation, ns/key | Byte Turtle | Generic Turtle | Native Tidwall (generic run) |
|---|---:|---:|---:|
| Get hit | 154.4 | 120.6 | 109.6 |
| Get miss | 146.4 | 119.8 | 114.2 |
| Update existing key | 43.4 | 20.7 | 118.0 |
| Fresh Put | 164.5 | 102.1 | 158.6 |
| Ordered scan, maximum 10,000 | 13.12 | 2.82 | 3.89 |
| Ordered scan, maximum 100,000 | 13.00 | 2.79 | 3.83 |
| Full ordered traversal | 13.07 | 2.76 | 2.36 |

Fresh Put improves about 38% and the longer scan about 79% in this matched
comparison. Get improves about 22%, while remaining slower than native Tidwall.
Tidwall's full traversal is still faster. Other-store columns and raw tables:
[baseline](profiles/generic-baseline-table.txt),
[generic](profiles/generic-native-table.txt).

## Longer samples and population size

Point traces repeat 8,192 uniform ranks. Hits/updates use loaded even keys;
fresh inserts add unique odd keys while growing from N to 2N. Construction and
fixture reloading are outside timing, with pending maintenance inside timed Put.
The update hot set fits in Turtle's MemTable: those results primarily measure
pending-entry overwrites, not uniform sustained writes across the whole tree.

64K medians use five 500 ms samples; 1M medians use three 300 ms samples:

| Operation, ns/key | Turtle 64K | Tidwall 64K | Turtle 1M | Tidwall 1M |
|---|---:|---:|---:|---:|
| Get hit | 120.2 | 109.8 | 476.7 | 185.9 |
| Update existing key | 20.64 | 124.6 | 19.78 | 193.1 |
| Fresh Put | 103.4 | 159.0 | 161.9 | 369.0 |

Fresh-insert medians favor Turtle at both sizes. The 1M read result shows a
remaining problem that representation alone does not solve: buffered runs and
large leaves impose read work. The root cause needs a separate read profile;
these write profiles do not establish its precise breakdown.

Raw samples: [64K](profiles/generic-native-points-64k.txt),
[1M](profiles/generic-native-points-1m.txt).
Machine-readable medians: [generic-summary.json](profiles/generic-summary.json).

## Large inline values

Native uint64-key stores compare uint64, `[100]byte`, and `[1024]byte` values
with assignment semantics and identical fresh-key traces. Medians of three
100 ms samples, initial N=8,192, growing to 16,384. Options remain the defaults.
At this size the initial population stays in Turtle's MemTable; the first flush
occurs at 16,384 distinct keys and its work is inside the insertion timer.
This is a different population/configuration interaction from the 64K tables.

| Value type, ns/fresh key | Turtle | Tidwall |
|---|---:|---:|
| uint64 | 158.2 | 122.0 |
| [100]byte | 343.4 | 254.2 |
| [1024]byte | 3,557 | 1,606 |

Turtle loses even with uint64 at this small population, and its cost grows more
steeply with value size. Copying whole values through radix passes, merges, and
method calls is an expense worth targeting. A typed value arena could keep
small value indexes in these records, trading copying for indirection and
reclamation work. No typed arena was added in this experiment.
[Raw samples](profiles/generic-value-sizes.txt).

Reproduce from `go/bench`:

```sh
TURTLE_BENCH_N=8192 GOMAXPROCS=1 taskset -c 6 go test -run '^$' -bench '^BenchmarkValueSizes$' -benchtime=100ms -count=3 -benchmem
```

## Memory and profiling

At 1M uint64 keys/values, signed process-metric deltas after Go GC:

| Store | Retained heap bytes/key | GC-scannable bytes/key |
|---|---:|---:|
| Turtle | 42.6 | 0.003 |
| BPTree | 23.2 | 0.37 |
| builtin Go map | 36.1 | 0.06 |
| Tidwall | 24.7 | 1.36 |
| red-black tree | 64.0 | 40.00 |

Turtle's pointer-free records keep scanning small, but its retained heap is
larger than Tidwall's. The measurement includes pooled scratch and MemTable
capacity, uses the comparison constructors/capacity hints, and keeps stores
alive after measurement. Runtime metric deltas are not complete allocator
accounting. [Raw memory diagnostic](profiles/generic-memory-1m.txt).

The generic fresh-write CPU profile selects `phase=fresh-put`, excluding
fixture reloads. Of 3.93 seconds of matching samples, sorting accounts for
52.42% flat / 54.45% cumulative; merging for 8.40% flat / 12.47% cumulative.
Insdict Get/Put account for 8.40% / 8.65% cumulative. These fractions overlap;
they must not be summed. Sorting remains the largest measured write cost.
Percentages from faster/slower binaries do not compare absolute operation costs.

Profiles: [CPU](profiles/generic-fresh.cpu.txt),
[allocation space](profiles/generic-fresh.alloc.txt),
[profile run](profiles/generic-fresh-profile-run.txt).
Allocation summaries include fixture loading and are not phase-filtered.
[Perf counters](profiles/perf-generic-fresh.txt) likewise include setup.
See [bench README](bench/README.md) for profiling commands.

## Validation and scope

Both modules pass tests, race checks, and vet. Numeric/string tests cover signed
keys, named types, integer widths, infinities, signed zeros, NaN equivalence,
radix ordering, zero values, structural splits, tombstones, and full traversal.
String map-model tests retain buffered-version, collision, scan-boundary, and
concurrent snapshot-publication coverage. Reference-value tests check assignment
semantics and clearing of reused buffers. Native-map, string-model, and scan-cache fuzz runs
pass 10,000 requested executions each. ARM64 and 386 tests compile. A 386
execution attempt was blocked by this environment with `Bad system call`,
so cross-architecture execution is not claimed. The library's external module requirement
remains xxhash through the local insdict copy; competitors remain in `bench/`.

The previous byte/C++ comparison artifacts remain historical evidence under
`profiles/`. They do not establish performance or API claims for this generic
implementation. The C++ harness remains a standalone comparison utility;
current native uint64 results compare the Go stores in the shared suite.


## Read/write tuning against Wormhole — 2026-10-11

`make bench` now includes the native seven-store comparison from
`~/wormhole/go/bench`: Wormhole, current local Turtle, BPTree, Tidwall, red-black
tree, insdict, and builtin map. It shares the native key permutation, seed-2023
8,192-rank traces, fresh values, scan counting, and per-worker affinity checks.
Wormhole is the local `cd1cce2` checkout with MetaCache=false; insdict is v0.14.6.
The separate bench module owns both new dependencies. The main module's
production API, algorithm, defaults, and dependency graph are unchanged.

All new performance runs are serial on CPU 6 with GOMAXPROCS=1, on the same
Threadripper and Go toolchain described above. The `make bench` target now
defaults to one worker and per-worker CPU pinning; these runs explicitly
select CPU 6 with `BENCH_CPU=6`. Older unpinned results above remain historical. The new seven-column
[baseline table](bench/results/read-tuning-baseline-table.txt) uses three
100 ms samples, N=65,536, and default Turtle sizes: hit reads 115.4 ns, misses
119.5 ns, fresh writes 128.5 ns, ordered scans up to 100K 2.78 ns/key. Wormhole
measures 60.2 / 55.4 / 142.1 / 2.49 respectively in that same table.

### Existing-knob confirmation

M, L, F below mean MemTableSize, LeafSize, and Fanout, in records. Reads are
measured immediately after the N-key even-key fixture load. `Hit after growth`
is measured after adding all N fresh odd keys, alternating probes of old and new
keys from the same 8,192-rank trace. No fixture is explicitly flushed or
compacted. Fresh-write runs time exactly N inserts, so they cover all growth
from N to 2N and include the final automatic flush when one occurs. Setup is
excluded. This avoids hiding a large flush outside a short timed sample.

N=65,536; medians of five 300 ms read samples and five 65,536-insert samples;
all timings ns/key:

| M / L / F | Hit | Miss | Fresh Put | Hit after growth |
| --- | ---: | ---: | ---: | ---: |
| 16384 / 65536 / 16 (defaults) | 135.2 | 135.7 | 106.7 | 266.4 |
| 65536 / 256 / 256 | 129.3 | 123.9 | 389.0 | 133.6 |
| 131072 / 65536 / 16 | 44.77 | 20.66 | 201.5 | 148.4 |
| 262144 / 65536 / 16 | 43.99 | 19.93 | 36.94 | 51.31 |

[Read samples](bench/results/read-tuning-finalists-64k.txt),
[complete fresh-write cycles](bench/results/read-tuning-fresh-cycle-64k.txt).
These confirmation samples differ from the table runner's shorter timed runs;
compare configurations within a confirmation rather than mixing absolute
numbers across different runs. The exploratory
[initial sweep](bench/results/read-tuning-sweep-64k.txt) includes additional
smaller leaf, fanout, and MemTable combinations. Most made initial reads worse.

N=1,048,576; medians of three 200 ms read samples and three 1,048,576-insert
samples; all timings ns/key:

| M / L / F | Hit | Miss | Fresh Put | Hit after growth |
| --- | ---: | ---: | ---: | ---: |
| 16384 / 65536 / 16 (defaults) | 498.2 | 645.5 | 168.6 | 825.0 |
| 65536 / 256 / 256 | 374.7 | 468.1 | 236.3 | 879.0 |
| 16384 / 1048576 / 16 | 311.0 | 271.4 | 297.8 | 566.0 |
| 131072 / 65536 / 16 | 548.0 | 748.2 | 156.9 | 655.4 |
| 262144 / 65536 / 16 | 408.9 | 544.3 | 157.6 | 610.2 |

[Read samples](bench/results/read-tuning-finalists-1m.txt),
[complete fresh-write cycles](bench/results/read-tuning-fresh-cycle-1m.txt).

Larger leaves reduce read time in the 1M fixture, but increase merging and
allocation work: fresh writes allocate 751 B/key versus 158 B/key for default
leaves. These are cumulative allocation bytes per insertion, not retained heap.
The large leaf setting takes 38% less time for initial hits, 58% less for misses,
and 31% less for hits after growth, while fresh-write time rises by 77%.
This is a measurable read/write tradeoff, rather than an across-the-board win.

The exceptionally fast large-MemTable results at 64K keep the entire working
set in the hash table. They disappear at its flush threshold. With M=262144,
an N=131072 fixture doubles to exactly M and then flushes: subsequent hits cost
148.5 ns/key, compared with 51.31 ns when doubling N=65536 stays below M.
[Threshold control](bench/results/read-tuning-memtable-boundary.txt).
Increasing M therefore postpones tree reads and shifts the cost into a larger
batch; it does not make the underlying tree match Wormhole. Defaults are
preserved, and both alternatives remain ordinary Options/env overrides.

### Reproducing the study

Run the table from `go/`:

```sh
make bench BENCH_CPU=6
TURTLE_BENCH_MEMTABLE=131072 make bench BENCH_CPU=6
TURTLE_BENCH_N=1048576 TURTLE_BENCH_LEAF=1048576 make bench BENCH_CPU=6
```

Run confirmation benchmarks from `go/bench`:

```sh
GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning$' -benchtime=100ms -bench-cpu=6
GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning/M(16384|65536|131072|262144)$/L(65536|256)$/F(16|256)$/(GetHit|GetMiss|GetHitAfterFresh)$' -benchtime=300ms -count=5 -bench-cpu=6
GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning/M(16384|65536|131072|262144)$/L(65536|256)$/F(16|256)$/FreshPut$' -benchtime=65536x -count=5 -bench-cpu=6
TURTLE_BENCH_N=1048576 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning/M(16384|65536|131072|262144)$/L(65536|1048576|256)$/F(16|256)$/(GetHit|GetMiss|GetHitAfterFresh)$' -benchtime=200ms -count=3 -bench-cpu=6
TURTLE_BENCH_N=1048576 GOMAXPROCS=1 go test -run '^$' -bench '^BenchmarkReadTuning/M(16384|65536|131072|262144)$/L(65536|1048576|256)$/F(16|256)$/FreshPut$' -benchtime=1048576x -count=3 -bench-cpu=6
```

[Machine-readable samples and medians](bench/results/read-tuning-summary.json)
retain the individual observations. The timings use NoLocks=true on Turtle,
normal Wormhole Ref operations, and no Wormhole metadata cache. Seven-store
correctness checks verify point operations, updates, fresh growth, and numeric
ordered scans. CPU-affinity tests verify pinning and restoration.


Additional controls confirm the interpretation:

- [Larger-MemTable full table](bench/results/read-tuning-memtable-table.txt),
  N=65536 and M=131072: Turtle hit/miss/fresh/scan100K =
  43.3 / 19.2 / 177.6 / 2.91 ns/key; Wormhole =
  60.7 / 54.4 / 148.9 / 2.49 in that same run. This uses the table's three
  100 ms samples, not the fixed-cycle confirmations.
- [Wormhole/Turtle lookup control](bench/results/read-tuning-controls-1m.txt),
  N=1048576, three 300 ms runs: Wormhole hit/miss =
  86.31 / 79.13 ns/key; default Turtle = 478.8 / 647.8 ns/key.
  [Complete-cycle fresh control](bench/results/read-tuning-control-fresh-1m.txt)
  records Wormhole at 355.3 ns/key and default Turtle at 169.6 ns/key,
  with three full-population write runs.
- [Large leaves at 64K](bench/results/read-tuning-large-leaf-64k.txt) and
  [fresh cycles](bench/results/read-tuning-large-leaf-fresh-64k.txt) show how
  L=1048576 behaves on the smaller population; this keeps reads in the tree
  while avoiding the default leaf split at N-to-2N growth. Medians of three
  runs: hit/miss = 115.4 / 117.5 ns/key, hit after growth = 131.6 ns/key,
  fresh Put = 187.6 ns/key.

The opt-in [tuning correctness check](bench/results/read-tuning-correctness.txt)
verifies every configuration at the full 64K population, against a builtin map,
before growth, after all fresh inserts, and after an explicit flush. It also
checks complete ordered traversal. Run it with:

```sh
TURTLE_TUNING_CHECK=1 go test -run '^TestReadTuningConfigurations$' -count=1
```

The [1M-key model check](bench/results/read-tuning-correctness-1m.txt) also
passes for the default and larger-leaf configurations.
