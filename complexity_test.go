package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseBody parses src as package main and returns the *ast.BlockStmt of the
// function named name. Fails the test if not found.
func parseBody(t *testing.T, src, name string) *ast.BlockStmt {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != name {
			continue
		}
		return fd.Body
	}
	t.Fatalf("function %q not found", name)
	return nil
}

func TestComplexity_Base(t *testing.T) {
	body := parseBody(t, `package main
func base() { x := 1; _ = x }`, "base")
	if got := ComplexityOf(body, true); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestComplexity_IfAndFor(t *testing.T) {
	src := `package main
func f(n int) int {
	if n > 0 { return 1 }
	for i := 0; i < n; i++ { return i }
	return 0
}`
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 3 {
		t.Fatalf("got %d, want 3 (base + if + for)", got)
	}
}

func TestComplexity_RangeSwitch(t *testing.T) {
	src := `package main
func f(s []int) int {
	sum := 0
	for _, v := range s { sum += v }
	switch sum {
	case 0:
		return 0
	case 1:
		return 1
	default:
		return -1
	}
}`
	// base 1 + for-range 1 + 3 case clauses = 5
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestComplexity_Select(t *testing.T) {
	src := "package main\n" + `func f(c chan int) int {
	select {
	case x := <-c:
		return x
	default:
		return 0
	}
}`
	// base 1 + 2 comm clauses = 3
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestComplexity_LogicalOps(t *testing.T) {
	src := `package main
func f(a, b, c bool) bool {
	return a && b || c
}`
	// base 1 + 1 (&&) + 1 (||) = 3
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestComplexity_AnonFuncLiteral(t *testing.T) {
	src := `package main
func f(n int) int {
	add := func(a, b int) int { if b > 0 { return a + b }; return a }
	return add(n, 1)
}`
	// base 1 + if inside FuncLit (descended) 1 = 2
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 2 {
		t.Fatalf("got %d, want 2 (FuncLit branches counted)", got)
	}
}

func TestComplexity_Combined(t *testing.T) {
	src := `package main
func f(n int, s []int) int {
	if n <= 0 {
		return 0
	}
	for i := 0; i < n; i++ {
		if i%2 == 0 && i > 1 {
			continue
		}
	}
	for _, v := range s {
		_ = v
	}
	switch n {
	case 1, 2:
		return 1
	default:
		return 2
	}
}`
	// base 1
	// + if (n<=0)               = 2
	// + for (i loop)            = 3
	// + if (i%2)                = 4
	// + &&                      = 5
	// + for-range (s)           = 6
	// + case (1,2) + default    = 8
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 8 {
		t.Fatalf("got %d, want 8", got)
	}
}

func TestComplexity_NilBody(t *testing.T) {
	if got := ComplexityOf(nil, true); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestComplexity_TypeSwitchCases(t *testing.T) {
	// Upstream's switch blind-spot fix maps to Go's type switches: every
	// case clause counts, default included, same as expression switches.
	src := `package main
func f(x interface{}) string {
	switch v := x.(type) {
	case int:
		return "int"
	case string:
		return "string"
	default:
		return "other"
	}
}`
	// base 1 + 3 type-switch clauses
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 4 {
		t.Fatalf("got %d, want 4", got)
	}
}

func TestComplexity_CaseGuardLogicalOps(t *testing.T) {
	// ||/&& inside case expressions count (the Go analog of upstream's
	// ||/&& pattern counting in switch members).
	src := `package main
func f(n int) string {
	switch {
	case n < 0 || n > 100:
		return "out"
	case n >= 0 && n < 10:
		return "small"
	}
	return "mid"
}`
	// base 1 + 2 cases + || + &&
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestComplexity_CountLambdasFlag(t *testing.T) {
	src := `package main
func f(a bool) {
	g := func(x bool) {
		if x {
			println("y")
		}
	}
	g(a)
}`
	if got := ComplexityOf(parseBody(t, src, "f"), true); got != 2 {
		t.Fatalf("count-lambdas: got %d, want 2 (base + if inside literal)", got)
	}
	if got := ComplexityOf(parseBody(t, src, "f"), false); got != 1 {
		t.Fatalf("no-lambdas: got %d, want 1", got)
	}
}
