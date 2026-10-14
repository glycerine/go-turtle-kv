package bench

import (
	"flag"
	"fmt"
	"github.com/glycerine/go-turtle-kv/bench/internal/cpuaffinity"
	"runtime"
	"testing"
)

var comparisonBenchCPU = flag.Int("bench-cpu", -1, "CPU for comparison benchmarks (-1 selects the first allowed CPU, or runs without affinity where unavailable)")

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
				// Linux failures and explicit CPU requests remain errors.
				if runtime.GOOS != "linux" && *comparisonBenchCPU == -1 {
					run(b)
					return
				}
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

func TestComparisonWithoutAffinity(t *testing.T) {
	if _, err := cpuaffinity.CPUs(); err == nil || runtime.GOOS == "linux" {
		t.Skip("requires a platform without CPU affinity")
	}
	saved := *comparisonBenchCPU
	*comparisonBenchCPU = -1
	defer func() { *comparisonBenchCPU = saved }()
	ran := false
	cases := pinComparisonCases([]comparisonBenchmark{{name: "no-affinity", run: func(b *testing.B) {
		ran = true
		for b.Loop() {
		}
	}}})
	result := testing.Benchmark(cases[0].run)
	if !ran || result.N <= 0 {
		t.Fatalf("default benchmark did not run without affinity: %+v", result)
	}
}
