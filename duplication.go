package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Duplication gate defaults (ported from crap4dart §11.11 + dc64e9c).
const (
	defaultDupThreshold = 1.0
	defaultDupMinTokens = 50
	defaultDupMinLines  = 5
)

// defaultDupExcludes mirrors the port's standard source exclusions (the
// same set FindSourceFiles skips: test files and vendor/), applied on top
// of the scan set so --source additions follow the same rules.
var defaultDupExcludes = []string{"**/*_test.go", "vendor/**"}

// dupToken is one normalized token: its raw lexeme, its masked value and
// its source line. Comments and auto-inserted semicolons never enter the
// stream; everything else keeps its lexeme, so copies with different
// whitespace or comments still match. The value equals the lexeme unless
// literal/local masking applies (see dupNormalize).
type dupToken struct {
	lexeme string
	value  string
	line   int
	dup    bool
}

// dupFile is one file's token stream plus its total source line count.
type dupFile struct {
	path       string
	tokens     []dupToken
	totalLines int
}

// DuplicationViolation is one reported finding: a file whose duplicated
// line percentage exceeds the threshold, or one clone group under
// --report clones.
type DuplicationViolation struct {
	Path    string
	Line    int
	Message string
}

// duplicationResult aggregates the scan: violations, the number of
// tokenized files, the duplicated-line percentage across all of them and
// the one-line summary the command prints.
type duplicationResult struct {
	violations []DuplicationViolation
	files      int
	percent    float64
	summary    string
}

// dupOptions holds the parsed `duplicates` subcommand configuration.
type dupOptions struct {
	threshold          float64
	minTokens          int
	minLines           int
	report             dupReport
	ignoreLocals       bool
	ignoreLiterals     bool
	ignoreIdentifiers  bool
	ignoreImports      bool
	skipDuplicateFiles bool
	excludes           []string
	sources            []string
	paths              []string
}

// dupReport selects what the `duplicates` subcommand reports violations
// for: per-file duplicated-line percentages (the default) or PMD-CPD-style
// clone groups.
type dupReport int

const (
	dupReportPercentage dupReport = iota
	dupReportClones
)

// parseDupReport parses a --report value: "percentage" or "clones".
func parseDupReport(value string) (dupReport, bool) {
	switch value {
	case "percentage":
		return dupReportPercentage, true
	case "clones":
		return dupReportClones, true
	}
	return 0, false
}

// RunDuplicatesCommand implements `crap4go duplicates [--threshold N]
// [--min-tokens N] [--min-lines N] [--report MODE] [--exclude GLOB]...
// [--source PATH]... [--ignore-locals] [--ignore-literals]
// [--ignore-identifiers] [--ignore-imports] [--skip-duplicate-files]
// [paths...]`: it tokenizes every scanned file, detects token blocks that
// appear at least twice (>= min-tokens tokens, spanning >= min-lines
// lines) within or across files, and either flags files whose duplicated
// line percentage exceeds --threshold (the default) or reports one
// violation per clone group (--report clones). Exit code 2 iff there are
// violations.
func RunDuplicatesCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseDuplicatesFlags(args, stderr)
	if !ok {
		return code
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	files = unionDupSources(files, opts.sources, root)
	files = excludeDupFiles(files, root, opts.excludes)
	result, scanned := CheckDuplicates(files, root, opts)
	if scanned == 0 {
		fmt.Fprintln(stdout, "no files with enough tokens")
		return 0
	}
	for _, v := range result.violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	fmt.Fprintln(stdout, result.summary)
	if len(result.violations) > 0 {
		return 2
	}
	return 0
}

// parseDuplicatesFlags parses the duplicates flags plus positional paths.
// ok=false means a usage error (already reported to stderr) or help
// printed; the code is the exit code to return.
func parseDuplicatesFlags(args []string, stderr io.Writer) (dupOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go duplicates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := dupOptions{
		threshold: defaultDupThreshold,
		minTokens: defaultDupMinTokens,
		minLines:  defaultDupMinLines,
	}
	fs.Float64Var(&opts.threshold, thresholdFlagName, defaultDupThreshold, "flag files over this % of duplicated lines")
	fs.IntVar(&opts.minTokens, "min-tokens", defaultDupMinTokens, "minimum tokens in a duplicated block")
	fs.IntVar(&opts.minLines, "min-lines", defaultDupMinLines, "minimum source lines in a duplicated block")
	report := fs.String("report", "percentage", "what violations report for: percentage or clones (PMD CPD style)")
	fs.BoolVar(&opts.ignoreLocals, "ignore-locals", false, "rename function-local identifiers consistently so renamed clones are detected")
	fs.BoolVar(&opts.ignoreLiterals, "ignore-literals", false, "replace string and numeric literals with placeholders so clones differing only in values are detected")
	fs.BoolVar(&opts.ignoreIdentifiers, "ignore-identifiers", false, "replace all identifiers with one placeholder before hashing (structure-only matching)")
	fs.BoolVar(&opts.ignoreImports, "ignore-imports", false, "exclude import declarations from tokenization")
	fs.BoolVar(&opts.skipDuplicateFiles, "skip-duplicate-files", false, "skip files whose token stream equals an earlier file's")
	excludes := &stringSlice{}
	sources := &stringSlice{}
	fs.Var(excludes, excludeFlagName, excludeFlagUsage)
	fs.Var(sources, "source", "extra file or directory unioned into the scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return dupOptions{}, 1, false
	}
	if opts.minTokens < 1 || opts.minLines < 1 {
		fmt.Fprintln(stderr, "--min-tokens and --min-lines must be positive")
		return dupOptions{}, 1, false
	}
	parsedReport, ok := parseDupReport(*report)
	if !ok {
		fmt.Fprintln(stderr, `--report must be "percentage" or "clones"`)
		return dupOptions{}, 1, false
	}
	opts.report = parsedReport
	opts.excludes = excludes.slice()
	if len(opts.excludes) == 0 {
		opts.excludes = defaultDupExcludes
	}
	opts.sources = sources.slice()
	opts.paths = fs.Args()
	return opts, 0, true
}

// unionDupSources resolves each --source path against root and unions the
// results into files: directories are scanned recursively for Go source
// (the standard selection rules), plain files are taken directly when they
// have the .go extension, missing paths are skipped silently. The union is
// deduped and sorted.
func unionDupSources(files []string, sources []string, root string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
	}
	for _, source := range sources {
		abs := source
		if !filepath.IsAbs(source) {
			abs = filepath.Join(root, source)
		}
		for _, f := range expandDupSource(abs) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files
}

// expandDupSource resolves one --source path: a directory is walked for Go
// source via FindSourceFiles, a file is kept when it has the .go
// extension, and anything unreadable yields nothing (skipped silently).
func expandDupSource(abs string) []string {
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		files, err := FindSourceFiles(abs)
		if err != nil {
			return nil
		}
		return files
	}
	if strings.HasSuffix(abs, ".go") {
		return []string{abs}
	}
	return nil
}

// excludeDupFiles drops files whose project-relative path matches any
// exclude glob.
func excludeDupFiles(files []string, root string, patterns []string) []string {
	var kept []string
	for _, f := range files {
		if !matchesAnyGlob(relPath(root, f), patterns) {
			kept = append(kept, f)
		}
	}
	return kept
}

// matchesAnyGlob reports whether name matches any of the glob patterns.
func matchesAnyGlob(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, name) {
			return true
		}
	}
	return false
}

// CheckDuplicates tokenizes the file set, detects duplicated token blocks
// and builds the result. It returns the result plus the number of files in
// the scan (those with at least minTokens tokens); files with fewer are
// skipped from the scan entirely.
func CheckDuplicates(files []string, root string, opts dupOptions) (duplicationResult, int) {
	scanned := scanDupFiles(files, opts)
	if len(scanned) == 0 {
		return duplicationResult{}, 0
	}
	groups := detectDupGroups(scanned, opts)
	if opts.report == dupReportClones {
		return buildClonesResult(scanned, groups, root, opts), len(scanned)
	}
	return buildDuplicationResult(scanned, root, opts.threshold), len(scanned)
}

// scanDupFiles reads and tokenizes every file, keeping those with at least
// minTokens tokens (unreadable files are skipped). With skip_duplicate_files
// a file whose token stream equals an earlier file's is dropped — a copied
// file is analyzed once (CPD --skip-duplicate-files).
func scanDupFiles(files []string, opts dupOptions) []dupFile {
	var scanned []dupFile
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		tokens := tokenizeGo(src, opts)
		if len(tokens) < opts.minTokens {
			continue
		}
		if opts.skipDuplicateFiles && hasDupTwin(scanned, tokens) {
			continue
		}
		scanned = append(scanned, dupFile{path: path, tokens: tokens, totalLines: dupTotalLines(src)})
	}
	return scanned
}

// hasDupTwin reports whether an already-scanned file carries an identical
// token stream (by masked value).
func hasDupTwin(scanned []dupFile, tokens []dupToken) bool {
	for _, f := range scanned {
		if sameDupTokens(f.tokens, tokens) {
			return true
		}
	}
	return false
}

// sameDupTokens reports whether two token streams are identical by value.
func sameDupTokens(a, b []dupToken) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].value != b[i].value {
			return false
		}
	}
	return true
}

// tokenizeGo scans src into normalized tokens with go/scanner: comments
// are skipped by the default scan mode, auto-inserted semicolons
// (SEMICOLON with literal "\n") are dropped, real semicolons and all other
// tokens keep their lexeme. ignore_literals replaces string literals with
// $STR and numeric literals with $NUM; ignore_identifiers replaces every
// identifier with $ID (structure-only matching); ignore_locals renames
// identifiers declared in an outermost function scope to $L1, $L2, ... in
// first-use order, so renamed (Type-2) clones hash identically while the
// API surface keeps its lexeme. ignore_imports drops import declarations
// from the stream entirely.
func tokenizeGo(src []byte, opts dupOptions) []dupToken {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	scopes, importRanges := dupMasks(src, opts)
	var s scanner.Scanner
	s.Init(file, src, nil /* errors ignored */, 0)
	var tokens []dupToken
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return tokens
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		offset := file.Offset(pos)
		if inDupRanges(offset, importRanges) {
			continue
		}
		value := tok.String()
		if lit != "" {
			value = lit
		}
		tokens = append(tokens, dupToken{
			lexeme: value,
			value:  dupNormalize(value, tok, offset, scopes, opts),
			line:   file.Line(pos),
		})
	}
}

// dupMasks parses src once when any masking knob needs the parse tree and
// returns the outermost function scopes (ignore_locals) and the import
// declaration ranges (ignore_imports); both nil when no knob applies.
func dupMasks(src []byte, opts dupOptions) ([]*dupScope, [][2]int) {
	if !opts.ignoreLocals && !opts.ignoreImports {
		return nil, nil
	}
	parsed, base := parseDupAST(src)
	var scopes []*dupScope
	var imports [][2]int
	if opts.ignoreLocals {
		scopes = collectDupScopes(parsed, base)
	}
	if opts.ignoreImports {
		imports = dupImportRanges(parsed, base)
	}
	return scopes, imports
}

// parseDupAST parses src for the masking passes, returning the file (nil
// when there is no parse tree) and the FileSet base for 0-based offsets.
func parseDupAST(src []byte) (*ast.File, int) {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "", src, 0)
	if f == nil {
		return nil, 0
	}
	return f, fset.File(f.Package).Base()
}

// dupImportRanges collects the offset ranges of the file's import
// declarations: a whole GenDecl — the keyword, the optional parens and
// every spec, blank imports included — drops out of tokenization under
// ignore_imports (upstream excludes import/export/part directives).
func dupImportRanges(f *ast.File, base int) [][2]int {
	if f == nil {
		return nil
	}
	var ranges [][2]int
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			ranges = append(ranges, [2]int{int(gd.Pos()) - base, int(gd.End()) - base})
		}
	}
	return ranges
}

// inDupRanges reports whether the offset falls inside any [start, end)
// range.
func inDupRanges(offset int, ranges [][2]int) bool {
	for _, r := range ranges {
		if offset >= r[0] && offset < r[1] {
			return true
		}
	}
	return false
}

// dupNormalize masks one token's value for hashing: under ignore_literals
// string literals become $STR and numeric (int, float, imaginary, rune)
// literals $NUM; under ignore_identifiers every identifier becomes $ID —
// structure-only matching (CPD --ignore-identifiers; Go's predeclared
// builtins are plain identifiers, so masking token.IDENT covers them);
// under ignore_locals an identifier declared in one of the outermost
// function scopes becomes that scope's $L placeholder. The API surface —
// called functions and methods, types, field references, package-level
// identifiers — keeps its lexeme unless ignore_identifiers masks it. Go
// string literals are single opaque tokens (there is no interpolation),
// so masking never splits a string.
func dupNormalize(value string, tok token.Token, offset int, scopes []*dupScope, opts dupOptions) string {
	if opts.ignoreLiterals {
		switch tok {
		case token.STRING:
			return "$STR"
		case token.INT, token.FLOAT, token.IMAG, token.CHAR:
			return "$NUM"
		}
	}
	if tok != token.IDENT {
		return value
	}
	if opts.ignoreIdentifiers {
		return "$ID"
	}
	if scope := dupScopeAt(scopes, offset); scope != nil && scope.names[value] {
		return scope.placeholderFor(value)
	}
	return value
}

// dupScope is one outermost function scope: identifiers declared inside
// the 0-based offset range [start, end) are renamed consistently for
// clone matching (ported from upstream's _FunctionScope).
type dupScope struct {
	start, end   int
	names        map[string]bool
	placeholders map[string]string
}

// placeholderFor returns the scope's placeholder for name, assigned in
// first-use order: renamed clones of the same algorithm hash identically,
// while two locals swapped against each other keep different placeholders
// and never match.
func (s *dupScope) placeholderFor(name string) string {
	if p, ok := s.placeholders[name]; ok {
		return p
	}
	p := fmt.Sprintf("$L%d", len(s.placeholders)+1)
	s.placeholders[name] = p
	return p
}

// dupScopeAt returns the scope containing the 0-based offset, or nil —
// binary search over the sorted, disjoint scopes.
func dupScopeAt(scopes []*dupScope, offset int) *dupScope {
	low, high := 0, len(scopes)-1
	for low <= high {
		mid := int(uint(low+high) >> 1)
		switch {
		case offset < scopes[mid].start:
			high = mid - 1
		case offset >= scopes[mid].end:
			low = mid + 1
		default:
			return scopes[mid]
		}
	}
	return nil
}

// collectDupScopes collects the parsed file's outermost function scopes:
// one per FuncDecl and one per function literal not nested in another
// function; nested literals share the enclosing scope, and everything
// declared inside them folds into it. Scope bounds are 0-based offsets
// into src (base is the parse FileSet's base). Scopes are sorted and
// disjoint. A nil file yields no scopes.
func collectDupScopes(f *ast.File, base int) []*dupScope {
	if f == nil {
		return nil
	}
	var scopes []*dupScope
	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			scopes = append(scopes, newDupScope(fn.Recv, fn.Type, fn.Body, fn, base))
			return false
		case *ast.FuncLit:
			scopes = append(scopes, newDupScope(nil, fn.Type, fn.Body, fn, base))
			return false
		}
		return true
	})
	return scopes
}

// newDupScope collects every identifier declared inside the function: the
// receiver name (a local binding — its fields are API state), named
// parameters and named results, type parameters, := locals including
// multi-assigns, range variables, and the bindings of function literals
// nested anywhere in the body. The function's own name is API surface and
// stays out of the scope.
func newDupScope(recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt, node ast.Node, base int) *dupScope {
	s := &dupScope{
		start:        int(dupScopeStart(recv, typ, body)) - base,
		end:          int(node.End()) - base,
		names:        map[string]bool{},
		placeholders: map[string]string{},
	}
	collectFieldNames(s.names, recv)
	collectFieldNames(s.names, typ.TypeParams)
	collectFieldNames(s.names, typ.Params)
	collectFieldNames(s.names, typ.Results)
	if body != nil {
		ast.Inspect(body, func(n ast.Node) bool {
			collectStmtNames(s.names, n)
			return true
		})
	}
	return s
}

// dupScopeStart picks the function's first declared-name position: the
// receiver when present (it precedes the name), else the type-parameter
// or parameter list — the function's own name stays outside the range.
func dupScopeStart(recv *ast.FieldList, typ *ast.FuncType, body *ast.BlockStmt) token.Pos {
	if recv != nil {
		return recv.Pos()
	}
	if typ.TypeParams != nil {
		return typ.TypeParams.Pos()
	}
	if typ.Params != nil {
		return typ.Params.Pos()
	}
	if body != nil {
		return body.Pos()
	}
	return typ.End()
}

// collectFieldNames adds every named identifier of a field list —
// receiver, type parameters, parameters, named results — to names.
func collectFieldNames(names map[string]bool, list *ast.FieldList) {
	if list == nil {
		return
	}
	for _, field := range list.List {
		for _, name := range field.Names {
			names[name.Name] = true
		}
	}
}

// collectStmtNames adds the identifiers a body declaration introduces:
// var/const declarations, := assignments, range variables, and the
// parameters of nested function literals.
func collectStmtNames(names map[string]bool, n ast.Node) {
	switch d := n.(type) {
	case *ast.ValueSpec:
		collectIdentNames(names, d.Names)
	case *ast.AssignStmt:
		if d.Tok == token.DEFINE {
			collectExprNames(names, d.Lhs...)
		}
	case *ast.RangeStmt:
		if d.Tok == token.DEFINE {
			collectExprNames(names, d.Key, d.Value)
		}
	case *ast.FuncLit:
		collectFieldNames(names, d.Type.Params)
		collectFieldNames(names, d.Type.Results)
	}
}

// collectIdentNames adds every identifier's name to names.
func collectIdentNames(names map[string]bool, idents []*ast.Ident) {
	for _, id := range idents {
		names[id.Name] = true
	}
}

// collectExprNames adds every identifier position of the given
// expressions to names.
func collectExprNames(names map[string]bool, exprs ...ast.Expr) {
	for _, e := range exprs {
		if id, ok := e.(*ast.Ident); ok {
			names[id.Name] = true
		}
	}
}

// dupTotalLines counts source lines the way the upstream gate does: a
// trailing newline does not start a new line; empty content has none.
func dupTotalLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := bytes.Count(content, []byte("\n"))
	if !bytes.HasSuffix(content, []byte("\n")) {
		n++
	}
	return n
}

// dupPos is a token window's position inside the scan's file list.
type dupPos struct {
	file  int
	token int
}

// dupOcc is one occurrence of a clone group: the token range [start, end)
// of one file.
type dupOcc struct {
	file  int
	start int
	end   int
}

// dupGroup is one candidate clone group: occurrences of the same token
// block, each spanning the same token count.
type dupGroup struct {
	occurrences []dupOcc
}

// tokenCount is the token count shared by every occurrence.
func (g *dupGroup) tokenCount() int {
	return g.occurrences[0].end - g.occurrences[0].start
}

// dupHashBase is the Rabin-Karp rolling base (upstream's 2^64/φ constant).
const dupHashBase = 0x9e3779b97f4a7c15

// detectDupGroups detects duplicated blocks, marks the involved tokens and
// returns the accepted clone groups (longest first, non-overlapping — the
// PMD CPD Match semantics). Two passes: the raw pass compares lexemes
// (Type-1 copy-paste) and runs whenever ignore_locals is enabled; the
// masked pass compares normalized values. Candidates from both passes
// compete in one greedy selection, so a longer renamed clone wins over the
// shorter exact copy it contains.
func detectDupGroups(files []dupFile, opts dupOptions) []dupGroup {
	var candidates []dupGroup
	if opts.ignoreLocals {
		candidates = append(candidates, collectDupGroups(files, opts, dupRawCode)...)
	}
	candidates = append(candidates, collectDupGroups(files, opts, dupMaskedCode)...)
	// Longest clones first, then most occurrences, then position — the
	// selection order that keeps reporting deterministic.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.tokenCount() != b.tokenCount() {
			return a.tokenCount() > b.tokenCount()
		}
		if len(a.occurrences) != len(b.occurrences) {
			return len(a.occurrences) > len(b.occurrences)
		}
		if a.occurrences[0].file != b.occurrences[0].file {
			return a.occurrences[0].file < b.occurrences[0].file
		}
		return a.occurrences[0].start < b.occurrences[0].start
	})
	return acceptDupGroups(files, candidates)
}

// dupRawCode hashes a token's raw lexeme; dupMaskedCode hashes its masked
// value (identical to the lexeme when no masking applies).
func dupRawCode(t dupToken) uint64 { return dupHash(t.lexeme) }

func dupMaskedCode(t dupToken) uint64 { return dupHash(t.value) }

// acceptDupGroups greedily accepts groups whose occurrences overlap no
// already-accepted range of their file and marks their tokens (the marks
// drive the percentage report).
func acceptDupGroups(files []dupFile, candidates []dupGroup) []dupGroup {
	var accepted []dupGroup
	taken := map[int][][2]int{}
	for _, group := range candidates {
		overlaps := false
		for _, o := range group.occurrences {
			if dupOverlaps(taken[o.file], o.start, o.end) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		for _, o := range group.occurrences {
			taken[o.file] = append(taken[o.file], [2]int{o.start, o.end})
			tokens := files[o.file].tokens
			for i := o.start; i < o.end; i++ {
				tokens[i].dup = true
			}
		}
		accepted = append(accepted, group)
	}
	return accepted
}

// dupOverlaps reports whether [start, end) intersects any accepted range.
func dupOverlaps(ranges [][2]int, start, end int) bool {
	for _, r := range ranges {
		if start < r[1] && r[0] < end {
			return true
		}
	}
	return false
}

// collectDupGroups runs one detection pass over the given token codes of
// every file and turns every bucket of at least two window positions into
// a candidate clone group.
func collectDupGroups(files []dupFile, opts dupOptions, code func(dupToken) uint64) []dupGroup {
	occurrences := map[uint64][]dupPos{}
	for i := range files {
		indexDupFile(&files[i], i, opts, code, occurrences)
	}
	codes := make([][]uint64, len(files))
	for i := range files {
		c := make([]uint64, len(files[i].tokens))
		for j, t := range files[i].tokens {
			c[j] = code(t)
		}
		codes[i] = c
	}
	var groups []dupGroup
	for _, positions := range occurrences {
		if len(positions) < 2 {
			continue
		}
		if g, ok := dupGroupFromBucket(files, positions, codes, opts); ok {
			groups = append(groups, g)
		}
	}
	return groups
}

// dupGroupFromBucket builds one candidate clone group from a bucket of
// window positions: the windows are verified against the bucket reference
// by actual code equality (Rabin-Karp fingerprints can collide), extended
// to the maximal extent all occurrences share, and clipped to the common
// token count. ok=false when the bucket holds no real duplicate of at
// least minLines lines.
func dupGroupFromBucket(files []dupFile, positions []dupPos, codes [][]uint64, opts dupOptions) (dupGroup, bool) {
	ref := positions[0]
	var occs []dupPos
	var extents [][2]int
	for _, pos := range positions {
		if !sameDupWindow(codes[pos.file], pos.token, codes[ref.file], ref.token, opts.minTokens) {
			continue // fingerprint collision, not a real duplicate
		}
		occs = append(occs, pos)
		extents = append(extents, dupExtent(codes[pos.file], pos.token, codes[ref.file], ref.token, opts.minTokens))
	}
	if len(occs) < 2 {
		return dupGroup{}, false
	}
	// Every verified occurrence's extent contains its window, so the
	// common length is the window size (upstream's min-clamp over the
	// extents is inert by the same argument): the reference is anchored
	// at its own window and never extends against itself — that would
	// grow it over the whole file — while each other occurrence anchors
	// at its shared extent start.
	common := opts.minTokens
	refTokens := files[occs[0].file].tokens
	refStart := occs[0].token
	if refTokens[refStart+common-1].line-refTokens[refStart].line+1 < opts.minLines {
		return dupGroup{}, false
	}
	group := dupGroup{occurrences: []dupOcc{{file: occs[0].file, start: refStart, end: refStart + common}}}
	for i := 1; i < len(occs); i++ {
		group.occurrences = append(group.occurrences, dupOcc{file: occs[i].file, start: extents[i][0], end: extents[i][0] + common})
	}
	return group, true
}

// sameDupWindow reports whether the two token windows are equal by code.
func sameDupWindow(a []uint64, aStart int, b []uint64, bStart, length int) bool {
	for i := range length {
		if a[aStart+i] != b[bStart+i] {
			return false
		}
	}
	return true
}

// dupExtent grows the window at curStart to the maximal extent shared with
// the reference window at refStart: left and right while the surrounding
// codes match.
func dupExtent(cur []uint64, curStart int, ref []uint64, refStart, minTokens int) [2]int {
	start, refS := curStart, refStart
	for start > 0 && refS > 0 && cur[start-1] == ref[refS-1] {
		start--
		refS--
	}
	end, refE := curStart+minTokens, refStart+minTokens
	for end < len(cur) && refE < len(ref) && cur[end] == ref[refE] {
		end++
		refE++
	}
	return [2]int{start, end}
}

// indexDupFile slides a Rabin-Karp window of minTokens token codes over
// the file and records each window that spans at least minLines lines.
// uint64 arithmetic wraps naturally, giving the mod-2^64 semantics.
func indexDupFile(f *dupFile, fileIdx int, opts dupOptions, code func(dupToken) uint64, occurrences map[uint64][]dupPos) {
	n := len(f.tokens)
	if n < opts.minTokens {
		return
	}
	codes := make([]uint64, n)
	lines := make([]int, n)
	for i, t := range f.tokens {
		codes[i] = code(t)
		lines[i] = t.line
	}
	minTokens, minLines := opts.minTokens, opts.minLines
	pow := powDupBase(minTokens - 1)
	var hash uint64
	for i := range minTokens {
		hash = hash*dupHashBase + codes[i]
	}
	recordDupWindow(occurrences, hash, fileIdx, 0, lines, minTokens, minLines)
	for start := 1; start+minTokens <= n; start++ {
		hash -= codes[start-1] * pow
		hash = hash*dupHashBase + codes[start+minTokens-1]
		recordDupWindow(occurrences, hash, fileIdx, start, lines, minTokens, minLines)
	}
}

// recordDupWindow indexes a window only when it spans at least minLines
// source lines.
func recordDupWindow(occurrences map[uint64][]dupPos, hash uint64, fileIdx, start int, lines []int, minTokens, minLines int) {
	if lines[start+minTokens-1]-lines[start]+1 < minLines {
		return
	}
	occurrences[hash] = append(occurrences[hash], dupPos{file: fileIdx, token: start})
}

// powDupBase returns dupHashBase^exp via binary exponentiation (uint64
// overflow supplies the mod 2^64).
func powDupBase(exp int) uint64 {
	result, base := uint64(1), uint64(dupHashBase)
	for exp > 0 {
		if exp&1 == 1 {
			result *= base
		}
		base *= base
		exp >>= 1
	}
	return result
}

// dupHash is FNV-1a: a stable per-lexeme code for the rolling hash.
func dupHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range []byte(s) {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

// buildDuplicationResult computes each file's duplicated line percentage
// and the scan-wide aggregate. A file violates when its percentage is
// strictly over the threshold; the violation line is the first duplicated
// line.
func buildDuplicationResult(files []dupFile, root string, threshold float64) duplicationResult {
	result := duplicationResult{files: len(files)}
	dupLines, totalLines := 0, 0
	for _, f := range files {
		dupSet := dupLineSet(f)
		totalLines += f.totalLines
		dupLines += len(dupSet)
		if v, ok := dupViolation(f, len(dupSet), root, threshold); ok {
			result.violations = append(result.violations, v)
		}
	}
	if totalLines > 0 {
		result.percent = float64(dupLines) / float64(totalLines) * 100
	}
	if len(result.violations) > 0 {
		result.summary = fmt.Sprintf("%d/%d files over %g%% duplication", len(result.violations), len(files), threshold)
	} else {
		result.summary = fmt.Sprintf("%d files, %.2f%% duplicated lines", len(files), result.percent)
	}
	return result
}

// buildClonesResult reports one violation per clone group (PMD CPD style):
// every occurrence location in the message, groups in the accepted
// longest-first order.
func buildClonesResult(files []dupFile, groups []dupGroup, root string, opts dupOptions) duplicationResult {
	result := duplicationResult{files: len(files)}
	for _, group := range groups {
		occs := group.occurrences
		first := files[occs[0].file]
		locations := make([]string, 0, len(occs))
		for _, o := range occs {
			tokens := files[o.file].tokens
			locations = append(locations, fmt.Sprintf("%s:%d-%d",
				relPath(root, files[o.file].path), tokens[o.start].line, tokens[o.end-1].line))
		}
		shown, more := locations, ""
		if len(locations) > 3 {
			shown, more = locations[:3], fmt.Sprintf(" (+%d more)", len(locations)-3)
		}
		refTokens := first.tokens
		lineSpan := refTokens[occs[0].end-1].line - refTokens[occs[0].start].line + 1
		result.violations = append(result.violations, DuplicationViolation{
			Path: relPath(root, first.path),
			Line: refTokens[occs[0].start].line,
			Message: fmt.Sprintf("clone: %d tokens / %d lines, %d occurrences: %s%s",
				group.tokenCount(), lineSpan, len(locations), strings.Join(shown, "; "), more),
		})
	}
	percent := dupTotalPercent(files)
	if len(groups) == 0 {
		result.summary = fmt.Sprintf("no clones >= %d lines across %d files, %.2f%% duplicated lines",
			opts.minLines, len(files), percent)
	} else {
		result.summary = fmt.Sprintf("%d clone groups (>= %d tokens, >= %d lines), %.2f%% duplicated lines",
			len(groups), opts.minTokens, opts.minLines, percent)
	}
	return result
}

// dupTotalPercent computes the project-wide duplicated-line percentage
// over the scan — lines carrying marked tokens vs total lines (the same
// number the percentage report aggregates) — so a clones-mode summary
// always carries a number too.
func dupTotalPercent(files []dupFile) float64 {
	dupLines, totalLines := 0, 0
	for _, f := range files {
		totalLines += f.totalLines
		dupLines += len(dupLineSet(f))
	}
	if totalLines == 0 {
		return 0
	}
	return float64(dupLines) / float64(totalLines) * 100
}

// dupLineSet collects the distinct source lines holding duplicated tokens.
func dupLineSet(f dupFile) map[int]bool {
	set := map[int]bool{}
	for _, t := range f.tokens {
		if t.dup {
			set[t.line] = true
		}
	}
	return set
}

// dupViolation builds the file's violation when it is over the threshold.
func dupViolation(f dupFile, dupLines int, root string, threshold float64) (DuplicationViolation, bool) {
	if f.totalLines == 0 || dupLines == 0 {
		return DuplicationViolation{}, false
	}
	percent := float64(dupLines) / float64(f.totalLines) * 100
	if percent <= threshold {
		return DuplicationViolation{}, false
	}
	return DuplicationViolation{
		Path:    relPath(root, f.path),
		Line:    minDupLine(f),
		Message: fmt.Sprintf("%.2f%% duplicated lines > %g%%", percent, threshold),
	}, true
}

// minDupLine returns the first line holding a duplicated token.
func minDupLine(f dupFile) int {
	first := 0
	for line := range dupLineSet(f) {
		if first == 0 || line < first {
			first = line
		}
	}
	return first
}
