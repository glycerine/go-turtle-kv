package insdict

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestMemoryMillionKeys compares retained heap and cumulative construction
// allocations for 1,000,000 int -> int entries. Run with:
//
//	go test -run '^TestMemoryMillionKeys$' -v -count=1
//
// Each case runs in a fresh process so previous tables and other tests cannot
// affect its baseline. Retained memory is measured after GC with the table
// still alive; total allocation includes discarded buffers from growth.
// These are heap deltas, not process RSS, and results depend on Go version and
// architecture. There are deliberately no fixed memory thresholds.
func TestMemoryMillionKeys(t *testing.T) {
	const childEnv = "INSDICT_MEMORY_TEST_CASE"
	if which := os.Getenv(childEnv); which != "" {
		runtime.GOMAXPROCS(1)
		const n = 1_000_000
		// A second collection releases transient sync.Pool contents from the
		// test runner before recording the baseline.
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		switch which {
		case "dict/grown", "dict/presized":
			var d *Dict[int, int]
			if which == "dict/presized" {
				d = NewDictSize[int, int](n)
			} else {
				d = NewDict[int, int]()
			}
			for i := 0; i < n; i++ {
				d.Put(i, i)
			}
			if d.Len() != n {
				t.Fatalf("got %d entries, want %d", d.Len(), n)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(d)
		case "map/grown", "map/presized":
			var m map[int]int
			if which == "map/presized" {
				m = make(map[int]int, n)
			} else {
				m = make(map[int]int)
			}
			for i := 0; i < n; i++ {
				m[i] = i
			}
			if len(m) != n {
				t.Fatalf("got %d entries, want %d", len(m), n)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(m)
		default:
			t.Fatalf("unknown memory case %q", which)
		}
		retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		total := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: retained=%d B (%.2f MiB), total_alloc=%d B (%.2f MiB)",
			which, retained, float64(retained)/(1<<20), total, float64(total)/(1<<20))
		return
	}

	t.Logf("%s %s/%s; 1,000,000 int -> int entries", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"dict/grown", "dict/presized", "map/grown", "map/presized"} {
		t.Run(which, func(t *testing.T) {
			cmd := exec.Command(executable, "-test.run=^TestMemoryMillionKeys$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", childEnv, which))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("memory measurement failed: %v\n%s", err, output)
			}
			t.Log(strings.TrimSpace(string(output)))
		})
	}
}
