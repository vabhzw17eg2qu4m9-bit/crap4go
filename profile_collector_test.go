package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runCollectorShards materializes the generated collector into a throwaway
// module and runs src as its test suite under the race detector, returning
// the collector output dir and its shard files (name → contents). The
// collector ships as generated source and runs inside foreign modules in
// production, so only a real go toolchain run faithfully exercises its
// enter/exit stack and flush semantics (mirrors upstream 0.9.5's
// collector_flush_test and ad4f27b's shard tests).
func runCollectorShards(t *testing.T, extraEnv []string, src string) (string, map[string]string) {
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
	env := append(append(os.Environ(), "CRAP_PROFILE_DIR="+outDir), extraEnv...)
	if err := runGoTests(dir, []string{"test", "-count=1", "-race", "."}, env, &stdout, &stderr); err != nil {
		t.Fatalf("collector driver failed: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	shards := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(outDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		shards[e.Name()] = string(data)
	}
	return outDir, shards
}

// runCollectorDriver is runCollectorShards + the host merge.
func runCollectorDriver(t *testing.T, src string) map[string]*timingStats {
	t.Helper()
	outDir, _ := runCollectorShards(t, nil, src)
	return readTimings(outDir)
}

// shardFlushes returns the '#flushes' diagnostic of the run's single
// shard file.
func shardFlushes(t *testing.T, shards map[string]string) int64 {
	t.Helper()
	if len(shards) != 1 {
		t.Fatalf("want exactly one shard file, got %v", keysOf(shards))
	}
	var doc map[string]map[string]int64
	for _, content := range shards {
		if err := json.Unmarshal([]byte(content), &doc); err != nil {
			t.Fatalf("shard is not the cumulative JSON map: %v\n%s", err, content)
		}
	}
	f, ok := doc["#flushes"]
	if !ok {
		t.Fatalf("missing #flushes diagnostic: %v", keysOf(shards))
	}
	return f["calls"]
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
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

// TestCollectorShardFormat pins the shard contract: one per-writer shard
// file (prof-<pid>-<shard>.json — per-pid ownership, no cross-process
// locking needed), no in-flight tmp residue, holding the cumulative JSON
// map with the '#flushes' diagnostic the host merge skips.
func TestCollectorShardFormat(t *testing.T) {
	_, shards := runCollectorShards(t, nil, `package collector

import "testing"

func TestSingle(t *testing.T) {
	crap4goEnter("solo")
	crap4goExit()
	crap4goExit() // unmatched: never produced by the balanced instrumentation
}
`)
	name, content, ok := "", "", false
	for n, c := range shards {
		name, content, ok = n, c, true
	}
	if !ok {
		t.Fatal("no shard file written")
	}
	if !regexp.MustCompile(`^prof-\d+-collector-\d+\.json$`).MatchString(name) {
		t.Errorf("shard name %q does not match prof-<pid>-<shard>.json", name)
	}
	if strings.Contains(name, ".tmp.") {
		t.Errorf("in-flight tmp file left behind: %q", name)
	}
	if got := shardFlushes(t, shards); got != 1 {
		t.Errorf("#flushes = %d, want 1 (one outermost exit, one flush)", got)
	}
	if !strings.Contains(content, `"solo"`) {
		t.Errorf("cumulative stats missing from shard:\n%s", content)
	}
}

// TestCollectorOutermostOnlyFlush is the ad4f27b observer-effect contract:
// nested exits write NOTHING to the filesystem — the inner call's exit
// cannot flush, and the outermost exit is the only flush point. The
// cumulative snapshot then carries both methods (an inner caller that
// dies before its outer completes is covered by the backstop).
func TestCollectorOutermostOnlyFlush(t *testing.T) {
	outDir, shards := runCollectorShards(t, nil, `package collector

import "testing"

func TestNested(t *testing.T) {
	crap4goEnter("outer")
	crap4goEnter("inner")
	crap4goExit() // nested: must not touch the filesystem
	crap4goExit() // outermost: the one flush
}
`)
	if got := shardFlushes(t, shards); got != 1 {
		t.Errorf("#flushes = %d, want 1 (a nested flush would make it 2)", got)
	}
	stats := readTimings(outDir)
	if stats["outer"] == nil || stats["inner"] == nil {
		t.Errorf("cumulative snapshot must carry both methods: %+v", stats)
	}
}

// TestCollectorBackstopFlush bounds the loss of a stuck stack: with
// CRAP_PROFILE_FLUSH_EVERY=3 the third NESTED exit flushes (stack still
// non-empty — a leaked frame's data reaches the shard), before the
// outermost exit adds its own flush.
func TestCollectorBackstopFlush(t *testing.T) {
	outDir, shards := runCollectorShards(t, []string{"CRAP_PROFILE_FLUSH_EVERY=3"}, `package collector

import "testing"

func TestBackstop(t *testing.T) {
	crap4goEnter("outer")
	for i := 0; i < 3; i++ {
		crap4goEnter("inner")
		crap4goExit() // nested: call 3 crosses the backstop
	}
	crap4goExit() // outermost: the always-flush
}
`)
	if got := shardFlushes(t, shards); got != 2 {
		t.Errorf("#flushes = %d, want 2 (backstop on nested call 3 + outermost exit)", got)
	}
	stats := readTimings(outDir)
	if inner := stats["inner"]; inner == nil || inner.Calls != 3 {
		t.Errorf("inner stats = %+v, want 3 calls delivered", inner)
	}
	if outer := stats["outer"]; outer == nil || outer.Calls != 1 {
		t.Errorf("outer stats = %+v, want 1 call", outer)
	}
}

// TestCollectorThrottleTailLoss pins the CRAP_PROFILE_FLUSH_MS opt-in
// trade: with a huge interval only the FIRST outermost flush happens
// (the zero lastFlush never throttles), the following outermost exits
// are throttled away, and the tail after the last throttled exit is
// lost — documented bounded tail loss, exact calls not required under
// the throttle. The default (no env) must flush on EVERY outermost exit.
func TestCollectorThrottleTailLoss(t *testing.T) {
	src := `package collector

import "testing"

func TestHot(t *testing.T) {
	for i := 0; i < 3; i++ {
		crap4goEnter("hot")
		crap4goExit() // outermost each time
	}
}
`
	_, throttled := runCollectorShards(t, []string{"CRAP_PROFILE_FLUSH_MS=60000"}, src)
	if got := shardFlushes(t, throttled); got != 1 {
		t.Errorf("#flushes = %d, want 1 (first flush only, tail throttled away)", got)
	}
	if hot := readTimings(dirOfShards(t, throttled))["hot"]; hot == nil || hot.Calls != 1 {
		t.Errorf("throttled shard should hold only the first call, got %+v", hot)
	}

	_, unthrottled := runCollectorShards(t, nil, src)
	if got := shardFlushes(t, unthrottled); got != 3 {
		t.Errorf("#flushes = %d, want 3 (default: every outermost exit flushes)", got)
	}
	if hot := readTimings(dirOfShards(t, unthrottled))["hot"]; hot == nil || hot.Calls != 3 {
		t.Errorf("unthrottled shard should hold all 3 calls, got %+v", hot)
	}
}

// dirOfShards rebuilds a directory from shard contents for the host
// merge; used where the test only kept the shard map.
func dirOfShards(t *testing.T, shards map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range shards {
		writeFile(t, filepath.Join(dir, name), content)
	}
	return dir
}
