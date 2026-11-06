package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSrc is a small Go program whose methods have known complexity. Lines
// are documented for the matching cover profile below.
const fixtureSrc = `package sample

func Add(a, b int) int { // line 3
	return a + b
}

func Max(a, b int) int { // line 7
	if a > b {
		return a
	}
	return b
}
`

const fixtureProfile = `mode: atomic
example.com/sample/sample.go:3.10,5.2 1 3
example.com/sample/sample.go:7.14,8.10 1 1
example.com/sample/sample.go:9.3,10.4 1 0
example.com/sample/sample.go:11.3,12.4 1 1
`

// setupProject writes a temp project directory with the fixture source and
// (optionally) a coverage profile, returning the paths.
func setupProject(t *testing.T, withCoverage bool) (root, srcPath, covPath string) {
	t.Helper()
	root = t.TempDir()
	srcPath = filepath.Join(root, "sample.go")
	if err := os.WriteFile(srcPath, []byte(fixtureSrc), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	covPath = filepath.Join(root, "cover.out")
	if withCoverage {
		if err := os.WriteFile(covPath, []byte(fixtureProfile), 0o644); err != nil {
			t.Fatalf("write cov: %v", err)
		}
	}
	return root, srcPath, covPath
}

func TestRun_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-help"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout missing Usage: %q", out.String())
	}
}

func TestRun_NoGoFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot(nil, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "No Go files to analyze.") {
		t.Errorf("stdout missing no-files message: %q", out.String())
	}
}

func TestRun_Exit0BelowThreshold(t *testing.T) {
	root, src, cov := setupProject(t, true)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-coverage", cov, src}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "CRAP Report") {
		t.Errorf("stdout missing report: %q", out.String())
	}
}

func TestRun_Exit2OverThreshold(t *testing.T) {
	root, src, cov := setupProject(t, true)
	// Add has CC=1 fully covered (CRAP=1). Force a high-CRAP path by passing a
	// trivially-low threshold so any positive max trips it.
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-coverage", cov, "-threshold", "0.5", src}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "CRAP threshold exceeded:") {
		t.Errorf("stderr missing threshold message: %q", errOut.String())
	}
}

func TestRun_ChangedWithPathsIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-changed", "some.go"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "--changed cannot be combined") {
		t.Errorf("stderr missing message: %q", errOut.String())
	}
}

func TestRun_BadThresholdIsUsageError(t *testing.T) {
	root, src, _ := setupProject(t, false)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-threshold", "0", src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_MissingCoverageExits1(t *testing.T) {
	root, src, _ := setupProject(t, false)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unit-test coverage is required") {
		t.Errorf("stderr missing fail-closed message: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "go test ./... -coverprofile") {
		t.Errorf("stderr missing coverage generation hint: %q", errOut.String())
	}
	if strings.Contains(out.String(), "CRAP Report") {
		t.Errorf("fail-closed run should print no report:\n%s", out.String())
	}
}

func TestRun_EmptyCoverageExits1(t *testing.T) {
	root, src, cov := setupProject(t, true)
	if err := os.WriteFile(cov, []byte("mode: atomic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-coverage", cov, src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no coverage records") {
		t.Errorf("stderr missing empty-coverage message: %q", errOut.String())
	}
}

func TestRun_EmptyLcovCoverageExits1(t *testing.T) {
	root, src, _ := setupProject(t, true)
	info := filepath.Join(root, "cover.info")
	if err := os.WriteFile(info, []byte("TN:covered\nend_of_record\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-coverage", info, src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no coverage records") {
		t.Errorf("stderr missing empty-coverage message: %q", errOut.String())
	}
}

func TestRun_UnknownFlagIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-bogus"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

// TestRun_BuiltBinary end-to-end: build the binary, then run it on the fixture
// directory. Confirms the produced binary works as a real CLI.
func TestRun_BuiltBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "crap4go")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	root, _, cov := setupProject(t, true)
	run := exec.Command(bin, "-coverage", cov, ".")
	run.Dir = root
	var out, errOut bytes.Buffer
	run.Stdout = &out
	run.Stderr = &errOut
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// exit 0 or 2 are both acceptable; report a real failure for other codes.
			if ee.ExitCode() != 2 {
				t.Fatalf("unexpected exit %d: stderr=%s", ee.ExitCode(), errOut.String())
			}
		} else {
			t.Fatalf("run: %v", err)
		}
	}
	if !strings.Contains(out.String(), "CRAP Report") {
		t.Errorf("binary output missing report:\n%s", out.String())
	}
}

func TestRun_HelpDocumentsNewFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-help"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	for _, flag := range []string{"--diff-base", "--strict", "--exclude"} {
		if !strings.Contains(out.String(), flag) {
			t.Errorf("help missing %s:\n%s", flag, out.String())
		}
	}
}

// TestRun_NoFlagsReportGolden pins the byte-identical contract: without the
// new flags the report is exactly what the 0.27.0 release printed.
func TestRun_NoFlagsReportGolden(t *testing.T) {
	root, src, cov := setupProject(t, true)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"-coverage", cov, src}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	header := fmt.Sprintf("%-30s %-35s %4s %7s %8s", "Method", "File", "CC", "Cov%", "CRAP")
	want := "CRAP Report\n===========\n" + header + "\n" + strings.Repeat("-", len(header)) + "\n" +
		fmt.Sprintf("%-30s %-35s %4d %7s %8s\n", "Max", src, 2, "  66.7%", "     2.1") +
		fmt.Sprintf("%-30s %-35s %4d %7s %8s\n", "Add", src, 1, " 100.0%", "     1.0") +
		"\nMax CRAP: 2.1 (threshold 8.0) — passed\n"
	if out.String() != want {
		t.Errorf("no-flags report changed:\ngot:\n%q\nwant:\n%q", out.String(), want)
	}
}

// fixtureLcov carries the same coverage data as fixtureProfile at statement
// granularity: Add's line 4 covered, Max's lines 8/9/11 covered/uncovered/
// covered (2 of 3 statements).
const fixtureLcov = `SF:sample.go
DA:4,1
DA:8,1
DA:9,0
DA:11,1
end_of_record
`

// TestRun_LcovCoverageMatchesNativeReport proves the lcov reader is
// interchangeable with the native cover profile: same coverage data, one
// byte-identical report. The relative SF: path resolves against the root.
func TestRun_LcovCoverageMatchesNativeReport(t *testing.T) {
	root, src, cov := setupProject(t, true)
	info := filepath.Join(root, "cover.info")
	if err := os.WriteFile(info, []byte(fixtureLcov), 0o644); err != nil {
		t.Fatalf("write lcov: %v", err)
	}
	var nativeOut, lcovOut, errOut bytes.Buffer
	native := runWithRoot([]string{"-coverage", cov, src}, root, &nativeOut, &errOut)
	lcov := runWithRoot([]string{"-coverage", info, src}, root, &lcovOut, &errOut)
	if native != 0 || lcov != 0 {
		t.Fatalf("exit codes native=%d lcov=%d; stderr=%s", native, lcov, errOut.String())
	}
	if nativeOut.String() != lcovOut.String() {
		t.Errorf("lcov report differs from native report:\nnative:\n%s\nlcov:\n%s",
			nativeOut.String(), lcovOut.String())
	}
}

func TestRun_StrictMissingCoverageExits1(t *testing.T) {
	root, src, _ := setupProject(t, false)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--strict", src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
}

func TestRun_StrictEmptyCoverageExits1(t *testing.T) {
	root, src, cov := setupProject(t, true)
	if err := os.WriteFile(cov, []byte("mode: atomic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--strict", "-coverage", cov, src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
}

func TestRun_StrictParseFailureExits1(t *testing.T) {
	root, src, cov := setupProject(t, true)
	if err := os.WriteFile(cov, []byte("mode: atomic\nnot a profile line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--strict", "-coverage", cov, src}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
}

// TestRun_StrictNoEntryFileReportsZeroCoverage: the selected file without a
// coverage entry reports 0.0% rows (not N/A) and does not fail the run.
func TestRun_StrictNoEntryFileReportsZeroCoverage(t *testing.T) {
	root, src, _ := setupProject(t, false)
	other := filepath.Join(root, "strict.go")
	if err := os.WriteFile(other, []byte("package sample\n\nfunc Lonely() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := filepath.Join(root, "cover.info")
	if err := os.WriteFile(info, []byte(fixtureLcov), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--strict", "-coverage", info, src, other}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "  0.0%") {
		t.Errorf("strict run should report 0.0%% for the entry-less file:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Lonely") {
		t.Errorf("strict run lost the entry-less file's method:\n%s", out.String())
	}
}

// writeAnalyzedFile writes one single-function Go source at rel under root.
func writeAnalyzedFile(t *testing.T, root, rel, fn string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package main\n\nfunc " + fn + "(x int) int {\n\treturn x + 1\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCoverProfile writes the default coverage profile under root: any
// report run needs one (coverage is required since the fail-closed default).
func writeCoverProfile(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "coverage.out"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// coveredFn is one fully covered single-statement block spanning fn's body
// (lines 3-5 in writeAnalyzedFile's source).
func coveredFn(file string) string {
	return file + ":3.1,5.2 1 1\n"
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// diffBaseSrc has a removable comment inside Hot so the working-tree edit is
// a pure deletion there and an addition inside Cold.
const diffBaseSrc = `package main

func Hot(x int) int {
	// removable
	return x + 1
}

func Cold(x int) int {
	return x + 1
}
`

const diffBaseEdited = `package main

func Hot(x int) int {
	return x + 1
}

func Cold(x int) int {
	// touched
	return x + 1
}
`

// TestRun_DiffBaseKeepsChangedAndDropsDeletedOnly: Cold gains a line (kept),
// Hot only loses one (deleted-only functions are dropped).
func TestRun_DiffBaseKeepsChangedAndDropsDeletedOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(diffBaseSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, root)
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(diffBaseEdited), 0o644); err != nil {
		t.Fatal(err)
	}
	// Edited file: Hot spans 3-5, Cold 7-10.
	writeCoverProfile(t, root, "mode: atomic\ncalc.go:3.1,5.2 1 1\ncalc.go:7.1,10.2 2 1\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Cold") {
		t.Errorf("diff-base lost the changed function:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Hot") {
		t.Errorf("diff-base kept the deleted-only function:\n%s", out.String())
	}
}

func TestRun_DiffBaseRenameUsesNewPath(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "calc.go", "Renamed")
	initGitRepo(t, root)
	runGit(t, root, "mv", "calc.go", "moved.go")
	edited := "package main\n\nfunc Renamed(x int) int {\n\t// touched\n\treturn x + 1\n}\n"
	if err := os.WriteFile(filepath.Join(root, "moved.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCoverProfile(t, root, "mode: atomic\n"+coveredFn("moved.go"))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Renamed") || !strings.Contains(out.String(), "moved.go") {
		t.Errorf("diff-base lost the renamed file's function:\n%s", out.String())
	}
	if strings.Contains(out.String(), "calc.go") {
		t.Errorf("renamed file still reported under its old path:\n%s", out.String())
	}
}

func TestRun_DiffBaseWithPathsScopes(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "a.go", "AFunc")
	writeAnalyzedFile(t, root, "b.go", "BFunc")
	initGitRepo(t, root)
	names := map[string]string{"a.go": "AFunc", "b.go": "BFunc"}
	for _, rel := range []string{"a.go", "b.go"} {
		edited := "package main\n\nfunc " + names[rel] + "(x int) int {\n\t// touched\n\treturn x + 1\n}\n"
		if err := os.WriteFile(filepath.Join(root, rel), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCoverProfile(t, root, "mode: atomic\n"+coveredFn("a.go")+coveredFn("b.go"))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD", "a.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "AFunc") {
		t.Errorf("explicit path lost its changed function:\n%s", out.String())
	}
	if strings.Contains(out.String(), "BFunc") {
		t.Errorf("explicit path leaked the unscoped file:\n%s", out.String())
	}
}

func TestRun_DiffBaseUnknownRefExits1(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "a.go", "AFunc")
	initGitRepo(t, root)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "nosuchref"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "git diff") {
		t.Errorf("stderr missing git error: %q", errOut.String())
	}
}

func TestRun_DiffBaseWithChangedIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD", "--changed"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "--diff-base cannot be combined") {
		t.Errorf("stderr missing message: %q", errOut.String())
	}
}

func TestRun_DiffBaseNoChangesExits0(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "a.go", "AFunc")
	initGitRepo(t, root)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No Go files to analyze.") {
		t.Errorf("clean diff should print the empty-selection message:\n%s", out.String())
	}
}

// TestRun_ExcludeDropsMatchedFiles drops only glob-matched files from the
// default selection.
func TestRun_ExcludeDropsMatchedFiles(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "a.go", "AFunc")
	writeAnalyzedFile(t, root, "sub/b.go", "BFunc")
	writeCoverProfile(t, root, "mode: atomic\n"+coveredFn("a.go")+coveredFn("sub/b.go"))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--exclude", "sub/*"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "AFunc") {
		t.Errorf("exclude dropped an unmatched file:\n%s", out.String())
	}
	if strings.Contains(out.String(), "BFunc") {
		t.Errorf("exclude kept the matched file:\n%s", out.String())
	}
}

func TestRun_ExcludeAllDroppedIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeAnalyzedFile(t, root, "a.go", "AFunc")
	writeAnalyzedFile(t, root, "sub/b.go", "BFunc")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--exclude", "a.go", "--exclude", "sub/*"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No Go files to analyze.") {
		t.Errorf("fully excluded selection should print the empty message:\n%s", out.String())
	}
}

// TestRun_DiffBaseOutsideFunctionsExits0: a diff that touches only lines
// outside any function keeps the file in the selection but selects no
// functions — the empty-selection message, exit 0.
func TestRun_DiffBaseOutsideFunctionsExits0(t *testing.T) {
	root := t.TempDir()
	base := "package main\n\n// top comment\n\nfunc Only(x int) int {\n\treturn x + 1\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, root)
	edited := "package main\n\n// changed comment\n\nfunc Only(x int) int {\n\treturn x + 1\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	// Edited file: Only spans 5-7; the diff touches only the line-3 comment.
	writeCoverProfile(t, root, "mode: atomic\na.go:5.1,7.2 1 1\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--diff-base", "HEAD"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No Go files to analyze.") {
		t.Errorf("out-of-function diff should select no functions:\n%s", out.String())
	}
}

func TestRun_StrictMissingPathExits1(t *testing.T) {
	root, _, cov := setupProject(t, true)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"--strict", "-coverage", cov, filepath.Join(root, "missing.go")}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errOut.String())
	}
}
