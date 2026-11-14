package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const instrSrc = `package sample

import "fmt"

func Add(a, b int) int {
	return a + b
}

func (c Calc) Show() {
	fmt.Println(c)
}

func Empty() {}

func ext()
`

func TestInstrumentSource(t *testing.T) {
	out, pkg, changed, err := instrumentSource([]byte(instrSrc), "sample.go")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	if pkg != "sample" || !changed {
		t.Fatalf("pkg=%q changed=%v, want sample true", pkg, changed)
	}
	src := string(out)
	for _, want := range []string{
		`crap4goEnter("sample.go|Add")`,
		`crap4goEnter("sample.go|(Calc)Show")`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("instrumented source missing %q:\n%s", want, src)
		}
	}
	// Every wrapped body defers the collector exit.
	if got := strings.Count(src, "defer crap4goExit()"); got != 3 {
		t.Errorf("want 3 deferred exits, got %d:\n%s", got, src)
	}
	// Empty bodies are wrapped too; bodiless declarations are not.
	if !strings.Contains(src, `crap4goEnter("sample.go|Empty")`) {
		t.Errorf("empty body not wrapped:\n%s", src)
	}
	if strings.Contains(src, "ext") && strings.Count(src, "crap4goEnter") != 3 {
		t.Errorf("bodiless decl instrumented:\n%s", src)
	}
	// The instrumented source must still parse.
	if _, err := parser.ParseFile(token.NewFileSet(), "", out, 0); err != nil {
		t.Fatalf("instrumented source does not parse: %v\n%s", err, src)
	}
}

func TestInstrumentSourceNoFuncs(t *testing.T) {
	src := []byte("package sample\n\nvar X = 1\n")
	out, pkg, changed, err := instrumentSource(src, "sample.go")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	if changed || string(out) != string(src) || pkg != "sample" {
		t.Errorf("expected unchanged source, got changed=%v pkg=%q", changed, pkg)
	}
}

func TestInstrumentSourceParseError(t *testing.T) {
	if _, _, _, err := instrumentSource([]byte("package sample\nfunc {"), "x.go"); err == nil {
		t.Fatal("expected parse error")
	}
}

// shardJSON is a minimal shard document: one method's cumulative stats.
func shardJSON(calls, total, self, minV, maxV int64) string {
	return fmt.Sprintf(`{"m": {"calls": %d, "totalMicros": %d, "totalSelfMicros": %d, "minMicros": %d, "maxMicros": %d}}`,
		calls, total, self, minV, maxV)
}

func TestMergeTimingStatsSumsAndExtremes(t *testing.T) {
	stats := map[string]*timingStats{}
	mergeTimingStats(stats, "a", &timingStats{Calls: 2, TotalMicros: 40, TotalSelfMicros: 28, MinMicros: 10, MaxMicros: 30})
	mergeTimingStats(stats, "a", &timingStats{Calls: 3, TotalMicros: 60, TotalSelfMicros: 55, MinMicros: 15, MaxMicros: 25})
	mergeTimingStats(stats, "b", &timingStats{Calls: 1, TotalMicros: 5, TotalSelfMicros: 5, MinMicros: 5, MaxMicros: 5})
	got := stats["a"]
	if got.Calls != 5 || got.TotalMicros != 100 || got.TotalSelfMicros != 83 ||
		got.MinMicros != 10 || got.MaxMicros != 30 {
		t.Errorf("merged a = %+v, want calls 5 total 100 self 83 min 10 max 30 (sum totals, min minima, max maxima)", got)
	}
	if got := stats["b"]; got.Calls != 1 || got.MeanMicros() != 5 || got.TotalSelfMicros != 5 {
		t.Errorf("merged b = %+v", got)
	}
}

func TestReadTimings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "prof-1-pkg-a.json"), shardJSON(2, 20, 17, 8, 12))
	// Diagnostics keys are skipped; corrupt shards and in-flight tmp
	// files and non-prof files are ignored.
	writeFile(t, filepath.Join(dir, "prof-42-pkg-b.json"), `{"m": {"calls": 1, "totalMicros": 10, "totalSelfMicros": 8, "minMicros": 10, "maxMicros": 10}, "#flushes": {"calls": 3}}`)
	writeFile(t, filepath.Join(dir, "prof-7-corrupt.json"), "{not json")
	writeFile(t, filepath.Join(dir, "prof-8-inflight.json.tmp.0"), shardJSON(99, 99, 99, 99, 99))
	writeFile(t, filepath.Join(dir, "ignore.txt"), shardJSON(99, 99, 99, 99, 99))
	stats := readTimings(dir)
	if len(stats) != 1 {
		t.Fatalf("got %d keys, want 1: %+v", len(stats), stats)
	}
	got := stats["m"]
	if got == nil || got.Calls != 3 || got.TotalMicros != 30 || got.TotalSelfMicros != 25 ||
		got.MinMicros != 8 || got.MaxMicros != 12 {
		t.Fatalf("m = %+v, want the two shards merged (sum calls/totals, min min, max max)", got)
	}
	if got := readTimings(filepath.Join(dir, "missing")); len(got) != 0 {
		t.Errorf("missing dir should yield empty, got %+v", got)
	}
}

// TestTailLinesAndReportTestFailure pins the ad4f27b runner contract:
// a failing test run is reported with the explicit TESTS FAILED marker
// and the LAST 30 lines of each stream (progress noise lives at the
// head, the actual error at the tail), so a threshold exit (2) is never
// mistakable for the test run's own failure.
func TestTailLinesAndReportTestFailure(t *testing.T) {
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	got := tailLines(strings.Join(lines, "\n"), 30)
	if strings.Contains(got, "line-10") || !strings.Contains(got, "line-11") {
		t.Errorf("tailLines kept the wrong window: %q", got)
	}
	if tailLines("one\ntwo", 30) != "one\ntwo" {
		t.Errorf("tailLines should keep short texts whole")
	}
	var stderr bytes.Buffer
	reportTestFailure(1, "head\n"+strings.Repeat("x\n", 35)+"actual error tail", "", &stderr)
	out := stderr.String()
	if !strings.Contains(out, "TESTS FAILED (exit code 1)") {
		t.Errorf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "actual error tail") || strings.Contains(out, "head\n") {
		t.Errorf("stdout tail not reported as the last 30 lines:\n%s", out)
	}
}

// TestRunInstrumentedTestsReportsFailureAndContinues is the real-path
// proof: a failing go test run must NOT abort profiling (nil error) —
// the marker plus the streams' tail go to stderr and the caller proceeds
// to read whatever profiling data exists.
func TestRunInstrumentedTestsReportsFailureAndContinues(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/failmod\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "boom_test.go"), `package failmod

import "testing"

func TestBoom(t *testing.T) {
	t.Fatal("kaboom")
}
`)
	var stdout, stderr bytes.Buffer
	err := runInstrumentedTests(dir, dir, filepath.Join(dir, "out"), profileOptions{}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("test failure must not abort: %v", err)
	}
	if !strings.Contains(stderr.String(), "TESTS FAILED (exit code 1)") {
		t.Errorf("missing TESTS FAILED marker:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "kaboom") {
		t.Errorf("test error tail missing from stderr:\n%s", stderr.String())
	}
}

func profileFixture() []methodProfile {
	return []methodProfile{
		{Method: MethodDescriptor{Name: "Slow", StartLine: 3}, File: "a.go",
			Timing: timingStats{Calls: 2, TotalMicros: 3000, TotalSelfMicros: 2000, MinMicros: 1000, MaxMicros: 2000}},
		{Method: MethodDescriptor{Name: "Fast", StartLine: 7}, File: "a.go",
			Timing: timingStats{Calls: 4, TotalMicros: 1000, TotalSelfMicros: 800, MinMicros: 100, MaxMicros: 400}},
	}
}

func TestFormatProfileReport(t *testing.T) {
	report := FormatProfileReport(profileFixture(), 0, 0)
	for _, want := range []string{
		"Profile Report (2 methods, total 4.00ms)",
		"TOTAL", "SELF",
		"Slow", "a.go:3", "2.00ms",
		"Fast", "a.go:7", "0.80ms",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	// Slow (75.0% of 4ms) must be listed before Fast.
	if strings.Index(report, "Slow") > strings.Index(report, "Fast") {
		t.Errorf("rows not sorted by total desc:\n%s", report)
	}
	if !strings.Contains(report, "75.0%") {
		t.Errorf("missing share percentage:\n%s", report)
	}
}

func TestFormatProfileReportTopAndThreshold(t *testing.T) {
	report := FormatProfileReport(profileFixture(), 1, 2.0)
	if strings.Contains(report, "Fast") {
		t.Errorf("top=1 should drop the second row:\n%s", report)
	}
	if !strings.Contains(report, "Threshold: 2.00ms — 1 method exceeds") {
		t.Errorf("missing threshold verdict:\n%s", report)
	}
	ok := FormatProfileReport(profileFixture(), 0, 5.0)
	if !strings.Contains(ok, "Threshold: 5.00ms — all methods OK") {
		t.Errorf("missing OK verdict:\n%s", ok)
	}
}

func TestProfileExitCode(t *testing.T) {
	var stderr bytes.Buffer
	if got := profileExitCode(profileFixture(), 0, &stderr); got != 0 {
		t.Errorf("threshold off: exit %d, want 0", got)
	}
	stderr.Reset()
	if got := profileExitCode(profileFixture(), 2.0, &stderr); got != 2 || stderr.Len() == 0 {
		t.Errorf("exceeded: exit %d stderr %q, want 2 with message", got, stderr.String())
	}
	stderr.Reset()
	if got := profileExitCode(profileFixture(), 5.0, &stderr); got != 0 {
		t.Errorf("under: exit %d, want 0", got)
	}
}

func TestAttributeTimings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package p\n\nfunc Slow() int { return 1 }\n")
	writeFile(t, filepath.Join(root, "broken.go"), "package p\nfunc {{{\n")
	timings := map[string]*timingStats{
		"a.go|Slow":     {Calls: 1, TotalMicros: 5},
		"ghost.go|Nope": {Calls: 1, TotalMicros: 9},
	}
	profiles := attributeTimings(root, timings)
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1: %+v", len(profiles), profiles)
	}
	p := profiles[0]
	if p.Method.Name != "Slow" || p.File != "a.go" || p.Timing.TotalMicros != 5 {
		t.Errorf("attributed profile = %+v", p)
	}
}

func TestParseProfileOptionsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--bogus"},
		{"--top", "0"},
		{"--threshold", "-1"},
	} {
		var stderr bytes.Buffer
		if _, code, ok := parseProfileOptions(args, &stderr); ok || code != 1 {
			t.Errorf("parseProfileOptions(%v) = code %d ok %v, want 1 false", args, code, ok)
		}
	}
	var stderr bytes.Buffer
	opts, code, ok := parseProfileOptions([]string{"--name", "X", "--top", "5", "p/..."}, &stderr)
	if !ok || code != 0 || opts.name != "X" || opts.top != 5 || len(opts.paths) != 1 {
		t.Errorf("valid options = %+v code %d ok %v", opts, code, ok)
	}
}

// setupProfileModule creates a tiny compilable module used by the end-to-end
// profile tests.
func setupProfileModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/prof\n\ngo 1.22\n")
	writeFile(t, filepath.Join(root, "math.go"), `package prof

// Slow accumulates n ints so the profiled duration is well above zero.
func Slow(n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		sum += i
	}
	return sum
}
`)
	writeFile(t, filepath.Join(root, "math_test.go"), `package prof

import "testing"

func TestSlow(t *testing.T) {
	if Slow(200000) <= 0 {
		t.Fatal("positive sum expected")
	}
}
`)
	return root
}

func TestRun_ProfileEndToEnd(t *testing.T) {
	root := setupProfileModule(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"profile", "--name", "TestSlow"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Slow") {
		t.Errorf("table missing Slow:\n%s\nstderr:\n%s", out.String(), errOut.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, "profile-reports"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("profile-reports contents wrong: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".crap_profile_temp")); !os.IsNotExist(err) {
		t.Errorf("temp dir not cleaned up: %v", err)
	}
}

func TestRun_ProfileThresholdExceeded(t *testing.T) {
	root := setupProfileModule(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"profile", "--name", "TestSlow", "--threshold", "0.001"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
}

func TestRun_ProfileBadFlag(t *testing.T) {
	root := setupProfileModule(t)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"profile", "--bogus"}, root, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

// TestRemapProfilePaths is the 0.9 regression: absolute paths under the
// original project must point into the instrumented copy, or go test runs
// the original, non-instrumented files and the report comes back empty.
func TestRemapProfilePaths(t *testing.T) {
	root := t.TempDir()
	src := root + string(filepath.Separator)
	got := remapProfilePaths(root, []string{
		src + "parser", // abs dir under root
		src + "pkg" + string(filepath.Separator) + "a_test.go", // abs file under root
		"./pkg/...",                // project-relative stays
		"/elsewhere/other_test.go", // abs outside root stays
	})
	want := []string{"./parser", "./pkg/a_test.go", "./pkg/...", "/elsewhere/other_test.go"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("remapped = %v, want %v", got, want)
	}
}

// TestProfileTestArgs is the 0.9.3 regression: explicit paths must be passed
// exclusively — a ./... default alongside them would profile the whole module
// in addition to the requested files. ./... may appear only when no paths are
// given.
func TestProfileTestArgs(t *testing.T) {
	root := t.TempDir()

	explicit := profileTestArgs(root, profileOptions{name: "TestFoo", paths: []string{"./pkg/a_test.go", "./b_test.go"}})
	wantExplicit := []string{"test", "-count=1", "-run", "TestFoo", "./pkg/a_test.go", "./b_test.go"}
	if fmt.Sprint(explicit) != fmt.Sprint(wantExplicit) {
		t.Errorf("explicit-paths argv = %v, want %v", explicit, wantExplicit)
	}

	noPaths := profileTestArgs(root, profileOptions{})
	wantDefault := []string{"test", "-count=1", "./..."}
	if fmt.Sprint(noPaths) != fmt.Sprint(wantDefault) {
		t.Errorf("no-paths argv = %v, want %v", noPaths, wantDefault)
	}
}

func TestFormatProfileReportMeanCaveat(t *testing.T) {
	profiles := []methodProfile{
		{Method: MethodDescriptor{Name: "Fastish"}, File: "a.go", Timing: timingStats{Calls: 100, TotalMicros: 1000, MinMicros: 10, MaxMicros: 10}},
		{Method: MethodDescriptor{Name: "Slowish"}, File: "a.go", Timing: timingStats{Calls: 10, TotalMicros: 1000000, MinMicros: 100000, MaxMicros: 100000}},
	}
	report := FormatProfileReport(profiles, 0, 0)
	if !strings.Contains(report, "~10.0") {
		t.Errorf("sub-30µs mean not marked with ~:\n%s", report)
	}
	if !strings.Contains(report, "100000.0") {
		t.Errorf("large mean rendered wrong:\n%s", report)
	}
	if strings.Count(report, "~") != 1 {
		t.Errorf("exactly one mean should carry the caveat:\n%s", report)
	}
}

func TestFmtTotal(t *testing.T) {
	tests := []struct {
		millis float64
		want   string
	}{
		{0, "0.00ms"},
		{82.5, "82.50ms"},
		{999.99, "999.99ms"},
		{1000, "1.00s"}, // ms/s boundary
		{13890, "13.89s"},
		{59999.99, "60.00s"},
		{60000, "1.00m"}, // s/m boundary
		{1350000, "22.50m"},
		{3599999, "60.00m"},
		{3600000, "1.00h"}, // m/h boundary
		{50004000, "13.89h"},
	}
	for _, tt := range tests {
		if got := fmtTotal(tt.millis); got != tt.want {
			t.Errorf("fmtTotal(%v) = %q, want %q", tt.millis, got, tt.want)
		}
	}
}

func TestProfileJSONSelfTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := writeProfileJSON(path, profileFixture()); err != nil {
		t.Fatalf("writeProfileJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report profileJSONReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(report.Methods) != 2 || report.Methods[0].TotalSelfMicros != 2000 ||
		report.Methods[1].TotalSelfMicros != 800 {
		t.Errorf("JSON self time wrong: %+v", report.Methods)
	}
}

func TestCollectorSourceCompiles(t *testing.T) {
	src := collectorSource("sample")
	if !strings.Contains(src, "package sample") {
		t.Fatalf("collector not in package sample:\n%s", src)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "", src, 0); err != nil {
		t.Fatalf("collector does not parse: %v\n%s", err, src)
	}
}
