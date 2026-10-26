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

func TestRun_FolderStructureSubtreeNeverCounts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/keep.go", "package p\n") // the parent's own single unit
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		writeFile(t, root+"/deep/nested/"+name, "package deep\n") // organized form
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--max-loose-files", "1"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (parent never pays for its subtree; stderr=%s)", code, errOut.String())
	}
	if want := "1 directories organized into packages"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_FolderStructureStemCollapse(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"api.go", "api.pb.go", "api.pb.gw.go", "api_string.go",
		"zz_generated.deepcopy.go", "zz_generated.effects.go",
		"other.go",
	} {
		writeFile(t, root+"/gen/"+name, "package gen\n")
	}
	// 7 files collapse into 3 units: api, zz_generated, other.
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--max-loose-files", "3", "./gen"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (generated siblings collapse; stderr=%s)", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = runWithRoot([]string{"folder-structure", "--max-loose-files", "2", "./gen"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (3 units over cap 1)", code)
	}
	if !strings.Contains(out.String(), "3 loose .go files directly in gen") {
		t.Errorf("output missing collapsed unit count:\n%s", out.String())
	}
}

func TestRun_FolderStructureGlobDirsReachNested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/pkg/one/one.go", "package one\n")
	writeFile(t, root+"/pkg/one/sub/two.go", "package sub\n") // nested: not matched by pkg/*
	writeFile(t, root+"/pkg/two/three.go", "package two\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--cap", "pkg/*=0"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"pkg/one: 1 loose .go files directly in pkg/one — group them into feature packages (max 0)",
		"pkg/two: 1 loose .go files directly in pkg/two",
		"2 directory(ies) with loose-file sprawl",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "pkg/one/sub") {
		t.Errorf("single-star glob crossed a path segment:\n%s", got)
	}
}

func TestRun_FolderStructureGlobSkipsHiddenAndTooling(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/.git/config.go", "package git\n")
	writeFile(t, root+"/.hidden/h.go", "package h\n")
	writeFile(t, root+"/vendor/v/v.go", "package v\n")
	writeFile(t, root+"/build/b.go", "package b\n")
	writeFile(t, root+"/real/r.go", "package r\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--cap", "*=0"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "real: 1 loose .go files") {
		t.Errorf("output missing real violation:\n%s", got)
	}
	for _, skipped := range []string{".git", ".hidden", "vendor", "build"} {
		if strings.Contains(got, skipped) {
			t.Errorf("hidden/tooling dir %s enumerated:\n%s", skipped, got)
		}
	}
}

func TestRun_FolderStructureCapsFirstWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/pkg/one/one.go", "package one\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--cap", "pkg/*=5", "--cap", "pkg/*=0"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (first pattern's cap applies; stderr=%s)", code, errOut.String())
	}
	out.Reset()
	code = runWithRoot([]string{"folder-structure", "--cap", "pkg/*=0", "--cap", "pkg/*=5"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (first pattern's cap applies)", code)
	}
	if !strings.Contains(out.String(), "(max 0)") {
		t.Errorf("output missing first pattern's cap:\n%s", out.String())
	}
}

func TestRun_FolderStructureCapsIgnorePositionalDirsAndMax(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/only/a.go", "package only\n")
	writeFile(t, root+"/only/b.go", "package only\n")
	writeFile(t, root+"/other/x.go", "package other\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--cap", "on*=0", "--max-loose-files", "99", "./other"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "only: 2 loose .go files directly in only — group them into feature packages (max 0)") {
		t.Errorf("caps form ignored or --max-loose-files leaked in:\n%s", got)
	}
	if strings.Contains(got, "other") {
		t.Errorf("positional dir consulted in caps form:\n%s", got)
	}
}

func TestRun_FolderStructureExcludeDropsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/d/keep.go", "package d\n")
	writeFile(t, root+"/d/skipme.go", "package d\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"folder-structure", "--exclude", "**/skipme.go", "./d"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "1 loose .go files directly in d") {
		t.Errorf("excluded file still counted:\n%s", out.String())
	}
}

func TestRun_FolderStructureCapUsageErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main.go", "package p\n")
	tests := []struct {
		name string
		args []string
	}{
		{"missing =N", []string{"folder-structure", "--cap", "pkg/*"}},
		{"non-integer cap", []string{"folder-structure", "--cap", "pkg/*=many"}},
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
