.PHONY: bench memory

BENCH_TIME ?= 100ms
BENCH_COUNT ?= 3
MEMORY_N ?= 100000
BENCH_CPU ?= -1
BENCH_META_CACHE ?= false
GOMAXPROCS ?= 1

bench:
	GOMAXPROCS=$(GOMAXPROCS) go -C bench test -v -run '^TestReadmeBenchmarkTable$$' -count=1 -benchtime='$(BENCH_TIME)' -timeout=15m -args -bench-table -bench-table-count='$(BENCH_COUNT)' -bench-cpu='$(BENCH_CPU)' -wormhole-meta-cache='$(BENCH_META_CACHE)'

memory:
	TURTLE_BENCH_N='$(MEMORY_N)' go -C bench test -v -run '^TestMemoryFootprint$$' -count=1 -timeout=5m -args -wormhole-meta-cache='$(BENCH_META_CACHE)'
