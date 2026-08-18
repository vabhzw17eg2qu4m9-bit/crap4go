package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_FolderStructureViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main.go", "package p\n")
	writeFile(t, root+"/util.go", "package p\n")
	writeFile(t, root+"/main_test.go", "package p\n") // test files never count
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		".: 2 loose .go files directly in . — group them into feature packages (max 0)",
		"1 directory(ies) with loose-file sprawl",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "main_test.go") {
		t.Errorf("test file counted as loose:\n%s", got)
	}
}

func TestRun_FolderStructureClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/go.mod", "module example.com/p\n\ngo 1.22\n")
	writeFile(t, root+"/feature/parser.go", "package feature\n")
	writeFile(t, root+"/feature/parser_test.go", "package feature\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 directories organized into packages"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_FolderStructureExplicitDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main.go", "package p\n")    // outside selection
	writeFile(t, root+"/sub/a.go", "package sub\n") // inside selection
	writeFile(t, root+"/sub/b.go", "package sub\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "./sub"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "sub: 2 loose .go files directly in sub — group them into feature packages (max 0)") {
		t.Errorf("output missing sub violation:\n%s", got)
	}
	if strings.Contains(got, " 1 loose .go files directly in .") {
		t.Errorf("directory outside selection checked:\n%s", got)
	}
}

func TestRun_FolderStructureUsageErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main.go", "package p\n")
	tests := []struct {
		name string
		args []string
	}{
		{"nonexistent dir", []string{"folder-structure", "/nonexistent/dir"}},
		{"file instead of dir", []string{"folder-structure", "./main.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runWithRoot(tt.args, root, &out, &errOut); code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr=%s)", code, errOut.String())
			}
		})
	}
}
