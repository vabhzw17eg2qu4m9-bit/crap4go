package main

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Mutation gate defaults (ported from crap4dart c865ec3, §11.22).
const (
	defaultMutationMinScore       = 0.8
	defaultMutationMaxSurvivors   = 0
	defaultMutationTimeoutSeconds = 60
	defaultMutationMaxMutants     = 500
)

// mutationOperator is one row of the declarative operator table: replacing
// original with mutant produces one mutant, tagged category. Adapted from the
// unclebob/mutator research conclusions.
type mutationOperator struct {
	category string
	original string
	mutant   string
}

// mutationOperatorCategories lists the operator categories in stable order;
// the `operators` flag accepts any subset of these names.
var mutationOperatorCategories = []string{
	"arithmetic", "boundary", "equality", "logical", "boolean", "constant", "unary",
}

// mutationOperators is the declarative operator table. Binary rows match
// BinaryExpression operator lexemes, boolean rows the `true`/`false`
// predefined identifiers, constant rows the integer literals `0`/`1` (plus
// the special `-1` -> `0` row implemented in addUnary). Dart's `/` -> `~/`
// truncating-division row has no Go counterpart: Go `/` on integers already
// truncates, so the `*` <-> `/` rows carry the arithmetic set.
var mutationOperators = []mutationOperator{
	{category: "arithmetic", original: "+", mutant: "-"},
	{category: "arithmetic", original: "-", mutant: "+"},
	{category: "arithmetic", original: "*", mutant: "/"},
	{category: "arithmetic", original: "/", mutant: "*"},
	{category: "boundary", original: ">", mutant: ">="},
	{category: "boundary", original: ">=", mutant: ">"},
	{category: "boundary", original: "<", mutant: "<="},
	{category: "boundary", original: "<=", mutant: "<"},
	{category: "boundary", original: ">", mutant: "<"},
	{category: "boundary", original: "<", mutant: ">"},
	{category: "boundary", original: ">=", mutant: "<="},
	{category: "boundary", original: "<=", mutant: ">="},
	{category: "equality", original: "==", mutant: "!="},
	{category: "equality", original: "!=", mutant: "=="},
	{category: "logical", original: "&&", mutant: "||"},
	{category: "logical", original: "||", mutant: "&&"},
	{category: "boolean", original: "true", mutant: "false"},
	{category: "boolean", original: "false", mutant: "true"},
	{category: "constant", original: "0", mutant: "1"},
	{category: "constant", original: "1", mutant: "0"},
}

// MutationSite is one place in a source file where an operator replacement
// applies. Offset and Length come from go/token positions of the parsed
// (unresolved) AST — never from regex.
type MutationSite struct {
	File     string
	Line     int
	Method   string
	Category string
	Original string
	Mutant   string
	Offset   int
	Length   int
}

// applyTo returns src with this site's span replaced by the mutant lexeme.
func (s MutationSite) applyTo(src []byte) []byte {
	out := make([]byte, 0, len(src)+len(s.Mutant)-s.Length)
	out = append(out, src[:s.Offset]...)
	out = append(out, s.Mutant...)
	out = append(out, src[s.Offset+s.Length:]...)
	return out
}

// String renders the site the way the upstream gate does, e.g.
// `main.go:12: + -> - (arithmetic)`.
func (s MutationSite) String() string {
	return fmt.Sprintf("%s:%d: %s -> %s (%s)", s.File, s.Line, s.Original, s.Mutant, s.Category)
}

// mutationDetector collects mutation sites for one parsed file. Only
// executable code is walked: declaration positions (field names, labels,
// type names) can never be expression-position `true`/`false`/`0`/`1`, so
// struct fields, labels and selectors are naturally excluded.
type mutationDetector struct {
	tf         *token.File
	path       string
	methods    []MethodDescriptor
	categories map[string]bool
	sites      []MutationSite
}

// detectMutationSites parses nothing itself: it walks the already-parsed file
// and returns the sites for the enabled categories, sorted by offset (the
// deterministic cap order is file order, then offset).
func detectMutationSites(path string, fset *token.FileSet, f *ast.File, methods []MethodDescriptor, categories map[string]bool) []MutationSite {
	d := &mutationDetector{
		tf:         fset.File(f.Pos()),
		path:       path,
		methods:    methods,
		categories: categories,
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			d.walkStmt(fn.Body)
		}
	}
	sort.SliceStable(d.sites, func(i, j int) bool { return d.sites[i].Offset < d.sites[j].Offset })
	return d.sites
}

// add records one site spanning [start, end), resolving line, method and
// byte offsets from the file's token positions.
func (d *mutationDetector) add(category, original, mutant string, start, end token.Pos) {
	offset := d.tf.Offset(start)
	line := d.tf.Line(start)
	method := ""
	if m := mutationMethodAt(d.methods, line); m != nil {
		method = m.Name
	}
	d.sites = append(d.sites, MutationSite{
		File:     d.path,
		Line:     line,
		Method:   method,
		Category: category,
		Original: original,
		Mutant:   mutant,
		Offset:   offset,
		Length:   d.tf.Offset(end) - offset,
	})
}

// addBinary matches the operator lexeme against every enabled table row.
func (d *mutationDetector) addBinary(e *ast.BinaryExpr) {
	lexeme := e.Op.String()
	for _, row := range mutationOperators {
		if row.original != lexeme || !d.categories[row.category] {
			continue
		}
		d.add(row.category, row.original, row.mutant, e.OpPos, e.OpPos+token.Pos(len(lexeme)))
	}
}

// addUnary handles the two prefix rows: deleting a `!` (unary) and turning
// `-1` into `0` (constant — the whole `-1` span is replaced).
func (d *mutationDetector) addUnary(e *ast.UnaryExpr) {
	if e.Op == token.NOT && d.categories["unary"] {
		d.add("unary", "!", "", e.OpPos, e.X.Pos())
		return
	}
	d.addNegOne(e)
}

// addNegOne matches the prefix form of the `-1` -> `0` constant row.
func (d *mutationDetector) addNegOne(e *ast.UnaryExpr) {
	if e.Op != token.SUB || !d.categories["constant"] {
		return
	}
	if lit, ok := e.X.(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "1" {
		d.add("constant", "-1", "0", e.OpPos, lit.End())
	}
}

// addIntLiteral matches the integer literals `0` and `1` (constant).
func (d *mutationDetector) addIntLiteral(lit *ast.BasicLit) {
	if lit.Kind != token.INT || !d.categories["constant"] {
		return
	}
	if lit.Value == "0" {
		d.add("constant", "0", "1", lit.Pos(), lit.End())
	} else if lit.Value == "1" {
		d.add("constant", "1", "0", lit.Pos(), lit.End())
	}
}

// addBoolIdent matches the predefined identifiers `true` and `false`
// (boolean). In executable positions these are always the literals.
func (d *mutationDetector) addBoolIdent(id *ast.Ident) {
	if (id.Name != "true" && id.Name != "false") || !d.categories["boolean"] {
		return
	}
	mutant := "false"
	if id.Name == "false" {
		mutant = "true"
	}
	d.add("boolean", id.Name, mutant, id.Pos(), id.End())
}

// walkExpr dispatches one expression node, collecting sites and recursing.
func (d *mutationDetector) walkExpr(e ast.Expr) {
	switch x := e.(type) {
	case *ast.BinaryExpr:
		d.addBinary(x)
		d.walkExpr(x.X)
		d.walkExpr(x.Y)
	case *ast.UnaryExpr:
		d.addUnary(x)
		d.walkExpr(x.X)
	case *ast.BasicLit:
		d.addIntLiteral(x)
	case *ast.Ident:
		d.addBoolIdent(x)
	default:
		d.walkRest(e)
	}
}

// walkRest handles the expression shapes carrying the first ring of children.
func (d *mutationDetector) walkRest(e ast.Expr) {
	switch x := e.(type) {
	case *ast.CallExpr:
		d.walkExpr(x.Fun)
		d.walkExprs(x.Args)
	case *ast.IndexExpr:
		d.walkExpr(x.X)
		d.walkExpr(x.Index)
	case *ast.IndexListExpr:
		d.walkExpr(x.X)
		d.walkExprs(x.Indices)
	case *ast.CompositeLit:
		d.walkExpr(x.Type)
		d.walkExprs(x.Elts)
	case *ast.KeyValueExpr:
		d.walkExpr(x.Key)
		d.walkExpr(x.Value)
	default:
		d.walkRest2(e)
	}
}

// walkRest2 handles the remaining expression shapes.
func (d *mutationDetector) walkRest2(e ast.Expr) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		d.walkExpr(x.X)
	case *ast.StarExpr:
		d.walkExpr(x.X)
	case *ast.SelectorExpr:
		d.walkExpr(x.X) // x.Sel is a field/method name, never a literal site.
	case *ast.TypeAssertExpr:
		d.walkExpr(x.X)
	default:
		d.walkRest3(e)
	}
}

// walkRest3 handles the last expression shapes: slice bounds, function
// literals and type expressions carrying lengths.
func (d *mutationDetector) walkRest3(e ast.Expr) {
	switch x := e.(type) {
	case *ast.SliceExpr:
		d.walkSlice(x)
	case *ast.FuncLit:
		d.walkStmt(x.Body)
	case *ast.ArrayType:
		d.walkExpr(x.Len) // e.g. make([0]int, ...) sizes are constant sites.
	default: // BadExpr and literal-free type expressions: nothing to walk.
	}
}

// walkSlice walks the optional slice bounds.
func (d *mutationDetector) walkSlice(x *ast.SliceExpr) {
	d.walkExpr(x.X)
	if x.Low != nil {
		d.walkExpr(x.Low)
	}
	if x.High != nil {
		d.walkExpr(x.High)
	}
	if x.Max != nil {
		d.walkExpr(x.Max)
	}
}

// walkExprs walks every expression of a list.
func (d *mutationDetector) walkExprs(exprs []ast.Expr) {
	for _, e := range exprs {
		d.walkExpr(e)
	}
}

// walkStmts walks every statement of a list.
func (d *mutationDetector) walkStmts(stmts []ast.Stmt) {
	for _, s := range stmts {
		d.walkStmt(s)
	}
}

// walkStmt dispatches one statement node (nil statements are no-ops).
func (d *mutationDetector) walkStmt(s ast.Stmt) {
	switch x := s.(type) {
	case *ast.IfStmt:
		d.walkStmt(x.Init)
		d.walkExpr(x.Cond)
		d.walkStmt(x.Body)
		d.walkStmt(x.Else)
	case *ast.ForStmt:
		d.walkStmt(x.Init)
		d.walkExpr(x.Cond)
		d.walkStmt(x.Post)
		d.walkStmt(x.Body)
	case *ast.RangeStmt:
		d.walkExpr(x.X) // x.Key/x.Value are declarations, never literals.
		d.walkStmt(x.Body)
	case *ast.SwitchStmt:
		d.walkStmt(x.Init)
		d.walkExpr(x.Tag)
		d.walkStmt(x.Body)
	case *ast.TypeSwitchStmt:
		d.walkStmt(x.Init)
		d.walkStmt(x.Assign)
		d.walkStmt(x.Body)
	default:
		d.walkNesting(s)
	}
}

// walkNesting handles the block-shaped statements.
func (d *mutationDetector) walkNesting(s ast.Stmt) {
	switch x := s.(type) {
	case *ast.BlockStmt:
		d.walkStmts(x.List)
	case *ast.LabeledStmt:
		d.walkStmt(x.Stmt) // x.Label is a label name, never a literal site.
	case *ast.CaseClause:
		d.walkExprs(x.List)
		d.walkStmts(x.Body)
	case *ast.CommClause:
		d.walkStmt(x.Comm)
		d.walkStmts(x.Body)
	case *ast.SelectStmt:
		d.walkStmt(x.Body)
	default:
		d.walkSimpleStmt(s)
	}
}

// walkSimpleStmt handles the expression-bearing leaf statements.
func (d *mutationDetector) walkSimpleStmt(s ast.Stmt) {
	switch x := s.(type) {
	case *ast.ExprStmt:
		d.walkExpr(x.X)
	case *ast.IncDecStmt:
		d.walkExpr(x.X)
	case *ast.AssignStmt:
		d.walkExprs(x.Lhs)
		d.walkExprs(x.Rhs)
	case *ast.ReturnStmt:
		d.walkExprs(x.Results)
	default:
		d.walkCallStmt(s)
	}
}

// walkCallStmt handles the communication and call statements.
func (d *mutationDetector) walkCallStmt(s ast.Stmt) {
	switch x := s.(type) {
	case *ast.SendStmt:
		d.walkExpr(x.Chan)
		d.walkExpr(x.Value)
	case *ast.GoStmt:
		d.walkExpr(x.Call)
	case *ast.DeferStmt:
		d.walkExpr(x.Call)
	default: // EmptyStmt and BranchStmt carry no expressions.
	}
}

// mutationMethodAt returns the method whose line range contains line, or nil.
func mutationMethodAt(methods []MethodDescriptor, line int) *MethodDescriptor {
	for i := range methods {
		if line >= methods[i].StartLine && line <= methods[i].EndLine {
			return &methods[i]
		}
	}
	return nil
}

// mutantOutcome classifies one per-mutant test re-run (ported from
// crap4dart's MutantOutcome).
type mutantOutcome int

const (
	mutantKilled mutantOutcome = iota
	mutantSurvived
	mutantTimeout
)

// mutationSiteOutcome pairs a site with its per-mutant outcome.
type mutationSiteOutcome struct {
	site    MutationSite
	outcome mutantOutcome
}

// mutationRunResult holds every per-site outcome of one engine invocation.
type mutationRunResult struct {
	outcomes []mutationSiteOutcome
}

// killedCount counts mutants the suite killed (compile errors included).
func (r mutationRunResult) killedCount() int {
	killed := 0
	for _, o := range r.outcomes {
		if o.outcome == mutantKilled {
			killed++
		}
	}
	return killed
}

// survivedCount counts mutants the suite noticed nothing about, timeouts
// included.
func (r mutationRunResult) survivedCount() int {
	return len(r.outcomes) - r.killedCount()
}

// score returns killed / (killed + survived); 1.0 when no mutant ran.
func (r mutationRunResult) score() float64 {
	if len(r.outcomes) == 0 {
		return 1.0
	}
	return float64(r.killedCount()) / float64(len(r.outcomes))
}

// survivorsByCategory groups surviving mutants by operator category.
func (r mutationRunResult) survivorsByCategory() map[string]int {
	counts := map[string]int{}
	for _, o := range r.outcomes {
		if o.outcome != mutantKilled {
			counts[o.site.Category]++
		}
	}
	return counts
}

// mutantTestRunner runs the project's test suite against the currently
// mutated tree; inject a fake in tests instead of running real suites.
type mutantTestRunner func(projectRoot string, timeout time.Duration) (mutantOutcome, error)

// runMutationEngine mutates every site one at a time and runs the project's
// tests against each mutant, restoring the file afterwards. The first
// spawn/unavailable error propagates to the caller.
func runMutationEngine(projectRoot string, sites []MutationSite, timeout time.Duration, runner mutantTestRunner) (mutationRunResult, error) {
	var outcomes []mutationSiteOutcome
	for _, site := range sites {
		outcome, err := runSingleMutant(site, projectRoot, timeout, runner)
		if err != nil {
			return mutationRunResult{}, err
		}
		outcomes = append(outcomes, mutationSiteOutcome{site: site, outcome: outcome})
	}
	return mutationRunResult{outcomes: outcomes}, nil
}

// runSingleMutant applies one mutation in place, runs the test runner, and
// always restores the original content — also after timeouts and errors.
func runSingleMutant(site MutationSite, projectRoot string, timeout time.Duration, runner mutantTestRunner) (mutantOutcome, error) {
	src, err := os.ReadFile(site.File)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(site.File, site.applyTo(src), 0o644); err != nil {
		return 0, err
	}
	defer func() { _ = os.WriteFile(site.File, src, 0o644) }() // always restore
	return runner(projectRoot, timeout)
}

// mutantTestRunnerOS is the default per-mutant runner: `go test ./...` with a
// kill-on-timeout guard. Discarded output keeps the child from blocking.
func mutantTestRunnerOS(projectRoot string, timeout time.Duration) (mutantOutcome, error) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = projectRoot
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("go test unavailable: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			// Non-zero exit: test failure or compile error — both kill the
			// mutant (`go test` refuses to run a broken build).
			return mutantKilled, nil
		}
		return mutantSurvived, nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		// A timeout means the suite did not kill the mutant
		// deterministically, so it counts as survived.
		return mutantTimeout, nil
	}
}

// mutationOptions holds the parsed `mutation` subcommand configuration.
type mutationOptions struct {
	coveragePath   string
	minScore       float64
	maxSurvivors   int
	timeoutSeconds int
	maxMutants     int
	operators      []string
	excludes       []string
	paths          []string
}

// RunMutationCommand implements `crap4go mutation [--coverage PATH]
// [--min-score F] [--max-survivors N] [--timeout-seconds N] [--max-mutants N]
// [--operators LIST] [--exclude GLOB]... [paths...]`: it detects mutation
// sites on the parsed ASTs of the selection, keeps only sites covered by the
// coverage profile, and mutates them one at a time, re-running `go test`
// per mutant. A non-zero exit kills the mutant (compile errors included), a
// timeout counts as survived. Exit code 2 when the score is below --min-score
// or more than --max-survivors mutants survive; one violation line is printed
// per surviving mutant. Skips (exit 0) when there is no coverage data, no
// test files, or `go` is unavailable.
func RunMutationCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseMutationFlags(args, stderr)
	if !ok {
		return code
	}
	return runMutationGate(opts, root, stdout, stderr)
}

// parseMutationFlags parses the mutation flags plus positional paths.
// ok=false means a usage error (already reported to stderr); the code is the
// exit code to return.
func parseMutationFlags(args []string, stderr io.Writer) (mutationOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go mutation", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := mutationOptions{
		coveragePath:   "coverage.out",
		minScore:       defaultMutationMinScore,
		maxSurvivors:   defaultMutationMaxSurvivors,
		timeoutSeconds: defaultMutationTimeoutSeconds,
		maxMutants:     defaultMutationMaxMutants,
	}
	fs.StringVar(&opts.coveragePath, "coverage", opts.coveragePath, "coverage profile path")
	fs.Float64Var(&opts.minScore, "min-score", opts.minScore, "minimum mutation score (killed/(killed+survived))")
	fs.IntVar(&opts.maxSurvivors, "max-survivors", opts.maxSurvivors, "maximum tolerated surviving mutants")
	fs.IntVar(&opts.timeoutSeconds, "timeout-seconds", opts.timeoutSeconds, "per-mutant test timeout; a timeout counts as survived")
	fs.IntVar(&opts.maxMutants, "max-mutants", opts.maxMutants, "cap on executed covered sites (file order, then offset)")
	var operators string
	fs.StringVar(&operators, "operators", "", "comma-separated operator categories (default: all)")
	excludes := &stringSlice{}
	fs.Var(excludes, "exclude", "glob of project-relative paths to skip (repeatable)")
	if err := fs.Parse(args); err != nil {
		return mutationOptions{}, 1, false
	}
	if !validMutationOptions(opts) {
		fmt.Fprintln(stderr, "--min-score, --max-survivors, --max-mutants and --timeout-seconds must be valid")
		return mutationOptions{}, 1, false
	}
	if operators != "" {
		opts.operators = strings.Split(operators, ",")
	}
	opts.excludes = excludes.slice()
	opts.paths = fs.Args()
	return opts, 0, true
}

// validMutationOptions reports whether the numeric flags are sane: the score
// within [0, 1], survivors non-negative, the timeout and mutant cap positive.
func validMutationOptions(opts mutationOptions) bool {
	return opts.minScore >= 0 && opts.minScore <= 1 &&
		opts.maxSurvivors >= 0 && opts.maxMutants >= 1 && opts.timeoutSeconds >= 1
}

// runMutationGate executes the gate after flag parsing. Skips are printed to
// stdout with exit 0 (a skipped gate never fails the run), violations exit 2.
func runMutationGate(opts mutationOptions, root string, stdout, stderr io.Writer) int {
	files, categories, code, ok := mutationSetup(opts, root, stderr)
	if !ok {
		return code
	}
	cov, code, skip := mutationPrereqs(root, opts.coveragePath, stdout, stderr)
	if skip {
		return code
	}
	sites, uncovered, err := mutationCoveredSites(files, root, cov, categories, opts.maxMutants, opts.excludes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(sites) == 0 {
		printNoSites(uncovered, stdout)
		return 0
	}
	timeout := time.Duration(opts.timeoutSeconds) * time.Second
	result, err := runMutationEngine(root, sites, timeout, mutantTestRunnerOS)
	if err != nil {
		return mutationEngineError(err, stdout, stderr)
	}
	return mutationVerdict(result, uncovered, opts, root, stdout)
}

// mutationPrereqs loads the coverage profile and checks the project has test
// files. skip=true means the gate cannot run: code is 0 for an absent
// profile or a test-less project (skip message printed to stdout) and 1 for
// a malformed profile or unreadable tree (already reported to stderr).
func mutationPrereqs(root, covPath string, stdout, stderr io.Writer) (map[string]*FileCoverage, int, bool) {
	cov, err := ParseCoverProfile(resolveCoveragePath(covPath, root))
	if os.IsNotExist(err) {
		fmt.Fprintf(stdout, "skip mutation: no coverage data at %s\n", covPath)
		return nil, 0, true
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1, true
	}
	tests, err := walkTestFiles(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1, true
	}
	if len(tests) == 0 {
		fmt.Fprintln(stdout, "skip mutation: no test files")
		return nil, 0, true
	}
	return cov, 0, false
}

// mutationSetup resolves the enabled operator categories and the scan file
// set. ok=false means a fatal error already reported to stderr.
func mutationSetup(opts mutationOptions, root string, stderr io.Writer) ([]string, map[string]bool, int, bool) {
	categories, err := mutationCategories(opts.operators)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, 1, false
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, 1, false
	}
	return excludeMutationFiles(files, root, opts.excludes), categories, 0, true
}

// mutationEngineError turns an engine failure into an exit code: a missing
// `go` binary skips the gate; anything else is fatal.
func mutationEngineError(err error, stdout, stderr io.Writer) int {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		fmt.Fprintln(stdout, "skip mutation:", err)
		return 0
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// mutationCategories validates the operator subset against the table's
// categories; an empty spec means all categories.
func mutationCategories(spec []string) (map[string]bool, error) {
	all := map[string]bool{}
	for _, name := range mutationOperatorCategories {
		all[name] = true
	}
	if len(spec) == 0 {
		return all, nil
	}
	chosen := map[string]bool{}
	for _, name := range spec {
		if !all[name] {
			return nil, fmt.Errorf("unknown operator category %q (valid: %s)", name, strings.Join(mutationOperatorCategories, ", "))
		}
		chosen[name] = true
	}
	return chosen, nil
}

// excludeMutationFiles drops files whose project-relative path matches any
// exclude glob; with no patterns the selection passes unchanged.
func excludeMutationFiles(files []string, root string, patterns []string) []string {
	if len(patterns) == 0 {
		return files
	}
	kept := make([]string, 0, len(files))
	for _, f := range files {
		if !matchesAnyGlob(relPath(root, f), patterns) {
			kept = append(kept, f)
		}
	}
	return kept
}

// mutationCoveredSites detects sites in every selected file and keeps the
// covered ones, capped at maxMutants (file order, then offset — the cap is
// deterministic). Returns the capped covered sites plus the uncovered count.
func mutationCoveredSites(files []string, root string, cov map[string]*FileCoverage, categories map[string]bool, maxMutants int, excludes []string) ([]MutationSite, int, error) {
	var sites []MutationSite
	uncovered := 0
	for _, file := range files {
		if len(sites) >= maxMutants {
			break
		}
		if matchesAnyGlob(relPath(root, file), excludes) {
			continue
		}
		fileSites, fileUncovered, err := mutationFileSites(file, cov, categories, maxMutants-len(sites))
		if err != nil {
			return nil, 0, err
		}
		uncovered += fileUncovered
		sites = append(sites, fileSites...)
	}
	return sites, uncovered, nil
}

// mutationFileSites detects one file's sites and keeps the covered ones up to
// budget, returning them plus the file's uncovered count.
func mutationFileSites(file string, cov map[string]*FileCoverage, categories map[string]bool, budget int) ([]MutationSite, int, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, err
	}
	methods, err := ExtractMethods(file, src)
	if err != nil {
		return nil, 0, err
	}
	var executed, unexecuted map[int]bool
	if fc := lookupCoverage(cov, file); fc != nil {
		executed, unexecuted = mutationLineFlags(fc)
	}
	var covered []MutationSite
	uncovered := 0
	for _, site := range mutationParseSites(file, src, methods, categories) {
		if len(covered) >= budget {
			break
		}
		if mutationCovered(site, methods, executed, unexecuted) {
			covered = append(covered, site)
		} else {
			uncovered++
		}
	}
	return covered, uncovered, nil
}

// mutationParseSites parses src and detects its mutation sites; a file that
// fails to parse has no sites (ExtractMethods already surfaced the error).
func mutationParseSites(file string, src []byte, methods []MethodDescriptor, categories map[string]bool) []MutationSite {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return nil
	}
	return detectMutationSites(file, fset, f, methods, categories)
}

// mutationLineFlags flattens cover-profile blocks into per-line flags: a line
// is executed when some positive-count block covers it and unexecuted when
// only zero-count blocks cover it (the standard `go tool cover` reading of
// the block-based profile).
func mutationLineFlags(fc *FileCoverage) (executed, unexecuted map[int]bool) {
	executed = map[int]bool{}
	unexecuted = map[int]bool{}
	for _, b := range fc.Blocks {
		target := &executed
		if b.Count == 0 {
			target = &unexecuted
		}
		for line := b.StartLine; line <= b.EndLine; line++ {
			(*target)[line] = true
		}
	}
	return executed, unexecuted
}

// mutationCovered reports whether a site enters the score: its containing
// method must have been executed and the site's line must not be recorded as
// unexecuted. Sites outside every method fall back to strict raw line
// coverage; a file absent from the profile is never covered.
func mutationCovered(site MutationSite, methods []MethodDescriptor, executed, unexecuted map[int]bool) bool {
	if executed == nil {
		return false
	}
	if m := mutationMethodAt(methods, site.Line); m != nil {
		return mutationMethodExecuted(m, executed) && mutationLineHit(site.Line, executed, unexecuted)
	}
	return executed[site.Line]
}

// mutationMethodExecuted reports whether any line inside the method's range
// was executed.
func mutationMethodExecuted(m *MethodDescriptor, executed map[int]bool) bool {
	for line := m.StartLine; line <= m.EndLine; line++ {
		if executed[line] {
			return true
		}
	}
	return false
}

// mutationLineHit reports whether the site line counts as hit: positive
// coverage wins, an explicitly unexecuted line loses, lines absent from the
// profile count as covered.
func mutationLineHit(line int, executed, unexecuted map[int]bool) bool {
	if executed[line] {
		return true
	}
	return !unexecuted[line]
}

// printNoSites reports the empty-scan outcome.
func printNoSites(uncovered int, stdout io.Writer) {
	if uncovered == 0 {
		fmt.Fprintln(stdout, "no mutation sites")
		return
	}
	fmt.Fprintf(stdout, "no covered mutation sites (%d uncovered)\n", uncovered)
}

// mutationVerdict prints the summary and, when the gate fails, one violation
// line per surviving mutant plus the per-category breakdown.
func mutationVerdict(result mutationRunResult, uncovered int, opts mutationOptions, root string, stdout io.Writer) int {
	score := result.score()
	summary := fmt.Sprintf("mutation score %.1f%% (%d killed, %d survived, %d uncovered)",
		score*100, result.killedCount(), result.survivedCount(), uncovered)
	if score >= opts.minScore && result.survivedCount() <= opts.maxSurvivors {
		fmt.Fprintln(stdout, summary)
		return 0
	}
	for _, o := range result.outcomes {
		if o.outcome != mutantKilled {
			fmt.Fprintf(stdout, "%s:%d: survived %s mutant `%s` -> `%s`%s\n",
				relPath(root, o.site.File), o.site.Line, o.site.Category,
				o.site.Original, o.site.Mutant, mutationMethodSuffix(o.site.Method))
		}
	}
	fmt.Fprintf(stdout, "%s; survivors by category: %s\n", summary, mutationCategorySummary(result.survivorsByCategory()))
	return 2
}

// mutationMethodSuffix renders the optional ` in (T)Method` message tail.
func mutationMethodSuffix(method string) string {
	if method == "" {
		return ""
	}
	return " in " + method
}

// mutationCategorySummary renders the survivor breakdown as sorted `k=v`
// pairs.
func mutationCategorySummary(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}
