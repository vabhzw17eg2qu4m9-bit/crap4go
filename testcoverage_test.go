package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// sourceFile is the single .go file a coverage fixture attributes.
const sourceFile = "package p\n\nfunc F() {}\n"

// coverProfile builds a native cover profile attributing per-file statement
// counts: entries are (file, numStmt, count) triples rendered as blocks.
func coverProfile(entries ...string) string {
	var b strings.Builder
	b.WriteString("mode: set\n")
	for _, e := range entries {
		b.WriteString(e)
		b.WriteString("\n")
	}
	return b.String()
}

func coverBlock(file string, numStmt, count int) string {
	return fmt.Sprintf("example.com/pkg/%s:3.10,5.3 %d %d", file, numStmt, count)
}

func TestRun_TestCoverageBelowMin(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/uncovered.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 5, 1), coverBlock("uncovered.go", 5, 0)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"coverage.out: total coverage 50.0% < min 80.0%",
		"coverage 50.0% < min 80.0%",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_TestCoveragePasses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 8, 1)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "coverage 100.0% >= 80.0%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageBoundaryPasses(t *testing.T) {
	// Exactly at the minimum passes (>=).
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/uncovered.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 5, 1), coverBlock("uncovered.go", 5, 0)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--min-percent", "50"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s)", code, out.String())
	}
	if want := "coverage 50.0% >= 50.0%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageMinPercentFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 8, 1)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--min-percent", "100.5"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if want := "coverage 100.0% < min 100.5%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageExcludeDropsFromRatio(t *testing.T) {
	// The uncovered file is excluded, lifting the ratio to 100%.
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/uncovered.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 5, 1), coverBlock("uncovered.go", 5, 0)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--exclude", "uncovered.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "coverage 100.0% >= 80.0%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageMissingProfileFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unit-test coverage is required") {
		t.Errorf("stderr missing fail-closed hint:\n%s", errOut.String())
	}
}

func TestRun_TestCoverageEmptyProfileFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/coverage.out", "mode: set\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no coverage records") {
		t.Errorf("stderr missing fail-closed hint:\n%s", errOut.String())
	}
}

func TestRun_TestCoverageLcovInput(t *testing.T) {
	// An .info path selects the lcov parser (one DA record per statement).
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/uncovered.go", sourceFile)
	writeFile(t, root+"/lcov.info", "TN:\nSF:covered.go\nDA:3,1\nend_of_record\nTN:\nSF:uncovered.go\nDA:3,0\nend_of_record\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--coverage", "lcov.info"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	if want := "total coverage 50.0% < min 80.0%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageFilesWithoutEntryIgnored(t *testing.T) {
	// A selected file with no coverage entry contributes nothing to the
	// ratio, and excludes dropping every entry-bearing file leave no
	// statements — the ratio is 100% (upstream empty-aggregate rule).
	root := t.TempDir()
	writeFile(t, root+"/covered.go", sourceFile)
	writeFile(t, root+"/uncovered.go", sourceFile)
	writeFile(t, root+"/orphan.go", sourceFile)
	writeFile(t, root+"/coverage.out", coverProfile(coverBlock("covered.go", 5, 1), coverBlock("uncovered.go", 5, 0)))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--exclude", "covered.go", "--exclude", "uncovered.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "coverage 100.0% >= 80.0%"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestCoverageUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-coverage", "--min-percent", "bogus"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
