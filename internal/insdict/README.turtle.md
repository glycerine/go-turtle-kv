This is a local copy of github.com/glycerine/insdict v0.14.5, under its original
MIT license (LICENSE). The MemTable imports this internal package so the port
also builds when imported by another module. Benchmark traces share this copy of insdict.Mix64.

The only production change is ResetReuse: it clears live entries and control
tags, retaining index/entry arrays, hash, comparator, and capacity. It rejects
active SlowWriteAll iterations, whose positions would otherwise be reused.
Stale indexes are safe because empty tags terminate probes before index access.
Turtle calls it only between exclusively synchronized MemTable generations.

Upstream Clear/DeleteAll behavior is unchanged. ResetReuse has focused tests
for collisions, backing-array reuse, pointer clearing, sorted-cache invalidation,
zero/nil receivers, and active-iteration rejection. The parent module's
`go test ./...` also runs the retained upstream tests.
