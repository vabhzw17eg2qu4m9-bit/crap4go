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
		{"duplicates", "--ignore-localss"},
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
	tokens := tokenizeGo(src, dupOptions{})
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

// --- Type-2 clone detection (ported from crap4dart 0599df2 + 94299e0) ---

// renamedBody renders a function whose body uses only its own locals:
// two copies that differ in local names are renamed (Type-2) clones,
// detected only under --ignore-locals. Locals appear on every line, so
// no clean 50-token window survives a rename.
func renamedBody(name, value, cut, out, i, item, tag string) string {
	return fmt.Sprintf(`func %[1]s(%[2]s []string, %[3]s string, %[4]s []string, %[7]s string) {
	for %[5]s := 0; %[5]s < len(%[2]s); %[5]s++ {
		%[6]s := strings.TrimSpace(%[2]s[%[5]s])
		if %[6]s == "" {
			continue
		}
		if strings.HasPrefix(%[6]s, %[3]s) {
			%[4]s = append(%[4]s, strings.TrimPrefix(%[6]s, %[3]s))
		} else if strings.HasSuffix(%[6]s, %[7]s) {
			%[4]s = append(%[4]s, strings.TrimSuffix(%[6]s, %[7]s))
		} else {
			%[4]s = append(%[4]s, strings.ToLower(%[6]s))
		}
	}
}`, name, value, cut, out, i, item, tag)
}

// swappableBody renders an accumulator bounded by its first parameter
// and fed by its second.
func swappableBody(name, a, b, i string) string {
	return fmt.Sprintf(`func %[1]s(%[2]s int, %[3]s int) int {
	total := 0
	for %[4]s := 0; %[4]s < %[2]s; %[4]s++ {
		total += %[3]s
		if total > %[2]s {
			total -= 2
		}
		if total == %[2]s {
			total += %[3]s
		}
	}
	for total < %[3]s {
		total += %[2]s
		if total < 0 {
			total = 0
		}
	}
	return total
}`, name, a, b, i)
}

// crossedBody renders the same algorithm with the two parameters
// crossed — the loop bounds on the second parameter and feeds on the
// first. Consistent renaming must never match the two: bound and feed
// keep different placeholders.
func crossedBody(name, a, b, i string) string {
	return fmt.Sprintf(`func %[1]s(%[2]s int, %[3]s int) int {
	total := 0
	for %[4]s := 0; %[4]s < %[3]s; %[4]s++ {
		total += %[2]s
		if total > %[3]s {
			total -= 2
		}
		if total == %[3]s {
			total += %[2]s
		}
	}
	for total < %[2]s {
		total += %[3]s
		if total < 0 {
			total = 0
		}
	}
	return total
}`, name, a, b, i)
}

// calleeBody renders a function that dispatches every branch result to a
// package-level function: the API call target is the only difference
// between the two clones besides the local names, and API names stay
// visible under --ignore-locals.
func calleeBody(name, value, out, i, item, fn string) string {
	return fmt.Sprintf(`func %[1]s(%[2]s []string, %[3]s []string) {
	for %[4]s := 0; %[4]s < len(%[2]s); %[4]s++ {
		%[5]s := %[2]s[%[4]s]
		if len(%[5]s) == 0 {
			%[3]s = append(%[3]s, %[6]s(%[5]s))
		} else if strings.HasPrefix(%[5]s, "x") {
			%[3]s = append(%[3]s, %[6]s(strings.ToUpper(%[5]s)))
		} else if strings.HasSuffix(%[5]s, "y") {
			%[3]s = append(%[3]s, %[6]s(strings.ToLower(%[5]s)))
		} else {
			%[3]s = append(%[3]s, %[6]s(strings.TrimSpace(%[5]s)))
		}
	}
}`, name, value, out, i, item, fn)
}

// literalBody renders a function whose branches compare and emit
// literals: two copies differing only in the literal values are exact
// clones under --ignore-literals.
func literalBody(name string, n int, tag string) string {
	return fmt.Sprintf(`func %[1]s(%[2]s []string, %[3]s []string) {
	for %[4]s := 0; %[4]s < len(%[2]s); %[4]s++ {
		if len(%[2]s) > %[5]d {
			panic("%[6]s")
		}
		%[7]s := %[2]s[%[4]s]
		if strings.HasPrefix(%[7]s, "%[6]s") {
			%[3]s = append(%[3]s, %[7]s)
		} else if strings.HasSuffix(%[7]s, "%[6]s") {
			%[3]s = append(%[3]s, strings.ToLower(%[7]s))
		} else {
			%[3]s = append(%[3]s, strings.TrimSpace(%[7]s))
		}
	}
}`, name, "value", "out", "i", n, tag, "item")
}

// kindsBody renders a function exercising every renamed declaration
// kind: type parameters, a named result, a := multi-assign, range
// variables, a local function literal with its own parameter, and a
// recovered panic value.
func kindsBody(name, tp, res, size, half, k, v, fn, arg, r string) string {
	return fmt.Sprintf(`func %[1]s[%[2]s any](%[3]s []%[2]s) (sum int) {
	%[4]s, %[5]s := len(%[3]s), 0
	for %[6]s, %[7]s := range %[3]s {
		sum += %[6]s + len(fmt.Sprint(%[7]s))
	}
	_ = %[4]s
	%[8]s := func(%[9]s int) int {
		return %[9]s + sum + %[5]s
	}
	defer func() {
		if %[10]s := recover(); %[10]s != nil {
			sum = -1
		}
	}()
	return %[8]s(%[5]s)
}`, name, tp, res, size, half, k, v, fn, arg, r)
}

// shiftedBlock is an exact-copy block whose enclosing scopes carry
// different leading locals, shifting the masked placeholder numbering:
// only the raw pass can match the two files (upstream 94299e0).
func shiftedBlock() string {
	return `	value := 0
	for i := 0; i < 10; i++ {
		value += i
		if value > 20 {
			value = 0
		}
		if value < 5 {
			value += 2
		}
		if value == 7 {
			value--
		}
		if value > 8 {
			value = 8
		}
		if value == 3 {
			value += 3
		}
	}
	_ = value
`
}

// runDupPair writes the two given files and runs the duplicates
// subcommand with the given extra flags.
func runDupPair(t *testing.T, args []string, a, b string) (string, int) {
	t.Helper()
	root := setupDupProject(t)
	writeFile(t, filepath.Join(root, "type2a.go"), "package demo\n\n"+a)
	writeFile(t, filepath.Join(root, "type2b.go"), "package demo\n\n"+b)
	var out, errOut bytes.Buffer
	code := runWithRoot(append([]string{"duplicates"}, args...), root, &out, &errOut)
	return out.String(), code
}

// TestRun_DuplicatesRenamedCloneType1Only pins back-compat: without the
// new flags the gate stays Type-1, and a renamed clone passes.
func TestRun_DuplicatesRenamedCloneType1Only(t *testing.T) {
	out, code := runDupPair(t, nil,
		renamedBody("calc", "data", "cut", "out", "i", "item", "mark"),
		renamedBody("scan", "rows", "limit", "sink", "n", "entry", "tail"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (default mode is Type-1 only):\n%s", code, out)
	}
	if want := "2 files, 0.00% duplicated lines"; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestRun_DuplicatesIgnoreLocalsDetectsRenamedClone(t *testing.T) {
	out, code := runDupPair(t, []string{"--ignore-locals"},
		renamedBody("calc", "data", "cut", "out", "i", "item", "mark"),
		renamedBody("scan", "rows", "limit", "sink", "n", "entry", "tail"))
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (renamed clone must be detected):\n%s", code, out)
	}
	if !strings.Contains(out, "type2a.go:") || !strings.Contains(out, "type2b.go:") {
		t.Errorf("output missing violations for both clones:\n%s", out)
	}
	if !strings.Contains(out, "2/2 files over 1% duplication") {
		t.Errorf("output missing fail summary:\n%s", out)
	}
}

// TestRun_DuplicatesIgnoreLocalsSwappedNeverMatch pins precision: two
// locals swapped against each other keep different placeholders, so the
// crossed algorithm never matches its original.
func TestRun_DuplicatesIgnoreLocalsSwappedNeverMatch(t *testing.T) {
	out, code := runDupPair(t, []string{"--ignore-locals"},
		swappableBody("calc", "limit", "step", "idx"),
		crossedBody("scan", "first", "second", "pos"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (swapped locals never match):\n%s", code, out)
	}
}

// TestRun_DuplicatesIgnoreLocalsKeepsAPISurface pins that only locals are
// renamed: two clones calling different package functions never match.
func TestRun_DuplicatesIgnoreLocalsKeepsAPISurface(t *testing.T) {
	out, code := runDupPair(t, []string{"--ignore-locals"},
		calleeBody("process", "value", "result", "i", "item", "wrap"),
		calleeBody("handle", "data", "sink", "n", "entry", "fold"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (API surface stays visible):\n%s", code, out)
	}
}

// TestRun_DuplicatesIgnoreLiteralsMasksValues pins both literal behaviors:
// clones differing only in literal values are detected under
// --ignore-literals and pass under --ignore-locals (Go strings are single
// opaque tokens — no interpolation — so literal masking is the only way
// string values ever become opaque).
func TestRun_DuplicatesIgnoreLiteralsMasksValues(t *testing.T) {
	orig := literalBody("calc", 100, "alpha")
	clone := literalBody("scan", 200, "beta")
	out, code := runDupPair(t, []string{"--ignore-literals"}, orig, clone)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (literals must be masked):\n%s", code, out)
	}
	out, code = runDupPair(t, []string{"--ignore-locals"}, orig, clone)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (ignore_locals does not mask literals):\n%s", code, out)
	}
	out, code = runDupPair(t, nil, orig, clone)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (literals stay visible by default):\n%s", code, out)
	}
}

// TestRun_DuplicatesIgnoreLocalsAllDeclarationKinds covers first-use
// renaming of every Go declaration kind: type parameters, named results,
// := multi-assigns, range variables, function-literal parameters and
// recovered panic values.
func TestRun_DuplicatesIgnoreLocalsAllDeclarationKinds(t *testing.T) {
	out, code := runDupPair(t, []string{"--ignore-locals"},
		kindsBody("run", "T", "items", "size", "half", "i", "v", "boost", "delta", "rec"),
		kindsBody("go", "U", "rows", "width", "rest", "n", "e", "lift", "step", "err"))
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (all declaration kinds renamed):\n%s", code, out)
	}
}

// TestRun_DuplicatesRawPassCatchesExactCopyUnderShiftedScopes is the
// upstream 94299e0 regression: two files carry an exact-copy block, but
// their enclosing scopes hold different leading locals, so the masked
// pass numbers placeholders differently and cannot match them — the raw
// pass (part of the union when --ignore-locals is on) still must.
func TestRun_DuplicatesRawPassCatchesExactCopyUnderShiftedScopes(t *testing.T) {
	a := "func shiftedA(alpha int) {\n\tbeta := alpha + 1\n" + shiftedBlock() + "}\n"
	b := "func shiftedB(gamma int) {\n" + shiftedBlock() + "}\n"
	out, code := runDupPair(t, []string{"--ignore-locals"}, a, b)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (exact copy never lost to shifted scopes):\n%s", code, out)
	}
}

func TestTokenizeGoIgnoreLocals(t *testing.T) {
	src := []byte("package demo\n\nfunc calc(alpha int) int {\n\tx := alpha + 1\n\treturn x\n}\n\nvar gen = func(n int) int { return n }\n")
	tokens := tokenizeGo(src, dupOptions{ignoreLocals: true})
	values := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		values = append(values, tok.value)
	}
	got := strings.Join(values, " ")
	want := "package demo func calc ( $L1 int ) int { $L2 := $L1 + 1 return $L2 } " +
		"var gen = func ( $L1 int ) int { return $L1 }"
	if got != want {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

func TestTokenizeGoIgnoreLiterals(t *testing.T) {
	src := []byte("package demo\n\nvar limit = 10\n\nfunc f(s string) string {\n\treturn s + \"lit\" + 'x' + 2.5\n}\n")
	tokens := tokenizeGo(src, dupOptions{ignoreLiterals: true})
	values := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		values = append(values, tok.value)
	}
	got := strings.Join(values, " ")
	want := `package demo var limit = $NUM func f ( s string ) string { return s + $STR + $NUM + $NUM }`
	if got != want {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

// repoBody renders a method reading and writing a struct field: the
// receiver name and the locals are renamed under --ignore-locals, while
// the receiver's field, the type and the method names are API surface
// and keep their lexemes.
func repoBody(recv, typ, method, field, key, value string) string {
	return fmt.Sprintf(`func (%[1]s *%[6]s) %[5]s(%[3]s string) int {
	if len(%[1]s.%[2]s) > 0 {
		return %[1]s.%[2]s[%[3]s]
	}
	%[4]s := parse(%[3]s)
	if %[4]s < 0 {
		%[4]s = 0
	}
	if %[4]s > 100 {
		%[4]s -= 100
	}
	%[1]s.%[2]s[%[3]s] = %[4]s
	if %[4]s == 7 {
		%[4]s++
	}
	return %[4]s
}`, recv, typ, method, field, key, value)
}

// TestRun_DuplicatesIgnoreLocalsKeepsReceiverFieldsVisible pins the Go
// API-surface rule: the receiver name is a local binding and is renamed,
// but the fields it selects (and type/method names) keep their lexemes —
// clones differing in the field never match, while clones differing only
// in local names still do.
func TestRun_DuplicatesIgnoreLocalsKeepsReceiverFieldsVisible(t *testing.T) {
	out, code := runDupPair(t, []string{"--ignore-locals"},
		repoBody("ra", "RepoA", "load", "cache", "k", "v"),
		repoBody("rb", "RepoB", "fetch", "store", "key", "val"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (receiver fields are API surface):\n%s", code, out)
	}
	out, code = runDupPair(t, []string{"--ignore-locals"},
		repoBody("ra", "Repo", "load", "cache", "k", "v"),
		repoBody("rb", "Repo", "load", "cache", "key", "val"))
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (renamed receiver and locals still match):\n%s", code, out)
	}
}
