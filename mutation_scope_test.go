package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scopeEngineSite is a minimal engine-level site over the fixture content
// `a + b` (port of upstream's site helper).
func scopeEngineSite(file string) MutationSite {
	return MutationSite{File: file, Line: 1, Category: "arithmetic", Original: "+", Mutant: "-", Offset: 2, Length: 1}
}

// invocationLog renders recorded runner invocations for comparison.
func invocationLog(invs [][]string) string {
	joined := make([]string, len(invs))
	for i, inv := range invs {
		joined[i] = strings.Join(inv, " ")
	}
	return strings.Join(joined, " | ")
}

// Engine-level confirmation semantics (port of upstream's
// mutation_engine_scope_test.dart).

func TestMutationEngineScopedSurvivorReconfirmed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("a + b"), 0o644); err != nil {
		t.Fatal(err)
	}
	var invocations [][]string
	runner := func(_ string, _ time.Duration, args []string) (mutantOutcome, error) {
		invocations = append(invocations, args)
		// The scoped run misses the mutant; the full invocation kills it.
		if len(args) > 1 {
			return mutantSurvived, nil
		}
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir, []MutationSite{scopeEngineSite(file)}, 5*time.Second,
		[]string{"test"}, func(MutationSite) []string { return []string{"test", "a_test.go"} }, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := invocationLog(invocations), "test a_test.go | test"; got != want {
		t.Fatalf("invocations %q, want %q", got, want)
	}
	if result.killedCount() != 1 || result.survivedCount() != 0 || result.scopedCount != 1 || result.confirmedCount != 1 {
		t.Errorf("result %d killed / %d survived / scoped %d / confirmed %d, want 1/0/1/1",
			result.killedCount(), result.survivedCount(), result.scopedCount, result.confirmedCount)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != "a + b" {
		t.Errorf("file not restored: %q, %v", after, err)
	}
}

func TestMutationEngineScopedKillIsFinal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a + b"), 0o644); err != nil {
		t.Fatal(err)
	}
	var invocations [][]string
	runner := func(_ string, _ time.Duration, args []string) (mutantOutcome, error) {
		invocations = append(invocations, args)
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir(t), []MutationSite{scopeEngineSite(file)}, 5*time.Second,
		[]string{"test"}, func(MutationSite) []string { return []string{"test", "a_test.go"} }, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A scoped kill is monotone (the scoped invocation is a subset of the
	// full suite): final, cached, never re-confirmed.
	if got, want := invocationLog(invocations), "test a_test.go"; got != want {
		t.Fatalf("invocations %q, want %q", got, want)
	}
	if result.killedCount() != 1 || result.scopedCount != 0 || result.confirmedCount != 0 {
		t.Errorf("result %d killed / scoped %d / confirmed %d, want 1/0/0",
			result.killedCount(), result.scopedCount, result.confirmedCount)
	}
}

func TestMutationEngineScopedSurvivorConfirmedSurvived(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a + b"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(string, time.Duration, []string) (mutantOutcome, error) {
		calls++
		return mutantSurvived, nil
	}
	result, err := runMutationEngine(dir(t), []MutationSite{scopeEngineSite(file)}, 5*time.Second,
		[]string{"test"}, func(MutationSite) []string { return []string{"test", "a_test.go"} }, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("runner called %d times, want scoped + confirmation = 2", calls)
	}
	if result.survivedCount() != 1 || result.confirmedCount != 0 {
		t.Errorf("result %d survived / confirmed %d, want 1/0", result.survivedCount(), result.confirmedCount)
	}
}

func TestMutationEngineScopedTimeoutConfirmed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a + b"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := func(_ string, _ time.Duration, args []string) (mutantOutcome, error) {
		if len(args) > 1 {
			return mutantTimeout, nil
		}
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir(t), []MutationSite{scopeEngineSite(file)}, 5*time.Second,
		[]string{"test"}, func(MutationSite) []string { return []string{"test", "a_test.go"} }, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A timeout counts as survived, so it is confirmed like any scoped
	// survivor — and the confirmation kill is the reported outcome.
	if result.killedCount() != 1 || result.scopedCount != 1 || result.confirmedCount != 1 {
		t.Errorf("result %d killed / scoped %d / confirmed %d, want 1/1/1",
			result.killedCount(), result.scopedCount, result.confirmedCount)
	}
}

func TestMutationEngineUnscopedSitesRunFullInvocation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("a + b"), 0o644); err != nil {
		t.Fatal(err)
	}
	var invocations [][]string
	runner := func(_ string, _ time.Duration, args []string) (mutantOutcome, error) {
		invocations = append(invocations, args)
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir(t), []MutationSite{scopeEngineSite(file)}, 5*time.Second,
		[]string{"test", "test/full"}, func(MutationSite) []string { return nil }, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	// No candidate test file: the full invocation runs, nothing is scoped.
	if got, want := invocationLog(invocations), "test test/full"; got != want {
		t.Fatalf("invocations %q, want %q", got, want)
	}
	if result.killedCount() != 1 || result.scopedCount != 0 {
		t.Errorf("result %d killed / scoped %d, want 1/0", result.killedCount(), result.scopedCount)
	}
}

// Monotone-kill caching: a scoped kill is cached as a regular kill, so a
// later full-suite run replays it.

func TestMutationEngineScopedKillCachesAsRegularKill(t *testing.T) {
	root := t.TempDir()
	writeCacheFixture(t, root, "a.go", cacheSrcB)
	sites, refs, live := cacheFixtureScan(t, root, "a.go", cacheSrcB)
	cacheFile := filepath.Join(root, mutationCacheFileName)
	session := &mutationCacheSession{cache: loadMutationCache(cacheFile, cacheTestMeta), cacheFile: cacheFile, siteMethods: refs, liveKeys: live}
	scoped := []string{"test", "./scoped"}
	first, err := runMutationEngine(root, sites, 5*time.Second, defaultMutationTestArgs,
		func(MutationSite) []string { return scoped },
		func(string, time.Duration, []string) (mutantOutcome, error) { return mutantKilled, nil }, session)
	if err != nil {
		t.Fatal(err)
	}
	// The scoped kill is final: executed once, nothing deferred.
	if first.killedCount() != 1 || first.fromCacheCount() != 0 || first.scopedCount != 0 {
		t.Fatalf("cold scoped result %d killed / %d cached / scoped %d, want 1/0/0",
			first.killedCount(), first.fromCacheCount(), first.scopedCount)
	}
	calls := 0
	second, err := runMutationEngine(root, sites, 5*time.Second, defaultMutationTestArgs, nil,
		func(string, time.Duration, []string) (mutantOutcome, error) {
			calls++
			return mutantKilled, nil
		},
		&mutationCacheSession{cache: loadMutationCache(cacheFile, cacheTestMeta), cacheFile: cacheFile, siteMethods: refs, liveKeys: live})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("full run re-executed %d mutants: scoped kill must cache as a regular kill", calls)
	}
	if second.killedCount() != 1 || second.fromCacheCount() != 1 {
		t.Errorf("warm result %d killed / %d cached, want 1/1", second.killedCount(), second.fromCacheCount())
	}
}

func TestMutationEngineScopedSurvivorCachesConfirmedOutcome(t *testing.T) {
	root := t.TempDir()
	writeCacheFixture(t, root, "a.go", cacheSrcB)
	sites, refs, live := cacheFixtureScan(t, root, "a.go", cacheSrcB)
	cacheFile := filepath.Join(root, mutationCacheFileName)
	session := &mutationCacheSession{cache: loadMutationCache(cacheFile, cacheTestMeta), cacheFile: cacheFile, siteMethods: refs, liveKeys: live}
	first, err := runMutationEngine(root, sites, 5*time.Second, []string{"test", "./full"},
		func(MutationSite) []string { return []string{"test", "./scoped"} },
		func(_ string, _ time.Duration, args []string) (mutantOutcome, error) {
			// The scoped run misses the mutant; the full invocation kills it.
			if equalArgs(args, []string{"test", "./full"}) {
				return mutantKilled, nil
			}
			return mutantSurvived, nil
		}, session)
	if err != nil {
		t.Fatal(err)
	}
	if first.killedCount() != 1 || first.scopedCount != 1 || first.confirmedCount != 1 {
		t.Fatalf("scoped result %d killed / scoped %d / confirmed %d, want 1/1/1",
			first.killedCount(), first.scopedCount, first.confirmedCount)
	}
	calls := 0
	second, err := runMutationEngine(root, sites, 5*time.Second, defaultMutationTestArgs, nil,
		func(string, time.Duration, []string) (mutantOutcome, error) {
			calls++
			return mutantKilled, nil
		},
		&mutationCacheSession{cache: loadMutationCache(cacheFile, cacheTestMeta), cacheFile: cacheFile, siteMethods: refs, liveKeys: live})
	if err != nil {
		t.Fatal(err)
	}
	// Only the confirmed outcome reached the cache: the warm full run
	// replays the kill and never re-executes the mutant.
	if calls != 0 {
		t.Errorf("warm run executed %d mutants: the confirmed kill must be cached, not the scoped survivor", calls)
	}
	if second.killedCount() != 1 || second.fromCacheCount() != 1 {
		t.Errorf("warm result %d killed / %d cached, want 1/1", second.killedCount(), second.fromCacheCount())
	}
}

// Diff-scoped execution: only sites whose containing method intersects the
// changed lines enter the scan.

func TestMutationCoveredSitesDiffScoped(t *testing.T) {
	root := t.TempDir()
	src := "package mut\n\nfunc Hot(x int) int {\n\treturn x + 1\n}\n\nfunc Cold(x int) int {\n\treturn x + 1\n}\n"
	hot := filepath.Join(root, "hot.go")
	if err := os.WriteFile(hot, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := strings.Join([]string{
		"mode: set",
		// Both methods executed.
		"example.com/m/hot.go:3.21,4.14 1 1",
		"example.com/m/hot.go:6.21,7.14 1 1",
		"",
	}, "\n")
	cov, err := ParseCoverProfileReader(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	// Only Cold's body line changed: her sites run, Hot's do not.
	diff := map[string]map[int]bool{"hot.go": {8: true}}
	scan, err := mutationCoveredSites([]string{hot}, root, cov, mutationAllCategories(), 500, nil, diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.sites) != 2 || scan.uncovered != 0 {
		t.Fatalf("got %d sites (%v), %d uncovered; want Cold's 2 sites, 0 uncovered", len(scan.sites), scan.sites, scan.uncovered)
	}
	for _, s := range scan.sites {
		if s.Method != "Cold" {
			t.Errorf("unchanged method's site executed: %v", s)
		}
	}
	// A diff that misses the file entirely: nothing executes, nothing counts.
	empty, err := mutationCoveredSites([]string{hot}, root, cov, mutationAllCategories(), 500, nil, map[string]map[int]bool{"other.go": {1: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.sites) != 0 || empty.uncovered != 0 {
		t.Errorf("got %d sites, %d uncovered; want none — not a mutant of this change", len(empty.sites), empty.uncovered)
	}
}

func TestMutationInDiff(t *testing.T) {
	methods := []MethodDescriptor{{Name: "M", StartLine: 3, EndLine: 5}}
	inMethod := MutationSite{Line: 4}
	diff := map[string]map[int]bool{"a.go": {5: true}}
	if !mutationInDiff(diff, "a.go", inMethod, methods) {
		t.Error("method span intersecting the diff must run")
	}
	outside := MutationSite{Line: 6}
	if mutationInDiff(diff, "a.go", outside, methods) {
		t.Error("site outside the diff must be skipped")
	}
	fallback := MutationSite{Line: 7}
	if !mutationInDiff(map[string]map[int]bool{"a.go": {7: true}}, "a.go", fallback, methods) {
		t.Error("site outside every method falls back to its own line")
	}
	if !mutationInDiff(nil, "a.go", MutationSite{Line: 100}, methods) {
		t.Error("no diff (full run) keeps everything")
	}
	if mutationInDiff(map[string]map[int]bool{"a.go": {1: true}}, "other.go", fallback, methods) {
		t.Error("a file the diff does not touch contributes nothing")
	}
}

// Unified-diff parsing.

func TestParseDiffLines(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/add.go b/add.go",
		"index 111..222 100644",
		"--- a/add.go",
		"+++ b/add.go",
		"@@ -2,0 +3,2 @@ func Hot() {",
		"+\tx := 1",
		"+\ty := 2",
		"@@ -9 +10 @@",
		"-\told",
		"+\tnew",
		"diff --git a/gone.go b/gone.go",
		"deleted file mode 100644",
		"--- a/gone.go",
		"+++ /dev/null",
		"@@ -1,3 +0,0 @@",
		"-a",
		"-b",
		"-c",
		"diff --git a/empty.go b/empty.go",
		"index 333..444 100644",
		"--- a/empty.go",
		"+++ b/empty.go",
		"@@ -1,2 +1,0 @@",
		"-dead",
		"-code",
		"diff --git a/bin.dat b/bin.dat",
		"index 555..666 100644",
		"Binary files a/bin.dat and b/bin.dat differ",
		"diff --git a/tricky.go b/tricky.go",
		"index 777..888 100644",
		"--- a/tricky.go",
		"+++ b/tricky.go",
		"@@ -1 +2 @@",
		"-x",
		"+++ looks like a header",
	}, "\n")
	got := parseDiffLines(diff)
	if !got["add.go"][3] || !got["add.go"][4] || !got["add.go"][10] || len(got["add.go"]) != 3 {
		t.Errorf("add.go changed lines %v, want {3, 4, 10}", got["add.go"])
	}
	if _, ok := got["gone.go"]; ok {
		t.Error("deleted file must not appear")
	}
	if len(got["empty.go"]) != 0 {
		t.Errorf("pure-removal hunks add no changed lines: %v", got["empty.go"])
	}
	if _, ok := got["bin.dat"]; ok {
		t.Error("binary file must not appear")
	}
	if !got["tricky.go"][2] || len(got["tricky.go"]) != 1 {
		t.Errorf("tricky.go changed lines %v, want {2} — added content is not a header", got["tricky.go"])
	}
}

func TestParseDiffHunk(t *testing.T) {
	cases := []struct {
		header      string
		start, want int
		ok          bool
	}{
		{"@@ -1,3 +4,5 @@", 4, 5, true},
		{"@@ -0,0 +1,2 @@", 1, 2, true},
		{"@@ -5 +6 @@", 6, 1, true},
		{"@@ -3,2 +4,0 @@ ctx", 0, 0, false},
		{"@@ -1,3 -4,5 @@", 0, 0, false},
		{"@@ -1 +x @@", 0, 0, false},
		{"@@ -1 +0,2 @@", 0, 0, false},
		{"@@ x @@", 0, 0, false},
	}
	for _, c := range cases {
		start, count, ok := parseDiffHunk(c.header)
		if ok != c.ok || (ok && (start != c.start || count != c.want)) {
			t.Errorf("parseDiffHunk(%q) = (%d, %d, %v), want (%d, %d, %v)", c.header, start, count, ok, c.start, c.want, c.ok)
		}
	}
}

// scope-tests derivation.

func TestMutationScopedTestArgs(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package m\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sub/calc.go")
	write("sub/calc_test.go")
	write("top.go")
	write("top_test.go")
	if got := mutationScopedTestArgs(root, filepath.Join(root, "sub", "calc.go")); !equalArgs(got, []string{"test", "./sub"}) {
		t.Errorf("in-package derivation = %v, want [test ./sub]", got)
	}
	if got := mutationScopedTestArgs(root, filepath.Join(root, "top.go")); !equalArgs(got, []string{"test", "."}) {
		t.Errorf("root derivation = %v, want [test .]", got)
	}
	write("lonely.go")
	if got := mutationScopedTestArgs(root, filepath.Join(root, "lonely.go")); got != nil {
		t.Errorf("missing candidate = %v, want nil (fallback to test_args)", got)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// dir names the engine's project root in the tests above.
func dir(t *testing.T) string { return t.TempDir() }

// Gate-level end-to-end: real git plumbing + real go test.

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", "-A")
	run("commit", "-m", "base")
}

func TestMutationCommandDiffBaseExecutesOnlyChangedMethods(t *testing.T) {
	requireGoBin(t)
	root := writeMutationProject(t, map[string]string{
		"calc.go": "package mut\n\nfunc Hot(x int) int {\n\treturn x + 1\n}\n\nfunc Cold(x int) int {\n\treturn x + 1\n}\n",
		"calc_test.go": `package mut

import "testing"

func TestHot(t *testing.T) {
	if Hot(1) != 2 {
		t.Error("Hot(1) != 2")
	}
}

func TestCold(t *testing.T) {
	if Cold(2) != 3 {
		t.Error("Cold(2) != 3")
	}
}
`,
	})
	initGitRepo(t, root)
	// Touch only Cold's body in the working tree: one added comment line,
	// so the suite stays green for coverage generation and the diff marks
	// exactly one line inside Cold's span.
	calc := filepath.Join(root, "calc.go")
	src, err := os.ReadFile(calc)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(src), "func Cold(x int) int {\n", "func Cold(x int) int {\n\t// touched by this change\n", 1)
	if edited == string(src) {
		t.Fatal("edit did not apply")
	}
	if err := os.WriteFile(calc, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	generateMutationCoverage(t, root)

	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--diff-base", "HEAD", "--cache=false"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("diff-scoped run exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	// Only Cold's sites (`+` and `1`) run; the unchanged Hot mutants must
	// never execute.
	if !strings.Contains(out.String(), "(2 killed, 0 survived") || strings.Contains(out.String(), "4 killed") {
		t.Errorf("diff-scoped run must execute only Cold's mutants:\n%s", out.String())
	}
	// Without --diff-base the unchanged method's mutants execute too.
	out.Reset()
	errOut.Reset()
	code = RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("full run exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "(4 killed,") {
		t.Errorf("full run must kill Hot's and Cold's mutants:\n%s", out.String())
	}
}

func TestMutationCommandScopeTestsNarrowsToDerivedTestFile(t *testing.T) {
	requireGoBin(t)
	root := writeMutationProject(t, map[string]string{
		"sub/calc.go": "package sub\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		// The derived test file exists and provides coverage, but its only
		// case (0, 0) survives the `+` -> `-` mutant: the scoped package
		// run misses the mutant.
		"sub/calc_test.go": `package sub

import "testing"

func TestAdd(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Error("Add(0,0) != 0")
	}
}
`,
		// Only the full suite kills the mutant.
		"use_test.go": `package mut

import (
	"testing"

	"example.com/mut/sub"
)

func TestUse(t *testing.T) {
	if sub.Add(1, 2) != 3 {
		t.Error("sub.Add(1,2) != 3")
	}
}
`,
	})
	generateMutationCoverage(t, root)

	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--scope-tests"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("scoped run exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	// Add's site survives the derived `go test ./sub`, then the
	// re-confirmation against the full suite kills it.
	if !strings.Contains(out.String(), "test-scoped 1 (1 re-confirmed killed)") {
		t.Errorf("scoped summary missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "(1 killed, 0 survived") {
		t.Errorf("scoped run must report only the confirmed kill:\n%s", out.String())
	}
}

func TestMutationCommandScopeTestsFallsBackToTestArgs(t *testing.T) {
	requireGoBin(t)
	root := writeMutationProject(t, map[string]string{
		"sub/calc.go": "package sub\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		// Provides coverage for sub/calc.go (and survives the mutant).
		"sub/calc_test.go": `package sub

import "testing"

func TestAdd(t *testing.T) {
	if Add(0, 0) != 0 {
		t.Error("Add(0,0) != 0")
	}
}
`,
		// Only the full suite kills the mutant.
		"use_test.go": `package mut

import (
	"testing"

	"example.com/mut/sub"
)

func TestUse(t *testing.T) {
	if sub.Add(1, 2) != 3 {
		t.Error("sub.Add(1,2) != 3")
	}
}
`,
	})
	generateMutationCoverage(t, root)
	// sub/calc_test.go produced the coverage, so without scoping the
	// derivation would pick it — remove it to force the fallback path
	// (exactly upstream's fixture order).
	if err := os.Remove(filepath.Join(root, "sub", "calc_test.go")); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--scope-tests"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("fallback run exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	// No candidate test file: the full invocation runs, nothing scoped.
	if strings.Contains(out.String(), "test-scoped") {
		t.Errorf("fallback must not report scoping:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "(1 killed, 0 survived") {
		t.Errorf("fallback must kill via test_args:\n%s", out.String())
	}
}

func TestMutationCommandFailsOnBadDiffBase(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"a.go":         "package mut\n\nfunc A() int { return 0 }\n",
		"a_test.go":    "package mut\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n",
		"coverage.out": "mode: set\n",
	})
	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--diff-base", "HEAD"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("bad diff base exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "git diff HEAD") {
		t.Errorf("stderr must carry the git failure:\n%s", errOut.String())
	}
}
