package bench

import (
	"flag"
	"fmt"
	"github.com/glycerine/go-turtle-kv/bench/internal/cpuaffinity"
	"testing"
)

var comparisonBenchCPU = flag.Int("bench-cpu", -1, "CPU for comparison benchmarks (-1 selects the first allowed CPU)")

func comparisonCPU() (int, error) {
	cpus, err := cpuaffinity.CPUs()
	if err != nil {
		return 0, err
	}
	if *comparisonBenchCPU < 0 {
		return cpus[0], nil
	}
	for _, cpu := range cpus {
		if cpu == *comparisonBenchCPU {
			return cpu, nil
		}
	}
	return 0, fmt.Errorf("benchmark CPU %d is outside allowed CPUs %v", *comparisonBenchCPU, cpus)
}

// testing.Benchmark runs each loop on its own goroutine. Pin there, before
// fixture loading and ResetTimer, and restore on that same goroutine afterward.
func pinComparisonCases(cases []comparisonBenchmark) []comparisonBenchmark {
	for i := range cases {
		run := cases[i].run
		cases[i].run = func(b *testing.B) {
			cpu, err := comparisonCPU()
			if err != nil {
				b.Fatal(err)
			}
			restore, err := cpuaffinity.Pin(cpu)
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				b.StopTimer()
				if err := restore(); err != nil {
					b.Fatal(err)
				}
			}()
			run(b)
		}
	}
	return cases
}
