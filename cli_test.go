package main

import (
	"bytes"
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

func TestRun_MissingCoverageWarnsButSucceeds(t *testing.T) {
	root, src, _ := setupProject(t, false)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{src}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "Warning:") {
		t.Errorf("stderr missing warning: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "Hint: generate coverage first") {
		t.Errorf("stderr missing coverage hint: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "N/A") {
		t.Errorf("stdout should show N/A: %q", out.String())
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
