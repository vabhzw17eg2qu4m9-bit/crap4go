package main

import (
	"bytes"
	"strings"
	"testing"
)

func runAnonStructSignatures(t *testing.T, files map[string]string, args ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, root+"/"+name, content)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot(append([]string{"anon-struct-signatures"}, args...), root, &out, &errOut)
	return out.String(), code
}

// TestRun_AnonStructFlagged kills the nested-composite mutation: the walk
// must descend into slice/map/chan/pointer and generic type arguments, not
// only check a parameter's direct type.
func TestRun_AnonStructFlagged(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"shapes.go": `package p

func load() map[string][]struct{ X int } { return nil }

func box(b Box[struct{ Name string }]) bool { return true }
`})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr within run)", code)
	}
	if !strings.Contains(out, "shapes.go:3: anonymous struct type in signature") {
		t.Errorf("output missing nested map/slice violation:\n%s", out)
	}
	if !strings.Contains(out, "shapes.go:5: anonymous struct type in signature") {
		t.Errorf("output missing generic type-argument violation:\n%s", out)
	}
}

// TestRun_MaxFieldsBoundary kills the off-by-one mutation (>= instead of >):
// at --max-fields 2 a two-field shape must not be called oversized while a
// three-field one must.
func TestRun_MaxFieldsBoundary(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"pair.go": `package p

func pair(p struct{ A, B int }) int { return p.A + p.B }

func triple(p struct{ A, B, C int }) int { return p.A + p.B + p.C }
`}, "--max-fields", "2")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "pair.go:3: anonymous struct type in signature: declare") {
		t.Errorf("two-field shape should be a plain anonymous-shape finding:\n%s", out)
	}
	if !strings.Contains(out, "pair.go:5: anonymous struct type with 3 fields (max 2)") {
		t.Errorf("three-field shape should be flagged oversized against max 2:\n%s", out)
	}
}

// TestRun_AnonStructDefaultCap pins the default field cap: 5 fields pass the
// cap wording, 6 do not.
func TestRun_AnonStructDefaultCap(t *testing.T) {
	out, _ := runAnonStructSignatures(t, map[string]string{"wide.go": `package p

func wide(p struct{ A, B, C, D, E, F int }) int { return p.A }
`})
	if !strings.Contains(out, "wide.go:3: anonymous struct type with 6 fields (max 5)") {
		t.Errorf("default cap 5 should flag a 6-field shape:\n%s", out)
	}
}

// TestRun_NamedTypesBlessed pins the blessed form: named struct types
// referenced by name in signatures are never flagged.
func TestRun_NamedTypesBlessed(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"named.go": `package p

type Point struct{ X, Y int }

func dist(a, b Point) float64 { return 0 }

type Handler func(req Point) (Point, error)
`})
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "2 signatures checked") {
		t.Errorf("output should count the two clean signatures:\n%s", out)
	}
}

// TestRun_UnnamedMultipleResultsLegal pins Go's core idiom: unnamed multiple
// results are positional in shape but are plain named types, not anonymous
// structs — never flagged, even at a tiny field cap.
func TestRun_UnnamedMultipleResultsLegal(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"results.go": `package p

type Point struct{ X, Y int }

func div(a, b int) (int, int) { return a / b, a % b }

func lookup(k string) (Point, bool) { return Point{}, false }
`}, "--max-fields", "1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out)
	}
}

// TestRun_AnonStructValuesLegal pins that anonymous struct VALUES in bodies
// stay legal — only signature shapes are flagged.
func TestRun_AnonStructValuesLegal(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"values.go": `package p

func f() int {
	v := struct{ X int }{X: 1}
	return v.X
}
`})
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "1 signatures checked") {
		t.Errorf("output should count only the function's signature:\n%s", out)
	}
}

// TestRun_FuncTypeAliasBlessesNothing ports upstream's "a function-type
// typedef blesses nothing": an anonymous struct hiding behind a named
// function type is still flagged.
func TestRun_FuncTypeAliasBlessesNothing(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"alias.go": `package p

type Handler func() struct{ OK bool }
`})
	if code != 2 {
		t.Fatalf("exit = %d, want 2:\n%s", code, out)
	}
	if !strings.Contains(out, "alias.go:3: anonymous struct type in signature") {
		t.Errorf("output missing function-type alias violation:\n%s", out)
	}
}

// TestRun_FuncLitAndMethodScannedOnce pins that each signature node is
// scanned exactly once: two anonymous shapes across three signatures (the
// clean enclosing function counts too) give two findings — a double scan of
// any declaration would report more.
func TestRun_FuncLitAndMethodScannedOnce(t *testing.T) {
	out, code := runAnonStructSignatures(t, map[string]string{"multi.go": `package p

type T struct{}

func (T) m(p struct{ A int }) int { return p.A }

func outer() int {
	f := func(q struct{ B int }) int { return q.B }
	return f(nil)
}
`})
	if code != 2 {
		t.Fatalf("exit = %d, want 2:\n%s", code, out)
	}
	if got := strings.Count(out, "multi.go:"); got != 2 {
		t.Errorf("finding count = %d, want 2 (method and literal, once each):\n%s", got, out)
	}
	if !strings.Contains(out, "2 anonymous struct types in 3 signatures") {
		t.Errorf("output should count all three signatures once:\n%s", out)
	}
}

// TestRun_AnonStructOutermostOnly pins upstream's outermost-only rule: an
// anonymous struct inside a flagged anonymous struct is fixed by the same
// extraction and not reported separately.
func TestRun_AnonStructOutermostOnly(t *testing.T) {
	out, _ := runAnonStructSignatures(t, map[string]string{"nested.go": `package p

func f(p struct{ Inner struct{ B int } }) int { return 0 }
`})
	if got := strings.Count(out, "nested.go:"); got != 1 {
		t.Errorf("violation count = %d, want 1 (outermost only):\n%s", got, out)
	}
}

func TestRun_AnonStructNoFiles(t *testing.T) {
	out, code := runAnonStructSignatures(t, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestRun_AnonStructUsageErrors(t *testing.T) {
	var errOut bytes.Buffer
	if code := runWithRoot([]string{"anon-struct-signatures", "--max-fields", "0"}, ".", &bytes.Buffer{}, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1 for --max-fields 0", code)
	}
	if code := runWithRoot([]string{"anon-struct-signatures", "--nope"}, ".", &bytes.Buffer{}, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1 for unknown flag", code)
	}
}
