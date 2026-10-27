package main

import (
	"bytes"
	"testing"
)

// The three-violation-line fixture: one line of Go violating three
// ignorable gates at once — magic-constants (hex color outside a const),
// unused-code (dead unexported var) and scattered-constants (the value
// already has a named constant in brand.go, so the bare literal is a
// regression).
const (
	ignoreTripleLine = "package p\n\nvar dead = 0xFF00AA\n"
	ignoreBrandDecl  = "package p\n\nconst Brand = 0xFF00AA\n"
)

// writeIgnoreFiles drops files into a fresh project root.
func writeIgnoreFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		writeFile(t, root+"/"+name, src)
	}
	return root
}

// runGateExit runs one gate subcommand and returns its exit code.
func runGateExit(t *testing.T, root, gate string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{gate}, root, &out, &errOut)
	return code, out.String()
}

// TestIgnore_GranularMarkerSilencesOnlyItsGate is the granular guarantee
// in its strongest shape: one marker on a three-violation line suppresses
// exactly its rule — the other two still fail their gates.
func TestIgnore_GranularMarkerSilencesOnlyItsGate(t *testing.T) {
	root := writeIgnoreFiles(t, map[string]string{
		"a.go":     "package p\n\nvar dead = 0xFF00AA // ignore:magic-constants\n",
		"brand.go": ignoreBrandDecl,
	})
	if code, out := runGateExit(t, root, "magic-constants"); code != 0 {
		t.Fatalf("magic-constants exit = %d, want 0 (the named rule is suppressed):\n%s", code, out)
	}
	if code, out := runGateExit(t, root, "unused-code"); code != 2 {
		t.Fatalf("unused-code exit = %d, want 2 (the unnamed rule still fires):\n%s", code, out)
	}
	if code, out := runGateExit(t, root, "scattered-constants"); code != 2 {
		t.Fatalf("scattered-constants exit = %d, want 2 (the third rule still fires too):\n%s", code, out)
	}
}

// TestIgnore_StackedMarkersAboveApplyWalkStops: a run of whole-line
// ignore markers directly above the code all apply to it; the first
// non-marker line stops the walk.
func TestIgnore_StackedMarkersAboveApplyWalkStops(t *testing.T) {
	stacked := "package p\n// ignore:magic-constants\n// ignore:unused-code\nvar dead = 0xFF00AA\n"
	root := writeIgnoreFiles(t, map[string]string{"a.go": stacked})
	if code, out := runGateExit(t, root, "magic-constants"); code != 0 {
		t.Fatalf("magic-constants exit = %d, want 0 (stacked markers apply):\n%s", code, out)
	}
	if code, out := runGateExit(t, root, "unused-code"); code != 0 {
		t.Fatalf("unused-code exit = %d, want 0 (stacked markers apply):\n%s", code, out)
	}

	blocked := "package p\n// ignore:magic-constants\n// an unrelated comment\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": blocked})
	if code, out := runGateExit(t, root, "magic-constants"); code != 2 {
		t.Fatalf("magic-constants exit = %d, want 2 (the walk stops at the non-marker line):\n%s", code, out)
	}
}

// TestIgnore_FileScopeFirstFiveLines: file-scoped granular markers in the
// first 5 lines — separate markers union, an empty id is blanket, line 5
// still counts and line 6 does not.
func TestIgnore_FileScopeFirstFiveLines(t *testing.T) {
	union := "// ignore-file:magic-constants\n// ignore-file:unused-code\n\npackage p\nvar dead = 0xFF00AA\n"
	root := writeIgnoreFiles(t, map[string]string{"a.go": union})
	if code, out := runGateExit(t, root, "magic-constants"); code != 0 {
		t.Fatalf("magic-constants exit = %d, want 0 (file marker unions):\n%s", code, out)
	}
	if code, out := runGateExit(t, root, "unused-code"); code != 0 {
		t.Fatalf("unused-code exit = %d, want 0 (file marker unions):\n%s", code, out)
	}

	blanket := "// ignore-file:\n\npackage p\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": blanket})
	for _, gate := range []string{"magic-constants", "unused-code"} {
		if code, out := runGateExit(t, root, gate); code != 0 {
			t.Fatalf("%s exit = %d, want 0 (empty id is blanket):\n%s", gate, code, out)
		}
	}

	onFifth := "package p\n// filler\n// filler\n// filler\n// ignore-file:magic-constants\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": onFifth})
	if code, out := runGateExit(t, root, "magic-constants"); code != 0 {
		t.Fatalf("magic-constants exit = %d, want 0 (line 5 still counts):\n%s", code, out)
	}

	onSixth := "package p\n// filler\n// filler\n// filler\n// filler\n// ignore-file:magic-constants\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": onSixth})
	if code, out := runGateExit(t, root, "magic-constants"); code != 2 {
		t.Fatalf("magic-constants exit = %d, want 2 (line 6 is out of scope):\n%s", code, out)
	}
}

// TestIgnore_LegacyParity: the blanket legacy markers keep suppressing
// every ignorable gate; a line-scoped `// crap:ignore` in the leading
// lines gives no file scope (only `crap:ignore-file` does).
func TestIgnore_LegacyParity(t *testing.T) {
	legacyLine := "package p\n\nvar dead = 0xFF00AA // crap:ignore\n"
	root := writeIgnoreFiles(t, map[string]string{"a.go": legacyLine})
	for _, gate := range []string{"magic-constants", "unused-code"} {
		if code, out := runGateExit(t, root, gate); code != 0 {
			t.Fatalf("%s exit = %d, want 0 (legacy blanket line marker):\n%s", gate, code, out)
		}
	}

	legacyFile := "// crap:ignore-file\n\npackage p\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": legacyFile})
	for _, gate := range []string{"magic-constants", "unused-code"} {
		if code, out := runGateExit(t, root, gate); code != 0 {
			t.Fatalf("%s exit = %d, want 0 (legacy blanket file marker):\n%s", gate, code, out)
		}
	}

	noFileScope := "// crap:ignore\n\npackage p\nvar dead = 0xFF00AA\n"
	root = writeIgnoreFiles(t, map[string]string{"a.go": noFileScope})
	if code, out := runGateExit(t, root, "magic-constants"); code != 2 {
		t.Fatalf("magic-constants exit = %d, want 2 (line marker is not file scope):\n%s", code, out)
	}
}

// TestIgnore_CommaInertUnknownNoop: a comma list is not a list — it names
// nothing; an unknown id matches nothing. Neither suppresses.
func TestIgnore_CommaInertUnknownNoop(t *testing.T) {
	for _, marker := range []string{
		"// ignore:magic-constants,unused-code",
		"// ignore:magic-constants-typo",
	} {
		src := "package p\n\nvar dead = 0xFF00AA " + marker + "\n"
		root := writeIgnoreFiles(t, map[string]string{"a.go": src})
		if code, out := runGateExit(t, root, "magic-constants"); code != 2 {
			t.Fatalf("%s: magic-constants exit = %d, want 2 (marker is inert):\n%s", marker, code, out)
		}
	}
}

// TestIgnore_LinelessViolationsFileScopeOnly: file-naming violations carry
// no line, so only file-scoped markers suppress them.
func TestIgnore_LinelessViolationsFileScopeOnly(t *testing.T) {
	root := writeIgnoreFiles(t, map[string]string{
		"utils.go": "// ignore-file:file-naming\npackage p\n\nfunc a() {}\n",
	})
	if code, out := runGateExit(t, root, "file-naming"); code != 0 {
		t.Fatalf("file-naming exit = %d, want 0 (file marker suppresses):\n%s", code, out)
	}

	root = writeIgnoreFiles(t, map[string]string{
		"utils.go": "// ignore:file-naming\npackage p\n\nfunc a() {}\n",
	})
	if code, out := runGateExit(t, root, "file-naming"); code != 2 {
		t.Fatalf("file-naming exit = %d, want 2 (line markers cannot reach a line-less violation):\n%s", code, out)
	}
}

// ignoreTestViolation is a minimal ignoreAware stand-in for the opt-in and
// unreadable-file unit checks.
type ignoreTestViolation struct {
	path string
	line int
}

func (v ignoreTestViolation) ignorePath() string { return v.path }
func (v ignoreTestViolation) ignoreLine() int    { return v.line }

// TestIgnore_OptInRegistry: suppression is opt-in per gate — a gate
// outside ignorableGates is never filtered, even by blanket markers, and
// violations in unreadable files are never suppressed.
func TestIgnore_OptInRegistry(t *testing.T) {
	root := writeIgnoreFiles(t, map[string]string{
		"a.go": "package p\n// ignore:magic-constants\nvar dead = 0xFF00AA\n",
	})
	viols := []ignoreTestViolation{{"a.go", 3}}
	if kept := filterIgnored("folder-structure", root, viols); len(kept) != 1 {
		t.Fatalf("non-ignorable gate: kept = %d, want 1 (no opt-in, no suppression)", len(kept))
	}
	kept := filterIgnored("magic-constants", root, viols)
	if len(kept) != 0 {
		t.Fatalf("ignorable gate: kept = %d, want 0 (marker names the gate)", len(kept))
	}
	mixed := []ignoreTestViolation{{"missing.go", 1}, {"a.go", 3}}
	kept = filterIgnored("magic-constants", root, mixed)
	if len(kept) != 1 || kept[0].path != "missing.go" {
		t.Fatalf("kept = %v, want only the unreadable file's violation", kept)
	}
}
