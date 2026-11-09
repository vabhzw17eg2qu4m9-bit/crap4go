package main

import (
	"bytes"
	"strings"
	"testing"
)

// undocumentedFunc is an exported function without a doc comment.
const undocumentedFunc = "package p\n\nfunc Exported() {}\n"

// documentedFunc carries a godoc comment directly above the declaration.
const documentedFunc = "package p\n\n// Exported does nothing.\nfunc Exported() {}\n"

func TestRun_PublicDocsViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/nodoc.go", "package p\n\nfunc Exported() {}\n\ntype ExportedType struct{}\n\nvar ExportedVar = 1\n\nconst ExportedConst = 2\n\nfunc (t T) ExportedMethod() {}\n\nfunc (d *ExportedType) PtrMethod() {}\n\ntype T struct{}\n")
	writeFile(t, root+"/docs.go", documentedFunc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		`nodoc.go:3: missing godoc on func "Exported"`,
		`nodoc.go:5: missing godoc on type "ExportedType"`,
		`nodoc.go:7: missing godoc on var "ExportedVar"`,
		`nodoc.go:9: missing godoc on const "ExportedConst"`,
		`nodoc.go:11: missing godoc on method "(T)ExportedMethod"`,
		`nodoc.go:13: missing godoc on method "(ExportedType)PtrMethod"`,
		`nodoc.go:15: missing godoc on type "T"`,
		"7/8 public declarations missing docs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_PublicDocsClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/docs.go", documentedFunc+"\n// A type.\ntype ExportedType struct{}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "2 public declarations documented"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_PublicDocsUnexportedSkipped(t *testing.T) {
	// Unexported declarations, methods on unexported receivers, and
	// unexported names inside value groups are not public API.
	root := t.TempDir()
	writeFile(t, root+"/internal.go", "package p\n\nfunc helper() {}\n\ntype helper struct{}\n\nfunc (h helper) Method() {}\n\nfunc (h *helper) Ptr() {}\n\nvar (\n\tshadowed = 1\n)\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s)", code, out.String())
	}
	if want := "0 public declarations documented"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_PublicDocsConstructorsExempt(t *testing.T) {
	// New* constructors are exempt (upstream constructor rule), and a
	// grouped decl's doc comment covers every spec in the group.
	root := t.TempDir()
	writeFile(t, root+"/ctor.go", "package p\n\n// Thing is a documented thing.\ntype Thing struct{}\n\n// New builds a Thing.\nfunc NewThing() *Thing { return nil }\n\n// Grouped names.\nconst (\n\tA = 1\n\tB = 2\n)\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s)", code, out.String())
	}
}

func TestRun_PublicDocsSpecDocCounts(t *testing.T) {
	// A comment directly above one spec inside a documented-elsewhere group
	// documents that spec.
	root := t.TempDir()
	writeFile(t, root+"/group.go", "package p\n\nconst (\n\t// A is first.\n\tA = 1\n\tB = 2\n)\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (output=%s)", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, `missing godoc on const "B"`) {
		t.Errorf("output missing const B violation:\n%s", got)
	}
	if strings.Contains(got, `"A"`) {
		t.Errorf("documented spec flagged:\n%s", got)
	}
}

func TestRun_PublicDocsExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/nodoc.go", undocumentedFunc)
	writeFile(t, root+"/docs.go", documentedFunc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs", "--exclude", "nodoc.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "nodoc.go") {
		t.Errorf("excluded file flagged:\n%s", out.String())
	}
}

func TestRun_PublicDocsIgnoreMarker(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/m.go", "package p\n\n// ignore:public-docs\nfunc Exported() {}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s)", code, out.String())
	}
}

func TestRun_PublicDocsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"public-docs", "--exclude"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
