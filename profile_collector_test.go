package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// runCollectorDriver materializes the generated collector into a throwaway
// module and runs src as its test suite under the race detector. The
// collector ships as generated source and runs inside foreign modules in
// production, so only a real go toolchain run faithfully exercises its
// enter/exit stack semantics (mirrors upstream 0.9.5's
// collector_flush_test).
func runCollectorDriver(t *testing.T, src string) map[string]*timingStats {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/collector\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "collector.go"), collectorSource("collector"))
	writeFile(t, filepath.Join(dir, "collector_test.go"), src)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := append(os.Environ(), "CRAP_PROFILE_DIR="+outDir)
	if err := runGoTests(dir, []string{"test", "-count=1", "-race", "."}, env, &stdout, &stderr); err != nil {
		t.Fatalf("collector driver failed: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	return readTimings(outDir)
}

// TestCollectorNestedSelfTime is the 0.9.5 SELF regression: a nested
// instrumented call must be subtracted from the enclosing frame (flamegraph
// self-time), with inclusive TOTAL unchanged and both non-negative.
func TestCollectorNestedSelfTime(t *testing.T) {
	stats := runCollectorDriver(t, `package collector

import (
	"testing"
	"time"
)

func TestNested(t *testing.T) {
	crap4goEnter("outer")
	crap4goEnter("inner")
	time.Sleep(2 * time.Millisecond)
	crap4goExit()
	crap4goExit()
}
`)
	outer, inner := stats["outer"], stats["inner"]
	if outer == nil || inner == nil {
		t.Fatalf("missing outer/inner stats: %+v", stats)
	}
	if outer.Calls != 1 || inner.Calls != 1 {
		t.Errorf("calls = outer %d inner %d, want 1 and 1", outer.Calls, inner.Calls)
	}
	if inner.TotalMicros < 2000 {
		t.Errorf("inner total %dµs, want >= 2000 (2ms sleep)", inner.TotalMicros)
	}
	if outer.TotalMicros < inner.TotalMicros {
		t.Errorf("outer total %d < inner total %d", outer.TotalMicros, inner.TotalMicros)
	}
	if outer.TotalSelfMicros < 0 || inner.TotalSelfMicros < 0 {
		t.Errorf("negative self time: outer %d inner %d", outer.TotalSelfMicros, inner.TotalSelfMicros)
	}
	// The inner call is outer's only nested call, so self must shrink by
	// exactly its inclusive time (same integer, computed in the frame).
	if outer.TotalSelfMicros != outer.TotalMicros-inner.TotalMicros {
		t.Errorf("outer self %d != total %d - inner %d",
			outer.TotalSelfMicros, outer.TotalMicros, inner.TotalMicros)
	}
}

// TestCollectorParallelGoroutines asserts no cross-goroutine corruption:
// two goroutines interleave identical enter/exit pairs concurrently, and
// each goroutine's outer frame must lose exactly its own inner call's time.
// A shared (non-per-goroutine) stack would pop the other goroutine's frames
// and break the exact self-time equality.
func TestCollectorParallelGoroutines(t *testing.T) {
	stats := runCollectorDriver(t, `package collector

import (
	"sync"
	"testing"
	"time"
)

func TestParallel(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				crap4goEnter("pouter")
				time.Sleep(time.Millisecond)
				crap4goEnter("pinner")
				time.Sleep(time.Millisecond)
				crap4goExit()
				crap4goExit()
			}
		}()
	}
	wg.Wait()
}
`)
	outer, inner := stats["pouter"], stats["pinner"]
	if outer == nil || inner == nil {
		t.Fatalf("missing pouter/pinner stats: %+v", stats)
	}
	if outer.Calls != 10 || inner.Calls != 10 {
		t.Errorf("calls = outer %d inner %d, want 10 and 10", outer.Calls, inner.Calls)
	}
	if outer.TotalSelfMicros < 0 || inner.TotalSelfMicros < 0 {
		t.Errorf("negative self time: outer %d inner %d", outer.TotalSelfMicros, inner.TotalSelfMicros)
	}
	if outer.TotalMicros <= inner.TotalMicros {
		t.Errorf("outer total %d <= inner total %d", outer.TotalMicros, inner.TotalMicros)
	}
	if outer.TotalSelfMicros != outer.TotalMicros-inner.TotalMicros {
		t.Errorf("cross-goroutine corruption: outer self %d != total %d - inner %d",
			outer.TotalSelfMicros, outer.TotalMicros, inner.TotalMicros)
	}
}

// TestCollectorLogFormat pins the host-side line contract: one
// "<key>\t<inclusive>\t<self>" line per exit; an unmatched exit (never
// produced by the balanced instrumentation) is ignored.
func TestCollectorLogFormat(t *testing.T) {
	stats := runCollectorDriver(t, `package collector

import "testing"

func TestSingle(t *testing.T) {
	crap4goEnter("solo")
	crap4goExit()
	crap4goExit()
}
`)
	if got := stats["solo"]; got == nil || got.Calls != 1 ||
		got.TotalSelfMicros < 0 || got.TotalSelfMicros > got.TotalMicros {
		t.Errorf("solo stats wrong: calls=%d total=%d self=%d",
			got.Calls, got.TotalMicros, got.TotalSelfMicros)
	}
}
