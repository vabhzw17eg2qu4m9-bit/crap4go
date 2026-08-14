package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// manyMethods builds a method-per-type source string with count methods of
// the given body.
func manyMethods(count int, body string) string {
	return methodsFrom(0, count, body)
}

// methodsFrom builds a source string with count methods named M<start> ...
// named uniquely across files of the same package.
func methodsFrom(start, count int, body string) string {
	var b strings.Builder
	b.WriteString("package p\n\ntype T int\n")
	for i := start; i < start+count; i++ {
		fmt.Fprintf(&b, "func (t T) M%d() {\n%s\n}\n", i, body)
	}
	return b.String()
}

// wmcBody is a method body with cyclomatic complexity 21 (1 + 20 ifs).
const wmcBody = `if c1 {} else if c2 {} else if c3 {} else if c4 {} else if c5 {}
	if c6 {} else if c7 {} else if c8 {} else if c9 {} else if c10 {}
	if c11 {} else if c12 {} else if c13 {} else if c14 {} else if c15 {}
	if c16 {} else if c17 {} else if c18 {} else if c19 {} else if c20 {}`

func TestRun_ClassSizeTooManyMethods(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/big.go", manyMethods(26, ""))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"class-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "big.go:4: T has 26 methods > max 25") {
		t.Errorf("output missing method-count violation:\n%s", got)
	}
	if strings.Contains(got, "WMC=") {
		t.Errorf("WMC violation reported for WMC 26 <= 80:\n%s", got)
	}
}

func TestRun_ClassSizeWmcOnly(t *testing.T) {
	root := t.TempDir()
	// 4 methods x complexity 21 = WMC 84 > 80, method count 4 <= 25.
	writeFile(t, root+"/complex.go", manyMethods(4, wmcBody))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"class-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "T WMC=84 > max 80") {
		t.Errorf("output missing WMC violation:\n%s", got)
	}
	if strings.Contains(got, "methods > max") {
		t.Errorf("method-count violation reported for 4 methods:\n%s", got)
	}
}

func TestRun_ClassSizeMethodsGatheredAcrossFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a.go", manyMethods(15, ""))
	writeFile(t, root+"/b.go", methodsFrom(15, 15, ""))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"class-size"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "T has 30 methods > max 25") {
		t.Errorf("methods not gathered across files:\n%s", out.String())
	}
}

func TestRun_ClassSizeClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/small.go", manyMethods(3, ""))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"class-size"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 types within 25 methods/WMC 80"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}
