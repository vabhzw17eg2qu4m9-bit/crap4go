package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"regexp"
	"sort"
	"strings"
)

// The `implicit-state` gate, ported from crap4dart 34e2163080's
// implicit_state: surfaces *implicit finite state machines* — lifecycle
// logic spread over boolean flag clusters, multi-mutator status fields
// and conditions that branch on flag combinations. All three make
// invalid states representable and transitions unauditable; the fix is
// an explicit model (enum + exhaustive switch).
//
// Detection is name-shape based on parsed (unresolved) sources:
//
//  1. Scattered lifecycle flag — a mutable bool field whose name matches
//     the lifecycle shape (is/has/was/will/should/can prefix + Capital,
//     or a lifecycle keyword suffix like `Loaded`, `Ready`) assigned from
//     at least minMutators distinct funcs.
//  2. Scattered status field — a field named status/state/phase/stage/
//     mode/lifecycle (any type) assigned from at least minMutators
//     distinct funcs.
//  3. Boolean-cluster FSM — at least minFlags matching mutable bool
//     fields, at least one of them assigned from at least minMutators
//     distinct funcs.
//  4. Flag-combination condition — an `if` condition reading two or more
//     declared lifecycle flags via the method's receiver (`r.isLoading &&
//     r.hasFailed`); selections through other objects never count.
//
// Go class analog: a struct type declaration plus its methods. Member
// scope — the funcs whose assignments and if-conditions are counted — is
// methods with a receiver of the struct plus plain funcs whose result is
// the struct/*struct (the New* factory, the constructor analog);
// package-level vars are excluded like upstream statics (struct fields
// are the only instance-field shape). Upstream counts assignment
// expressions only, so composite-literal field keys (`&T{isLoading:
// true}`) never count — ported exactly: only assignment statements
// count, and Go's bare identifiers name package vars or locals, never
// fields (upstream's implicit-this SimpleIdentifier target has no host
// shape). Like string-hazards, the upstream `severity` config key maps
// to the `--severity` flag — warning (default) prints findings and exits
// 0, error exits 2. Findings are ignorable via the shared ignore-marker
// system (kebab-case id `implicit-state`).
//
// Divergence note (upstream code vs spec §11.30): upstream's finish()
// hardcodes `>= 2` for the cluster's scattered check while the spec and
// the min_mutators knob say min_mutators — this port follows the spec
// and uses minMutators (the two agree at the defaults 3/2).

// stateFlagName matches the lifecycle-flag name shape: an
// is/has/was/will/should/can prefix (`isLoading`, `hasFailed`) or a
// lifecycle keyword suffix (`shardLoaded`, `engineReady`) — the
// camelCase shapes as-is.
var stateFlagName = regexp.MustCompile(
	`^(?:is|has|was|will|should|can)[A-Z]` +
		`|(?:Loaded|Started|Stopped|Disposed|Pending|Ready|Done|Failed` +
		`|Scheduled|Driving|Running|Closed|Opened|Initialized|Connected` +
		`|Completed|Submitted|Finished|Canceled|Cancelled|Active|Busy` +
		`|Alive|Mounted|Broken|Dirty|Locked|Expired|Cached)$`)

// statusFieldName matches a whole-name lifecycle stage (compared
// lowercased): `status`, `state`, `phase`, `stage`, `mode`, `lifecycle`.
var statusFieldName = regexp.MustCompile(`^(?:status|state|phase|stage|mode|lifecycle)$`)

// Implicit-state defaults, ported from crap4dart 34e2163080.
const (
	defaultImplicitMinFlags    = 3
	defaultImplicitMinMutators = 2
)

// ImplicitStateViolation is one implicit-state finding.
type ImplicitStateViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v ImplicitStateViolation) ignorePath() string { return v.Path }

func (v ImplicitStateViolation) ignoreLine() int { return v.Line }

// implicitStateOptions holds the parsed implicit-state CLI configuration.
type implicitStateOptions struct {
	paths       []string
	fail        bool
	minFlags    int
	minMutators int
}

// RunImplicitStateCommand implements `crap4go implicit-state
// [--severity warning|error] [--min-flags N] [--min-mutators N]
// [paths...]`: it prints one line per finding plus a summary, exits 0 on
// findings at the default warning severity, and 2 with `--severity
// error` (ported from crap4dart 34e2163080).
func RunImplicitStateCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseImplicitStateFlags(args, stderr)
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
	violations, structs := CheckImplicitState(files, root, opts.minFlags, opts.minMutators)
	violations = filterIgnored("implicit-state", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d implicit-state findings (%d structs checked)\n", len(violations), structs)
		if opts.fail {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "%d structs checked, no implicit state machines\n", structs)
	return 0
}

// parseImplicitStateFlags parses the implicit-state flags plus positional
// paths. ok=false means a usage error (already reported to stderr); the
// code is the exit code to return.
func parseImplicitStateFlags(args []string, stderr io.Writer) (implicitStateOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go implicit-state", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := implicitStateOptions{
		minFlags:    defaultImplicitMinFlags,
		minMutators: defaultImplicitMinMutators,
	}
	severity := fs.String("severity", "warning", "finding severity: warning (default, exit 0) or error (exit 2 on findings)")
	fs.IntVar(&opts.minFlags, "min-flags", defaultImplicitMinFlags, "distinct lifecycle bool fields before the cluster rule applies")
	fs.IntVar(&opts.minMutators, "min-mutators", defaultImplicitMinMutators, "distinct funcs a flag/status must be mutated from")
	if err := fs.Parse(args); err != nil {
		return implicitStateOptions{}, 1, false
	}
	if *severity != "warning" && *severity != "error" {
		fmt.Fprintf(stderr, "invalid --severity %q: want warning or error\n", *severity)
		return implicitStateOptions{}, 1, false
	}
	if opts.minFlags < 1 {
		fmt.Fprintln(stderr, "--min-flags must be at least 1")
		return implicitStateOptions{}, 1, false
	}
	if opts.minMutators < 1 {
		fmt.Fprintln(stderr, "--min-mutators must be at least 1")
		return implicitStateOptions{}, 1, false
	}
	opts.paths = fs.Args()
	opts.fail = *severity == "error"
	return opts, 0, true
}

// implicitStructEntry is one top-level struct type declaration to scan,
// with its file's parse context.
type implicitStructEntry struct {
	fset *token.FileSet
	path string
	spec *ast.TypeSpec
	body *ast.StructType
}

// CheckImplicitState scans every parseable file for implicit state
// machines and returns the violations plus the number of structs
// examined.
func CheckImplicitState(files []string, root string, minFlags, minMutators int) ([]ImplicitStateViolation, int) {
	entries, funcs := collectImplicitScope(files, root)
	var violations []ImplicitStateViolation
	for _, e := range entries {
		violations = append(violations, scanImplicitEntry(e, funcs, minFlags, minMutators)...)
	}
	return violations, len(entries)
}

// collectImplicitScope parses every selection file and collects its
// top-level struct types and funcs: the structs to scan, and the
// package-wide func set member resolution matches against — Go methods
// may live in any file of the package, so a struct declared in a.go
// with its mutating methods in b.go is one machine (upstream Dart keeps
// a class and its members in one file). Function-local struct types are
// not collected: they have no methods and can never violate —
// upstream's nested-class analog has no Go shape.
func collectImplicitScope(files []string, root string) ([]implicitStructEntry, []*ast.FuncDecl) {
	var entries []implicitStructEntry
	var funcs []*ast.FuncDecl
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		entries, funcs = collectImplicitDecls(entries, funcs, file.Decls, fset, relPath(root, f))
	}
	return entries, funcs
}

// collectImplicitDecls folds one file's top-level decls into the
// package scope: funcs join the func set, struct type specs join the
// scan list.
func collectImplicitDecls(entries []implicitStructEntry, funcs []*ast.FuncDecl, decls []ast.Decl, fset *token.FileSet, path string) ([]implicitStructEntry, []*ast.FuncDecl) {
	for _, decl := range decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			funcs = append(funcs, fd)
			continue
		}
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			entries = appendImplicitEntry(entries, spec, fset, path)
		}
	}
	return entries, funcs
}

// appendImplicitEntry appends spec to the scan list when it declares a
// struct type.
func appendImplicitEntry(entries []implicitStructEntry, spec ast.Spec, fset *token.FileSet, path string) []implicitStructEntry {
	ts, ok := spec.(*ast.TypeSpec)
	if !ok {
		return entries
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return entries
	}
	return append(entries, implicitStructEntry{fset: fset, path: path, spec: ts, body: st})
}

// scanImplicitEntry scans one struct: its fields, then every member
// func of the whole selection, then the finish() findings.
func scanImplicitEntry(e implicitStructEntry, funcs []*ast.FuncDecl, minFlags, minMutators int) []ImplicitStateViolation {
	scan := &implicitScan{
		fset:        e.fset,
		path:        e.path,
		minFlags:    minFlags,
		minMutators: minMutators,
		flags:       map[string]*implicitFieldSites{},
		statuses:    map[string]*implicitFieldSites{},
	}
	scan.collectFields(e.body)
	for _, fd := range funcs {
		if implicitMemberOf(fd, e.spec.Name.Name) {
			scan.walkMember(fd)
		}
	}
	scan.finish(e.fset.Position(e.spec.Pos()).Line)
	return scan.violations
}

// implicitFieldSites is one field's declaration line plus the distinct
// member funcs that assign it.
type implicitFieldSites struct {
	line     int
	mutators map[*ast.FuncDecl]bool
}

// implicitTypeIdent resolves an expression to its base type identifier,
// unwrapping pointers and generic instantiations (`*Downloader`,
// `Box[T]`).
func implicitTypeIdent(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.StarExpr:
		return implicitTypeIdent(t.X)
	case *ast.IndexExpr:
		return implicitTypeIdent(t.X)
	case *ast.IndexListExpr:
		return implicitTypeIdent(t.X)
	}
	return "", false
}

// implicitMemberOf reports whether fd belongs to the struct's member
// scope: a method whose receiver type is the struct, or a plain func
// whose result is the struct/*struct (the New* factory, the constructor
// analog).
func implicitMemberOf(fd *ast.FuncDecl, structName string) bool {
	return fd.Body != nil && (implicitReceiverOf(fd, structName) || implicitFactoryOf(fd, structName))
}

// implicitReceiverOf reports whether fd is a method on the struct: a
// receiver list of exactly one field whose type resolves to the struct
// name (`T`, `*T`, generic `T[P]`).
func implicitReceiverOf(fd *ast.FuncDecl, structName string) bool {
	if fd.Recv == nil {
		return false
	}
	if len(fd.Recv.List) != 1 {
		return false
	}
	base, ok := implicitTypeIdent(fd.Recv.List[0].Type)
	return ok && base == structName
}

// implicitFactoryOf reports whether fd is a plain func returning the
// struct — the New* factory, the constructor analog.
func implicitFactoryOf(fd *ast.FuncDecl, structName string) bool {
	if fd.Type.Results == nil {
		return false
	}
	for _, res := range fd.Type.Results.List {
		if base, ok := implicitTypeIdent(res.Type); ok && base == structName {
			return true
		}
	}
	return false
}

// implicitScan collects one struct's lifecycle flags, status fields,
// their mutation sites per member func, and flag-combination conditions.
type implicitScan struct {
	fset        *token.FileSet
	path        string
	minFlags    int
	minMutators int
	flags       map[string]*implicitFieldSites
	statuses    map[string]*implicitFieldSites
	violations  []ImplicitStateViolation
}

// collectFields records the struct's matching fields: lifecycle flags
// are bool fields with the flag name shape (Go struct fields are always
// mutable — the host has no final/const field shape); every other field
// named status/state/phase/stage/mode/lifecycle is a status field of any
// type. Embedded fields carry no name and never match.
func (s *implicitScan) collectFields(st *ast.StructType) {
	for _, field := range st.Fields.List {
		isBool := false
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "bool" {
			isBool = true
		}
		for _, name := range field.Names {
			line := s.fset.Position(name.Pos()).Line
			if isBool && stateFlagName.MatchString(name.Name) {
				s.flags[name.Name] = &implicitFieldSites{line: line, mutators: map[*ast.FuncDecl]bool{}}
			} else if statusFieldName.MatchString(strings.ToLower(name.Name)) {
				s.statuses[name.Name] = &implicitFieldSites{line: line, mutators: map[*ast.FuncDecl]bool{}}
			}
		}
	}
}

// walkMember visits one member func's body: selector assignments whose
// field name matches a declared field count as mutation sites regardless
// of the target object (upstream counts the property name shape only),
// and every if condition is checked for receiver flag combinations. Func
// literals inside the body count too — they run within the member.
func (s *implicitScan) walkMember(fd *ast.FuncDecl) {
	receiver := implicitReceiverName(fd)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				return true // := declares, it never mutates a field
			}
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue // bare identifiers are package vars or locals, never fields
				}
				if site, ok := s.flags[sel.Sel.Name]; ok {
					site.mutators[fd] = true
				} else if site, ok := s.statuses[sel.Sel.Name]; ok {
					site.mutators[fd] = true
				}
			}
		case *ast.IfStmt:
			s.checkCondition(node, receiver)
		}
		return true
	})
}

// implicitReceiverName returns the method's receiver variable name, or
// "" (factory funcs and unnamed receivers have none).
func implicitReceiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 || len(fd.Recv.List[0].Names) != 1 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

// checkCondition flags an if condition reading two or more DISTINCT
// declared lifecycle flags through the method's receiver (`r.isLoading`)
// — an implicit state combination. Selections through other objects
// (`other.isReady`) and bare identifiers (package vars or locals in Go,
// never fields) do not count.
func (s *implicitScan) checkCondition(node *ast.IfStmt, receiver string) {
	if receiver == "" {
		return
	}
	var names []string
	seen := map[string]bool{}
	ast.Inspect(node.Cond, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != receiver {
			return true
		}
		name := sel.Sel.Name
		if _, ok := s.flags[name]; ok && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		return true
	})
	if len(names) < 2 {
		return
	}
	s.violations = append(s.violations, ImplicitStateViolation{
		Path:    s.path,
		Line:    s.fset.Position(node.Pos()).Line,
		Message: fmt.Sprintf("condition branches on %d lifecycle flags (%s) — an implicit state combination; model the states explicitly", len(names), strings.Join(names, ", ")),
	})
}

// finish emits the per-field and cluster violations after the whole
// struct's member scope has been visited: scattered lifecycle flags,
// scattered status fields (fields in declaration order), then the
// boolean-cluster finding on the struct line.
func (s *implicitScan) finish(structLine int) {
	for _, name := range s.sortedFieldNames(s.flags) {
		s.reportField(name, s.flags[name], "lifecycle flag", "part of an implicit state machine")
	}
	for _, name := range s.sortedFieldNames(s.statuses) {
		s.reportField(name, s.statuses[name], "status field", "implicit transitions; centralize them")
	}
	var assigned []*implicitFieldSites
	scattered := false
	for _, sites := range s.flags {
		if len(sites.mutators) == 0 {
			continue
		}
		assigned = append(assigned, sites)
		if len(sites.mutators) >= s.minMutators {
			scattered = true
		}
	}
	if len(assigned) < s.minFlags || !scattered {
		return
	}
	s.violations = append(s.violations, ImplicitStateViolation{
		Path:    s.path,
		Line:    structLine,
		Message: fmt.Sprintf("struct holds %d lifecycle flags mutated across methods — a boolean-cluster state machine; model it with an enum/sealed state", len(assigned)),
	})
}

// reportField emits one scattered-field violation when the field is
// mutated from at least minMutators distinct funcs.
func (s *implicitScan) reportField(name string, sites *implicitFieldSites, kind, tail string) {
	count := len(sites.mutators)
	if count < s.minMutators {
		return
	}
	s.violations = append(s.violations, ImplicitStateViolation{
		Path:    s.path,
		Line:    sites.line,
		Message: fmt.Sprintf("%s '%s' is mutated from %d places — %s", kind, name, count, tail),
	})
}

// sortedFieldNames returns the fields in declaration order (Dart's map
// iteration is insertion-ordered; Go's is not).
func (s *implicitScan) sortedFieldNames(fields map[string]*implicitFieldSites) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return fields[names[i]].line < fields[names[j]].line
	})
	return names
}
