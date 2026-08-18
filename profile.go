package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// timingStats is the per-method timing aggregate collected by the generated
// collector and merged host-side.
type timingStats struct {
	Calls       int64 `json:"calls"`
	TotalMicros int64 `json:"totalMicros"`
	MinMicros   int64 `json:"minMicros"`
	MaxMicros   int64 `json:"maxMicros"`
}

// MeanMicros returns the average time per call in microseconds.
func (t timingStats) MeanMicros() float64 {
	if t.Calls == 0 {
		return 0
	}
	return float64(t.TotalMicros) / float64(t.Calls)
}

// methodProfile pairs an analyzed method with its timing aggregate and the
// method's file path relative to the project root.
type methodProfile struct {
	Method MethodDescriptor
	File   string
	Timing timingStats
}

// profileOptions holds the parsed `profile` subcommand configuration.
type profileOptions struct {
	name        string
	thresholdMs float64
	top         int
	paths       []string
}

// parseProfileOptions parses the profile subcommand flags: --name, --threshold
// (ms, default off), --top (default 20). Returns ok=false on a bad flag or
// value, with the error already reported to stderr.
func parseProfileOptions(args []string, stderr io.Writer) (profileOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go profile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "run only tests matching this pattern (go test -run)")
	threshold := fs.Float64("threshold", 0, "exit 2 when a method's total exceeds this many ms (0 = off)")
	top := fs.Int("top", 20, "console rows shown")
	if err := fs.Parse(args); err != nil {
		return profileOptions{}, 1, false
	}
	if *top <= 0 {
		fmt.Fprintln(stderr, "top must be positive")
		return profileOptions{}, 1, false
	}
	if *threshold < 0 {
		fmt.Fprintln(stderr, "threshold must not be negative")
		return profileOptions{}, 1, false
	}
	return profileOptions{
		name:        *name,
		thresholdMs: *threshold,
		top:         *top,
		paths:       fs.Args(),
	}, 0, true
}

// RunProfileCommand implements `crap4go profile`: it copies the module to a
// temp dir, instruments every function body with a defer-based timer, runs
// `go test` against the copy, prints the timing table, writes full reports to
// profile-reports/, and cleans up. Returns exit code 2 when --threshold is
// set and any method's total exceeds it.
func RunProfileCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseProfileOptions(args, stderr)
	if !ok {
		return code
	}
	tempDir := filepath.Join(root, ".crap_profile_temp")
	if err := prepareInstrumentedCopy(root, tempDir); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanupTempDir(tempDir)

	outDir := filepath.Join(tempDir, ".crap_profile_out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "Running instrumented tests...")
	if err := runInstrumentedTests(root, tempDir, outDir, opts, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	profiles := attributeTimings(root, readTimings(outDir))
	if len(profiles) == 0 {
		fmt.Fprintln(stderr, "Profiling did not produce any results.")
		return 1
	}
	fmt.Fprint(stdout, FormatProfileReport(profiles, opts.top, opts.thresholdMs))
	writeProfileReports(root, profiles, opts.thresholdMs, stderr)
	return profileExitCode(profiles, opts.thresholdMs, stderr)
}

// cleanupTempDir removes the instrumented copy unless CRAP_PROFILE_DEBUG is
// set (mirrors crap4dart's keep-temp debug knob).
func cleanupTempDir(tempDir string) {
	if os.Getenv("CRAP_PROFILE_DEBUG") != "" {
		return
	}
	os.RemoveAll(tempDir)
}

// prepareInstrumentedCopy recreates tempDir as an instrumented copy of the
// module at root: full file copy, then every non-test .go file gets its
// function bodies wrapped and a collector file is added per package.
func prepareInstrumentedCopy(root, tempDir string) error {
	if err := os.RemoveAll(tempDir); err != nil {
		return err
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}
	if err := copyModule(root, tempDir); err != nil {
		return err
	}
	return instrumentModule(tempDir)
}

// skipCopyEntry reports whether the repo entry named is not needed in the
// instrumented module copy.
func skipCopyEntry(name string, isDir bool) bool {
	if !isDir {
		return strings.HasSuffix(name, ".out")
	}
	switch name {
	case ".git", ".crap_profile_temp", "profile-reports":
		return true
	}
	return false
}

// copyModule recursively copies the module at src into dst, skipping .git,
// the temp dir itself, profile-reports/, and coverage artifacts.
func copyModule(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != src {
			if skipCopyEntry(d.Name(), d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, relPath(src, path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// instrumentModule instruments every source file under root and writes one
// collector file into each directory that received instrumentation. Files
// that fail to parse (e.g. testdata fixtures) are left unchanged.
func instrumentModule(root string) error {
	files, err := FindSourceFiles(root)
	if err != nil {
		return err
	}
	collectors := map[string]string{}
	for _, f := range files {
		if err := instrumentFile(f, root, collectors); err != nil {
			return err
		}
	}
	return writeCollectors(collectors)
}

// instrumentFile instruments one source file in place, recording its package
// name under the file's directory when any function was wrapped.
func instrumentFile(path, root string, collectors map[string]string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, pkg, changed, err := instrumentSource(src, relPath(root, path))
	if err != nil || !changed {
		return nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	collectors[filepath.Dir(path)] = pkg
	return nil
}

// instrumentSource wraps every function/method body in src with
// `defer crap4goRecord("<key>")()` immediately after the opening brace. Keys
// are "<relPath>|<method name>" using the same naming as ExtractMethods.
// Returns the (possibly unchanged) source, the package name, whether any
// function was wrapped, and a parse error if src does not parse.
func instrumentSource(src []byte, relPath string) ([]byte, string, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil, "", false, err
	}
	var insertions []funcInsertion
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		key := relPath + "|" + funcDisplayName(fd)
		offset := fset.Position(fd.Body.Lbrace).Offset + 1
		insertions = append(insertions, funcInsertion{
			offset: offset,
			text:   "\n\tdefer crap4goRecord(" + strconv.Quote(key) + ")()\n",
		})
	}
	if len(insertions) == 0 {
		return src, f.Name.Name, false, nil
	}
	return applyInsertions(src, insertions), f.Name.Name, true, nil
}

// funcInsertion is one deferred-timer insertion at a byte offset.
type funcInsertion struct {
	offset int
	text   string
}

// applyInsertions splices the insertions into src from last to first so
// earlier offsets stay valid.
func applyInsertions(src []byte, insertions []funcInsertion) []byte {
	out := string(src)
	for i := len(insertions) - 1; i >= 0; i-- {
		out = out[:insertions[i].offset] + insertions[i].text + out[insertions[i].offset:]
	}
	return []byte(out)
}

// funcDisplayName returns the ExtractMethods-style name of a FuncDecl:
// "(Foo)Bar" for methods, "Bar" for plain functions.
func funcDisplayName(fd *ast.FuncDecl) string {
	if recv := receiverTypeName(fd); recv != "" {
		return "(" + recv + ")" + fd.Name.Name
	}
	return fd.Name.Name
}

// writeCollectors writes one generated collector file per instrumented
// directory, declaring the package's own name so it compiles into that
// package.
func writeCollectors(collectors map[string]string) error {
	for dir, pkg := range collectors {
		path := filepath.Join(dir, "zz_crap_collector.go")
		if err := os.WriteFile(path, []byte(collectorSource(pkg)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runInstrumentedTests runs `go test -count=1` in dir with the collector
// output directory in the environment. -count=1 bypasses Go's test cache,
// which would otherwise skip running the instrumented binaries. Positional
// paths are remapped from the original project root into the instrumented
// copy; without paths the whole module (./...) runs.
func runInstrumentedTests(root, dir, outDir string, opts profileOptions, stdout, stderr io.Writer) error {
	args := []string{"test", "-count=1"}
	if opts.name != "" {
		args = append(args, "-run", opts.name)
	}
	if len(opts.paths) > 0 {
		args = append(args, remapProfilePaths(root, opts.paths)...)
	} else {
		args = append(args, "./...")
	}
	return runGoTests(dir, args, append(os.Environ(), "CRAP_PROFILE_DIR="+outDir), stdout, stderr)
}

// remapProfilePaths remaps explicit test paths into the instrumented temp
// copy: project-relative paths stay unchanged (they resolve inside the temp
// copy, the working directory of go test), and absolute paths under the
// original project root become "./<rel>" — otherwise go test would run the
// ORIGINAL, non-instrumented files and produce empty reports (ported from
// crap4dart 0.9).
func remapProfilePaths(root string, paths []string) []string {
	remapped := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if !filepath.IsAbs(p) || err != nil || strings.HasPrefix(rel, "..") {
			remapped = append(remapped, p)
			continue
		}
		remapped = append(remapped, "./"+filepath.ToSlash(rel))
	}
	return remapped
}

// readTimings aggregates the collectors' prof-*.jsonl logs in dir into
// per-key timing stats. A missing dir yields an empty map.
func readTimings(dir string) map[string]*timingStats {
	stats := map[string]*timingStats{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stats
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			aggregateTimingFile(filepath.Join(dir, e.Name()), stats)
		}
	}
	return stats
}

// aggregateTimingFile folds one collector log into stats.
func aggregateTimingFile(path string, stats map[string]*timingStats) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, micros, ok := splitTimingLine(line)
		if ok {
			mergeTiming(stats, key, micros)
		}
	}
}

// splitTimingLine parses one "<key>\t<micros>" log line.
func splitTimingLine(line string) (string, int64, bool) {
	key, rest, found := strings.Cut(line, "\t")
	if !found || key == "" {
		return "", 0, false
	}
	micros, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return key, micros, true
}

// mergeTiming folds one recorded call into the per-key aggregate.
func mergeTiming(stats map[string]*timingStats, key string, micros int64) {
	s := stats[key]
	if s == nil {
		s = &timingStats{MinMicros: micros}
		stats[key] = s
	}
	s.Calls++
	s.TotalMicros += micros
	s.MinMicros = min(s.MinMicros, micros)
	s.MaxMicros = max(s.MaxMicros, micros)
}

// attributeTimings matches timing keys to the project's method inventory
// (same parsing as analyze) and returns profiles sorted by total time
// descending. Timing entries without a matching method are ignored.
func attributeTimings(root string, timings map[string]*timingStats) []methodProfile {
	files, err := FindSourceFiles(root)
	if err != nil {
		return nil
	}
	profiles := []methodProfile{}
	for _, f := range files {
		profiles = append(profiles, attributeFile(root, f, timings)...)
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		return profiles[i].Timing.TotalMicros > profiles[j].Timing.TotalMicros
	})
	return profiles
}

// attributeFile matches timings to the methods of one source file.
func attributeFile(root, path string, timings map[string]*timingStats) []methodProfile {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	methods, err := ExtractMethods(path, src)
	if err != nil {
		return nil
	}
	rel := relPath(root, path)
	var profiles []methodProfile
	for _, m := range methods {
		if t, ok := timings[rel+"|"+m.Name]; ok {
			profiles = append(profiles, methodProfile{Method: m, File: rel, Timing: *t})
		}
	}
	return profiles
}

// relPath returns path relative to root with forward slashes, or path itself
// when it cannot be made relative.
func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// totalMicros sums the total time across all profiled methods.
func totalMicros(profiles []methodProfile) int64 {
	var sum int64
	for _, p := range profiles {
		sum += p.Timing.TotalMicros
	}
	return sum
}

// FormatProfileReport renders the contract table: TOTAL(ms) | % | CALLS |
// MEAN(µs) | MAX(µs) | @60fps(ms) | METHOD | FILE:LINE, sorted by total
// descending. top > 0 limits the rows shown; thresholdMs > 0 appends the
// threshold verdict line.
func FormatProfileReport(profiles []methodProfile, top int, thresholdMs float64) string {
	total := totalMicros(profiles)
	var b strings.Builder
	fmt.Fprintf(&b, "Profile Report (%d methods, total %.2fms)\n", len(profiles), float64(total)/1000.0)
	b.WriteString("TOTAL(ms)     %  CALLS  MEAN(µs)  MAX(µs)  @60fps(ms)  METHOD                     FILE:LINE\n")
	b.WriteString("------------------------------------------------------------------------------------------\n")
	for _, p := range topProfiles(profiles, top) {
		fmt.Fprintf(&b, "%9.2f %5.1f%% %6d %9s %8d %10.2f  %-25s %s:%d\n",
			float64(p.Timing.TotalMicros)/1000.0,
			shareOfTotal(p.Timing.TotalMicros, total),
			p.Timing.Calls,
			formatMeanMicros(p.Timing.MeanMicros()),
			p.Timing.MaxMicros,
			p.Timing.MeanMicros()*60.0/1000.0,
			p.Method.Name,
			p.File,
			p.Method.StartLine,
		)
	}
	b.WriteString("\n")
	writeThresholdVerdict(&b, profiles, thresholdMs)
	return b.String()
}

// topProfiles returns at most top entries (all when top <= 0).
func topProfiles(profiles []methodProfile, top int) []methodProfile {
	if top <= 0 || top >= len(profiles) {
		return profiles
	}
	return profiles[:top]
}

// formatMeanMicros renders the MEAN column, marking sub-30µs means with ~:
// the defer-based instrumentation costs on the order of a microsecond per
// call, so such means are mostly noise from the profiler itself — read the
// CALLS and TOTAL deltas instead (ported from crap4dart 0.9.2).
func formatMeanMicros(mean float64) string {
	s := fmt.Sprintf("%.1f", mean)
	if mean < 30 {
		return "~" + s
	}
	return s
}

// shareOfTotal returns the percentage share of total time.
func shareOfTotal(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100.0
}

// writeThresholdVerdict appends the threshold line when a threshold is set.
func writeThresholdVerdict(b *strings.Builder, profiles []methodProfile, thresholdMs float64) {
	if thresholdMs <= 0 {
		return
	}
	exceeding := 0
	for _, p := range profiles {
		if float64(p.Timing.TotalMicros)/1000.0 > thresholdMs {
			exceeding++
		}
	}
	if exceeding > 0 {
		plural := "methods exceed"
		if exceeding == 1 {
			plural = "method exceeds"
		}
		fmt.Fprintf(b, "Threshold: %.2fms — %d %s\n", thresholdMs, exceeding, plural)
		return
	}
	fmt.Fprintf(b, "Threshold: %.2fms — all methods OK\n", thresholdMs)
}

// profileExitCode returns 2 when --threshold is set and any method's total
// exceeds it (reported to stderr), else 0.
func profileExitCode(profiles []methodProfile, thresholdMs float64, stderr io.Writer) int {
	for _, p := range profiles {
		if thresholdMs <= 0 || float64(p.Timing.TotalMicros)/1000.0 <= thresholdMs {
			continue
		}
		fmt.Fprintf(stderr, "Profile threshold exceeded: %s total %.2fms > %.2fms\n",
			p.Method.Name, float64(p.Timing.TotalMicros)/1000.0, thresholdMs)
		return 2
	}
	return 0
}

// writeProfileReports writes the full (untruncated) table and a JSON report
// to profile-reports/profile-<timestamp>.{txt,json} under root.
func writeProfileReports(root string, profiles []methodProfile, thresholdMs float64, stderr io.Writer) {
	dir := filepath.Join(root, "profile-reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return
	}
	stamp := time.Now().Format("20060102-150405")
	txtPath := filepath.Join(dir, "profile-"+stamp+".txt")
	jsonPath := filepath.Join(dir, "profile-"+stamp+".json")
	if err := os.WriteFile(txtPath, []byte(FormatProfileReport(profiles, 0, thresholdMs)), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return
	}
	if err := writeProfileJSON(jsonPath, profiles); err != nil {
		fmt.Fprintln(stderr, err)
		return
	}
	fmt.Fprintf(stderr, "Profile report written to %s and %s\n", txtPath, jsonPath)
}

// writeProfileJSON writes the machine-readable report.
func writeProfileJSON(path string, profiles []methodProfile) error {
	methods := make([]profileJSONMethod, len(profiles))
	for i, p := range profiles {
		methods[i] = profileJSONMethod{
			Method:      p.Method.Name,
			File:        p.File,
			Line:        p.Method.StartLine,
			Calls:       p.Timing.Calls,
			TotalMicros: p.Timing.TotalMicros,
			MinMicros:   p.Timing.MinMicros,
			MaxMicros:   p.Timing.MaxMicros,
			MeanMicros:  p.Timing.MeanMicros(),
		}
	}
	data, err := json.MarshalIndent(profileJSONReport{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Methods:     methods,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// profileJSONReport is the JSON profile report document.
type profileJSONReport struct {
	GeneratedAt string              `json:"generatedAt"`
	Methods     []profileJSONMethod `json:"methods"`
}

// profileJSONMethod is one method entry in the JSON profile report.
type profileJSONMethod struct {
	Method      string  `json:"method"`
	File        string  `json:"file"`
	Line        int     `json:"line"`
	Calls       int64   `json:"calls"`
	TotalMicros int64   `json:"totalMicros"`
	MinMicros   int64   `json:"minMicros"`
	MaxMicros   int64   `json:"maxMicros"`
	MeanMicros  float64 `json:"meanMicros"`
}
