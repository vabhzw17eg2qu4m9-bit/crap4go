package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViolationForStem(t *testing.T) {
	tests := []struct {
		stem string
		want string // "" = no violation
	}{
		{"util", `generic name "util.go" — split by domain instead of accumulating unrelated declarations`},
		{"UTILS", `generic name "UTILS.go" — split by domain instead of accumulating unrelated declarations`},
		{"parser", ""},
		{"base64", ""}, // numeric suffix but allowlisted
		{"sha256", ""}, // numeric suffix but allowlisted
		{"batch1", `numeric suffix in "batch1.go" — split by domain instead of numbered parts (batch1, part2, v2 ...)`},
		{"Report2", `numeric suffix in "Report2.go" — split by domain instead of numbered parts (batch1, part2, v2 ...)`},
		{"day_1", `numeric suffix in "day_1.go" — split by domain instead of numbered parts (batch1, part2, v2 ...)`},
		{"configv3", `numeric suffix in "configv3.go" — split by domain instead of numbered parts (batch1, part2, v2 ...)`},
		{"123", ""}, // digits not preceded by a letter/underscore
	}
	for _, tt := range tests {
		if got := violationForStem(tt.stem); got != tt.want {
			t.Errorf("violationForStem(%q) = %q, want %q", tt.stem, got, tt.want)
		}
	}
}

// writeFile is a test helper creating a file with mode 0644.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupNamingProject creates a project with clean and violating files,
// including files that must be excluded from selection.
func setupNamingProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parser.go"), "package p\n")
	writeFile(t, filepath.Join(root, "util.go"), "package p\n")
	writeFile(t, filepath.Join(root, "batch1.go"), "package p\n")
	writeFile(t, filepath.Join(root, "base64.go"), "package p\n")
	writeFile(t, filepath.Join(root, "util_test.go"), "package p\n")      // excluded: test file
	writeFile(t, filepath.Join(root, "vendor", "util.go"), "package v\n") // excluded: vendor
	return root
}

func TestRun_FileNamingViolations(t *testing.T) {
	root := setupNamingProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"file-naming"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"util.go: generic name",
		"batch1.go: numeric suffix",
		"2/4 files with mechanical names",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "util_test.go:") || strings.Contains(got, "vendor") {
		t.Errorf("excluded files flagged:\n%s", got)
	}
	if strings.Contains(got, "base64.go:") {
		t.Errorf("allowlisted stem flagged:\n%s", got)
	}
}

func TestRun_FileNamingClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "parser.go"), "package p\n")
	writeFile(t, filepath.Join(root, "base64.go"), "package p\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"file-naming"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "2 files have domain-meaningful names"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_FileNamingNoFiles(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"file-naming"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_FileNamingExplicitPaths(t *testing.T) {
	root := setupNamingProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"file-naming", filepath.Join(root, "util.go")}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "1/1 files with mechanical names") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}
