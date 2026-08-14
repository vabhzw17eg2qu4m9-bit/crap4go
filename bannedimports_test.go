package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func setupImportProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.21\n")
	writeFile(t, filepath.Join(root, "ui", "panel.go"), "package ui\n\nimport (\n\t\"os\"\n\t\"example.com/demo/db\"\n)\n\nvar _ = os.Args\nvar _ = db.Query\n")
	writeFile(t, filepath.Join(root, "db", "store.go"), "package db\n\nimport \"os\"\n\nvar Query, _ = os.Getenv, 0\n")
	return root
}

func TestRun_BannedImportsViolation(t *testing.T) {
	root := setupImportProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{
		"banned-imports",
		"--from", "ui/**", "--forbid", "**/db/**", "--message", "UI must not touch storage",
	}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, `ui/panel.go:5: import "example.com/demo/db" is banned for ui/panel.go — UI must not touch storage`) {
		t.Errorf("output missing violation:\n%s", got)
	}
	if !strings.Contains(got, "1 banned import(s) in 1 files") {
		t.Errorf("output missing summary:\n%s", got)
	}
}

func TestRun_BannedImportsClean(t *testing.T) {
	root := setupImportProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{
		"banned-imports",
		"--from", "ui/**", "--forbid", "encoding/json", "--message", "no json in UI",
	}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 files comply with 1 rule(s)"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_BannedImportsNoRules(t *testing.T) {
	root := setupImportProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"banned-imports"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "no rules configured"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_BannedImportsUnpairedFlagsAreUsageError(t *testing.T) {
	root := setupImportProject(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"banned-imports", "--from", "ui/**"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "must appear in pairs") {
		t.Errorf("stderr missing pairing error:\n%s", errOut.String())
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"ui/**", "ui/panel.go", true},
		{"ui/**", "ui/nested/panel.go", true},
		{"ui/*", "ui/panel.go", true},
		{"ui/*", "ui/nested/panel.go", false},
		{"**/db/**", "db", true},
		{"**/db/**", "example.com/demo/db", true},
		{"**/db/**", "example.com/other/dbx", false},
		{"**", "anything/at/all", true},
		{"encoding/json", "encoding/json", true},
		{"encoding/*", "encoding/json", true},
		{"encoding/*", "encoding/json/sub", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*.go", "panel.go", true},
		{"*.go", "ui/panel.go", false},
	}
	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.name); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}
