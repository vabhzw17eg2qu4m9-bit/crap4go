package main

import (
	"bytes"
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
		`defer crap4goRecord("sample.go|Add")()`,
		`defer crap4goRecord("sample.go|(Calc)Show")()`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("instrumented source missing %q:\n%s", want, src)
		}
	}
	// Empty bodies are wrapped too; bodiless declarations are not.
	if !strings.Contains(src, `defer crap4goRecord("sample.go|Empty")()`) {
		t.Errorf("empty body not wrapped:\n%s", src)
	}
	if strings.Contains(src, "ext") && strings.Count(src, "crap4goRecord") != 3 {
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

func TestSplitTimingLine(t *testing.T) {
	tests := []struct {
		line   string
		key    string
		micros int64
		ok     bool
	}{
		{"sample.go|Add\t42", "sample.go|Add", 42, true},
		{"k\t0", "k", 0, true},
		{"no-tab", "", 0, false},
		{"\t5", "", 0, false},
		{"k\tnotanumber", "", 0, false},
		{"", "", 0, false},
	}
	for _, tt := range tests {
		key, micros, ok := splitTimingLine(tt.line)
		if ok != tt.ok || key != tt.key || micros != tt.micros {
			t.Errorf("splitTimingLine(%q) = (%q,%d,%v), want (%q,%d,%v)",
				tt.line, key, micros, ok, tt.key, tt.micros, tt.ok)
		}
	}
}

func TestMergeTiming(t *testing.T) {
	stats := map[string]*timingStats{}
	mergeTiming(stats, "a", 10)
	mergeTiming(stats, "a", 30)
	mergeTiming(stats, "b", 5)
	got := stats["a"]
	if got.Calls != 2 || got.TotalMicros != 40 || got.MinMicros != 10 || got.MaxMicros != 30 {
		t.Errorf("merge a = %+v", got)
	}
	if got := stats["b"]; got.Calls != 1 || got.MeanMicros() != 5 {
		t.Errorf("merge b = %+v", got)
	}
}

func TestReadTimings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "prof-1.jsonl"), "a\t10\nb\t5\n")
	writeFile(t, filepath.Join(dir, "prof-2.jsonl"), "a\t20\n")
	writeFile(t, filepath.Join(dir, "ignore.txt"), "a\t99\n")
	stats := readTimings(dir)
	if len(stats) != 2 {
		t.Fatalf("got %d keys, want 2: %+v", len(stats), stats)
	}
	if got := stats["a"]; got.Calls != 2 || got.TotalMicros != 30 || got.MaxMicros != 20 {
		t.Errorf("a = %+v", got)
	}
	if got := readTimings(filepath.Join(dir, "missing")); len(got) != 0 {
		t.Errorf("missing dir should yield empty, got %+v", got)
	}
}

func profileFixture() []methodProfile {
	return []methodProfile{
		{Method: MethodDescriptor{Name: "Slow", StartLine: 3}, File: "a.go",
			Timing: timingStats{Calls: 2, TotalMicros: 3000, MinMicros: 1000, MaxMicros: 2000}},
		{Method: MethodDescriptor{Name: "Fast", StartLine: 7}, File: "a.go",
			Timing: timingStats{Calls: 4, TotalMicros: 1000, MinMicros: 100, MaxMicros: 400}},
	}
}

func TestFormatProfileReport(t *testing.T) {
	report := FormatProfileReport(profileFixture(), 0, 0)
	for _, want := range []string{
		"Profile Report (2 methods, total 4.00ms)",
		"Slow", "a.go:3",
		"Fast", "a.go:7",
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

func TestCollectorSourceCompiles(t *testing.T) {
	src := collectorSource("sample")
	if !strings.Contains(src, "package sample") {
		t.Fatalf("collector not in package sample:\n%s", src)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "", src, 0); err != nil {
		t.Fatalf("collector does not parse: %v\n%s", err, src)
	}
}
