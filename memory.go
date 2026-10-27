package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The `memory` subcommand, ported from crap4dart 3bae178bc0 (memory:
// per-test GC/alloc attribution via --verbose_gc, phase 1).
//
// Upstream runs `dart --verbose_gc test --concurrency=1 -r json` and
// attributes GC events to suites/tests via a voted clock offset between
// the VM's monotonic GC clock and the reporter's wall clock. The Go
// mapping replaces both clocks with STREAM POSITION: gctrace lines and
// === RUN / --- PASS markers interleave in one ordered output stream
// (the go tool relays the test binary's combined output live), so a GC
// belongs to the innermost open test window — no clock math at all
// (which is why there is no clockOffsetSeconds in the JSON).
//
// Tracing is scoped to the test binaries with the go tool's -exec hook
// (`go test -exec 'env GODEBUG=gctrace=1'`): GODEBUG exported into the
// environment would also trace the go tool and every compile/link
// subprocess, whose GC lines would pollute the interleaving. The wrapper
// sets it only for the test binary — the same filtering role upstream's
// `isolate.startsWith('test_suite')` plays. GODEBUG is stripped from the
// command environment (including an ambient go env -w value) for the
// same reason.
//
// Metrics (documented divergence from upstream's new/old-space split —
// Go's gctrace prints one heap triple `start->end->liveAfter MB`):
//   - churn  = sum of max(0, heap-at-GC-start - live-heap-after-GC):
//     the volume this GC cycle reclaimed, the honest "allocated MB"
//     proxy (upstream: reclaimedNewMb = newBefore - newAfter).
//   - oldGrowth = live-heap-after-GC delta vs the previous GC, baseline
//     0 for each test binary's first GC (upstream: oldAfter - oldBefore;
//     Go has no old-gen field in the trace, so retained-heap growth
//     across GCs is the analog — positive and never freed = leak
//     suspect). The baseline resets per package: each package runs as
//     its own test binary (same reason upstream never merges isolates).
//
// Attribution windows (the interleaving rules; sequential `-p 1`):
//   - `=== RUN <name>` opens a window; subtests nest inside parents.
//   - `--- PASS/FAIL/SKIP: <name>` and `=== PAUSE <name>` close it
//     (`=== CONT <name>` reopens the paused one); `=== NAME <name>`
//     raises an open window back to the top of the stack.
//   - A GC line lands on the innermost open window; with no open window
//     it is unattributed: only rows INSIDE test windows are trusted, so
//     loading/teardown rows and rows between suites land in the report's
//     unattributed bucket — reported, never polluting test or package
//     totals (upstream phase 2, 11551739c8: rows outside a suite's test
//     windows go to an unattributed bucket that never pollutes file
//     totals).
//   - GCs before a `?  <pkg>  [no test files]` summary are discarded:
//     no test binary ran for that package.
//   - A GC line after --- PASS but before the next === RUN must not
//     leak into the next test — it is unattributed by construction.

// gcLineRe matches a real gctrace row, e.g.
// "gc 12 @0.005s 5%: 0.042+0.065+0.003 ms clock, 0.59+0/0.11/0.017+0.043 ms cpu, 1->1->0 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 14 P (forced)".
// Only the gc number, start seconds and the heap triple are read; the
// clock/cpu sections, goal/stacks/globals fields and the (forced) suffix
// vary across Go versions and are ignored.
var gcLineRe = regexp.MustCompile(`^gc (\d+) @([0-9.]+)s .*?(\d+)->(\d+)->(\d+) MB`)

// gcAnchorRe locates a row START anywhere in a (possibly torn) line.
var gcAnchorRe = regexp.MustCompile(`gc \d+ @[0-9.]+s`)

// resultMarkerRe recovers a result marker and its duration from anywhere
// in a line: subtest results print indented (four spaces per depth), and
// a following gctrace row can be glued onto the duration by the go
// tool's output relay. Subtest names may contain spaces (t.Run labels),
// so the name is the shortest run before ` (<duration>)`.
var resultMarkerRe = regexp.MustCompile(`--- (PASS|FAIL|SKIP): (.+?) \([0-9hms.]+\)`)

// gcEvent is one gctrace row.
type gcEvent struct {
	seq          int
	startSeconds float64
	heapStartMb  float64 // heap at GC start
	heapEndMb    float64 // heap at GC end
	heapLiveMb   float64 // live heap after the GC
	oldDeltaMb   float64 // liveAfter delta vs the previous GC (0 on baseline)
}

// churnMb is the volume this GC reclaimed — the allocated-MB proxy.
func (g gcEvent) churnMb() float64 {
	if g.heapStartMb > g.heapLiveMb {
		return g.heapStartMb - g.heapLiveMb
	}
	return 0
}

// parseGcRow parses one gctrace row from the start of s, returning it
// with the number of bytes consumed (the row ends at its heap triple's
// " MB" — anything glued after that is processed separately). A row torn
// mid-numbers parses as nothing: a fragment must never become a row.
func parseGcRow(s string) (gcEvent, int, bool) {
	loc := gcLineRe.FindStringSubmatchIndex(s)
	if loc == nil {
		return gcEvent{}, 0, false
	}
	num := func(group int) float64 {
		v, _ := strconv.ParseFloat(s[loc[2*group]:loc[2*group+1]], 64)
		return v
	}
	seq, _ := strconv.Atoi(s[loc[2]:loc[3]])
	gc := gcEvent{
		seq:          seq,
		startSeconds: num(2),
		heapStartMb:  num(3),
		heapEndMb:    num(4),
		heapLiveMb:   num(5),
	}
	return gc, loc[1], true
}

// parseGcLine parses one complete gctrace row; ok=false for every other
// line.
func parseGcLine(line string) (gcEvent, bool) {
	gc, _, ok := parseGcRow(line)
	return gc, ok
}

// memoryEntry aggregates the GCs of one test or package.
type memoryEntry struct {
	label  string
	pkg    string // owning package path, for test entries
	result string // pass/fail/skip, empty until the test closed
	gcs    []gcEvent
}

func (e *memoryEntry) churnMb() float64 { return sumChurn(e.gcs) }

func (e *memoryEntry) oldGrowthMb() float64 { return sumOldGrowth(e.gcs) }

func sumChurn(gcs []gcEvent) float64 {
	total := 0.0
	for _, gc := range gcs {
		total += gc.churnMb()
	}
	return total
}

func sumOldGrowth(gcs []gcEvent) float64 {
	total := 0.0
	for _, gc := range gcs {
		total += gc.oldDeltaMb
	}
	return total
}

// memoryReport is the full attribution result of one run.
type memoryReport struct {
	testCount    int
	totalGcs     []gcEvent      // every parsed row (bucket rows included)
	unattributed []gcEvent      // rows no test window claims (loading/teardown/between suites)
	packages     []*memoryEntry // in `ok` flush order
	tests        []*memoryEntry // in run start order
}

// unattributedChurnMb is the reclaimed MB of the unattributed bucket.
func (r *memoryReport) unattributedChurnMb() float64 { return sumChurn(r.unattributed) }

// topChurn returns tests with GC activity sorted by churn, descending.
func (r *memoryReport) topChurn() []*memoryEntry {
	return topBy(r.tests, func(e *memoryEntry) float64 { return e.churnMb() })
}

// topOldGrowth returns tests with GC activity sorted by old-gen growth,
// descending (leak suspects first).
func (r *memoryReport) topOldGrowth() []*memoryEntry {
	return topBy(r.tests, func(e *memoryEntry) float64 { return e.oldGrowthMb() })
}

func topBy(entries []*memoryEntry, key func(*memoryEntry) float64) []*memoryEntry {
	var withGcs []*memoryEntry
	for _, e := range entries {
		if len(e.gcs) > 0 {
			withGcs = append(withGcs, e)
		}
	}
	sort.SliceStable(withGcs, func(i, j int) bool { return key(withGcs[i]) > key(withGcs[j]) })
	return withGcs
}

// memoryStreamParser folds one interleaved go test output stream into a
// memoryReport (the gc_log.dart + attribution analog).
type memoryStreamParser struct {
	report   memoryReport
	open     []*memoryEntry // open test windows, innermost last
	paused   []*memoryEntry // t.Parallel() windows awaiting === CONT
	pkgTests []*memoryEntry // this package's tests, pending the `ok` line
	pending  []gcEvent      // GCs with no open window (loading/teardown)
	prevLive float64        // live-heap baseline for oldDeltaMb
	hasPrev  bool
}

// parseMemoryStream builds the report from one run's combined output.
func parseMemoryStream(output string) memoryReport {
	p := &memoryStreamParser{}
	for _, line := range strings.Split(output, "\n") {
		p.line(line)
	}
	return p.report
}

// anchorKind classifies what nextAnchor found in a (possibly torn) line.
type anchorKind int

const (
	anchorNone anchorKind = iota
	anchorGC
	anchorRun
	anchorPause
	anchorCont
	anchorName
	anchorResult
)

// markerAnchors locates attribution markers anywhere in a line: control
// markers, result words (which print indented one level per subtest
// depth) — all recoverable mid-line because the go tool's relay can glue
// fragments together.
var markerAnchors = []struct {
	kind    anchorKind
	literal string
}{
	{anchorRun, "=== RUN "},
	{anchorPause, "=== PAUSE "},
	{anchorCont, "=== CONT "},
	{anchorName, "=== NAME "},
	{anchorResult, "--- PASS: "},
	{anchorResult, "--- FAIL: "},
	{anchorResult, "--- SKIP: "},
}

// nextAnchor locates the earliest attribution anchor: a gctrace row
// start or a marker, at any indentation and even mid-line.
func nextAnchor(s string) (int, anchorKind) {
	best, kind := len(s), anchorNone
	if m := gcAnchorRe.FindStringIndex(s); m != nil && m[0] < best {
		best, kind = m[0], anchorGC
	}
	for _, a := range markerAnchors {
		if i := strings.Index(s, a.literal); i >= 0 && i < best {
			best, kind = i, a.kind
		}
	}
	return best, kind
}

// line folds one physical output line. Package summaries are checked at
// column 0 first (they are the go tool's own rare writes); everything
// else is tokenized by anchor so tears and indented subtest markers are
// recovered.
func (p *memoryStreamParser) line(s string) {
	if isPackageSummary(s) {
		p.packageSummary(s)
		return
	}
	p.tokens(s)
}

// tokens consumes anchor after anchor, dropping everything that does not
// validate by shape: a row fragment is never treated as a row, a marker
// without its duration is dropped, and logged text between anchors is
// ignored. Each iteration consumes at least one byte, so the loop ends.
func (p *memoryStreamParser) tokens(s string) {
	for s != "" {
		i, kind := nextAnchor(s)
		switch kind {
		case anchorNone:
			return
		case anchorGC:
			s = p.gcToken(s, i)
		case anchorResult:
			s = p.resultToken(s, i)
		default:
			literal := controlLiteral(kind)
			rest := s[i+len(literal):]
			j, _ := nextAnchor(rest)
			p.control(kind, strings.TrimSpace(rest[:j]))
			s = rest[j:]
		}
	}
}

// gcToken consumes one gctrace row starting at s[i:] — or, when the row
// is torn mid-numbers, just the fragment start (its churn is lost, but a
// marker may be glued into it) — and returns the unconsumed remainder.
func (p *memoryStreamParser) gcToken(s string, i int) string {
	gc, end, ok := parseGcRow(s[i:])
	if !ok {
		return s[i+len("gc "):]
	}
	p.attribute(gc)
	return s[i+end:]
}

// resultToken consumes one result marker at s[i:] — the test name is the
// shortest run before its ` (<duration>)` — and returns the unconsumed
// remainder. A result word without a duration is torn: skip its literal
// and keep scanning.
func (p *memoryStreamParser) resultToken(s string, i int) string {
	loc := resultMarkerRe.FindStringSubmatchIndex(s[i:])
	if loc == nil {
		return s[i+resultLiteralLen(s[i:]):]
	}
	p.close(s[i+loc[4]:i+loc[5]], strings.ToLower(s[i+loc[2]:i+loc[3]]))
	return s[i+loc[1]:]
}

// control dispatches a `=== RUN/PAUSE/CONT/NAME` marker.
func (p *memoryStreamParser) control(kind anchorKind, name string) {
	switch kind {
	case anchorRun:
		p.push(&memoryEntry{label: name})
	case anchorPause:
		p.pause(name)
	case anchorCont:
		p.cont(name)
	case anchorName:
		p.raise(name)
	}
}

func controlLiteral(kind anchorKind) string {
	for _, a := range markerAnchors {
		if a.kind == kind {
			return a.literal
		}
	}
	return ""
}

// resultLiteralLen returns the length of the result literal at the start
// of s (1 when none matches, so callers always make progress).
func resultLiteralLen(s string) int {
	for _, a := range markerAnchors {
		if a.kind == anchorResult && strings.HasPrefix(s, a.literal) {
			return len(a.literal)
		}
	}
	return 1
}

// packageSummary dispatches one go tool per-package summary line.
func (p *memoryStreamParser) packageSummary(s string) {
	if strings.Fields(s)[0] == "?" {
		p.discard()
		return
	}
	p.flush(packageSummaryFields(s))
}

// attribute lands one GC on the innermost open window, or on the
// pending bucket (no open window: loading/teardown/between suites) that
// flush routes to the report's unattributed bucket. The old-gen
// baseline advances on every row either way — the chain must not reset
// at window edges.
func (p *memoryStreamParser) attribute(gc gcEvent) {
	if p.hasPrev {
		gc.oldDeltaMb = gc.heapLiveMb - p.prevLive
	}
	p.prevLive = gc.heapLiveMb
	p.hasPrev = true
	p.report.totalGcs = append(p.report.totalGcs, gc)
	if n := len(p.open); n > 0 {
		p.open[n-1].gcs = append(p.open[n-1].gcs, gc)
		return
	}
	p.pending = append(p.pending, gc)
}

func (p *memoryStreamParser) push(e *memoryEntry) {
	p.open = append(p.open, e)
	p.pkgTests = append(p.pkgTests, e)
}

// pause closes a t.Parallel() window: GCs while paused are not the
// test's (=== CONT reopens the same entry).
func (p *memoryStreamParser) pause(name string) {
	if i := lastIndexOfLabel(p.open, name); i >= 0 {
		p.paused = append(p.paused, p.open[i])
		p.open = append(p.open[:i], p.open[i+1:]...)
	}
}

func (p *memoryStreamParser) cont(name string) {
	if i := lastIndexOfLabel(p.paused, name); i >= 0 {
		p.open = append(p.open, p.paused[i])
		p.paused = append(p.paused[:i], p.paused[i+1:]...)
	}
}

// raise implements `=== NAME <name>`: subsequent output (including GC
// lines) belongs to that window; bring it back to the top of the stack.
func (p *memoryStreamParser) raise(name string) {
	if i := lastIndexOfLabel(p.open, name); i >= 0 && i != len(p.open)-1 {
		top := p.open[i]
		p.open = append(append(p.open[:i], p.open[i+1:]...), top)
		return
	}
	p.cont(name)
}

// close records a test result and shuts its attribution window. A GC
// line after this point (before the next === RUN) stays out of the
// closed test — it has no open window and becomes unattributed.
func (p *memoryStreamParser) close(name, result string) {
	if e := takeByLabel(&p.open, name); e != nil {
		e.result = result
		return
	}
	if e := takeByLabel(&p.paused, name); e != nil {
		e.result = result
	}
}

// discard drops pending GCs when a package summary is a `?  <pkg>
// [no test files]` line: no test binary ran for it, so nothing it could
// have traced belongs in the report — not even the unattributed bucket
// (compile-phase noise under an ambient GODEBUG would land here).
func (p *memoryStreamParser) discard() {
	p.report.totalGcs = p.report.totalGcs[:len(p.report.totalGcs)-len(p.pending)]
	p.pending = nil
}

// flush lands the package summary: pending GCs (no test window was open
// — loading, teardown, inter-suite rows) join the report's unattributed
// bucket instead of the package entry, so a package's totals carry only
// what its tests' windows claimed; the per-process old-gen baseline
// resets for the next test binary.
func (p *memoryStreamParser) flush(pkg string) {
	p.report.unattributed = append(p.report.unattributed, p.pending...)
	entry := &memoryEntry{label: pkg}
	for _, t := range p.pkgTests {
		entry.gcs = append(entry.gcs, t.gcs...)
		t.pkg = pkg
		p.report.tests = append(p.report.tests, t)
		if t.result != "" {
			p.report.testCount++
		}
	}
	p.report.packages = append(p.report.packages, entry)
	p.pkgTests = nil
	p.pending = nil
	p.open = nil
	p.paused = nil
	p.prevLive = 0
	p.hasPrev = false
}

func lastIndexOfLabel(entries []*memoryEntry, label string) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].label == label {
			return i
		}
	}
	return -1
}

// takeByLabel removes and returns the innermost entry with the label.
func takeByLabel(entries *[]*memoryEntry, label string) *memoryEntry {
	i := lastIndexOfLabel(*entries, label)
	if i < 0 {
		return nil
	}
	e := (*entries)[i]
	*entries = append((*entries)[:i], (*entries)[i+1:]...)
	return e
}

// isPackageSummary reports whether the line is one of the go tool's
// per-package summary lines (`ok  <pkg>  0.5s`, `FAIL <pkg> ...`,
// `?   <pkg>  [no test files]`) — not a bare `FAIL`/`PASS` line.
func isPackageSummary(s string) bool {
	f := strings.Fields(s)
	return len(f) >= 2 && (f[0] == "ok" || f[0] == "FAIL" || f[0] == "?")
}

// packageSummaryFields returns the package path from a summary line.
func packageSummaryFields(s string) string {
	return strings.Fields(s)[1]
}

// memoryRunner runs the traced test suite and returns both output
// streams; injectable for tests (the SuiteRunner analog).
type memoryRunner func(dir string, args, env []string) (string, string, error)

// tracedExecWrapper is passed to `go test -exec`: the go tool runs every
// test binary through it, so only the test binaries trace.
const tracedExecWrapper = "env GODEBUG=gctrace=1"

// memoryTestArgs builds the go test argv: verbose markers, cache
// bypass, sequential packages, tracing scoped to the test binaries.
// Explicit paths are exclusive; `./...` is the default.
func memoryTestArgs(paths []string) []string {
	args := []string{goTestSubcommand, "-v", "-count=1", "-p", "1", "-exec", tracedExecWrapper}
	if len(paths) > 0 {
		return append(args, paths...)
	}
	return append(args, goTestAllPackages)
}

// tracedEnv strips GODEBUG from the environment (an ambient value or a
// `go env -w GODEBUG` would trace the go tool and the compilers too)
// and replaces it with an empty one; the -exec wrapper re-sets it for
// the test binaries only.
func tracedEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GODEBUG=") {
			env = append(env, kv)
		}
	}
	return append(env, "GODEBUG=")
}

// runTracedGoTest is the default memoryRunner: captures the interleaved
// stream from stdout (the go tool relays the test binary's combined
// output there) and the go tool's own stderr separately.
func runTracedGoTest(dir string, args, env []string) (string, string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// profileMemory runs the traced suite in root and attributes the
// stream. A test-binary failure (go test exit code 1) still yields a
// report — the command is informational.
func profileMemory(root string, paths []string, runner memoryRunner) (memoryReport, error) {
	stdout, _, err := runner(root, memoryTestArgs(paths), tracedEnv())
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return memoryReport{}, err
		}
	}
	return parseMemoryStream(stdout), nil
}

// memoryProfile is the command's profiler seam (tests override it; the
// real path always uses runTracedGoTest).
var memoryProfile = func(root string, paths []string) (memoryReport, error) {
	return profileMemory(root, paths, runTracedGoTest)
}

// memoryOptions holds the parsed `memory` subcommand configuration.
type memoryOptions struct {
	top            int
	format         string
	badgePath      string  // --badge: write the churn SVG here (empty = off)
	badgeThreshold float64 // --badge-threshold MB (green at or below)
	paths          []string
}

// parseMemoryOptions parses the memory flags: --top (default 20),
// --format console|json, --badge, --badge-threshold (default 500).
// ok=false means a usage error (exit 1), already reported to stderr.
func parseMemoryOptions(args []string, stderr io.Writer) (memoryOptions, int, bool) {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", 20, "How many entries per leaderboard to print.")
	format := fs.String("format", defaultSimFormat, "Output format: console or json.")
	badge := fs.String("badge", "", "Write a shields-style SVG badge with the suite churn "+
		"to this path (green at or below, yellow at most twice, red over "+
		"the --badge-threshold MB).")
	// 500 MB is a different fact from mutation's defaultMutationMaxMutants
	// (a mutant count): cross-protocol coincidence by the
	// scattered-constants gate's own verification clause, kept as a
	// literal.
	badgeThreshold := fs.Float64("badge-threshold", 500, "Badge green/yellow boundary in MB.")
	if err := fs.Parse(args); err != nil {
		return memoryOptions{}, 1, false
	}
	if *format != defaultSimFormat && *format != "json" {
		fmt.Fprintf(stderr, "memory: unknown --format %q (want console or json)\n", *format)
		return memoryOptions{}, 1, false
	}
	if *top < 0 {
		fmt.Fprintf(stderr, "memory: --top must be >= 0, got %d\n", *top)
		return memoryOptions{}, 1, false
	}
	if *badgeThreshold < 0 {
		fmt.Fprintf(stderr, "memory: --badge-threshold must be >= 0, got %v\n", *badgeThreshold)
		return memoryOptions{}, 1, false
	}
	return memoryOptions{top: *top, format: *format, badgePath: *badge, badgeThreshold: *badgeThreshold, paths: fs.Args()}, 0, true
}

// isGoProject reports whether root looks like a Go module with a
// runnable test suite (the pubspec check analog).
func isGoProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}

// RunMemoryCommand implements `crap4go memory`: it runs the test suite
// under gctrace, attributes GC events and reclaimed memory to tests and
// packages, and reports allocation-churn / old-gen-growth hotspots.
//
// Informational, like upstream: always exits 0 — only usage errors and
// an empty run exit 1. Test failures do not change the exit code.
func RunMemoryCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseMemoryOptions(args, stderr)
	if !ok {
		return code
	}
	if !isGoProject(root) {
		fmt.Fprintf(stderr, "memory: no Go module (go.mod) found in %s "+
			"(the memory profiler runs `go test`)\n", root)
		return 1
	}
	fmt.Fprintln(stderr, "Running go test with GODEBUG=gctrace=1 (this may take a while)")
	report, err := memoryProfile(root, opts.paths)
	if err != nil {
		fmt.Fprintf(stderr, "memory: %v\n", err)
		return 1
	}
	if report.testCount == 0 {
		fmt.Fprintln(stderr, "memory: no tests executed")
		return 1
	}
	if opts.format == "json" {
		printMemoryJSON(report, stdout)
	} else {
		printMemoryConsole(report, opts.top, stdout)
	}
	return maybeWriteBadge(opts, report, stderr)
}

// maybeWriteBadge writes the --badge SVG when requested; a write
// failure is reported to stderr and exits 1 (usage-level error).
func maybeWriteBadge(opts memoryOptions, report memoryReport, stderr io.Writer) int {
	if opts.badgePath == "" {
		return 0
	}
	if err := writeBadge(opts.badgePath, report, opts.badgeThreshold); err != nil {
		fmt.Fprintf(stderr, "memory: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "Badge written to %s\n", opts.badgePath)
	return 0
}

// printMemoryConsole prints the upstream-shaped leaderboard report.
// Allocation volume (b7c227d) has no exact Go source: gctrace exposes no
// out-of-process allocation counter, so allocated MB is the churn proxy.
func printMemoryConsole(report memoryReport, top int, stdout io.Writer) {
	churn := sumChurn(report.totalGcs)
	allocated := ""
	if churn > 0 {
		allocated = fmt.Sprintf(", allocated %.1f MB", churn)
	}
	fmt.Fprintf(stdout, "memory: %d tests, %d GCs, churn %.1f MB new-gen, old-gen %+.1f MB%s\n",
		report.testCount, len(report.totalGcs), churn, sumOldGrowth(report.totalGcs), allocated)
	fmt.Fprintf(stdout, "unattributed (outside test windows): %d GCs, %.1f MB\n",
		len(report.unattributed), report.unattributedChurnMb())
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "top %d by allocation volume (churn MB proxy on go):\n", top)
	printMemoryTable(stdout, limitBy(report.topChurn(), top))
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "top %d by old-gen growth (leak suspects first):\n", top)
	printMemoryTable(stdout, limitBy(report.topOldGrowth(), top))
}

// limitBy returns at most top entries (all when top <= 0).
func limitBy(entries []*memoryEntry, top int) []*memoryEntry {
	if top > 0 && top < len(entries) {
		return entries[:top]
	}
	return entries
}

// Badge colors from the shields.io palette (upstream badge_svg.dart):
// green at or below the threshold, yellow at most twice it, red beyond.
const (
	badgeGreen  = "#4c1"
	badgeYellow = "#dfb317"
	badgeRed    = "#e05d44"
)

// badgeColorFor picks the badge color for churn against threshold MB:
// at or below green; above it but at most 2× yellow; beyond that red.
func badgeColorFor(churn, threshold float64) string {
	if churn <= threshold {
		return badgeGreen
	}
	if churn <= 2*threshold {
		return badgeYellow
	}
	return badgeRed
}

// svgEscape escapes the text nodes of the badge (SVG body text only —
// the label is the constant "memory churn", the message is digits+MB).
func svgEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// renderBadgeSVG renders a flat shields.io-style SVG badge: a grey
// label plate and a colored message plate (upstream badge_svg.dart,
// plate width = round(6.3·len)+10 for Verdana 11px).
func renderBadgeSVG(label, message, color string) string {
	plate := func(text string) int { return int(float64(len(text))*6.3+0.5) + 10 }
	lw, vw := plate(label), plate(message)
	w := lw + vw
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" viewBox="0 0 %d 20" role="img" aria-label="%s: %s">
  <title>%s: %s</title>
  <rect width="%d" height="20" rx="3" fill="#555"/>
  <rect x="%d" width="%d" height="20" rx="3" fill="%s"/>
  <rect width="%d" height="20" rx="3" fill="none"/>
  <g fill="#fff" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle">
    <text x="%d" y="14">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>
`,
		w, w, svgEscape(label), svgEscape(message),
		svgEscape(label), svgEscape(message),
		lw, lw, vw, color, w,
		lw/2, svgEscape(label),
		lw+vw/2, svgEscape(message))
}

// writeBadge writes the churn SVG at path: the suite churn (every
// parsed row, bucket rows included — the whole run's volume, as
// upstream's badge uses totalChurnMb) rounded to whole MB.
func writeBadge(path string, report memoryReport, threshold float64) error {
	churn := sumChurn(report.totalGcs)
	svg := renderBadgeSVG("memory churn", fmt.Sprintf("%.0f MB", math.Round(churn)), badgeColorFor(churn, threshold))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(svg), writableFilePerm)
}

// printMemoryTable prints one leaderboard block (upstream paddings).
func printMemoryTable(stdout io.Writer, entries []*memoryEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "  (no GC activity)")
		return
	}
	fmt.Fprintf(stdout, "  %5s  %9s  %9s  test\n", "GCs", "alloc MB", "old Δ MB")
	for _, e := range entries {
		fmt.Fprintf(stdout, "%7d  %9.1f  %9.1f  %s\n", len(e.gcs), e.churnMb(), e.oldGrowthMb(), e.label)
	}
}

// memoryJSONEntry is one entry of the JSON report (upstream field names;
// `suite` becomes `package`).
type memoryJSONEntry struct {
	Label       string  `json:"label"`
	Package     string  `json:"package,omitempty"`
	GCs         int     `json:"gcs"`
	ChurnNewMb  float64 `json:"churnNewMb"`
	OldGrowthMb float64 `json:"oldGrowthMb"`
}

// memoryJSON is the JSON report document. Upstream's clockOffsetSeconds
// has no Go analog — attribution is by stream position, not by clock
// comparison — so it is omitted. totalAllocatedMb mirrors upstream
// b7c227d's allocation-volume total; on Go it is the churn proxy (no
// exact allocation counter is exposed out-of-process by the runtime).
type memoryJSON struct {
	TestCount           int               `json:"testCount"`
	GCCount             int               `json:"gcCount"`
	TotalChurnMb        float64           `json:"totalChurnMb"`
	TotalAllocatedMb    float64           `json:"totalAllocatedMb"`
	TotalOldGrowth      float64           `json:"totalOldGrowthMb"`
	UnattributedGCs     int               `json:"unattributedGcs"`
	UnattributedChurnMb float64           `json:"unattributedChurnMb"`
	Packages            []memoryJSONEntry `json:"packages"`
	Tests               []memoryJSONEntry `json:"tests"`
}

// printMemoryJSON writes the machine-readable report (stdout stays
// JSON-only; progress goes to stderr).
func printMemoryJSON(report memoryReport, stdout io.Writer) {
	churn := round1(sumChurn(report.totalGcs))
	doc := memoryJSON{
		TestCount:           report.testCount,
		GCCount:             len(report.totalGcs),
		TotalChurnMb:        churn,
		TotalAllocatedMb:    churn,
		TotalOldGrowth:      round1(sumOldGrowth(report.totalGcs)),
		UnattributedGCs:     len(report.unattributed),
		UnattributedChurnMb: round1(report.unattributedChurnMb()),
		Packages:            jsonEntries(report.packages),
		Tests:               jsonEntries(report.tests),
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

func jsonEntries(entries []*memoryEntry) []memoryJSONEntry {
	out := make([]memoryJSONEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, memoryJSONEntry{
			Label:       e.label,
			Package:     e.pkg,
			GCs:         len(e.gcs),
			ChurnNewMb:  round1(e.churnMb()),
			OldGrowthMb: round1(e.oldGrowthMb()),
		})
	}
	return out
}

// round1 rounds to one decimal (the upstream JSON precision).
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
