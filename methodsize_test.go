package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// makeFuncOfLines builds a file whose function's decl span covers exactly n
// lines: the func keyword line, n-2 body lines, closing brace.
func makeFuncOfLines(name string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package p\n\nfunc %s() {\n", name)
	for range n - 2 {
		b.WriteString("\t_ = 0\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// makeParamsFunc builds a file declaring a function with n parameters.
func makeParamsFunc(name string, n int) string {
	params := make([]string, n)
	for i := range params {
		params[i] = fmt.Sprintf("a%d int", i)
	}
	return fmt.Sprintf("package p\n\nfunc %s(%s) {}\n", name, strings.Join(params, ", "))
}

func TestRun_MethodSizeViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/long.go", makeFuncOfLines("F", 61))
	writeFile(t, root+"/params.go", makeParamsFunc("F", 7))
	writeFile(t, root+"/fine.go", makeParamsFunc("F", 6))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"long.go:3: F has 61 lines > max 60",
		"params.go:3: F has 7 params > max 6",
		"2 violations in 3 methods over their size limits",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fine.go") {
		t.Errorf("function within limits flagged:\n%s", got)
	}
}

func TestRun_MethodSizeBoundaryPasses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/exact.go", makeFuncOfLines("F", 60))
	writeFile(t, root+"/params.go", makeParamsFunc("F", 6))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "2 methods within their size limits"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_MethodSizeFlags(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/f.go", makeFuncOfLines("F", 10))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size", "--max-lines", "5"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if want := "f.go:3: F has 10 lines > max 5"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_MethodSizeConstructorParamsOnly(t *testing.T) {
	// New* constructors are exempt from the line limit but still
	// parameter-checked (upstream constructor rule, Go New* convention).
	root := t.TempDir()
	writeFile(t, root+"/ctor.go", makeFuncOfLines("NewThing", 61))
	writeFile(t, root+"/ctorparams.go", makeParamsFunc("NewThing", 7))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (output=%s)", code, out.String())
	}
	got := out.String()
	if strings.Contains(got, "ctor.go") {
		t.Errorf("New* constructor flagged for lines:\n%s", got)
	}
	if want := "ctorparams.go:3: NewThing has 7 params > max 6"; !strings.Contains(got, want) {
		t.Errorf("output missing %q:\n%s", want, got)
	}
}

func TestRun_MethodSizeMethods(t *testing.T) {
	// Receiver-bearing declarations report as (Type)Name.
	root := t.TempDir()
	writeFile(t, root+"/m.go", "package p\n\ntype T struct{}\n\nfunc (t T) M(a, b, c, d, e, f, g int) {}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if want := "m.go:5: (T)M has 7 params > max 6"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_MethodSizeIgnoreMarker(t *testing.T) {
	// The gate id is ignorable: a line marker suppresses the violation.
	root := t.TempDir()
	writeFile(t, root+"/m.go", "package p\n\ntype T struct{}\n\n// ignore:method-size\nfunc (t T) M(a, b, c, d, e, f, g int) {}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s)", code, out.String())
	}
}

func TestRun_MethodSizeUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"method-size", "--max-params", "x"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
