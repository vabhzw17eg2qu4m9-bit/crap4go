package main

import (
	"bytes"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sortedTokens is the sorted token stream of src, comments included —
// the reorder transform must preserve it exactly (it moves code, never
// adds or drops it).
func sortedTokens(t *testing.T, src string) []string {
	t.Helper()
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("src.go", fset.Base(), len(src))
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	var out []string
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		_ = pos
		if lit != "" {
			out = append(out, lit)
		} else {
			out = append(out, tok.String())
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// expectConsistent asserts the full consistency contract for src: the
// transform is idempotent and preserves the token multiset.
func expectConsistent(t *testing.T, src string) string {
	t.Helper()
	once, err := reorderSource([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("reorderSource: %v", err)
	}
	twice, err := reorderSource([]byte(once), "fixture.go")
	if err != nil {
		t.Fatalf("reorderSource(once): %v", err)
	}
	if twice != once {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
	if got, want := strings.Join(sortedTokens(t, once), " "), strings.Join(sortedTokens(t, src), " "); got != want {
		t.Fatalf("token multiset not preserved:\nwas:  %s\nnow:  %s", want, got)
	}
	return once
}

func TestReorderCanonicalSequence(t *testing.T) {
	// go/parser rejects imports after other declarations, so every
	// parseable Go file already leads with its imports; the remaining
	// categories are what the transform orders.
	src := `package p

import "fmt"

func run() {}

// Type docs.
type T struct{}

var v = 1

const C = 1
`
	want := `package p

import "fmt"

const C = 1

var v = 1

// Type docs.
type T struct{}

func run() {}
`
	if got, err := reorderSource([]byte(src), "x.go"); err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderStableWithinCategory(t *testing.T) {
	src := `package p

func second() {}

const after = 1

func first() {}

const before = 2

func third() {}
`
	want := `package p

const after = 1

const before = 2

func second() {}

func first() {}

func third() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderCanonicalByteIdentical(t *testing.T) {
	cases := []string{
		"package p\n",                // package only
		"package p\n\nconst A = 1\n", // single declaration
		`package p

import "fmt"

const A = 1


var B = 2

type T struct{}

func f() {}
`, // canonical whatever its blank-line layout
	}
	for _, src := range cases {
		got, err := reorderSource([]byte(src), "x.go")
		if err != nil {
			t.Fatalf("reorderSource(%q): %v", src, err)
		}
		if got != src {
			t.Fatalf("canonical file changed:\nwas:\n%s\ngot:\n%s", src, got)
		}
	}
}

func TestReorderGroupedBlocksMoveWhole(t *testing.T) {
	src := `package p

func f() {}

const (
	first = iota // zero
	second       // one
	third
)

var (
	a, b = 1, 2
	c    = a + b
)
`
	want := `package p

const (
	first = iota // zero
	second       // one
	third
)

var (
	a, b = 1, 2
	c    = a + b
)

func f() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderInitRelativeOrderPreserved(t *testing.T) {
	src := `package p

func init() { order = append(order, 1) }

var order []int

func init() { order = append(order, 2) }
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Index(got, "order, 1")
	second := strings.Index(got, "order, 2")
	if first == -1 || second == -1 || first > second {
		t.Fatalf("init order not preserved:\n%s", got)
	}
	expectConsistent(t, src)
}

func TestReorderDocCommentsMoveWithDecl(t *testing.T) {
	src := `package p

// Doc for Run.
func Run() {}

// Doc for Size.
const Size = 3
`
	want := `package p

// Doc for Size.
const Size = 3

// Doc for Run.
func Run() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderOneBlankAttaches(t *testing.T) {
	src := `package p

func z() {}

// note

const a = 1
`
	want := `package p

// note

const a = 1

func z() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderTwoBlanksDetachToPreceding(t *testing.T) {
	src := `package p

func z() {}


// orphan note


const a = 1
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "// orphan note") {
		t.Fatalf("detached comment dropped:\n%s", got)
	}
	expectConsistent(t, src)
}

func TestReorderTrailingSameLineCommentRides(t *testing.T) {
	src := `package p

func f() {}

const A = 1 // alpha

type T struct{}
`
	want := `package p

const A = 1 // alpha

type T struct{}

func f() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

func TestReorderPreambleNeverEaten(t *testing.T) {
	src := `//go:build linux

// Package p does things.
package p

func f() {}

const A = 1

//go:build never

`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil {
		t.Fatal(err)
	}
	// Build tag, package doc and package clause keep their positions;
	// the trailing comment after the last declaration stays in place.
	for _, keep := range []string{
		"//go:build linux\n\n// Package p does things.\npackage p\n",
		"//go:build never\n",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("preamble damaged (%q missing):\n%s", keep, got)
		}
	}
	if !strings.HasPrefix(got, "//go:build linux\n") {
		t.Fatalf("build tag moved:\n%s", got)
	}
	if !strings.HasSuffix(got, "//go:build never\n") && !strings.HasSuffix(got, "//go:build never\n\n") {
		t.Fatalf("trailing comment after last decl changed:\n%s", got)
	}
	expectConsistent(t, src)
}

func TestReorderSameLineDecls(t *testing.T) {
	src := "package p\n\nfunc f() {}\n\nconst A = 1; const B = 2\n"
	got := expectConsistent(t, src)
	if !strings.Contains(got, "const A = 1") || !strings.Contains(got, "const B = 2") {
		t.Fatalf("same-line decls damaged:\n%s", got)
	}
	if strings.Index(got, "func f") != -1 && strings.Index(got, "func f") < strings.Index(got, "const A") {
		t.Fatalf("not reordered:\n%s", got)
	}
}

func TestReorderCRLFPreserved(t *testing.T) {
	src := "package p\r\n\r\nfunc f() {\r\n}\r\n\r\nconst A = 1\r\n"
	got := expectConsistent(t, src)
	if !strings.Contains(got, "const A = 1\r\n") {
		t.Fatalf("CRLF not preserved:\n%q", got)
	}
	if !strings.Contains(got, "\r\n\r\n") {
		t.Fatalf("joined with LF inside CRLF file:\n%q", got)
	}
	if strings.Contains(got, "1\n") {
		t.Fatalf("lone LF introduced:\n%q", got)
	}
}

func TestReorderMultiLineBlockDocMoves(t *testing.T) {
	src := `package p

func f() {}

/*
Run does the thing,
thoroughly.
*/
func Run() {}
`
	got := expectConsistent(t, src)
	if strings.Index(got, "func f() {}") != -1 && strings.Index(got, "func f() {}") > strings.Index(got, "func Run") {
		t.Fatalf("doc block not carried:\n%s", got)
	}
}

func TestReorderParseErrorsAndTinyFiles(t *testing.T) {
	for _, src := range []string{"", "// only a comment\n", "package p\nfunc broken( {\n"} {
		if _, err := reorderSource([]byte(src), "x.go"); err == nil && strings.Contains(src, "broken") {
			t.Fatalf("broken source parsed: %q", src)
		}
	}
	// Files that do parse but have fewer than two declarations are
	// returned unchanged.
	for _, src := range []string{"package p\n", "package p\n\nconst A = 1\n"} {
		got, err := reorderSource([]byte(src), "x.go")
		if err != nil || got != src {
			t.Fatalf("tiny file changed: %q -> %q (%v)", src, got, err)
		}
	}
}

// reorderFixtures is the invariant battery corpus: for every fixture the
// reorder must (1) be idempotent, (2) preserve the token multiset, and
// (3) produce a file that still parses.
var reorderFixtures = map[string]string{
	"kitchen-sink": `package main

import "fmt"

func main() { fmt.Println(label) }

var label = "l"

// The answer.
const answer = 42

type Kitchen struct{ width, height int }

func (k Kitchen) area() int { return k.width * k.height }

func newKitchen() Kitchen { return Kitchen{width: 1} }

type Painter struct{}

func (p Painter) paint(k Kitchen) {}

var global = answer

const limit = 8
`,
	"gate-shape": `package gates

func passes(v int) bool { return v <= limit }

const limit = 5

type Gate struct{ limit int }

func (g Gate) check(v int) bool { return v <= g.limit }

func newGate(limit int) Gate { return Gate{limit: limit} }
`,
	"single-decl": "package p\n\nconst A = 1\n",
	"same-line":   "package p\n\nvar b = 2; var a = 1\n",
	"comments-everywhere": `package p

// leading comment

func c() {}

// before d
func d() {}

/* block */
var b = 1

const a = 0

// trailing comment
`,
	"crlf":         "package p\r\n\r\nfunc z() {\r\n}\r\n\r\nconst a = 1\r\n",
	"mixed-repeat": "package p\n\nfunc later() {}\n\nconst first = 1\n\nvar middle = 2\n\nfunc earlier() {}\n\nconst second = 3\n",
}

func TestReorderInvariantsBattery(t *testing.T) {
	for name, src := range reorderFixtures {
		once, err := reorderSource([]byte(src), name+".go")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		twice, err := reorderSource([]byte(once), name+".go")
		if err != nil {
			t.Fatalf("%s (second pass): %v", name, err)
		}
		if twice != once {
			t.Errorf("%s: not idempotent", name)
		}
		if got, want := strings.Join(sortedTokens(t, once), " "), strings.Join(sortedTokens(t, src), " "); got != want {
			t.Errorf("%s: token multiset not preserved", name)
		}
	}
}

func TestReorderRealRepoFiles(t *testing.T) {
	for _, name := range []string{"cli.go", "parser.go", "files.go", "reorder.go", "similarity.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		once, err := reorderSource(src, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		twice, err := reorderSource([]byte(once), name)
		if err != nil {
			t.Fatalf("%s (second pass): %v", name, err)
		}
		if twice != once {
			t.Errorf("%s: not idempotent", name)
		}
		if got, want := strings.Join(sortedTokens(t, once), " "), strings.Join(sortedTokens(t, string(src)), " "); got != want {
			t.Errorf("%s: token multiset not preserved", name)
		}
	}
}

func TestRunReorderCommandDryRunAndWrite(t *testing.T) {
	root := t.TempDir()
	src := "package p\n\nfunc f() {}\n\nconst A = 1\n"
	writeFile(t, filepath.Join(root, "a.go"), src)

	var stdout, stderr bytes.Buffer
	code := RunReorderCommand([]string{"--dry-run"}, root, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("dry-run exit = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "would reorder a.go") {
		t.Fatalf("dry-run output: %s", stdout.String())
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.go")); string(got) != src {
		t.Fatalf("dry-run wrote the file:\n%s", got)
	}

	stdout.Reset()
	code = RunReorderCommand(nil, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}
	want := "package p\n\nconst A = 1\n\nfunc f() {}\n"
	if got, _ := os.ReadFile(filepath.Join(root, "a.go")); string(got) != want {
		t.Fatalf("written file:\n%s", got)
	}

	stdout.Reset()
	code = RunReorderCommand(nil, root, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "already canonical") {
		t.Fatalf("second run: exit %d, output %s", code, stdout.String())
	}
}

func TestRunReorderCommandParseErrorSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "broken.go"), "package p\nfunc broken( {\n")
	writeFile(t, filepath.Join(root, "good.go"), "package p\n\nfunc f() {}\n\nconst A = 1\n")

	var stdout, stderr bytes.Buffer
	code := RunReorderCommand(nil, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (parse errors are notes, not fatal)", code)
	}
	if !strings.Contains(stdout.String(), "skipped (parse error): broken.go") {
		t.Fatalf("output: %s", stdout.String())
	}
	if got, _ := os.ReadFile(filepath.Join(root, "good.go")); !strings.Contains(string(got), "const A = 1\n\nfunc f()") {
		t.Fatalf("good.go not reordered:\n%s", got)
	}
}

func TestRunReorderCommandSelectionDefaults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package p\n\nfunc f() {}\n\nconst A = 1\n")
	writeFile(t, filepath.Join(root, "main_test.go"), "package p\n\nfunc f() {}\n\nconst A = 1\n")
	writeFile(t, filepath.Join(root, "vendor", "v.go"), "package v\n\nfunc f() {}\n\nconst A = 1\n")

	var stdout, stderr bytes.Buffer
	if code := RunReorderCommand([]string{"--dry-run"}, root, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "main.go") || strings.Contains(out, "main_test.go") || strings.Contains(out, "vendor") {
		t.Fatalf("selection wrong: %s", out)
	}
	// An explicit path is honored verbatim, including test files.
	stdout.Reset()
	if code := RunReorderCommand([]string{"--dry-run", filepath.Join(root, "main_test.go")}, root, &stdout, &stderr); code != 2 {
		t.Fatalf("explicit test file exit = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "would reorder") {
		t.Fatalf("explicit path output: %s", stdout.String())
	}
}

// TestRunReorderCommandWriteFailureNotesAndContinues: an unwritable
// file is skipped with a note, other files still reorder, exit 0.
func TestRunReorderCommandWriteFailureNotesAndContinues(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked.go")
	writeFile(t, locked, "package p\n\nfunc f() {}\n\nconst A = 1\n")
	if err := os.Chmod(locked, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o644) })
	writeFile(t, filepath.Join(root, "good.go"), "package p\n\nfunc f() {}\n\nconst A = 1\n")

	var stdout, stderr bytes.Buffer
	if code := RunReorderCommand(nil, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "skipped (") {
		t.Fatalf("no skip note: %s", stdout.String())
	}
	if got, _ := os.ReadFile(locked); string(got) != "package p\n\nfunc f() {}\n\nconst A = 1\n" {
		t.Fatalf("locked file modified:\n%s", got)
	}
}

// TestReorderLineDirectiveNoPanic: //line directives shift the parser's
// adjusted line numbers away from the raw line table; the transform
// must index raw positions (no panic) and keep directives riding with
// the declaration they precede.
func TestReorderLineDirectiveNoPanic(t *testing.T) {
	src := `package p

func z() {}

//line fake.go:42

const a = 1
`
	got := expectConsistent(t, src)
	if !strings.Contains(got, "//line fake.go:42") {
		t.Fatalf("//line directive dropped:\n%s", got)
	}
	if strings.Index(got, "const a = 1") > strings.Index(got, "func z()") {
		t.Fatalf("not reordered:\n%s", got)
	}
	// A directive above the package clause is preamble and stays put.
	pre := "//line fake.go:7\n\npackage p\n\nfunc z() {}\n\nconst a = 1\n"
	got = expectConsistent(t, pre)
	if !strings.HasPrefix(got, "//line fake.go:7") {
		t.Fatalf("leading directive moved:\n%s", got)
	}
}

// TestReorderMultiLineBlockInGapNotDropped: a multi-line block comment
// detached by two blank lines (its tail lines are not whole-line
// comments) must ride to the preceding declaration, never vanish.
func TestReorderMultiLineBlockInGapNotDropped(t *testing.T) {
	src := `package p

func z() {}


/*
Block notes,
two lines.
*/


const a = 1
`
	got := expectConsistent(t, src)
	for _, keep := range []string{"/*", "Block notes,", "two lines.", "*/"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("multi-line gap block dropped (%q missing):\n%s", keep, got)
		}
	}
	if strings.Index(got, "Block notes,") < strings.Index(got, "const a = 1") {
		t.Fatalf("block rode forward with the following declaration instead of gluing to the preceding one:\n%s", got)
	}
}

// TestReorderFirstDeclOneBlankAttach: a comment one blank above the
// FIRST declaration (floor = the package clause line) attaches to it
// and moves with it.
func TestReorderFirstDeclOneBlankAttach(t *testing.T) {
	src := `package p

// note on z

func z() {}

const a = 1
`
	want := `package p

const a = 1

// note on z

func z() {}
`
	got, err := reorderSource([]byte(src), "x.go")
	if err != nil || got != want {
		t.Fatalf("got:\n%s\nerr: %v", got, err)
	}
	expectConsistent(t, src)
}

// TestReorderCRLFCommandRoundTrip proves the written file keeps CRLF
// endings at command level too.
func TestReorderCRLFCommandRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crlf.go"), "package p\r\n\r\nfunc f() {\r\n}\r\n\r\nconst A = 1\r\n")
	var stdout, stderr bytes.Buffer
	if code := RunReorderCommand(nil, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got, _ := os.ReadFile(filepath.Join(root, "crlf.go"))
	if !bytes.Contains(got, []byte("\r\n\r\n")) || bytes.Contains(got, []byte("\n\n")) {
		t.Fatalf("CRLF not preserved on write: %q", got)
	}
}

// TestReorderLinePrimitives unit-tests the line classifiers the attach
// walk is built on, including branches unreachable through parseable
// whole files.
func TestReorderLinePrimitives(t *testing.T) {
	src := "\t// c\r\n/* b */  \r\n/* open\nplain\n  \nx / y"
	lines := splitLines([]byte(src))
	if len(lines) != 6 {
		t.Fatalf("splitLines: %d lines, want 6", len(lines))
	}
	if !isCommentLine(lines[0], []byte(src)) || !isCommentLine(lines[1], []byte(src)) {
		t.Fatal("comment lines not recognized")
	}
	if isCommentLine(lines[2], []byte(src)) { // unterminated /* block start
		t.Fatal("unterminated block start treated as whole-line comment")
	}
	for _, i := range []int{3, 4, 5} { // plain line, blank line, code with slash
		if isCommentLine(lines[i], []byte(src)) {
			t.Fatalf("line %d treated as comment", i)
		}
	}
	if !isBlankLine(lines[4], []byte(src)) || isBlankLine(lines[3], []byte(src)) {
		t.Fatal("isBlankLine wrong")
	}
	if got := lineOfOffset(lines, 0); got != 1 {
		t.Fatalf("lineOfOffset(0) = %d", got)
	}
	if got := lineOfOffset(lines, len(src)-1); got != 6 {
		t.Fatalf("lineOfOffset(last) = %d", got)
	}
}

// TestReorderOneBlankAttachWalk exercises attachLines directly: a
// comment run crosses one blank line but not two.
func TestReorderOneBlankAttachWalk(t *testing.T) {
	one := "package p\nconst A = 1\n\n// note\n\nconst B = 2\n"
	lines := splitLines([]byte(one))
	// B is on line 6: crosses the single blank onto "// note", then
	// stops at the blank above it (line 2 is code).
	if got := attachLines(lines, []byte(one), 6, 2); got != 4 {
		t.Fatalf("one-blank walk = %d, want 4 (the // note line)", got)
	}
	// A floor at the blank line stops the walk before crossing it.
	if got := attachLines(lines, []byte(one), 6, 5); got != 6 {
		t.Fatalf("floored walk = %d, want 6", got)
	}
	// Two blanks detach: the walk never leaves the declaration line.
	two := "package p\nconst A = 1\n\n// note\n\n\nconst B = 2\n"
	if got := attachLines(splitLines([]byte(two)), []byte(two), 7, 2); got != 7 {
		t.Fatalf("two-blank walk = %d, want 7 (detached)", got)
	}
}

func TestParseReorderArgs(t *testing.T) {
	dryRun, paths := parseReorderArgs([]string{"-n", "a.go", "--dry-run", "b.go"})
	if !dryRun || len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("parseReorderArgs = %v, %v", dryRun, paths)
	}
	dryRun, paths = parseReorderArgs([]string{"a.go"})
	if dryRun || len(paths) != 1 {
		t.Fatalf("parseReorderArgs plain = %v, %v", dryRun, paths)
	}
}
