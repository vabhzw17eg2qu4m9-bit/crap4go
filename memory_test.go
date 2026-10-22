package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are real interleaved `go test -v -exec 'env GODEBUG=gctrace=1'`
// output shapes (verified against go1.27.2 on darwin/arm64): the go tool
// relays the test binary's combined output to stdout, so gctrace rows and
// === RUN / --- PASS markers interleave in one ordered stream. Heap
// numbers are hand-picked so every expected total is computable by hand.

const memoryFixturePkgA = `
=== RUN   TestHeavy
gc 1 @0.002s 2%: 0.26+0.21+0.004 ms clock, 3.7+0/0.37/0.20+0.057 ms cpu, 1->1->0 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 14 P (forced)
gc 2 @0.009s 3%: 0.028+0.071+0.003 ms clock, 0.44+0/0.11/0.035+0.064 ms cpu, 6->2->4 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestHeavy (0.09s)
gc 3 @0.100s 3%: 0.030+0.082+0.002 ms clock, 0.48+0/0.12/0.040+0.062 ms cpu, 4->1->3 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
=== RUN   TestLight
gc 4 @0.120s 4%: 0.021+0.059+0.002 ms clock, 0.36+0/0.09/0.020+0.053 ms cpu, 3->1->1 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestLight (0.05s)
PASS
ok  	example.com/pkgA	0.634s
`

// Package B reuses the name TestHeavy (different package, its own test
// binary: the old-gen baseline resets) and exercises subtest nesting
// (result lines print indented, real go1.27.2 shape), t.Parallel
// pause/continue and post-run teardown GCs.
const memoryFixturePkgB = `
=== RUN   TestOther
    other_test.go:8: t.Parallel()
=== PAUSE TestOther
=== RUN   TestHeavy
=== RUN   TestHeavy/sub
gc 5 @0.030s 5%: 0.044+0.086+0.005 ms clock, 0.69+0/0.13/0.052+0.070 ms cpu, 9->3->5 MB, 12 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestHeavy (0.02s)
    --- PASS: TestHeavy/sub (0.01s)
=== CONT  TestOther
gc 6 @0.045s 6%: 0.027+0.064+0.003 ms clock, 0.41+0/0.10/0.028+0.058 ms cpu, 7->2->4 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestOther (0.03s)
gc 7 @0.051s 6%: 0.031+0.058+0.002 ms clock, 0.47+0/0.09/0.024+0.049 ms cpu, 6->1->3 MB, 6 MB goal, 0 MB stacks, 0 MB globals, 14 P
PASS
ok  	example.com/pkgB	0.513s
`

const memoryFixtureTwoPackages = memoryFixturePkgA + memoryFixturePkgB

func parseFixture(t *testing.T, stream string) memoryReport {
	t.Helper()
	return parseMemoryStream(stream)
}

func findTest(t *testing.T, r memoryReport, label string) *memoryEntry {
	t.Helper()
	for _, e := range r.tests {
		if e.label == label {
			return e
		}
	}
	t.Fatalf("no test entry %q in report", label)
	return nil
}

// TestParseGcLine parses real gctrace rows (with and without the
// stacks/globals tail and the forced suffix) and rejects everything else.
func TestParseGcLine(t *testing.T) {
	gc, ok := parseGcLine("gc 12 @0.005s 5%: 0.042+0.065+0.003 ms clock, 0.59+0/0.11/0.017+0.043 ms cpu, 1->1->0 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 14 P (forced)")
	if !ok {
		t.Fatal("forced row not recognized")
	}
	if gc.seq != 12 || gc.startSeconds != 0.005 || gc.heapStartMb != 1 || gc.heapEndMb != 1 || gc.heapLiveMb != 0 {
		t.Errorf("wrong fields: %+v", gc)
	}
	// Older Go releases print no stacks/globals fields.
	if gc, ok := parseGcLine("gc 3 @1.234s 2%: 0.1+2.3+0.2 ms clock, 0.8+0.45/4.6/0.9+0.8 ms cpu, 8->9->4 MB, 16 MB goal, 12 P"); !ok ||
		gc.heapStartMb != 8 || gc.heapEndMb != 9 || gc.heapLiveMb != 4 || gc.seq != 3 || gc.startSeconds != 1.234 {
		t.Errorf("short row misparsed: %+v ok=%v", gc, ok)
	}
	for _, line := range []string{
		"",
		"gcwithoutspace",
		"=== RUN   TestHeavy",
		"--- PASS: TestHeavy (0.09s)",
		"ok  \texample.com/pkgA\t0.634s",
		"gc 1 @0.005s 5%: no heap triple here, 4 MB goal",
	} {
		if _, ok := parseGcLine(line); ok {
			t.Errorf("non-gc line parsed as GC: %q", line)
		}
	}
}

// TestGcChurnIsReclaimedVolume: churn per row is max(0, start - liveAfter)
// — the heap the cycle reclaimed; negative deltas clamp to 0.
func TestGcChurnIsReclaimedVolume(t *testing.T) {
	gc, ok := parseGcLine("gc 1 @0.1s 0%: 0.1+0.1+0.1 ms clock, 1+1/1/1+1 ms cpu, 6->2->4 MB, 8 MB goal, 0 P")
	if !ok {
		t.Fatal("not parsed")
	}
	if gc.churnMb() != 2 {
		t.Errorf("churn = %v, want 2 (6 start - 4 live)", gc.churnMb())
	}
	if gc, ok = parseGcLine("gc 2 @0.2s 0%: 0.1+0.1+0.1 ms clock, 1+1/1/1+1 ms cpu, 3->4->5 MB, 8 MB goal, 0 P"); !ok || gc.churnMb() != 0 {
		t.Errorf("growing heap must clamp to 0 churn, got %v (ok=%v)", gc.churnMb(), ok)
	}
}

// TestBoundaryGcDoesNotLeakIntoNextTest is the killing test for the
// attribution boundary rule: gc 3 prints AFTER --- PASS: TestHeavy and
// BEFORE === RUN TestLight — it must land in neither test and neither
// package total (unattributed bucket), not leak into the next window.
// Mutating the boundary (letting a teardown GC rejoin the last test or
// the package entry) fails this test.
func TestBoundaryGcDoesNotLeakIntoNextTest(t *testing.T) {
	r := parseFixture(t, memoryFixturePkgA)
	heavy := findTest(t, r, "TestHeavy")
	light := findTest(t, r, "TestLight")
	if got := len(heavy.gcs); got != 2 {
		t.Errorf("TestHeavy gcs = %d, want 2 (boundary GC must not stick to the closed test)", got)
	}
	if got := len(light.gcs); got != 1 {
		t.Errorf("TestLight gcs = %d, want 1 (boundary GC must not leak into the next test)", got)
	}
	pkg := r.packages[0]
	if pkg.label != "example.com/pkgA" || len(pkg.gcs) != 3 {
		t.Errorf("pkgA entry = %s with %d gcs, want 3 (only its tests' windows)", pkg.label, len(pkg.gcs))
	}
	if got := len(r.unattributed); got != 1 || r.unattributed[0].seq != 3 {
		t.Errorf("unattributed = %+v, want exactly gc 3", r.unattributed)
	}
	if got := round1(r.unattributedChurnMb()); got != 1.0 {
		t.Errorf("unattributed churn = %v, want 1 (4 start - 3 live)", got)
	}
}

// TestNestedWindowsResolveToInnermost: a GC inside a subtest's window
// lands on the subtest (the shorter span), not the parent.
func TestNestedWindowsResolveToInnermost(t *testing.T) {
	r := parseFixture(t, memoryFixturePkgB)
	parent := findTest(t, r, "TestHeavy")
	sub := findTest(t, r, "TestHeavy/sub")
	if len(parent.gcs) != 0 {
		t.Errorf("parent TestHeavy got %d gcs, want 0 (sub window is the shorter span)", len(parent.gcs))
	}
	if len(sub.gcs) != 1 || sub.gcs[0].seq != 5 {
		t.Errorf("TestHeavy/sub gcs = %+v, want exactly gc 5", sub.gcs)
	}
	if sub.pkg != "example.com/pkgB" {
		t.Errorf("sub pkg = %q, want example.com/pkgB", sub.pkg)
	}
}

// TestLoadingGcIsUnattributed: a GC before the first === RUN has no
// open window — it lands in the unattributed bucket, never a test and
// never the package entry.
func TestLoadingGcIsUnattributed(t *testing.T) {
	stream := "gc 1 @0.001s 1%: 0.1+0.2+0.1 ms clock, 1+1/1/1+1 ms cpu, 5->1->2 MB, 6 MB goal, 0 P\n" +
		"=== RUN   TestX\n--- PASS: TestX (0.01s)\nok  \texample.com/pkg\t0.100s\n"
	r := parseFixture(t, stream)
	if got := len(r.totalGcs); got != 1 {
		t.Fatalf("totalGcs = %d, want 1", got)
	}
	if e := findTest(t, r, "TestX"); len(e.gcs) != 0 {
		t.Errorf("TestX got %d gcs, want 0 (loading GC is unattributed)", len(e.gcs))
	}
	if len(r.packages) != 1 || len(r.packages[0].gcs) != 0 {
		t.Errorf("package entry must carry no GCs, got %+v", r.packages)
	}
	if len(r.unattributed) != 1 || round1(r.unattributedChurnMb()) != 3.0 {
		t.Errorf("bucket = %+v churn %v, want gc 1 / 3 MB (5 start - 2 live)",
			r.unattributed, r.unattributedChurnMb())
	}
}

// TestParallelPauseContinuesAttribution: a GC while a test is paused
// (t.Parallel) belongs to the innermost RUNNING window; after === CONT
// the same entry wins again.
func TestParallelPauseContinuesAttribution(t *testing.T) {
	r := parseFixture(t, memoryFixturePkgB)
	other := findTest(t, r, "TestOther")
	if len(other.gcs) != 1 || other.gcs[0].seq != 6 {
		t.Errorf("TestOther gcs = %+v, want exactly gc 6 (after CONT)", other.gcs)
	}
	if other.result != "pass" {
		t.Errorf("TestOther result = %q, want pass", other.result)
	}
}

// TestSameTestNameAcrossPackages: each package is its own test binary;
// entries never merge across an `ok` boundary.
func TestSameTestNameAcrossPackages(t *testing.T) {
	r := parseFixture(t, memoryFixtureTwoPackages)
	heavies := 0
	for _, e := range r.tests {
		if e.label == "TestHeavy" {
			heavies++
		}
	}
	if heavies != 2 {
		t.Errorf("found %d TestHeavy entries, want 2 (one per package)", heavies)
	}
	if len(r.packages) != 2 || r.packages[0].label != "example.com/pkgA" || r.packages[1].label != "example.com/pkgB" {
		t.Errorf("packages = %v, want pkgA then pkgB", r.packages)
	}
	if r.testCount != 5 {
		t.Errorf("testCount = %d, want 5 (closed windows incl. subtests)", r.testCount)
	}
}

// TestChurnAndOldGrowthTotals: hand-computed sums over the fixture.
// Packages carry only their tests' windows now; the boundary/teardown
// rows (pkgA gc 3, pkgB gc 7) land in the unattributed bucket.
// pkgA churn = (1-0)+(6-4)+(3-1) = 5; pkgA old = 0+(4-0)+(1-3) = +2.
// pkgB churn = (9-5)+(7-4) = 7; pkgB old resets its baseline, so
// = 0+(4-5) = -1. Bucket = gc 3 + gc 7: churn (4-3)+(6-3) = 4,
// old (3-4)+(3-4) = -2.
func TestChurnAndOldGrowthTotals(t *testing.T) {
	r := parseFixture(t, memoryFixtureTwoPackages)
	if got := round1(sumChurn(r.totalGcs)); got != 16.0 {
		t.Errorf("total churn = %v, want 16", got)
	}
	if got := round1(sumOldGrowth(r.totalGcs)); got != -1.0 {
		t.Errorf("total old growth = %v, want -1", got)
	}
	sub := findTest(t, r, "TestHeavy/sub")
	if got := round1(sub.churnMb()); got != 4.0 {
		t.Errorf("sub churn = %v, want 4 (9 start - 5 live)", got)
	}
	if got := round1(sub.oldGrowthMb()); got != 0.0 {
		t.Errorf("sub old growth = %v, want 0 (per-process baseline)", got)
	}
	pkgA, pkgB := r.packages[0], r.packages[1]
	if got := round1(pkgA.churnMb()); got != 5.0 {
		t.Errorf("pkgA churn = %v, want 5 (boundary gc 3 is unattributed)", got)
	}
	if got := round1(pkgB.churnMb()); got != 7.0 {
		t.Errorf("pkgB churn = %v, want 7 (teardown gc 7 is unattributed)", got)
	}
	if got := round1(pkgA.oldGrowthMb()); got != 2.0 {
		t.Errorf("pkgA old growth = %v, want +2", got)
	}
	if got := round1(pkgB.oldGrowthMb()); got != -1.0 {
		t.Errorf("pkgB old growth = %v, want -1", got)
	}
	if len(r.unattributed) != 2 {
		t.Fatalf("bucket = %d gcs, want 2 (gc 3 + gc 7)", len(r.unattributed))
	}
	if got := round1(r.unattributedChurnMb()); got != 4.0 {
		t.Errorf("bucket churn = %v, want 4", got)
	}
	if got := round1(sumOldGrowth(r.unattributed)); got != -2.0 {
		t.Errorf("bucket old growth = %v, want -2", got)
	}
}

// TestTopLists: only tests with GC activity, churn descending, stable on
// ties (earlier-run entry first).
func TestTopLists(t *testing.T) {
	r := parseFixture(t, memoryFixtureTwoPackages)
	var labels []string
	for _, e := range r.topChurn() {
		labels = append(labels, e.label)
	}
	want := []string{"TestHeavy/sub", "TestHeavy", "TestOther", "TestLight"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("topChurn = %v, want %v", labels, want)
	}
	var growth []string
	for _, e := range r.topOldGrowth() {
		growth = append(growth, e.label)
	}
	wantGrowth := []string{"TestHeavy", "TestHeavy/sub", "TestOther", "TestLight"}
	if strings.Join(growth, ",") != strings.Join(wantGrowth, ",") {
		t.Errorf("topOldGrowth = %v, want %v", growth, wantGrowth)
	}
}

// TestDiscardBeforeNoTestFiles: GCs before a `? pkg [no test files]`
// summary belong to no test binary — dropped entirely.
func TestDiscardBeforeNoTestFiles(t *testing.T) {
	stream := "gc 1 @0.001s 1%: 0.1+0.2+0.1 ms clock, 1+1/1/1+1 ms cpu, 5->1->2 MB, 6 MB goal, 0 P\n" +
		"?   \texample.com/empty\t[no test files]\n" +
		"=== RUN   TestX\n--- PASS: TestX (0.01s)\nok  \texample.com/real\t0.100s\n"
	r := parseFixture(t, stream)
	if got := len(r.totalGcs); got != 0 {
		t.Errorf("totalGcs = %d, want 0 (discarded with the no-test package)", got)
	}
	if len(r.packages) != 1 || r.packages[0].label != "example.com/real" {
		t.Errorf("packages = %+v, want only example.com/real", r.packages)
	}
}

// TestProfileMemoryInjectsRunner: the runner's output drives the report;
// a failing test binary (go test exit 1) still yields one.
func TestProfileMemoryInjectsRunner(t *testing.T) {
	var gotArgs, gotEnv []string
	var gotDir string
	runner := func(dir string, args, env []string) (string, string, error) {
		gotDir, gotArgs, gotEnv = dir, args, env
		return memoryFixturePkgA, "", nil
	}
	r, err := profileMemory("/proj", nil, runner)
	if err != nil {
		t.Fatalf("profileMemory: %v", err)
	}
	if gotDir != "/proj" {
		t.Errorf("dir = %q", gotDir)
	}
	if r.testCount != 2 || len(r.totalGcs) != 4 {
		t.Errorf("report = %d tests / %d gcs, want 2 / 4", r.testCount, len(r.totalGcs))
	}
	if len(gotEnv) == 0 || gotEnv[len(gotEnv)-1] != "GODEBUG=" {
		t.Errorf("env must end with empty GODEBUG (neutralizes ambient/go-env-file values), got %v", gotEnv)
	}
	for _, kv := range gotEnv {
		if kv != "GODEBUG=" && strings.HasPrefix(kv, "GODEBUG=") {
			t.Errorf("ambient GODEBUG not stripped: %q", kv)
		}
	}
	if !isMemoryTestInvocation(gotArgs) {
		t.Errorf("args = %v, want traced go test invocation", gotArgs)
	}

	failing := func(dir string, args, env []string) (string, string, error) {
		return memoryFixturePkgA, "", &exec.ExitError{}
	}
	if r, err = profileMemory("/proj", nil, failing); err != nil || r.testCount != 2 {
		t.Errorf("exit-1 run must still report: err=%v tests=%d", err, r.testCount)
	}
	broken := func(dir string, args, env []string) (string, string, error) {
		return "", "", errors.New("exec: not found")
	}
	if _, err = profileMemory("/proj", nil, broken); err == nil {
		t.Error("runner failure (not an exit error) must surface")
	}
}

// isMemoryTestInvocation pins the traced-argv shape: verbose markers,
// cache bypass, sequential packages, scoped tracing wrapper.
func isMemoryTestInvocation(args []string) bool {
	want := []string{"test", "-v", "-count=1", "-p", "1", "-exec", "env GODEBUG=gctrace=1"}
	if len(args) != len(want)+1 || args[len(args)-1] != "./..." {
		return false
	}
	for i, w := range want {
		if args[i] != w {
			return false
		}
	}
	return true
}

// TestMemoryTestArgs: explicit paths are exclusive (the default ./...
// must not be appended alongside them).
func TestMemoryTestArgs(t *testing.T) {
	defaults := memoryTestArgs(nil)
	if defaults[len(defaults)-1] != "./..." {
		t.Errorf("default args = %v, want ./... last", defaults)
	}
	withPaths := memoryTestArgs([]string{"./parser", "./cli"})
	for _, p := range withPaths[len(withPaths)-2:] {
		if p != "./parser" && p != "./cli" {
			t.Errorf("paths not passed exclusively: %v", withPaths)
		}
	}
	if strings.Contains(strings.Join(withPaths, " "), "./...") {
		t.Errorf("./... appended alongside explicit paths: %v", withPaths)
	}
}

// overrideMemoryProfile swaps the injectable profiler for a test and
// restores it afterwards.
func overrideMemoryProfile(t *testing.T, fn func(root string, paths []string) (memoryReport, error)) {
	t.Helper()
	prev := memoryProfile
	memoryProfile = fn
	t.Cleanup(func() { memoryProfile = prev })
}

func memoryCommandRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/proj\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

const memoryFixtureJSONInput = `
=== RUN   TestA
gc 1 @0.002s 2%: 0.26+0.21+0.004 ms clock, 3.7+0/0.37/0.20+0.057 ms cpu, 1->1->0 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 14 P (forced)
--- PASS: TestA (0.09s)
gc 2 @0.050s 2%: 0.028+0.071+0.003 ms clock, 0.44+0/0.11/0.035+0.064 ms cpu, 4->1->3 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
ok  	example.com/proj	0.634s
`

// TestRunMemoryCommandJSON: --format json keeps stdout JSON-only and
// reports the unattributed bucket.
func TestRunMemoryCommandJSON(t *testing.T) {
	overrideMemoryProfile(t, func(root string, paths []string) (memoryReport, error) {
		return parseMemoryStream(memoryFixtureJSONInput), nil
	})
	root := memoryCommandRoot(t)
	var stdout, stderr bytes.Buffer
	code := RunMemoryCommand([]string{"--format", "json"}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	var doc memoryJSON
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout is not JSON-only: %v\n%s", err, stdout.String())
	}
	if doc.TestCount != 1 || doc.GCCount != 2 {
		t.Errorf("doc = %+v, want 1 test / 2 gcs", doc)
	}
	if doc.UnattributedGCs != 1 || doc.UnattributedChurnMb != 1 {
		t.Errorf("bucket = %d gcs / %v MB, want 1 / 1 (teardown gc: 4 start - 3 live)",
			doc.UnattributedGCs, doc.UnattributedChurnMb)
	}
	if len(doc.Tests) != 1 || doc.Tests[0].Label != "TestA" || doc.Tests[0].GCs != 1 {
		t.Errorf("tests = %+v", doc.Tests)
	}
	if len(doc.Packages) != 1 || doc.Packages[0].Label != "example.com/proj" || doc.Packages[0].GCs != 1 {
		t.Errorf("packages = %+v", doc.Packages)
	}
}

// TestRunMemoryCommandConsole: default format prints the summary line
// and both leaderboards.
func TestRunMemoryCommandConsole(t *testing.T) {
	overrideMemoryProfile(t, func(root string, paths []string) (memoryReport, error) {
		return parseMemoryStream(memoryFixturePkgA), nil
	})
	root := memoryCommandRoot(t)
	var stdout, stderr bytes.Buffer
	if code := RunMemoryCommand(nil, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"memory: 2 tests, 4 GCs, churn 6.0 MB new-gen, old-gen +1.0 MB",
		"unattributed (outside test windows): 1 GCs, 1.0 MB",
		"top 20 by allocation churn (reclaimed new-gen MB):",
		"top 20 by old-gen growth (leak suspects first):",
		"TestHeavy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("console report missing %q:\n%s", want, out)
		}
	}
}

// TestBadgeColorForThresholds is the killing test for the threshold
// logic: green at or below (boundary inclusive), yellow at most twice
// the threshold (boundary inclusive), red beyond.
func TestBadgeColorForThresholds(t *testing.T) {
	cases := []struct {
		churn, threshold float64
		want             string
	}{
		{0, 500, badgeGreen},
		{500, 500, badgeGreen},
		{500.1, 500, badgeYellow},
		{1000, 500, badgeYellow},
		{1000.01, 500, badgeRed},
		{999999, 0.5, badgeRed},
	}
	for _, tc := range cases {
		if got := badgeColorFor(tc.churn, tc.threshold); got != tc.want {
			t.Errorf("badgeColorFor(%v, %v) = %s, want %s", tc.churn, tc.threshold, got, tc.want)
		}
	}
}

// TestRenderBadgeSVG pins the shields-style geometry and content.
func TestRenderBadgeSVG(t *testing.T) {
	svg := renderBadgeSVG("memory churn", "245 MB", badgeGreen)
	// Plates: round(6.3·len)+10 → label 12 chars = 86, value 6 chars = 48.
	for _, want := range []string{
		`width="134" height="20" viewBox="0 0 134 20"`,
		`aria-label="memory churn: 245 MB"`,
		`<title>memory churn: 245 MB</title>`,
		`<rect width="86" height="20" rx="3" fill="#555"/>`,
		`<rect x="86" width="48" height="20" rx="3" fill="#4c1"/>`,
		`<text x="43" y="14">memory churn</text>`,
		`<text x="110" y="14">245 MB</text>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("badge SVG missing %q:\n%s", want, svg)
		}
	}
}

// TestRunMemoryCommandBadge: --badge writes the churn SVG (green at or
// below the threshold, yellow above it); without --badge nothing is
// written. Fixture pkgA churns 6.0 MB in total.
func TestRunMemoryCommandBadge(t *testing.T) {
	overrideMemoryProfile(t, func(root string, paths []string) (memoryReport, error) {
		return parseMemoryStream(memoryFixturePkgA), nil
	})
	root := memoryCommandRoot(t)

	yellow := filepath.Join(root, "badges", "memory.svg")
	var stdout, stderr bytes.Buffer
	if code := RunMemoryCommand([]string{"--badge", yellow, "--badge-threshold", "5"}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(yellow)
	if err != nil {
		t.Fatalf("badge not written: %v", err)
	}
	svg := string(data)
	if !strings.Contains(svg, "memory churn") || !strings.Contains(svg, "6 MB") {
		t.Errorf("badge missing churn message:\n%s", svg)
	}
	if !strings.Contains(svg, badgeYellow) {
		t.Errorf("churn 6 over threshold 5 must be yellow:\n%s", svg)
	}
	if !strings.Contains(stderr.String(), "Badge written to ") {
		t.Errorf("stderr must note the badge path, got %q", stderr.String())
	}

	green := filepath.Join(root, "green.svg")
	if code := RunMemoryCommand([]string{"--badge", green, "--badge-threshold", "6"}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d (churn == threshold is green, not an error)", code)
	}
	if data, err = os.ReadFile(green); err != nil || !strings.Contains(string(data), badgeGreen) {
		t.Errorf("churn 6 at threshold 6 must be green: %v\n%s", err, string(data))
	}
}

// TestRunMemoryCommandUsageErrors: usage errors exit 1; an empty run
// exits 1; a directory without go.mod exits 1. Informational runs always
// exit 0 — even with injected failures downstream of usage.
func TestRunMemoryCommandUsageErrors(t *testing.T) {
	overrideMemoryProfile(t, func(root string, paths []string) (memoryReport, error) {
		return memoryReport{}, nil
	})
	emptyRoot := t.TempDir()
	cases := []struct {
		name string
		args []string
		root string
		want int
	}{
		{"bad format", []string{"--format", "yaml"}, emptyRoot, 1},
		{"negative top", []string{"--top", "-1"}, emptyRoot, 1},
		{"negative badge threshold", []string{"--badge-threshold", "-1"}, memoryCommandRoot(t), 1},
		{"bad flag", []string{"--nope"}, emptyRoot, 1},
		{"no go.mod", nil, emptyRoot, 1},
		{"no tests executed", nil, memoryCommandRoot(t), 1},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		if got := RunMemoryCommand(tc.args, tc.root, &stdout, &stderr); got != tc.want {
			t.Errorf("%s: exit = %d, want %d (stderr: %s)", tc.name, got, tc.want, stderr.String())
		}
		if stdout.Len() > 0 {
			t.Errorf("%s: usage errors must not write stdout, got %q", tc.name, stdout.String())
		}
	}
}

// TestNameMarkerRaisesWindow: `=== NAME` re-targets attribution at an
// open window even when another is innermost, reopens a paused one via
// its CONT fallback, and `--- SKIP` closes a paused window.
func TestNameMarkerRaisesWindow(t *testing.T) {
	stream := `
=== RUN   TestA
=== PAUSE TestA
=== RUN   TestB
=== NAME  TestA
gc 1 @0.001s 1%: 0.1+0.2+0.1 ms clock, 1+1/1/1+1 ms cpu, 5->1->2 MB, 6 MB goal, 0 P
--- SKIP: TestA (0.00s)
=== NAME  TestB
gc 2 @0.002s 1%: 0.1+0.2+0.1 ms clock, 1+1/1/1+1 ms cpu, 5->1->2 MB, 6 MB goal, 0 P
--- PASS: TestB (0.01s)
ok  	example.com/pkg	0.100s
`
	r := parseFixture(t, stream)
	a := findTest(t, r, "TestA")
	b := findTest(t, r, "TestB")
	if len(a.gcs) != 1 || a.gcs[0].seq != 1 || a.result != "skip" {
		t.Errorf("TestA = %+v result %q, want gc 1 / skip (NAME reopened the paused window)", a.gcs, a.result)
	}
	if len(b.gcs) != 1 || b.gcs[0].seq != 2 || b.result != "pass" {
		t.Errorf("TestB = %+v result %q, want gc 2 / pass (NAME raised it back)", b.gcs, b.result)
	}
	// NAME an open-but-not-innermost window: it moves to the top of the
	// stack and wins the next GC.
	stream2 := `
=== RUN   TestC
=== RUN   TestD
=== NAME  TestC
gc 3 @0.003s 1%: 0.1+0.2+0.1 ms clock, 1+1/1/1+1 ms cpu, 5->1->2 MB, 6 MB goal, 0 P
--- PASS: TestC (0.00s)
--- PASS: TestD (0.00s)
ok  	example.com/pkg	0.100s
`
	r2 := parseFixture(t, stream2)
	c := findTest(t, r2, "TestC")
	d := findTest(t, r2, "TestD")
	if len(c.gcs) != 1 || c.gcs[0].seq != 3 {
		t.Errorf("TestC = %+v, want gc 3 (NAME moved the open window to the top)", c.gcs)
	}
	if len(d.gcs) != 0 {
		t.Errorf("TestD = %+v, want 0", d.gcs)
	}
}

// TestIndentedSubtestBoundary kills the indented-marker defect: subtest
// result lines print INDENTED (4 spaces per depth, real go1.27.2 shape)
// and AFTER the parent's unindented result. The indented line must close
// child_one's window — the boundary GC after it must become unattributed,
// not leak into the next test.
func TestIndentedSubtestBoundary(t *testing.T) {
	stream := `
=== RUN   TestParent
=== RUN   TestParent/child_one
gc 1 @0.002s 2%: 0.26+0.21+0.004 ms clock, 3.7+0/0.37/0.20+0.057 ms cpu, 9->3->5 MB, 12 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestParent (0.01s)
    --- PASS: TestParent/child_one (0.01s)
gc 2 @0.010s 2%: 0.028+0.071+0.003 ms clock, 0.44+0/0.11/0.035+0.064 ms cpu, 6->2->4 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
=== RUN   TestNext
gc 3 @0.020s 2%: 0.030+0.082+0.002 ms clock, 0.48+0/0.12/0.040+0.062 ms cpu, 4->1->3 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
--- PASS: TestNext (0.00s)
ok  	example.com/pkg	0.100s
`
	r := parseFixture(t, stream)
	child := findTest(t, r, "TestParent/child_one")
	next := findTest(t, r, "TestNext")
	if len(child.gcs) != 1 || child.gcs[0].seq != 1 {
		t.Errorf("child_one gcs = %+v, want exactly gc 1 (indented result must close the window)", child.gcs)
	}
	if len(next.gcs) != 1 || next.gcs[0].seq != 3 {
		t.Errorf("TestNext gcs = %+v, want exactly gc 3 (boundary GC must not leak in)", next.gcs)
	}
	if r.testCount != 3 {
		t.Errorf("testCount = %d, want 3", r.testCount)
	}
	if len(r.packages) != 1 || len(r.packages[0].gcs) != 2 {
		t.Errorf("package must carry only its tests' 2 GCs, got %+v", r.packages)
	}
	if len(r.unattributed) != 1 || r.unattributed[0].seq != 2 {
		t.Errorf("bucket = %+v, want exactly gc 2 (boundary after the last window)", r.unattributed)
	}
}

// TestTornRowsKeepChurnAndMarkers kills the torn-row defect: the go
// tool's relay merges the test binary's stdout and stderr at arbitrary
// byte boundaries, so gctrace rows and markers can share a line or tear
// mid-row. Real tear shapes (observed under load):
//   - a complete row with a marker glued after it (no newline),
//   - a row torn mid-row with a marker glued into the fragment,
//   - a result marker with a complete row glued after its duration.
//
// The reassembly rules: rows and markers are recovered by shape, a row
// fragment is never a row (its churn is lost, nothing misattributed),
// and no window may stay stuck open.
func TestTornRowsKeepChurnAndMarkers(t *testing.T) {
	stream := `
=== RUN   TestA
    a_test.go:8: t.Parallel()
=== PAUSE TestA
=== RUN   TestB
=== CONT  TestA
gc 1 @0.001s 2%: 0.26+0.21+0.004 ms clock, 3.7+0/0.37/0.20+0.057 ms cpu, 9->3->5 MB, 12 MB goal, 0 MB stacks, 0 MB globals, 14 P=== RUN   TestC
gc 2 @0.005s 2%: 0.028+0.071+0.003 ms clock, 0.44+0/0.11/0.035+0.064 ms cpu, 6->2->4 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
gc 3 @0.010s 2%: 0.04+0.08+0.00 ms clo=== RUN   TestD
--- SKIP: TestZgc 9 @0.030s 1%: 0.1+0.2+0.1 ms clo
--- PASS: TestD (0.00s)
--- PASS: TestC (0.01s)
--- PASS: TestB (0.01s)
--- PASS: TestA (0.01s)gc 4 @0.020s 2%: 0.030+0.082+0.002 ms clock, 0.48+0/0.12/0.040+0.062 ms cpu, 7->1->3 MB, 8 MB goal, 0 MB stacks, 0 MB globals, 14 P
ok  	example.com/pkg	0.200s
`
	r := parseFixture(t, stream)
	a := findTest(t, r, "TestA")
	c := findTest(t, r, "TestC")
	d := findTest(t, r, "TestD")
	if len(a.gcs) != 1 || a.gcs[0].seq != 1 {
		t.Errorf("TestA gcs = %+v, want exactly gc 1 (CONT reopened it)", a.gcs)
	}
	if len(c.gcs) != 1 || c.gcs[0].seq != 2 {
		t.Errorf("TestC gcs = %+v, want exactly gc 2 (RUN glued after torn-free row must open it)", c.gcs)
	}
	if len(d.gcs) != 0 {
		t.Errorf("TestD gcs = %+v, want 0 (torn row fragment is not a row)", d.gcs)
	}
	for _, e := range r.tests {
		if e.label == "TestZ" || e.label == "TestZgc 9 @0.030s 1%: 0.1+0.2+0.1 ms clo" {
			t.Errorf("torn SKIP fragment must not create a test entry, got %q", e.label)
		}
	}
	if got := len(r.totalGcs); got != 3 {
		t.Errorf("totalGcs = %d, want 3 (torn gc 3 fragment must not parse as a row; glued gc 4 must)", got)
	}
	if got := round1(sumChurn(r.totalGcs)); got != 10.0 {
		t.Errorf("total churn = %v, want 10 (4+2+4: glued rows keep their churn)", got)
	}
	if r.testCount != 4 {
		t.Errorf("testCount = %d, want 4 (every window closed despite tears)", r.testCount)
	}
	if len(r.packages) != 1 {
		t.Fatalf("packages = %+v, want one", r.packages)
	}
	seqs := []int{}
	for _, g := range r.packages[0].gcs {
		seqs = append(seqs, g.seq)
	}
	if len(seqs) != 2 || seqs[0] != 1 {
		t.Errorf("package gcs = %v, want [1 2]: only the tests' own rows", seqs)
	}
	bucket := []int{}
	for _, g := range r.unattributed {
		bucket = append(bucket, g.seq)
	}
	if len(bucket) != 1 || bucket[0] != 4 {
		t.Errorf("bucket = %v, want [4]: gc 4 printed after the last window closed", bucket)
	}
}

// TestLimitBy: top <= 0 means all entries.
func TestLimitBy(t *testing.T) {
	entries := []*memoryEntry{{label: "a"}, {label: "b"}}
	if got := limitBy(entries, 1); len(got) != 1 {
		t.Errorf("limitBy(1) = %d entries, want 1", len(got))
	}
	if got := limitBy(entries, 0); len(got) != 2 {
		t.Errorf("limitBy(0) = %d entries, want all", len(got))
	}
}
