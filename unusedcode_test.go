package main

import (
	"bytes"
	"strings"
	"testing"
)

const unusedCodeFixture = `package p

func helperUsed() int { return 1 }

func helperUnused() int { return 2 }

type widget struct{}

const magicUnused = 3

var cacheUnused map[string]int

var _ = 4 // blank identifier is never a candidate

func UseIt() int { return helperUsed() }
`

func TestRun_UnusedCodeViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/decls.go", unusedCodeFixture)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-code"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"decls.go:5: helperUnused is never referenced",
		"decls.go:7: widget is never referenced",
		"decls.go:9: magicUnused is never referenced",
		"decls.go:11: cacheUnused is never referenced",
		"4/5 unexported declarations unused",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "helperUsed is never referenced") {
		t.Errorf("referenced declaration flagged:\n%s", got)
	}
}

func TestRun_UnusedCodeReferenceInSamePackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/decl.go", "package p\n\nfunc helper() int { return 1 }\n")
	writeFile(t, root+"/use.go", "package p\n\nfunc Use() int { return helper() }\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-code"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 unexported declarations used"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

// TestRun_UnusedCodeCrossClassPrivateAccess is the 0.7.1 regression: a
// private declaration referenced from a method on another type (here, in a
// different file of the same package) must not be flagged — declaring a
// private declaration must not strip its name from the reference set.
func TestRun_UnusedCodeCrossClassPrivateAccess(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/calc.go", "package p\n\nfunc calc(n int) int { return n * 2 }\n")
	writeFile(t, root+"/widget.go", "package p\n\ntype widget struct{}\n\nfunc (w widget) Render() int { return calc(21) }\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-code"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if strings.Contains(out.String(), "calc is never referenced") {
		t.Errorf("cross-class private access flagged:\n%s", out.String())
	}
}

func TestRun_UnusedCodeSkipsPartialSelection(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/decl.go", unusedCodeFixture)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-code", root + "/decl.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "unused-code: not meaningful for a partial selection"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "never referenced") {
		t.Errorf("partial selection reported violations:\n%s", out.String())
	}
}

func TestRun_UnusedCodeNoFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"unused-code"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}
