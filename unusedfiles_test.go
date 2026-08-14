package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// setupModule creates a module with a main package importing pkgA, an
// imported package pkgA, and an orphan package pkgB.
func setupModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.21\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n\nimport \"example.com/demo/pkgA\"\n\nfunc main() { _ = pkgA.A }\n")
	writeFile(t, filepath.Join(root, "pkgA", "a.go"), "package pkgA\n\nconst A = 1\n")
	writeFile(t, filepath.Join(root, "pkgB", "b.go"), "package pkgB\n\nconst B = 1\n")
	return root
}

func TestRun_UnusedFilesViolations(t *testing.T) {
	root := setupModule(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-files"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "pkgB: package pkgB is never imported by any analyzed package") {
		t.Errorf("output missing pkgB violation:\n%s", got)
	}
	if !strings.Contains(got, "1/2 packages are never imported") {
		t.Errorf("output missing summary:\n%s", got)
	}
	for _, used := range []string{"pkgA:", "main"} {
		if strings.Contains(got, used) {
			t.Errorf("imported or main package flagged:\n%s", got)
		}
	}
}

func TestRun_UnusedFilesClean(t *testing.T) {
	root := setupModule(t)
	writeFile(t, filepath.Join(root, "main.go"),
		"package main\n\nimport (\n\t\"example.com/demo/pkgA\"\n\t\"example.com/demo/pkgB\"\n)\n\nfunc main() { _ = pkgA.A; _ = pkgB.B }\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-files"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "2 packages are imported by analyzed code"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_UnusedFilesMainPackagesNeverFlagged(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.21\n")
	writeFile(t, filepath.Join(root, "cmd", "tool", "main.go"), "package main\n\nfunc main() {}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-files"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if strings.Contains(out.String(), "never imported") {
		t.Errorf("main package flagged:\n%s", out.String())
	}
}

func TestRun_UnusedFilesSkipsPartialSelection(t *testing.T) {
	root := setupModule(t)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-files", filepath.Join(root, "pkgB")}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "unused-files: not meaningful for a partial selection"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "never imported") {
		t.Errorf("partial selection reported violations:\n%s", out.String())
	}
}

func TestResolveImportDir(t *testing.T) {
	tests := []struct {
		path, module, want string
	}{
		{"example.com/demo", "example.com/demo", "."},
		{"example.com/demo/pkgA", "example.com/demo", "pkgA"},
		{"example.com/demo/nested/pkg", "example.com/demo", "nested/pkg"},
		{"example.com/other/pkg", "example.com/demo", ""},
		{"os", "example.com/demo", ""},
		{"example.com/demoevil", "example.com/demo", ""},
		{"anything", "", ""},
	}
	for _, tt := range tests {
		if got := resolveImportDir(tt.path, tt.module); got != tt.want {
			t.Errorf("resolveImportDir(%q, %q) = %q, want %q", tt.path, tt.module, got, tt.want)
		}
	}
}
