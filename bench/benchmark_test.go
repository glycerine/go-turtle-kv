package bench

import (
	"os"
	"strconv"

	"github.com/glycerine/go-turtle-kv"
)

var benchSink uint64

func turtleOptions() turtle.Options {
	// Match the unlocked Tidwall Map and the other single-threaded stores.
	opts := turtle.Options{NoLocks: true}
	for _, field := range []struct {
		name string
		dest *int
	}{
		{"TURTLE_BENCH_MEMTABLE", &opts.MemTableSize},
		{"TURTLE_BENCH_LEAF", &opts.LeafSize},
		{"TURTLE_BENCH_FANOUT", &opts.Fanout},
	} {
		if text := os.Getenv(field.name); text != "" {
			n, err := strconv.Atoi(text)
			if err != nil || n < 1 {
				panic("invalid " + field.name)
			}
			*field.dest = n
		}
	}
	return opts
}
