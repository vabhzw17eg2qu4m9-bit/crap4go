package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Scattered-constants defaults, ported from crap4dart e766d68's
// scattered_constants gate.
const (
	defaultScatteredMinFiles  = 3
	defaultScatteredMinLength = 4
	defaultScatteredMinValue  = 100
)

// generatedMarkerRE matches the Go community's generated-code marker comment
// (the go generate convention). Files carrying it are generated output and
// are excluded by default — the host-language analog of upstream's
// generated-code exclude globs (**.g.dart and friends).
var generatedMarkerRE = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// ScatteredConstantsViolation is one scattered literal occurrence.
type ScatteredConstantsViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v ScatteredConstantsViolation) ignorePath() string { return v.Path }
func (v ScatteredConstantsViolation) ignoreLine() int    { return v.Line }

// NamedScatteredConstant is a named declaration in the corpus that every
// hardcoded occurrence of the same value should reference.
type NamedScatteredConstant struct {
	Name string
	File string
	Line int
}

// scatteredOptions holds the parsed `scattered-constants` subcommand
// configuration.
type scatteredOptions struct {
	minFiles    int
	minLength   int
	minValue    int
	flagAliases bool
	paths       []string
}

// RunScatteredConstantsCommand implements `crap4go scattered-constants
// [--min-files N] [--min-length N] [--min-value N] [--aliases] [paths...]`:
// the corpus-wide sibling of magic-constants. It flags three kinds of
// findings (ported from crap4dart e766d68 and a314b485): constant
// regressions — a named const or top-level var for the value already
// exists, so every hardcoded re-introduction flags immediately, naming the
// constant to reference — scattered literals appearing in at least minFiles
// distinct files (one violation per occurrence), the "change 86400 in every
// service" hazard that in-file repeat counting cannot see, and pure value
// aliases — an unexported const whose initializer is another constant's
// bare identifier (suppressed with --aliases=false). Exit code 2 iff there
// are violations.
func RunScatteredConstantsCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseScatteredFlags(args, stderr)
	if !ok {
		return code
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, noGoFilesNotice)
		return 0
	}
	violations, checked := CheckScatteredConstants(files, root, opts.minFiles, opts.minLength, opts.minValue, opts.flagAliases)
	violations = filterIgnored("scattered-constants", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d scattered constant(s) in %d files\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d files free of scattered constants\n", checked)
	return 0
}

// parseScatteredFlags parses the scattered-constants flags plus positional
// paths. ok=false means a usage error (already reported to stderr); the
// code is the exit code to return.
func parseScatteredFlags(args []string, stderr io.Writer) (scatteredOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go scattered-constants", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := scatteredOptions{
		minFiles:  defaultScatteredMinFiles,
		minLength: defaultScatteredMinLength,
		minValue:  defaultScatteredMinValue,
	}
	fs.IntVar(&opts.minFiles, "min-files", defaultScatteredMinFiles, "distinct files a value must reach before its occurrences are flagged")
	fs.IntVar(&opts.minLength, "min-length", defaultScatteredMinLength, "string values shorter than this are coincidence")
	fs.IntVar(&opts.minValue, "min-value", defaultScatteredMinValue, "numbers with magnitude at or below this are coincidence")
	fs.BoolVar(&opts.flagAliases, "aliases", true, "flag pure value aliases — unexported consts initialized to another constant's identifier; --aliases=false keeps them")
	if err := fs.Parse(args); err != nil {
		return scatteredOptions{}, 1, false
	}
	if opts.minFiles < 2 {
		fmt.Fprintln(stderr, "--min-files must be at least 2")
		return scatteredOptions{}, 1, false
	}
	if opts.minLength < 1 {
		fmt.Fprintln(stderr, "--min-length must be positive")
		return scatteredOptions{}, 1, false
	}
	if opts.minValue < 0 {
		fmt.Fprintln(stderr, "--min-value must not be negative")
		return scatteredOptions{}, 1, false
	}
	opts.paths = fs.Args()
	return opts, 0, true
}

// CheckScatteredConstants scans every parseable file and returns the
// constant-regression and scattered-literal violations — plus, when
// flagAliases is on, pure value alias violations — and the number of files
// checked. Values compare semantically: digit separators (1_000) and number
// bases (0x3E8) do not split a fact.
func CheckScatteredConstants(files []string, root string, minFiles, minLength, minValue int, flagAliases bool) ([]ScatteredConstantsViolation, int) {
	literals := map[string]*scatteredLiteral{}
	constants := map[string]NamedScatteredConstant{}
	var aliases []scatteredAlias
	checked := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		if scatteredSkipDefault(f, file) {
			continue
		}
		checked++
		hits, fileAliases := collectScatteredHits(file, fset, minLength, minValue)
		rel := relPath(root, f)
		for i := range fileAliases {
			fileAliases[i].file = rel
		}
		aliases = append(aliases, fileAliases...)
		mergeScatteredHits(hits, rel, literals, constants)
	}
	violations := scatteredViolations(literals, constants, minFiles)
	if flagAliases {
		violations = append(violations, scatteredAliasViolations(aliases)...)
	}
	return violations, checked
}

// mergeScatteredHits folds one file's hits into the corpus maps: top-level
// declaration hits register the corpus constant (first declaration wins)
// and every hit becomes an occurrence site.
func mergeScatteredHits(hits []scatteredHit, rel string, literals map[string]*scatteredLiteral, constants map[string]NamedScatteredConstant) {
	for _, hit := range hits {
		if hit.isDecl && hit.topLevel {
			if _, ok := constants[hit.key]; !ok {
				constants[hit.key] = NamedScatteredConstant{Name: hit.name, File: rel, Line: hit.line}
			}
		}
		lit := literals[hit.key]
		if lit == nil {
			lit = &scatteredLiteral{display: hit.display}
			literals[hit.key] = lit
		}
		lit.sites = append(lit.sites, scatteredSite{file: rel, line: hit.line, isDecl: hit.isDecl})
	}
}

// scatteredSkipDefault reports whether a file is excluded by the gate's
// default excludes: test files (upstream's test/**) and generated files
// (files carrying the "Code generated ... DO NOT EDIT." marker).
func scatteredSkipDefault(path string, file *ast.File) bool {
	if strings.HasSuffix(path, testFileSuffix) {
		return true
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if generatedMarkerRE.MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

// scatteredSite is one occurrence of a literal value; isDecl marks the
// plain-literal initializer of a named const or top-level var declaration.
type scatteredSite struct {
	file   string
	line   int
	isDecl bool
}

// scatteredLiteral tracks one literal value across the corpus: the display
// form for messages and every occurrence site.
type scatteredLiteral struct {
	display string
	sites   []scatteredSite
}

// scatteredHit is one collected occurrence before corpus aggregation; decl
// hits carry the declaring identifier's name, and topLevel decl hits are
// the referencable forms that register the corpus constant (top-level const
// and top-level var; a function-local const cannot anchor a cross-file
// reference).
type scatteredHit struct {
	key      string
	display  string
	line     int
	isDecl   bool
	topLevel bool
	name     string
}

// scatteredAlias is one pure value alias: a const declaration whose
// initializer is another constant's bare identifier — `const x = kY`
// (ported from crap4dart a314b485's 'aliases' rule).
type scatteredAlias struct {
	name   string
	target string
	file   string
	line   int
}

// scatteredPredeclaredConsts are Go's predeclared constant identifiers:
// true and false are Go's boolean literals and iota is the enum machinery —
// initializing a const with one is computed, not an alias (nil cannot
// legally initialize a const and is listed for completeness).
var scatteredPredeclaredConsts = map[string]bool{
	"true": true, "false": true, "iota": true, "nil": true,
}

// scatteredCollector accumulates one file's hits while marking the
// identifier-position lines (map keys, index operands, case labels, struct
// tags) that never count.
type scatteredCollector struct {
	fset    *token.FileSet
	exempt  map[int]bool
	seen    map[ast.Node]bool
	hits    []scatteredHit
	aliases []scatteredAlias
}

// collectScatteredHits walks file once and returns the literal occurrences
// eligible for corpus comparison plus the pure value aliases found in const
// declarations. Named declarations — every const and top-level var with a
// plain string/int/float initializer (Go's referencable-value forms) —
// contribute isDecl hits that register the corpus constant; computed
// initializers and all other literals become ordinary sites. Import paths,
// struct tags and identifier-position strings are protocol vocabulary and
// never collected.
func collectScatteredHits(file *ast.File, fset *token.FileSet, minLength, minValue int) ([]scatteredHit, []scatteredAlias) {
	c := &scatteredCollector{fset: fset, exempt: map[int]bool{}, seen: map[ast.Node]bool{}}
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && (gd.Tok == token.CONST || gd.Tok == token.VAR) {
			c.collectSpecs(gd, true, minLength, minValue)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		return c.walk(n, minLength, minValue)
	})
	return c.hits, c.aliases
}

// walk handles one node of the file walk: imports stop the descent,
// declarations contribute decl hits, literals become sites, and protocol
// positions are marked exempt.
func (c *scatteredCollector) walk(n ast.Node, minLength, minValue int) bool {
	switch n := n.(type) {
	case *ast.ImportSpec:
		return false
	case *ast.GenDecl:
		if n.Tok == token.CONST {
			c.collectSpecs(n, false, minLength, minValue)
		}
	case *ast.BasicLit:
		c.collectLiteral(n, minLength, minValue)
	default:
		c.markProtocol(n)
	}
	return true
}

// markProtocol marks the identifier-position lines of protocol nodes —
// struct tags, composite-literal keys, index operands and case labels —
// as exempt.
func (c *scatteredCollector) markProtocol(n ast.Node) {
	switch n := n.(type) {
	case *ast.Field:
		markScatteredTag(n.Tag, c.fset, c.exempt)
	case *ast.KeyValueExpr:
		markScatteredIdentifiers(c.fset, c.exempt, n.Key)
	case *ast.IndexExpr:
		markScatteredIdentifiers(c.fset, c.exempt, n.Index)
	case *ast.CaseClause:
		markScatteredIdentifiers(c.fset, c.exempt, n.List...)
	}
}

// collectSpecs records the plain-literal initializers of one const or var
// declaration as decl hits; topLevel marks the referencable top-level forms
// that register the corpus constant. A computed initializer's inner
// literals descend as ordinary sites during the main walk.
func (c *scatteredCollector) collectSpecs(decl *ast.GenDecl, topLevel bool, minLength, minValue int) {
	for _, spec := range decl.Specs {
		vs := spec.(*ast.ValueSpec)
		if !topLevel {
			c.collectAlias(vs)
		}
		for i, value := range vs.Values {
			lit, ok := value.(*ast.BasicLit)
			if !ok || c.seen[lit] {
				continue
			}
			c.seen[lit] = true
			hit, ok := scatteredHitOf(lit, c.fset, nil, minLength, minValue)
			if !ok {
				continue
			}
			hit.line = c.fset.Position(lit.Pos()).Line
			hit.isDecl = true
			hit.topLevel = topLevel
			hit.name = vs.Names[i].Name
			c.hits = append(c.hits, hit)
		}
	}
}

// collectAlias records the pure value aliases of one const spec's
// initializers: a bare identifier naming another constant. Called with
// topLevel=false only — the Inspect walk sees every const declaration
// (top-level and nested) exactly once, while the Decls loop overlaps it
// for the registry pass and var specs never alias. true/false/iota are
// computed, not aliases; a missing initializer (an iota-block
// continuation) has nothing to alias.
func (c *scatteredCollector) collectAlias(vs *ast.ValueSpec) {
	for i, value := range vs.Values {
		id, ok := value.(*ast.Ident)
		if !ok || scatteredPredeclaredConsts[id.Name] {
			continue
		}
		c.aliases = append(c.aliases, scatteredAlias{
			name:   vs.Names[i].Name,
			target: id.Name,
			line:   c.fset.Position(vs.Pos()).Line,
		})
	}
}

// collectLiteral renders one basic literal as a corpus-comparable hit
// unless it is a declaration initializer (already collected) or sits in a
// protocol position.
func (c *scatteredCollector) collectLiteral(lit *ast.BasicLit, minLength, minValue int) {
	if c.seen[lit] {
		return
	}
	if hit, ok := scatteredHitOf(lit, c.fset, c.exempt, minLength, minValue); ok {
		hit.line = c.fset.Position(lit.Pos()).Line
		c.hits = append(c.hits, hit)
	}
}

// markScatteredTag marks a struct-tag literal's line as exempt (Go
// adaptation: tags hold protocol wire names and must stay literals).
func markScatteredTag(tag *ast.BasicLit, fset *token.FileSet, exempt map[int]bool) {
	if tag != nil {
		exempt[fset.Position(tag.Pos()).Line] = true
	}
}

// markScatteredIdentifiers marks the lines of direct string-literal keys,
// index operands and case labels as exempt: protocol identifiers, not magic
// constants (same rule as magic-constants).
func markScatteredIdentifiers(fset *token.FileSet, exempt map[int]bool, exprs ...ast.Expr) {
	for _, expr := range exprs {
		if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			exempt[fset.Position(lit.Pos()).Line] = true
		}
	}
}

// scatteredHitOf renders one basic literal as a corpus-comparable hit:
// strings by unquoted value against minLength, ints and floats by numeric
// value against minValue, in canonical form so 1_000, 0x3E8 and 1000 are
// one fact. Char and imaginary literals have no upstream counterpart and
// never count.
func scatteredHitOf(lit *ast.BasicLit, fset *token.FileSet, exempt map[int]bool, minLength, minValue int) (scatteredHit, bool) {
	if exempt[fset.Position(lit.Pos()).Line] {
		return scatteredHit{}, false
	}
	key, display, raw, ok := scatteredLiteralKey(lit)
	if !ok {
		return scatteredHit{}, false
	}
	if lit.Kind == token.STRING && len(raw) < minLength {
		return scatteredHit{}, false
	}
	if lit.Kind != token.STRING && !scatteredMagnitudeCounts(raw, minValue) {
		return scatteredHit{}, false
	}
	return scatteredHit{key: key, display: display}, true
}

// scatteredLiteralKey returns the canonical corpus key and display form of
// a plain string, int or float literal: the unquoted string, the decimal
// numeric value (1_000, 0x3E8 and 1000 share one key), or false for char
// and imaginary literals. raw is the filterable value: the unquoted string
// or the numeric lexeme.
func scatteredLiteralKey(lit *ast.BasicLit) (key, display, raw string, ok bool) {
	switch lit.Kind {
	case token.STRING:
		return scatteredStringKey(lit)
	case token.INT:
		return scatteredIntKey(lit)
	case token.FLOAT:
		return scatteredFloatKey(lit)
	}
	return "", "", "", false
}

// scatteredStringKey keys a string literal by its unquoted value; the raw
// lexeme is the fallback when unquoting fails.
func scatteredStringKey(lit *ast.BasicLit) (string, string, string, bool) {
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		value = lit.Value
	}
	return "s:" + value, scatteredDisplay(value), value, true
}

// scatteredIntKey keys an integer literal by its decimal value — 1_000,
// 0x3E8 and 1000 share one key; lexemes beyond int64 group by spelling.
func scatteredIntKey(lit *ast.BasicLit) (string, string, string, bool) {
	if v, err := strconv.ParseInt(lit.Value, 0, 64); err == nil {
		return "i:" + strconv.FormatInt(v, 10), strconv.FormatInt(v, 10), lit.Value, true
	}
	return "i:" + lit.Value, lit.Value, lit.Value, true
}

// scatteredFloatKey keys a float literal by its canonical value — 1_000.5
// and 1000.50 share one key; unparsable lexemes group by spelling.
func scatteredFloatKey(lit *ast.BasicLit) (string, string, string, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(lit.Value, "_", ""), 64)
	if err != nil {
		return "d:" + lit.Value, lit.Value, lit.Value, true
	}
	return "d:" + strconv.FormatFloat(v, 'g', -1, 64), strconv.FormatFloat(v, 'g', -1, 64), lit.Value, true
}

// scatteredMagnitudeCounts reports whether a numeric lexeme's magnitude is
// above minValue — smaller numbers are coincidence (0, 1, 10, 100 happen
// everywhere).
func scatteredMagnitudeCounts(lexeme string, minValue int) bool {
	if v, err := strconv.ParseInt(lexeme, 0, 64); err == nil {
		return v > int64(minValue) || v < -int64(minValue)
	}
	if v, err := strconv.ParseFloat(strings.ReplaceAll(lexeme, "_", ""), 64); err == nil {
		return math.Abs(v) > float64(minValue)
	}
	return true
}

// scatteredDisplay renders a string value quoted and truncated in messages.
func scatteredDisplay(value string) string {
	if runes := []rune(value); len(runes) > 40 {
		return "'" + string(runes[:40]) + "…'"
	}
	return "'" + value + "'"
}

// scatteredViolations turns the corpus-wide literal map into violations.
// Constant regression: a named declaration for the value already exists —
// every re-introduction (bare literal or duplicate declaration, any
// occurrence count, including the declaring file) flags immediately,
// naming the constant and its file:line; only the winning declaration
// itself is exempt. Scattered literal: no constant exists and the value
// reached minFiles distinct files — one violation per occurrence. Values
// in sorted order for deterministic output (ported from crap4dart
// e766d68).
func scatteredViolations(literals map[string]*scatteredLiteral, constants map[string]NamedScatteredConstant, minFiles int) []ScatteredConstantsViolation {
	var violations []ScatteredConstantsViolation
	for _, key := range scatteredSortedKeys(literals) {
		lit := literals[key]
		if shared, ok := constants[key]; ok {
			violations = append(violations, scatteredRegressionViolations(lit, shared)...)
			continue
		}
		files := scatteredDistinctFiles(lit.sites)
		if files < minFiles {
			continue
		}
		scope := fmt.Sprintf("%d files (%d occurrences)", files, len(lit.sites))
		for _, site := range lit.sites {
			violations = append(violations, ScatteredConstantsViolation{
				Path: site.file, Line: site.line,
				Message: fmt.Sprintf("literal %s repeats in %s — extract a shared constant and reference it from every site; verify all occurrences mean the same thing first", lit.display, scope),
			})
		}
	}
	return violations
}

// scatteredRegressionViolations flags every re-introduction of a value
// whose named declaration already exists: bare literals and duplicate
// declarations, any occurrence count — only the winning declaration itself
// is exempt (ported from crap4dart e766d68's constant-regression rule).
func scatteredRegressionViolations(lit *scatteredLiteral, shared NamedScatteredConstant) []ScatteredConstantsViolation {
	var violations []ScatteredConstantsViolation
	for _, site := range lit.sites {
		if site.isDecl && site.file == shared.File && site.line == shared.Line {
			continue
		}
		violations = append(violations, ScatteredConstantsViolation{
			Path: site.file, Line: site.line,
			Message: fmt.Sprintf("hardcoded %s duplicates constant `%s` (%s:%d) — reference the constant instead of repeating the literal", lit.display, shared.Name, shared.File, shared.Line),
		})
	}
	return violations
}

// scatteredAliasViolations flags private pure value aliases (the upstream
// a314b485 'aliases' rule): an unexported const initialized to another
// constant's bare identifier adds a name without information — reference
// the constant directly. Exported declarations are exempt (re-export shims
// are a deliberate API shape), the blank identifier is exempt (it discards
// the value; it introduces no name), and computed initializers never reach
// here.
func scatteredAliasViolations(aliases []scatteredAlias) []ScatteredConstantsViolation {
	var violations []ScatteredConstantsViolation
	for _, a := range aliases {
		if ast.IsExported(a.name) || a.name == "_" {
			continue
		}
		violations = append(violations, ScatteredConstantsViolation{
			Path: a.file, Line: a.line,
			Message: fmt.Sprintf("alias `%s` adds no information — reference `%s` directly", a.name, a.target),
		})
	}
	return violations
}

// scatteredDistinctFiles counts the distinct files holding sites.
func scatteredDistinctFiles(sites []scatteredSite) int {
	files := map[string]bool{}
	for _, s := range sites {
		files[s.file] = true
	}
	return len(files)
}

// scatteredSortedKeys returns the literal map's keys in sorted order.
func scatteredSortedKeys(literals map[string]*scatteredLiteral) []string {
	keys := make([]string, 0, len(literals))
	for key := range literals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
