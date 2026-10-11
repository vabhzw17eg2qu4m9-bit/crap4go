package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cache fixtures (port of upstream's fileA/fileB): a.go has two methods with
// one site each, b.go one method whose `Add` name collides with a.go's.
const cacheSrcA = "package mut\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\nfunc Zero() int {\n\treturn 0\n}\n"

const cacheSrcB = "package mut\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n"

func writeCacheFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cacheFixtureScan detects the real sites of content and binds every one to
// its containing method's cache identity, exactly as the gate does.
func cacheFixtureScan(t *testing.T, dir, rel, content string) ([]MutationSite, map[MutationSite]mutationMethodRef, map[string]bool) {
	t.Helper()
	path := filepath.Join(dir, rel)
	methods, err := ExtractMethods(path, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	sites := mutationParseSites(path, []byte(content), methods, mutationAllCategories())
	if len(sites) == 0 {
		t.Fatalf("no mutation sites in fixture %s", rel)
	}
	keyed := make(map[string]string, len(methods))
	for _, m := range methods {
		keyed[m.Name] = mutationCacheKey(rel, m.Name)
	}
	refs := make(map[MutationSite]mutationMethodRef, len(sites))
	for _, site := range sites {
		ref, ok := mutationMethodRefFor(site, methods, []byte(content), keyed)
		if !ok {
			t.Fatalf("no cache identity for site %v", site)
		}
		refs[site] = ref
	}
	live := make(map[string]bool, len(keyed))
	for _, key := range keyed {
		live[key] = true
	}
	return sites, refs, live
}

var cacheTestMeta = currentMutationCacheMeta(mutationAllCategories(), defaultMutationTestArgs)

// runCachedEngine is one gate-run-shaped engine invocation: load the
// on-disk cache, execute, merge and persist.
func runCachedEngine(t *testing.T, dir string, sites []MutationSite, refs map[MutationSite]mutationMethodRef, live map[string]bool, runner mutantTestRunner, meta mutationCacheMeta) mutationRunResult {
	t.Helper()
	cacheFile := filepath.Join(dir, mutationCacheFileName)
	result, err := runMutationEngine(dir, sites, 5*time.Second, runner, &mutationCacheSession{
		cache:       loadMutationCache(cacheFile, meta),
		cacheFile:   cacheFile,
		siteMethods: refs,
		liveKeys:    live,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMutationCacheReplaysKillsOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	sites, refs, live := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	calls := 0
	runner := func(string, time.Duration) (mutantOutcome, error) {
		calls++
		return mutantKilled, nil
	}
	first := runCachedEngine(t, dir, sites, refs, live, runner, cacheTestMeta)
	if calls != 2 {
		t.Fatalf("cold run executed %d mutants, want 2", calls)
	}
	if first.killedCount() != 2 || first.fromCacheCount() != 0 {
		t.Errorf("cold result %d killed / %d cached, want 2 / 0", first.killedCount(), first.fromCacheCount())
	}
	if _, err := os.Stat(filepath.Join(dir, mutationCacheFileName)); err != nil {
		t.Fatalf("cache file not persisted: %v", err)
	}

	coldCalls := calls
	second := runCachedEngine(t, dir, sites, refs, live, runner, cacheTestMeta)
	if calls != coldCalls {
		t.Errorf("warm run executed %d mutants, want 0: killed mutants of unchanged methods replay", calls-coldCalls)
	}
	if second.fromCacheCount() != 2 || second.killedCount() != 2 {
		t.Errorf("warm result %d killed / %d cached, want 2 / 2", second.killedCount(), second.fromCacheCount())
	}
	if second.score() != first.score() {
		t.Errorf("warm score %g != cold score %g — verdicts must be identical", second.score(), first.score())
	}
}

func TestMutationCacheSurvivorsAlwaysRerun(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	sites, refs, live := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	call := 0
	first := runCachedEngine(t, dir, sites, refs, live, func(string, time.Duration) (mutantOutcome, error) {
		call++
		if call == 1 {
			return mutantKilled, nil
		}
		return mutantSurvived, nil
	}, cacheTestMeta)
	if first.killedCount() != 1 || first.survivedCount() != 1 {
		t.Fatalf("first run %d killed / %d survived, want 1 / 1", first.killedCount(), first.survivedCount())
	}

	secondCalls := 0
	second := runCachedEngine(t, dir, sites, refs, live, func(string, time.Duration) (mutantOutcome, error) {
		secondCalls++
		return mutantKilled, nil
	}, cacheTestMeta)
	if secondCalls != 1 {
		t.Errorf("warm run executed %d mutants, want 1: only the survivor re-executes", secondCalls)
	}
	if second.fromCacheCount() != 1 || second.killedCount() != 2 || second.survivedCount() != 0 {
		t.Errorf("warm result %d killed / %d survived / %d cached, want 2 / 0 / 1",
			second.killedCount(), second.survivedCount(), second.fromCacheCount())
	}
}

func TestMutationCacheEditInvalidatesOnlyEditedMethod(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	sites, refs, live := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	runCachedEngine(t, dir, sites, refs, live, func(string, time.Duration) (mutantOutcome, error) {
		return mutantKilled, nil
	}, cacheTestMeta)

	// Add a comment inside Add only; Zero stays byte-identical.
	edited := strings.Replace(cacheSrcA, "\treturn a + b\n", "\t// a clarifying comment\n\treturn a + b\n", 1)
	writeCacheFixture(t, dir, "a.go", edited)
	editedSites, editedRefs, editedLive := cacheFixtureScan(t, dir, "a.go", edited)
	calls := 0
	second := runCachedEngine(t, dir, editedSites, editedRefs, editedLive, func(string, time.Duration) (mutantOutcome, error) {
		calls++
		return mutantKilled, nil
	}, cacheTestMeta)
	if calls != 1 {
		t.Errorf("warm run executed %d mutants, want 1: only Add changed; Zero replays from cache", calls)
	}
	if second.fromCacheCount() != 1 || second.killedCount() != 2 {
		t.Errorf("warm result %d killed / %d cached, want 2 / 1", second.killedCount(), second.fromCacheCount())
	}
}

func TestMutationCacheMetaMismatchWipes(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	sites, refs, live := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	runCachedEngine(t, dir, sites, refs, live, func(string, time.Duration) (mutantOutcome, error) {
		return mutantKilled, nil
	}, cacheTestMeta)

	calls := 0
	second := runCachedEngine(t, dir, sites, refs, live, func(string, time.Duration) (mutantOutcome, error) {
		calls++
		return mutantKilled, nil
	}, currentMutationCacheMeta(map[string]bool{"arithmetic": true}, defaultMutationTestArgs))
	if calls != 2 {
		t.Errorf("meta change re-executed %d mutants, want 2 (cache wiped)", calls)
	}
	if second.fromCacheCount() != 0 {
		t.Errorf("meta change replayed %d mutants, want 0", second.fromCacheCount())
	}
}

func TestMutationCacheLoadRobustness(t *testing.T) {
	dir := t.TempDir()
	meta := cacheTestMeta
	cacheFile := filepath.Join(dir, mutationCacheFileName)

	if got := loadMutationCache(cacheFile, meta); len(got.Methods) != 0 {
		t.Errorf("missing file: got %d methods, want a fresh cache", len(got.Methods))
	}
	if err := os.WriteFile(cacheFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadMutationCache(cacheFile, meta); len(got.Methods) != 0 {
		t.Errorf("corrupt file: got %d methods, want a fresh cache", len(got.Methods))
	}
	if err := os.WriteFile(cacheFile, []byte(`{"version":99,"meta":{},"methods":{"k":{"digest":"d"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadMutationCache(cacheFile, meta); len(got.Methods) != 0 {
		t.Errorf("version mismatch: got %d methods, want a fresh cache", len(got.Methods))
	}
	if err := os.WriteFile(cacheFile, []byte(`{"version":1,"meta":{"tool":"other","operators":"","test_args":""},"methods":{"k":{"digest":"d"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadMutationCache(cacheFile, meta); len(got.Methods) != 0 {
		t.Errorf("meta mismatch: got %d methods, want a fresh cache", len(got.Methods))
	}
	if err := os.WriteFile(cacheFile, []byte(`{"version":1,"meta":{"tool":"`+cacheTestMeta.Tool+`","operators":"`+cacheTestMeta.Operators+`","test_args":"`+cacheTestMeta.TestArgs+`"},"methods":{"k":{"digest":"d"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadMutationCache(cacheFile, meta)
	if len(got.Methods) != 1 || got.Methods["k"].Digest != "d" {
		t.Errorf("matching meta: got %+v, want the stored method", got.Methods)
	}
}

func TestMutationCachePrunesVanishedMethods(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	writeCacheFixture(t, dir, "b.go", cacheSrcB)
	sitesA, refsA, liveA := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	sitesB, refsB, liveB := cacheFixtureScan(t, dir, "b.go", cacheSrcB)
	allRefs := map[MutationSite]mutationMethodRef{}
	for site, ref := range refsA {
		allRefs[site] = ref
	}
	for site, ref := range refsB {
		allRefs[site] = ref
	}
	allLive := map[string]bool{}
	for key := range liveA {
		allLive[key] = true
	}
	for key := range liveB {
		allLive[key] = true
	}
	runCachedEngine(t, dir, append(append([]MutationSite{}, sitesA...), sitesB...), allRefs, allLive,
		func(string, time.Duration) (mutantOutcome, error) { return mutantKilled, nil }, cacheTestMeta)
	cacheFile := filepath.Join(dir, mutationCacheFileName)
	if got := loadMutationCache(cacheFile, cacheTestMeta); len(got.Methods) != 3 {
		t.Fatalf("got %d cached methods, want 3: %v", len(got.Methods), got.Methods)
	}

	// b.go vanishes; a re-run prunes its entries.
	if err := os.Remove(filepath.Join(dir, "b.go")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runCachedEngine(t, dir, sitesA, refsA, liveA, func(string, time.Duration) (mutantOutcome, error) {
		calls++
		return mutantKilled, nil
	}, cacheTestMeta)
	if calls != 0 {
		t.Errorf("pruning run re-executed %d mutants, want 0", calls)
	}
	got := loadMutationCache(cacheFile, cacheTestMeta)
	if len(got.Methods) != 2 || got.Methods["a.go#Add"].Digest == "" || got.Methods["a.go#Zero"].Digest == "" {
		t.Errorf("pruned methods %v, want only a.go#Add and a.go#Zero", got.Methods)
	}
}

func TestMutationCacheCrossFileKeys(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	writeCacheFixture(t, dir, "b.go", cacheSrcB)
	sitesA, refsA, liveA := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	sitesB, refsB, liveB := cacheFixtureScan(t, dir, "b.go", cacheSrcB)
	allRefs := map[MutationSite]mutationMethodRef{}
	for site, ref := range refsA {
		allRefs[site] = ref
	}
	for site, ref := range refsB {
		allRefs[site] = ref
	}
	allLive := map[string]bool{}
	for key := range liveA {
		allLive[key] = true
	}
	for key := range liveB {
		allLive[key] = true
	}
	allSites := append(append([]MutationSite{}, sitesA...), sitesB...)
	// Kill everything in a.go, let all of b.go survive.
	call := 0
	runCachedEngine(t, dir, allSites, allRefs, allLive, func(string, time.Duration) (mutantOutcome, error) {
		call++
		if call <= len(sitesA) {
			return mutantKilled, nil
		}
		return mutantSurvived, nil
	}, cacheTestMeta)

	secondCalls := 0
	second := runCachedEngine(t, dir, allSites, allRefs, allLive, func(string, time.Duration) (mutantOutcome, error) {
		secondCalls++
		return mutantKilled, nil
	}, cacheTestMeta)
	if secondCalls != len(sitesB) {
		t.Errorf("warm run executed %d mutants, want %d: only b.go survivors re-execute; a.go replays", secondCalls, len(sitesB))
	}
	if second.fromCacheCount() != len(sitesA) || second.killedCount() != len(allSites) {
		t.Errorf("warm result %d killed / %d cached, want %d / %d",
			second.killedCount(), second.fromCacheCount(), len(allSites), len(sitesA))
	}
}

// TestMutationIntegrityHealsStrandedMutant injects the out-of-band defeat
// the integrity check exists for: a write that lands after the per-site
// restore (external edit or a killed process between apply and restore).
// The goroutine handshake keeps it deterministic: the stranded write to
// a.go is released by the last runner call and acknowledged before it
// returns, so it lands after a.go's own restore but before the
// end-of-run check.
func TestMutationIntegrityHealsStrandedMutant(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	writeCacheFixture(t, dir, "b.go", cacheSrcB)
	sitesA, _, _ := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	sitesB, refsB, _ := cacheFixtureScan(t, dir, "b.go", cacheSrcB)
	fileA := filepath.Join(dir, "a.go")
	_, refsA, _ := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	refs := map[MutationSite]mutationMethodRef{}
	for site, ref := range refsA {
		refs[site] = ref
	}
	for site, ref := range refsB {
		refs[site] = ref
	}
	sites := append(append([]MutationSite{}, sitesA...), sitesB...)

	proceed := make(chan struct{})
	done := make(chan struct{})
	call := 0
	runner := func(string, time.Duration) (mutantOutcome, error) {
		call++
		if call == 1 {
			go func() {
				<-proceed
				src, readErr := os.ReadFile(fileA)
				if readErr == nil {
					_ = os.WriteFile(fileA, sitesA[0].applyTo(src), 0o644)
				}
				close(done)
			}()
			return mutantKilled, nil
		}
		if call == len(sites) {
			close(proceed)
			<-done // the stranded write lands before this runner returns
		}
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir, sites, 5*time.Second, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.integrityViolations) != 1 || result.integrityViolations[0] != fileA {
		t.Fatalf("violations %v, want [%s]", result.integrityViolations, fileA)
	}
	after, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != cacheSrcA {
		t.Errorf("integrity check did not restore the stranded mutant:\n%s", after)
	}
	afterB, err := os.ReadFile(filepath.Join(dir, "b.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterB) != cacheSrcB {
		t.Errorf("healthy file was touched:\n%s", afterB)
	}
}

// TestMutationIntegrityCleanOnMidLoopError pins the finally guarantee: an
// engine that dies mid-loop still leaves every mutated file byte-identical.
func TestMutationIntegrityCleanOnMidLoopError(t *testing.T) {
	dir := t.TempDir()
	writeCacheFixture(t, dir, "a.go", cacheSrcA)
	writeCacheFixture(t, dir, "b.go", cacheSrcB)
	sitesA, refsA, liveA := cacheFixtureScan(t, dir, "a.go", cacheSrcA)
	sitesB, refsB, liveB := cacheFixtureScan(t, dir, "b.go", cacheSrcB)
	refs := map[MutationSite]mutationMethodRef{}
	for site, ref := range refsA {
		refs[site] = ref
	}
	for site, ref := range refsB {
		refs[site] = ref
	}
	sites := append(append([]MutationSite{}, sitesA...), sitesB...)
	call := 0
	_, err := runMutationEngine(dir, sites, 5*time.Second, func(string, time.Duration) (mutantOutcome, error) {
		call++
		if call == len(sitesA)+1 {
			return 0, errors.New("suite runner gone")
		}
		return mutantKilled, nil
	}, &mutationCacheSession{
		cache:       freshMutationCache(cacheTestMeta),
		cacheFile:   filepath.Join(dir, mutationCacheFileName),
		siteMethods: refs,
		liveKeys:    mergeLive(liveA, liveB),
	})
	if err == nil {
		t.Fatal("engine error swallowed")
	}
	for name, want := range map[string]string{"a.go": cacheSrcA, "b.go": cacheSrcB} {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != want {
			t.Errorf("%s not restored after mid-loop error:\n%s", name, after)
		}
	}
}

func mergeLive(liveA, liveB map[string]bool) map[string]bool {
	live := map[string]bool{}
	for key := range liveA {
		live[key] = true
	}
	for key := range liveB {
		live[key] = true
	}
	return live
}

func TestMutationVerdictReportsCachedAndIntegrity(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "calc.go")
	if err := os.WriteFile(file, []byte(cacheSrcA), 0o644); err != nil {
		t.Fatal(err)
	}
	result := mutationRunResult{
		outcomes: []mutationSiteOutcome{
			{site: MutationSite{File: file, Line: 3, Category: "arithmetic", Original: "+", Mutant: "-"}, outcome: mutantKilled, fromCache: true},
			{site: MutationSite{File: file, Line: 7, Category: "constant", Original: "0", Mutant: "1"}, outcome: mutantSurvived},
		},
		integrityViolations: []string{file},
	}
	var out, errOut bytes.Buffer
	code := mutationVerdict(result, 1, mutationOptions{minScore: 0.8}, dir, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	stdout := out.String()
	if !strings.Contains(stdout, "1 cached") {
		t.Errorf("summary missing cached count:\n%s", stdout)
	}
	if !strings.Contains(stdout, "; INTEGRITY: re-restored after run: calc.go") {
		t.Errorf("summary missing integrity violations:\n%s", stdout)
	}
	if !strings.Contains(errOut.String(), "working tree did not restore cleanly (1 file(s) re-restored)") {
		t.Errorf("stderr warning missing:\n%s", errOut.String())
	}
}

func TestMutationTestArgsFlag(t *testing.T) {
	var stderr bytes.Buffer
	opts, code, ok := parseMutationFlags([]string{"--test-args", "test -run TestAdd"}, &stderr)
	if !ok {
		t.Fatalf("parse failed (code %d): %s", code, stderr.String())
	}
	if strings.Join(opts.testArgs, " ") != "test -run TestAdd" {
		t.Errorf("testArgs %v, want [test -run TestAdd] — scoped invocation replaces the default", opts.testArgs)
	}
	opts, _, ok = parseMutationFlags(nil, &stderr)
	if !ok || strings.Join(opts.testArgs, " ") != "test ./..." {
		t.Errorf("default testArgs %v (ok %v), want [test ./...]", opts.testArgs, ok)
	}
	if code := func() int {
		opts, code, ok := parseMutationFlags([]string{"--test-args", "  "}, &stderr)
		if ok {
			t.Errorf("empty arg list accepted: %+v", opts)
		}
		return code
	}(); code != 1 {
		t.Errorf("empty arg list exit %d, want 1", code)
	}
	opts, _, ok = parseMutationFlags([]string{"--cache=false"}, &stderr)
	if !ok || opts.cache {
		t.Errorf("--cache=false not honored: %+v (ok %v)", opts, ok)
	}
}

// TestMutantRunnerTestArgsScopeRun proves the scoping contract end to end:
// the full suite kills the mutant, the scoped suite (-run) does not — a
// mutant is killed only if the scoped suite would kill it.
func TestMutantRunnerTestArgsScopeRun(t *testing.T) {
	requireGoBin(t)
	root := writeMutationProject(t, map[string]string{
		"a.go": "package mut\n\nfunc A() int { return 1 }\n",
		"a_test.go": `package mut

import "testing"

func TestKiller(t *testing.T) {
	if A() != 2 {
		t.Error("nope")
	}
}

func TestOther(t *testing.T) {
	if A() != 1 {
		t.Error("nope")
	}
}
`,
	})
	full, err := mutantTestRunnerOS(root, 30*time.Second, defaultMutationTestArgs)
	if err != nil || full != mutantKilled {
		t.Fatalf("full suite: %v, %v; want killed", full, err)
	}
	scoped, err := mutantTestRunnerOS(root, 30*time.Second, []string{"test", "-run", "TestOther", "./..."})
	if err != nil || scoped != mutantSurvived {
		t.Errorf("scoped suite: %v, %v; want survived", scoped, err)
	}
}

// TestMutationCommandWarmRunReplaysKills is the gate-level cold→warm proof
// with the real `go test` runner: the second run replays every kill and
// reports it as cached.
func TestMutationCommandWarmRunReplaysKills(t *testing.T) {
	requireGoBin(t)
	root := writeMutationProject(t, map[string]string{
		"sign.go": "package mut\n\nfunc Sign(x int) string {\n\tif x < 0 {\n\t\treturn \"neg\"\n\t}\n\treturn \"pos\"\n}\n",
		"sign_test.go": `package mut

import "testing"

func TestSign(t *testing.T) {
	if Sign(-1) != "neg" {
		t.Error("Sign(-1) != neg")
	}
	if Sign(1) != "pos" {
		t.Error("Sign(1) != pos")
	}
	if Sign(0) != "pos" {
		t.Error("Sign(0) != pos")
	}
}
`,
	})
	generateMutationCoverage(t, root)

	var out, errOut bytes.Buffer
	if code := RunMutationCommand(nil, root, &out, &errOut); code != 0 {
		t.Fatalf("cold exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "3 killed, 0 survived, 0 uncovered, 0 cached"; !strings.Contains(out.String(), want) {
		t.Fatalf("cold summary missing %q:\n%s", want, out.String())
	}
	cacheFile := filepath.Join(root, mutationCacheFileName)
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("cache not persisted: %v", err)
	}

	out.Reset()
	errOut.Reset()
	if code := RunMutationCommand(nil, root, &out, &errOut); code != 0 {
		t.Fatalf("warm exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "3 killed, 0 survived, 0 uncovered, 3 cached"; !strings.Contains(out.String(), want) {
		t.Errorf("warm summary missing %q:\n%s", want, out.String())
	}
}
