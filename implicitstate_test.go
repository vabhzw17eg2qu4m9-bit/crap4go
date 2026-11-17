package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Fixtures are full Go files; `package p` is line 1. Line numbers in the
// assertions pin the violation sites. Every flagged fixture carries a
// real implicit-state shape; clean fixtures pin the negative rulings
// (package vars, bare identifiers, composite-literal keys, other-object
// selections) so the scan cannot pass vacuously.

// clusterSrc: three lifecycle flags mutated across three methods — the
// boolean-cluster finding at the struct line plus the scattered flags
// with at least two mutators each (isRunning 3, hasFailed 2; isDisposed
// has one mutator and stays quiet).
const clusterSrc = `package p

type Downloader struct {
	isRunning  bool
	hasFailed  bool
	isDisposed bool
}

func (d *Downloader) start() {
	d.isRunning = true
	d.hasFailed = false
}

func (d *Downloader) stop() {
	d.isRunning = false
	d.isDisposed = true
}

func (d *Downloader) fail() {
	d.hasFailed = true
	d.isRunning = false
}
`

// twoFlagsSrc: two lifecycle flags — below the default --min-flags 3, so
// no cluster finding; the per-field scattered findings remain.
const twoFlagsSrc = `package p

type Small struct {
	isRunning bool
	hasFailed bool
}

func (s *Small) start() {
	s.isRunning = true
	s.hasFailed = false
}

func (s *Small) stop() {
	s.isRunning = false
	s.hasFailed = true
}
`

// oneFlagSrc: one flag mutated from three methods — the scattered
// finding at the field's declaration line.
const oneFlagSrc = `package p

type Job struct {
	isDone bool
}

func (j *Job) run() {
	j.isDone = false
}

func (j *Job) complete() {
	j.isDone = true
}

func (j *Job) reset() {
	j.isDone = false
}
`

// statusSrc: a status field (any type) assigned from two methods.
const statusSrc = `package p

type Job struct {
	status string
}

func (j *Job) run() {
	j.status = "running"
}

func (j *Job) finish() {
	j.status = "done"
}
`

// onceSrc: one mutation site — quiet at the default --min-mutators 2.
const onceSrc = `package p

type Once struct {
	isStarted bool
}

func (o *Once) start() {
	o.isStarted = true
}
`

// comboSrc: an if-condition reading two declared lifecycle flags through
// the receiver.
const comboSrc = `package p

type Guard struct {
	isLoading bool
	hasFailed bool
}

func (g *Guard) check() {
	if g.isLoading || g.hasFailed {
		return
	}
}
`

// plainSrc: selections through other objects (`other.isReady`) and a
// single-flag condition never combine.
const plainSrc = `package p

type Plain struct {
	isReady bool
}

func (p *Plain) check(other *Plain) {
	if other.isReady {
		return
	}
	if p.isReady {
		return
	}
}
`

// packageVarsSrc: flag-shaped bools at package level — Go's analog of
// upstream statics — are not instance fields and never count.
const packageVarsSrc = `package p

var isGlobalEnabled = false
var hasWorkers = true

func enable() {
	isGlobalEnabled = true
}

func disable() {
	isGlobalEnabled = false
}
`

// bareIdentSrc: a bare `isLoading = true` targets a package var or a
// local in Go — upstream's implicit-this assignment target has no host
// shape, so it is never a field mutation site.
const bareIdentSrc = `package p

var isLoading = false

type Tracked struct {
	isLoading bool
}

func (t *Tracked) begin() {
	isLoading = true
}

func (t *Tracked) finish() {
	isLoading = false
}
`

// compositeSrc: composite-literal field keys are not assignments
// (upstream counts assignment expressions only) — two declared flags
// with zero assignment sites produce nothing.
const compositeSrc = `package p

type Config struct {
	isLoaded bool
	hasCache bool
}

func NewConfig() *Config {
	return &Config{isLoaded: true, hasCache: true}
}
`

// factorySrc: a factory returning *T is the constructor analog — its
// assignments count, so isOpen is mutated from two distinct funcs
// (NewConn, close). Its if-condition has no receiver: the combination
// check bails out early (a factory has no receiver to read through).
const factorySrc = `package p

type Conn struct {
	isOpen bool
}

func NewConn() *Conn {
	c := &Conn{}
	c.isOpen = true
	if c.isOpen {
		return c
	}
	return nil
}

func (c *Conn) close() {
	c.isOpen = false
}
`

// mutedSrc: bool fields with no lifecycle signal — mutated from two
// methods, still quiet.
const mutedSrc = `package p

type Settings struct {
	verbose bool
	colored bool
	strict  bool
}

func (s *Settings) toggle() {
	s.verbose = !s.verbose
	s.colored = !s.colored
	s.strict = !s.strict
}

func (s *Settings) reset() {
	s.verbose = false
	s.colored = true
	s.strict = false
}
`

// cleanSrc: no matching fields — the canonical clean file.
const cleanSrc = `package p

type Repo struct {
	name string
}

func (r *Repo) rename(name string) {
	r.name = name
}
`

// trioOnceSrc: three flags, each mutated from exactly ONE method —
// quiet at the defaults; with --min-mutators 1 the cluster finding
// fires, proving the cluster's scattered check follows the knob (the
// upstream divergence: its code hardcodes 2, its spec says the knob).
const trioOnceSrc = `package p

type Trio struct {
	isWarm   bool
	hasData  bool
	isSealed bool
}

func (t *Trio) warm() { t.isWarm = true }
func (t *Trio) fill() { t.hasData = true }
func (t *Trio) seal() { t.isSealed = true }
`

// separateScanSrc: two structs scanned independently — the same field
// name in two structs counts each struct's own mutators only.
const separateScanSrc = `package p

type A struct {
	isBusy bool
}

type B struct {
	isBusy bool
}

func (a *A) mark() { a.isBusy = true }

func (b *B) mark() { b.isBusy = true }

func (a *A) clear() { a.isBusy = false }

func (b *B) clear() { b.isBusy = false }
`

// splitStructSrc / splitMethodsSrc: the struct (fields only) lives in
// one file and its mutating methods in another — legal Go (methods may
// live in any file of the package), so member resolution must span the
// whole selection; this kills file-local member resolution.
const splitStructSrc = `package p

type Downloader struct {
	isRunning  bool
	hasFailed  bool
	isDisposed bool
}
`

const splitMethodsSrc = `package p

func (d *Downloader) start() {
	d.isRunning = true
	d.hasFailed = false
}

func (d *Downloader) stop() {
	d.isRunning = false
	d.isDisposed = true
}

func (d *Downloader) fail() {
	d.hasFailed = true
	d.isRunning = false
}
`

// implicitStateStrings runs CheckImplicitState over one fixture file at
// the given thresholds and returns "line:message" strings plus the
// number of structs checked (0 is meaningful: package-level fixtures
// scan no structs at all).
func implicitStateStrings(t *testing.T, src string, minFlags, minMutators int) ([]string, int) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root+"/state.go", src)
	violations, structs := CheckImplicitState([]string{root + "/state.go"}, root, minFlags, minMutators)
	var got []string
	for _, v := range violations {
		if v.Path != "state.go" {
			t.Errorf("path = %q, want state.go", v.Path)
		}
		got = append(got, fmt.Sprintf("%d:%s", v.Line, v.Message))
	}
	return got, structs
}

// implicitStateFilesStrings is the multi-file variant: the fixture map's
// file names are written sorted, so the selection order is deterministic.
func implicitStateFilesStrings(t *testing.T, files map[string]string, minFlags, minMutators int) ([]string, int) {
	t.Helper()
	root := t.TempDir()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var paths []string
	for _, name := range names {
		path := filepath.Join(root, name)
		writeFile(t, path, files[name])
		paths = append(paths, path)
	}
	violations, structs := CheckImplicitState(paths, root, minFlags, minMutators)
	var got []string
	for _, v := range violations {
		got = append(got, fmt.Sprintf("%s:%d:%s", v.Path, v.Line, v.Message))
	}
	return got, structs
}

func TestImplicitStateCluster(t *testing.T) {
	got, structs := implicitStateStrings(t, clusterSrc, 3, 2)
	if structs != 1 {
		t.Errorf("structs = %d, want 1", structs)
	}
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isRunning' is mutated from 3 places — part of an implicit state machine",
		"5:lifecycle flag 'hasFailed' is mutated from 2 places — part of an implicit state machine",
		"3:struct holds 3 lifecycle flags mutated across methods — a boolean-cluster state machine; model it with an enum/sealed state",
	})
}

func TestImplicitStateClusterRespectsMinFlags(t *testing.T) {
	got, _ := implicitStateStrings(t, twoFlagsSrc, 3, 2)
	for _, v := range got {
		if strings.Contains(v, "boolean-cluster") {
			t.Errorf("2 flags < min-flags 3 must not cluster: %v", got)
		}
	}
	// The same fixture clusters with --min-flags 2 (per-field findings
	// stay).
	got, _ = implicitStateStrings(t, twoFlagsSrc, 2, 2)
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isRunning' is mutated from 2 places — part of an implicit state machine",
		"5:lifecycle flag 'hasFailed' is mutated from 2 places — part of an implicit state machine",
		"3:struct holds 2 lifecycle flags mutated across methods — a boolean-cluster state machine; model it with an enum/sealed state",
	})
}

func TestImplicitStateScatteredFlag(t *testing.T) {
	got, _ := implicitStateStrings(t, oneFlagSrc, 3, 2)
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isDone' is mutated from 3 places — part of an implicit state machine",
	})
}

func TestImplicitStateStatusField(t *testing.T) {
	got, _ := implicitStateStrings(t, statusSrc, 3, 2)
	matchViolations(t, got, []string{
		"4:status field 'status' is mutated from 2 places — implicit transitions; centralize them",
	})
}

func TestImplicitStateMinMutatorsKnob(t *testing.T) {
	if got, _ := implicitStateStrings(t, onceSrc, 3, 2); len(got) != 0 {
		t.Errorf("one mutation site must stay quiet at min-mutators 2: %v", got)
	}
	got, _ := implicitStateStrings(t, onceSrc, 3, 1)
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isStarted' is mutated from 1 places — part of an implicit state machine",
	})
}

func TestImplicitStateComboCondition(t *testing.T) {
	got, _ := implicitStateStrings(t, comboSrc, 3, 2)
	matchViolations(t, got, []string{
		"9:condition branches on 2 lifecycle flags (isLoading, hasFailed) — an implicit state combination; model the states explicitly",
	})
	// Other-object selections and single-flag conditions never combine.
	if got, _ := implicitStateStrings(t, plainSrc, 3, 2); len(got) != 0 {
		t.Errorf("plainSrc must stay quiet: %v", got)
	}
}

func TestImplicitStateNegatives(t *testing.T) {
	for _, negative := range []struct {
		name string
		src  string
	}{
		{"package vars excluded like statics", packageVarsSrc},
		{"bare identifiers are not field sites", bareIdentSrc},
		{"composite-literal keys are not assignments", compositeSrc},
		{"non-state bool fields ignored", mutedSrc},
		{"clean file", cleanSrc},
	} {
		got, _ := implicitStateStrings(t, negative.src, 3, 2)
		if len(got) != 0 {
			t.Errorf("%s: violations = %v, want none", negative.name, got)
		}
	}
}

func TestImplicitStateClusterFollowsMinMutatorsKnob(t *testing.T) {
	// Three flags mutated from one method each: quiet at the defaults,
	// clustered at --min-mutators 1 — the spec's knob drives the
	// cluster's scattered check (upstream code hardcodes 2, its spec
	// says the knob; this port follows the spec).
	if got, _ := implicitStateStrings(t, trioOnceSrc, 3, 2); len(got) != 0 {
		t.Errorf("single-mutator flags must stay quiet at the defaults: %v", got)
	}
	got, _ := implicitStateStrings(t, trioOnceSrc, 3, 1)
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isWarm' is mutated from 1 places — part of an implicit state machine",
		"5:lifecycle flag 'hasData' is mutated from 1 places — part of an implicit state machine",
		"6:lifecycle flag 'isSealed' is mutated from 1 places — part of an implicit state machine",
		"3:struct holds 3 lifecycle flags mutated across methods — a boolean-cluster state machine; model it with an enum/sealed state",
	})
}

func TestImplicitStateFactoryCountsAsMutator(t *testing.T) {
	got, _ := implicitStateStrings(t, factorySrc, 3, 2)
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isOpen' is mutated from 2 places — part of an implicit state machine",
	})
}

func TestImplicitStateStructsScanSeparately(t *testing.T) {
	got, structs := implicitStateStrings(t, separateScanSrc, 3, 2)
	if structs != 2 {
		t.Errorf("structs = %d, want 2", structs)
	}
	matchViolations(t, got, []string{
		"4:lifecycle flag 'isBusy' is mutated from 2 places — part of an implicit state machine",
		"8:lifecycle flag 'isBusy' is mutated from 2 places — part of an implicit state machine",
	})
}

func TestImplicitStateSplitFileMethods(t *testing.T) {
	// The struct and its mutating methods live in different files of the
	// same package — member resolution spans the selection, so the
	// machine is flagged exactly like the single-file clusterSrc.
	got, structs := implicitStateFilesStrings(t, map[string]string{
		"state.go":         splitStructSrc,
		"state_methods.go": splitMethodsSrc,
	}, 3, 2)
	if structs != 1 {
		t.Errorf("structs = %d, want 1", structs)
	}
	matchViolations(t, got, []string{
		"state.go:4:lifecycle flag 'isRunning' is mutated from 3 places — part of an implicit state machine",
		"state.go:5:lifecycle flag 'hasFailed' is mutated from 2 places — part of an implicit state machine",
		"state.go:3:struct holds 3 lifecycle flags mutated across methods — a boolean-cluster state machine; model it with an enum/sealed state",
	})
	// Either half alone has no machine: fields without mutators, or
	// mutators without declared fields.
	if got, _ := implicitStateFilesStrings(t, map[string]string{"state.go": splitStructSrc}, 3, 2); len(got) != 0 {
		t.Errorf("fields without mutators must stay quiet: %v", got)
	}
	if got, _ := implicitStateFilesStrings(t, map[string]string{"state_methods.go": splitMethodsSrc}, 3, 2); len(got) != 0 {
		t.Errorf("mutators without declared fields must stay quiet: %v", got)
	}
}

func TestImplicitTypeIdent(t *testing.T) {
	// The resolution rule the receiver/factory matching depends on:
	// unwrap pointers and generic instantiations to the base type
	// identifier; any other expression shape has no base.
	ident := &ast.Ident{Name: "T"}
	cases := []struct {
		name string
		expr ast.Expr
		want string
		ok   bool
	}{
		{"plain", ident, "T", true},
		{"pointer", &ast.StarExpr{X: ident}, "T", true},
		{"generic", &ast.IndexExpr{X: ident, Index: &ast.Ident{Name: "K"}}, "T", true},
		{"generic two params", &ast.IndexListExpr{X: ident, Indices: []ast.Expr{ident, ident}}, "T", true},
		{"other shape", &ast.SelectorExpr{X: ident, Sel: &ast.Ident{Name: "Base"}}, "", false},
	}
	for _, c := range cases {
		got, ok := implicitTypeIdent(c.expr)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: implicitTypeIdent = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestImplicitMemberOf(t *testing.T) {
	ident := func(name string) ast.Expr { return &ast.Ident{Name: name} }
	funcDecl := func(recv []*ast.Field, results *ast.FieldList, withBody bool) *ast.FuncDecl {
		fd := &ast.FuncDecl{Name: &ast.Ident{Name: "f"}, Type: &ast.FuncType{Results: results}}
		if withBody {
			fd.Body = &ast.BlockStmt{}
		}
		if recv != nil {
			fd.Recv = &ast.FieldList{List: recv}
		}
		return fd
	}
	cases := []struct {
		name string
		fd   *ast.FuncDecl
		want bool
	}{
		{"bodyless external decl", funcDecl(nil, nil, false), false},
		{"matching value receiver", funcDecl([]*ast.Field{{Type: ident("Conn")}}, nil, true), true},
		{"matching pointer receiver", funcDecl([]*ast.Field{{Type: &ast.StarExpr{X: ident("Conn")}}}, nil, true), true},
		{"other receiver type", funcDecl([]*ast.Field{{Type: ident("Other")}}, nil, true), false},
		{"two receivers", funcDecl([]*ast.Field{{Type: ident("A")}, {Type: ident("B")}}, nil, true), false},
		{"factory returning pointer", funcDecl(nil, &ast.FieldList{List: []*ast.Field{{Type: &ast.StarExpr{X: ident("Conn")}}}}, true), true},
		{"factory returning tuple with match", funcDecl(nil, &ast.FieldList{List: []*ast.Field{{Type: ident("error")}, {Type: ident("Conn")}}}, true), true},
		{"plain func without results", funcDecl(nil, nil, true), false},
		{"plain func returning other", funcDecl(nil, &ast.FieldList{List: []*ast.Field{{Type: ident("string")}}}, true), false},
	}
	for _, c := range cases {
		if got := implicitMemberOf(c.fd, "Conn"); got != c.want {
			t.Errorf("%s: implicitMemberOf = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCollectImplicitDeclsSkipsNonStructs(t *testing.T) {
	// The collection ladder's miss branches: a func joins the func set,
	// an import spec / non-struct type / bad decl collect nothing.
	fset := token.NewFileSet()
	src := `package p

import "os"

type Alias = Conn

type Box struct{ v int }

func open() {}
`
	file, err := parser.ParseFile(fset, "edges.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, funcs := collectImplicitDecls(nil, nil, file.Decls, fset, "edges.go")
	if len(funcs) != 1 || funcs[0].Name.Name != "open" {
		t.Errorf("funcs = %v, want [open]", funcs)
	}
	if len(entries) != 1 || entries[0].spec.Name.Name != "Box" || entries[0].path != "edges.go" {
		t.Errorf("entries = %+v, want [Box]", entries)
	}
	bad := &ast.BadDecl{From: token.Pos(1), To: token.Pos(5)}
	entries2, funcs2 := collectImplicitDecls(nil, nil, []ast.Decl{bad}, fset, "bad.go")
	if len(entries2) != 0 || len(funcs2) != 0 {
		t.Errorf("bad decl collected: %v %v", entries2, funcs2)
	}
}

func TestImplicitStateSkipsUnparseableFiles(t *testing.T) {
	// A syntactically broken file is skipped; the machine in the good
	// file still scans.
	got, structs := implicitStateFilesStrings(t, map[string]string{
		"broken.go": "package p\n\nfunc {\n",
		"state.go":  splitStructSrc,
		"work.go":   splitMethodsSrc,
	}, 3, 2)
	if structs != 1 {
		t.Errorf("structs = %d, want 1 — the broken file is skipped", structs)
	}
	if len(got) != 3 {
		t.Fatalf("violations = %v, want the split-file cluster findings", got)
	}
}

func TestRun_ImplicitStateWarningDefaultExitsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/state.go", clusterSrc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"implicit-state"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 at the default warning severity (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"state.go:4: lifecycle flag 'isRunning' is mutated from 3 places",
		"3 implicit-state findings (1 structs checked)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_ImplicitStateCleanSummary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/clean.go", cleanSrc)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"implicit-state"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 structs checked, no implicit state machines"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_ImplicitStateSeverityErrorExitsTwo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/state.go", comboSrc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"implicit-state", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --severity error", code)
	}
}

func TestRun_ImplicitStateMinFlagsFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/state.go", twoFlagsSrc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"implicit-state", "--min-flags", "2", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --min-flags 2 --severity error", code)
	}
	if !strings.Contains(out.String(), "boolean-cluster state machine") {
		t.Errorf("cluster finding missing:\n%s", out.String())
	}
}

func TestRun_ImplicitStateMinMutatorsFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/state.go", onceSrc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"implicit-state", "--min-mutators", "1", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --min-mutators 1 --severity error", code)
	}
	if !strings.Contains(out.String(), "'isStarted' is mutated from 1 places") {
		t.Errorf("single-mutator finding missing:\n%s", out.String())
	}
}

func TestRun_ImplicitStateUnknownFlagIsUsageError(t *testing.T) {
	// Strict unknown-key handling: the yaml analog's unknown keys are
	// config errors upstream; unknown flags are usage errors here.
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"implicit-state", "--min-flagz", "3"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for an unknown flag (stderr=%s)", code, errOut.String())
	}
}

func TestRun_ImplicitStateIgnorable(t *testing.T) {
	root := t.TempDir()
	marked := strings.Replace(comboSrc, "if g.isLoading", "// ignore:implicit-state\n\tif g.isLoading", 1)
	writeFile(t, root+"/state.go", marked)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"implicit-state", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the granular ignore marker (stderr=%s)", code, errOut.String())
	}
	writeFile(t, root+"/state.go", strings.Replace(comboSrc, "if g.isLoading", "// crap:ignore\n\tif g.isLoading", 1))
	out.Reset()
	if code := runWithRoot([]string{"implicit-state", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the blanket ignore marker", code)
	}
}
