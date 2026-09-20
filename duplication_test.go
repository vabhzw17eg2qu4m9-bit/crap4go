package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// cleanBlock returns a Go function whose body carries well over the
// default 50 tokens across 15 lines. Two copies of the same block trip the
// gate; blocks with different tags never match, because every 50-token
// window reaches a shifted constant.
func cleanBlock(tag int) string {
	var b strings.Builder
	b.WriteString("func dup(A, B, C, D, E, F, G, H int) int {\n")
	b.WriteString("\ttotal := A + B + C + D + E + F + G + H\n")
	for i := range 12 {
		fmt.Fprintf(&b, "\ttotal = total*%d + %d\n", i+2+tag, i+tag)
	}
	b.WriteString("\treturn total\n}\n")
	return b.String()
}

func dupBlock() string { return cleanBlock(0) }

func writeDupFile(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "package demo\n\n"+dupBlock())
}

func setupDupProject(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.22\n")
	for _, name := range names {
		writeDupFile(t, filepath.Join(root, name))
	}
	return root
}

func TestRun_DuplicatesWithinFile(t *testing.T) {
	// The two copies differ in comments — token lexemes (not comments)
	// must still match them.
	body := dupBlock()
	commented := strings.Replace(body, "\n", "\n// noise\n", 1)
	root := setupDupProject(t)
	writeFile(t, filepath.Join(root, "a.go"), "package demo\n\n"+body+commented)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"duplicates"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "a.go:3: ") || !strings.Contains(got, "duplicated lines > 1%") {
		t.Errorf("output missing a.go violation at first duplicated line 3:\n%s", got)
	}
	if !strings.Contains(got, "1/1 files over 1% duplication") {
		t.Errorf("output missing fail summary:\n%s", got)
	}
}

func TestRun_DuplicatesCrossFile(t *testing.T) {
	root := setupDupProject(t, "a.go", "b.go")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"duplicates"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	// The shared "package demo" token opens a 50-token window that
	// matches across the files, so the first duplicated line is 1.
	if !strings.Contains(got, "a.go:1: ") || !strings.Contains(got, "b.go:1: ") {
		t.Errorf("output missing cross-file violations:\n%s", got)
	}
	if !strings.Contains(got, "2/2 files over 1% duplication") {
		t.Errorf("output missing fail summary:\n%s", got)
	}
}

func TestRun_DuplicatesClean(t *testing.T) {
	root := setupDupProject(t)
	// Same shape, different constants: both files clear the 50-token
	// minimum (so both count in the summary) yet never match.
	writeFile(t, filepath.Join(root, "a.go"), "package demo\n\n"+cleanBlock(100))
	writeFile(t, filepath.Join(root, "b.go"), "package demo\n\n"+cleanBlock(200))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"duplicates"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s%s", code, out.String(), errOut.String())
	}
	if want := "2 files, 0.00% duplicated lines"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_DuplicatesTooFewTokens(t *testing.T) {
	root := setupDupProject(t)
	writeFile(t, filepath.Join(root, "tiny.go"), "package demo\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"duplicates"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s%s", code, out.String(), errOut.String())
	}
	if want := "no files with enough tokens"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

// TestRun_DuplicatesBoundaries pins both window thresholds: a block of
// exactly minTokens tokens on exactly minLines lines trips; one fewer
// token or line does not. The two adjacent copies share no 16-token
// window (copy 1's continue into copy 2's, which differ at the seam).
func TestRun_DuplicatesBoundaries(t *testing.T) {
	// 15 tokens on exactly 3 lines, present twice per file.
	block := "a := 1 + 2\nb := 3 + 4\nc := 5 + 6\n"
	fileSrc := "package demo\n\nfunc dup() {\n" + block + block + "}\n"
	root := setupDupProject(t)
	writeFile(t, filepath.Join(root, "a.go"), fileSrc)

	cases := []struct {
		args []string
		code int
	}{
		{[]string{"duplicates", "--min-tokens", "15", "--min-lines", "3"}, 2},
		{[]string{"duplicates", "--min-tokens", "16", "--min-lines", "3"}, 0},
		{[]string{"duplicates", "--min-tokens", "15", "--min-lines", "4"}, 0},
		{[]string{"duplicates", "--threshold", "100"}, 0},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		code := runWithRoot(tc.args, root, &out, &errOut)
		if code != tc.code {
			t.Errorf("%v: exit = %d, want %d (out=%s err=%s)", tc.args, code, tc.code, out.String(), errOut.String())
		}
	}
}

func TestRun_DuplicatesExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n\ngo 1.22\n")
	writeDupFile(t, filepath.Join(root, "a.go"))
	writeDupFile(t, filepath.Join(root, "gen", "generated.go"))

	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"duplicates"}, root, &out, &errOut); code != 2 {
		t.Fatalf("default run exit = %d, want 2:\n%s", code, out.String())
	}
	out.Reset()
	if code := runWithRoot([]string{"duplicates", "--exclude", "gen/**"}, root, &out, &errOut); code != 0 {
		t.Fatalf("--exclude run exit = %d, want 0:\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "1 files, ") {
		t.Errorf("excluded file must leave the scan:\n%s", out.String())
	}

	// The default exclusions (test files, vendor/) apply without flags.
	writeDupFile(t, filepath.Join(root, "a_test.go"))
	writeDupFile(t, filepath.Join(root, "vendor", "v.go"))
	out.Reset()
	if code := runWithRoot([]string{"duplicates", "--exclude", "gen/**"}, root, &out, &errOut); code != 0 {
		t.Fatalf("default-excluded copies flagged: exit = %d:\n%s%s", code, out.String(), errOut.String())
	}
}

func TestRun_DuplicatesSourceUnion(t *testing.T) {
	root := setupDupProject(t, "a.go")
	sibling := t.TempDir() // outside the module: only reachable via --source
	writeDupFile(t, filepath.Join(sibling, "dup.go"))
	writeFile(t, filepath.Join(sibling, "notes.txt"), "not go")

	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"duplicates", "--source", sibling}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "dup.go:") {
		t.Errorf("output missing --source violation:\n%s", got)
	}

	// A missing --source path is skipped silently; a plain .go file is
	// taken directly (here: dup.go, excluded again to restore a pass).
	out.Reset()
	code = runWithRoot([]string{"duplicates", "--source", filepath.Join(sibling, "gone"), "--source", filepath.Join(sibling, "dup.go"), "--exclude", "**/dup.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "gone") {
		t.Errorf("missing --source path must be skipped silently:\n%s", errOut.String())
	}
}

func TestRun_DuplicatesBadFlags(t *testing.T) {
	root := setupDupProject(t)
	for _, args := range [][]string{
		{"duplicates", "--min-tokens", "0"},
		{"duplicates", "--min-lines", "-1"},
		{"duplicates", "--nope"},
	} {
		var out, errOut bytes.Buffer
		if code := runWithRoot(args, root, &out, &errOut); code != 1 {
			t.Errorf("%v: exit = %d, want 1", args, code)
		}
	}
}

func TestTokenizeGo(t *testing.T) {
	src := []byte("package demo // comment\n\nfunc f() {\n\tx := \"lit\" + 1\n}\n")
	tokens := tokenizeGo(src)
	values := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		values = append(values, tok.value)
	}
	got := strings.Join(values, " ")
	want := "package demo func f ( ) { x := \"lit\" + 1 }"
	if got != want {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
	if tokens[0].line != 1 || tokens[7].line != 4 {
		t.Errorf("lines = %d/%d, want 1/4", tokens[0].line, tokens[7].line)
	}
}

func TestDupTotalLines(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"a":      1,
		"a\n":    1,
		"a\nb":   2,
		"a\nb\n": 2,
	}
	for content, want := range cases {
		if got := dupTotalLines([]byte(content)); got != want {
			t.Errorf("dupTotalLines(%q) = %d, want %d", content, got, want)
		}
	}
}

func TestUnionDupSources(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package demo\n")
	writeFile(t, filepath.Join(root, "sub", "b.go"), "package sub\n")
	plain := filepath.Join(root, "notes.txt")
	writeFile(t, plain, "text")
	got := unionDupSources([]string{filepath.Join(root, "a.go")}, []string{"sub", "missing", plain, "a.go"}, root)
	rel := make([]string, len(got))
	for i, path := range got {
		rel[i] = relPath(root, path)
	}
	want := "a.go,sub/b.go"
	if joined := strings.Join(rel, ","); joined != want {
		t.Fatalf("union = %q, want %q", joined, want)
	}
}
