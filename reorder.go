package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"sort"
	"strings"
)

// Canonical categories for top-level declarations — the Go-idiomatic
// file order (Go has no class bodies, so unlike the Dart original the
// canonical scope is the whole file): imports, constants, variables,
// types, functions. Declarations of one category keep their original
// relative order — a stable partition, never an alphabetical sort.
const (
	categoryImport = iota
	categoryConst
	categoryVar
	categoryType
	categoryFunc
)

// declSlice is one sliced top-level declaration: its canonical category,
// its source index (the stable-partition tiebreaker), and the byte span
// it moves within — the declaration plus its attached comments.
type declSlice struct {
	category int
	index    int
	start    int
	end      int
}

// line is one source line as a byte range (end exclusive, terminator
// not included).
type line struct{ start, end int }

// rawPos is the position without //line-directive adjustment: the
// transform's line table indexes the raw file, so every line lookup
// must use the raw position or a directive like cgo's would shift the
// indexes out of the table.
func rawPos(fset *token.FileSet, p token.Pos) token.Position {
	return fset.PositionFor(p, false)
}

// reorderSource deterministically reorders the top-level declarations of
// one Go source file into the canonical order (imports, consts, vars,
// types, funcs — stable within each category). Declarations already in
// canonical order return the input byte-identical, whatever their
// layout. Doc comments and whole-line comment blocks above a
// declaration move with it — including a block separated by a single
// blank line; two or more blank lines detach the block, and a detached
// block between declarations sticks to the preceding declaration so
// comments are never dropped. A comment group starting on a
// declaration's line (a trailing same-line comment and its contiguous
// continuation lines) belongs to that declaration. Grouped const, var
// and type blocks move whole — iota and struct-field order are
// semantics and are never touched — and the stable partition preserves
// the relative order of functions, so multiple init() functions in one
// file keep their execution order. The file's line-ending style (LF or
// CRLF) is preserved.
func reorderSource(src []byte, path string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	if len(f.Decls) < 2 {
		return string(src), nil
	}
	tf := fset.File(f.Pos())
	lines := splitLines(src)
	slices := fileSlices(f, fset, tf, lines, src)
	sorted := canonicalOrder(slices)
	if sorted == nil {
		return string(src), nil
	}
	return rewriteSpan(src, slices, sorted), nil
}

// fileSlices slices every top-level declaration: end offsets first
// (each extends over its trailing semicolon and comment group), then
// start offsets (comments above under the attach rules), then floating
// gap blocks glue to the preceding slice.
func fileSlices(f *ast.File, fset *token.FileSet, tf *token.File, lines []line, src []byte) []declSlice {
	pkgLine := rawPos(fset, f.Package).Line
	slices := make([]declSlice, len(f.Decls))
	for i, d := range f.Decls {
		slices[i] = declSlice{category: declCategory(d), index: i, end: declEnd(f, fset, tf, src, d)}
	}
	for i, d := range f.Decls {
		slices[i].start = sliceStart(f, fset, lines, src, d, i, slices, pkgLine)
	}
	extendOverFloatingComments(lines, src, slices)
	return slices
}

// canonicalOrder stable-sorts the slices by category; it returns nil
// when the source order already is canonical (so the file stays
// byte-identical whatever its layout).
func canonicalOrder(slices []declSlice) []declSlice {
	sorted := append([]declSlice(nil), slices...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].category < sorted[b].category })
	for i, s := range sorted {
		if s.index != i {
			return sorted
		}
	}
	return nil
}

// declCategory maps a top-level declaration to its canonical category;
// only *ast.GenDecl and *ast.FuncDecl occur at file level.
func declCategory(d ast.Decl) int {
	gd, ok := d.(*ast.GenDecl)
	if !ok {
		return categoryFunc
	}
	switch gd.Tok {
	case token.IMPORT:
		return categoryImport
	case token.CONST:
		return categoryConst
	case token.VAR:
		return categoryVar
	case token.TYPE:
		return categoryType
	}
	return categoryFunc
}

// declDoc is the declaration's attached doc comment group, or nil.
func declDoc(d ast.Decl) *ast.CommentGroup {
	switch dd := d.(type) {
	case *ast.GenDecl:
		return dd.Doc
	case *ast.FuncDecl:
		return dd.Doc
	}
	return nil
}

// declEnd is the declaration's end offset extended over its trailing
// explicit semicolon (a GenDecl's End excludes it) and its trailing
// comment group — a comment group starting on the declaration's last
// line (including contiguous continuation lines) belongs to it.
func declEnd(f *ast.File, fset *token.FileSet, tf *token.File, src []byte, d ast.Decl) int {
	end := tf.Offset(d.End())
	if end < len(src) && src[end] == ';' {
		end++
	}
	for _, g := range f.Comments {
		if tf.Offset(g.Pos()) < end {
			continue
		}
		if rawPos(fset, g.Pos()).Line == rawPos(fset, d.End()).Line {
			return tf.Offset(g.End())
		}
		break
	}
	return end
}

// sliceStart is the inclusive start offset of a declaration's slice: its
// doc comment (when the parser attached one directly above), extended
// upward over whole-line comment blocks under the one-blank attach /
// two-blank detach rule, never overlapping the previous slice.
func sliceStart(f *ast.File, fset *token.FileSet, lines []line, src []byte, d ast.Decl, i int, slices []declSlice, pkgLine int) int {
	floor := pkgLine
	if i > 0 {
		floor = rawPos(fset, f.Decls[i-1].End()).Line
	}
	startLine := attachLines(lines, src, docStartLine(fset, d, rawPos(fset, d.Pos()).Line), floor)
	return clampStart(src, lines[startLine-1].start, i, slices)
}

// docStartLine moves the walk's starting line up to an attached doc
// comment (the parser sets Doc only when the group ends directly above
// the declaration).
func docStartLine(fset *token.FileSet, d ast.Decl, declLine int) int {
	if doc := declDoc(d); doc != nil && rawPos(fset, doc.End()).Line == declLine-1 {
		return rawPos(fset, doc.Pos()).Line
	}
	return declLine
}

// clampStart keeps a slice from overlapping the previous one — its
// trailing comment group, or a declaration sharing its line via an
// explicit semicolon — by starting at the declaration's own first
// token.
func clampStart(src []byte, start, i int, slices []declSlice) int {
	if i == 0 || slices[i-1].end <= start {
		return start
	}
	return skipToToken(src, slices[i-1].end)
}

// skipToToken advances past the separator bytes between two
// same-line declarations (spaces, tabs, newlines, semicolons).
func skipToToken(src []byte, start int) int {
	for start < len(src) && isJoinByte(src[start]) {
		start++
	}
	return start
}

// isJoinByte reports whether c is a separator byte the clamp may skip.
func isJoinByte(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ';':
		return true
	}
	return false
}

// attachLines walks upward over whole-line comment blocks: a comment
// line attaches directly, and a single blank line continues the run
// when another comment line sits above it (one blank keeps a block with
// the declaration below it; two or more blank lines detach). floor is
// the lowest line (1-based) the walk may step on.
func attachLines(lines []line, src []byte, startLine, floor int) int {
	for {
		switch {
		case startLine-1 >= floor && isCommentLine(lines[startLine-2], src):
			startLine--
		case startLine-2 >= floor && isBlankLine(lines[startLine-2], src) && isCommentLine(lines[startLine-3], src):
			startLine -= 2
		default:
			return startLine
		}
	}
}

// extendOverFloatingComments glues comment blocks that no declaration
// claimed to the preceding declaration's slice end, so a rewrite never
// drops a comment. Unclaimed means detached from the declaration below
// by two or more blank lines (or not attachable by the walk: the tail
// lines of a multi-line /* */ block). Everything strictly between two
// top-level declarations is comment content by the grammar, so any
// non-blank gap line is glued. Blocks before the first slice and after
// the last stay outside the rewritten span and remain in place.
func extendOverFloatingComments(lines []line, src []byte, slices []declSlice) {
	for i := 0; i+1 < len(slices); i++ {
		from := lineOfOffset(lines, slices[i].end)
		to := lineOfOffset(lines, slices[i+1].start)
		last := -1
		for l := from; l < to-1; l++ {
			if !isBlankLine(lines[l], src) {
				last = l
			}
		}
		if last >= 0 {
			slices[i].end = lines[last].end
		}
	}
}

// rewriteSpan joins the sorted slices with one blank line (in the
// file's line-ending style) and splices them over the source span the
// slices cover. Only the rewritten span is normalized — the region
// before the first slice and after the last is untouched.
func rewriteSpan(src []byte, slices, sorted []declSlice) string {
	sep := "\n\n"
	if bytes.Contains(src, []byte("\r\n")) {
		sep = "\r\n\r\n"
	}
	var b strings.Builder
	for i, s := range sorted {
		if i > 0 {
			b.WriteString(sep)
		}
		b.Write(src[s.start:s.end])
	}
	return string(src[:slices[0].start]) + b.String() + string(src[slices[len(slices)-1].end:])
}

// lineOfOffset is the 1-based line containing byte offset o.
func lineOfOffset(lines []line, o int) int {
	return sort.Search(len(lines), func(i int) bool { return lines[i].end >= o }) + 1
}

// splitLines splits src into lines terminated by \n; a missing final
// terminator keeps the last line.
func splitLines(src []byte) []line {
	var lines []line
	for start := 0; start < len(src); {
		end := start + bytes.IndexByte(src[start:], '\n')
		if end < start {
			end = len(src)
		}
		lines = append(lines, line{start: start, end: end})
		start = end + 1
	}
	return lines
}

// isCommentLine reports whether the line is a whole-line comment: a //
// line or a complete single-line /* block */ comment (a multi-line
// block attaches only via its doc position above the declaration).
func isCommentLine(l line, src []byte) bool {
	rest, ok := lineRemainder(l, src)
	if !ok {
		return false
	}
	if rest[1] == '/' {
		return true
	}
	return rest[1] == '*' && isCompleteBlock(rest)
}

// lineRemainder skips leading whitespace up to a comment-start slash;
// false when the line is blank, too short, or starts with code.
func lineRemainder(l line, src []byte) ([]byte, bool) {
	i := l.start
	for i < l.end && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r') {
		i++
	}
	if i+1 >= l.end || src[i] != '/' {
		return nil, false
	}
	return src[i:l.end], true
}

// isCompleteBlock reports whether rest opens and closes a /* */
// comment on one line (trailing spaces, tabs and a final \r are
// tolerated).
func isCompleteBlock(rest []byte) bool {
	end := len(rest)
	for end > 0 && (rest[end-1] == ' ' || rest[end-1] == '\t' || rest[end-1] == '\r') {
		end--
	}
	return end >= 4 && rest[end-1] == '/' && rest[end-2] == '*'
}

// isBlankLine reports whether the line is empty or whitespace only.
func isBlankLine(l line, src []byte) bool {
	for i := l.start; i < l.end; i++ {
		if src[i] != ' ' && src[i] != '\t' && src[i] != '\r' {
			return false
		}
	}
	return true
}

// RunReorderCommand implements `crap4go reorder [--dry-run] [paths...]`:
// it rewrites the selected Go files so their top-level declarations
// follow the canonical order (imports, consts, vars, types, funcs —
// stable within each category). By default the files are written and
// the command exits 0; with --dry-run nothing is written, the files
// that would change are listed, and the command exits 2 when at least
// one would change. Files that fail to parse are skipped with a note,
// never fatal. Selection follows the standard rules (non-test,
// non-vendor .go files under root unless paths are given).
func RunReorderCommand(args []string, root string, stdout, stderr io.Writer) int {
	dryRun, paths := parseReorderArgs(args)
	files, err := selectFiles(false, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, noGoFilesNotice)
		return 0
	}
	changed := 0
	for _, file := range files {
		if reorderOneFile(file, root, dryRun, stdout) {
			changed++
		}
	}
	return reorderSummary(changed, len(files), dryRun, stdout)
}

// parseReorderArgs splits the --dry-run/-n flag from positional paths.
func parseReorderArgs(args []string) (bool, []string) {
	dryRun := false
	var paths []string
	for _, a := range args {
		if a == "--dry-run" || a == "-n" {
			dryRun = true
			continue
		}
		paths = append(paths, a)
	}
	return dryRun, paths
}

// formatClean runs gofmt's own printer (go/format) over a rewritten
// file: reordering can create new adjacency groups of one-line
// declarations whose columnar body alignment no longer matches, and the
// printer is what recomputes it. CRLF files skip the pass — the
// printer emits LF only — and keep their line-ending style; on a
// printer error the plain reorder result stands. Already-canonical
// files never reach this (they are returned byte-identical).
func formatClean(src []byte) ([]byte, bool) {
	if bytes.Contains(src, []byte("\r\n")) {
		return src, false
	}
	out, err := format.Source(src)
	if err != nil {
		return src, false
	}
	return out, true
}

// reorderOneFile reorders (or dry-runs) one file, printing the outcome;
// it reports whether the file changed or would change.
func reorderOneFile(file, root string, dryRun bool, stdout io.Writer) bool {
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stdout, "skipped (%s): %s\n", err, relPath(root, file))
		return false
	}
	out, err := reorderSource(src, file)
	if err != nil {
		fmt.Fprintf(stdout, "skipped (parse error): %s\n", relPath(root, file))
		return false
	}
	if out == string(src) {
		return false
	}
	verb := "reordered"
	if dryRun {
		verb = "would reorder"
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, relPath(root, file))
	if dryRun {
		return true
	}
	return writeReordered(file, out, root, stdout)
}

// writeReordered writes the reordered file (gofmt-normalized for LF
// sources); it reports whether the write succeeded.
func writeReordered(file, out, root string, stdout io.Writer) bool {
	if clean, ok := formatClean([]byte(out)); ok {
		out = string(clean)
	}
	if err := os.WriteFile(file, []byte(out), writableFilePerm); err != nil {
		fmt.Fprintf(stdout, "skipped (%s): %s\n", err, relPath(root, file))
		return false
	}
	return true
}

// reorderSummary prints the run summary and returns the exit code: 0
// when nothing changed, 2 on a dry run with would-change files, 0
// after writing.
func reorderSummary(changed, total int, dryRun bool, stdout io.Writer) int {
	if changed == 0 {
		fmt.Fprintf(stdout, "All %d files already canonical.\n", total)
		return 0
	}
	if dryRun {
		fmt.Fprintf(stdout, "%d of %d files would be reordered.\n", changed, total)
		return 2
	}
	fmt.Fprintf(stdout, "%d of %d files reordered.\n", changed, total)
	return 0
}
