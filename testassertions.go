package main

import (
	"fmt"
	"go/ast"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minTestAssertions is the minimum assertion count per test (ported from
// crap4dart's test_assertions gate default).
const minTestAssertions = 1

// failingTestMethods are the *testing.T methods capable of failing a test;
// calls to them count as assertions. A Go test that can call none of them
// (and never panics) is vacuous.
var failingTestMethods = map[string]bool{
	"Error":   true,
	"Errorf":  true,
	"Fatal":   true,
	"Fatalf":  true,
	"Fail":    true,
	"FailNow": true,
}

// TestAssertionViolation is one test body that cannot fail.
type TestAssertionViolation struct {
	Path    string
	Line    int
	Message string
}

// RunTestAssertionsCommand implements `crap4go test-assertions [paths...]`:
// it flags Test* functions in *_test.go files whose bodies contain neither
// a fail-capable *testing.T method call nor a panic() call — a test without
// assertions verifies nothing (Go adaptation of crap4dart 0.9's
// test_assertions gate).
func RunTestAssertionsCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectTestFiles(args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckTestAssertions(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d tests without assertions\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d tests assert their expectations\n", checked)
	return 0
}

// selectTestFiles returns the *_test.go files of the selection: each
// explicit path is resolved against root (directories are walked for test
// files; non-test files are dropped); without paths the whole root is
// walked. vendor/ trees are excluded.
func selectTestFiles(args []string, root string) ([]string, error) {
	if len(args) == 0 {
		return walkTestFiles(root)
	}
	seen := map[string]bool{}
	var files []string
	for _, arg := range args {
		walked, err := expandTestPath(arg, root)
		if err != nil {
			return nil, err
		}
		for _, f := range walked {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// expandTestPath resolves one CLI arg against root into its *_test.go
// paths: a directory is walked, a file is kept when it is a test file.
func expandTestPath(arg, root string) ([]string, error) {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, arg)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return walkTestFiles(path)
	}
	if strings.HasSuffix(path, "_test.go") {
		return []string{path}, nil
	}
	return nil, nil
}

// walkTestFiles walks root and returns the sorted *_test.go paths, skipping
// vendor/ trees.
func walkTestFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// CheckTestAssertions returns the Test* functions whose bodies cannot fail
// the test, plus the number of test functions checked.
func CheckTestAssertions(files []string, root string) ([]TestAssertionViolation, int) {
	var violations []TestAssertionViolation
	checked := 0
	for _, f := range files {
		fileViolations, count := testAssertionViolations(f, root)
		checked += count
		violations = append(violations, fileViolations...)
	}
	return violations, checked
}

// testAssertionViolations returns the vacuous tests of one file plus the
// number of test functions found in it.
func testAssertionViolations(path, root string) ([]TestAssertionViolation, int) {
	file, fset, err := parseGoFile(path)
	if err != nil {
		return nil, 0
	}
	var violations []TestAssertionViolation
	checked := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !isTestFuncName(fd.Name.Name) {
			continue
		}
		checked++
		assertions := countFailCapableCalls(fd)
		if assertions >= minTestAssertions {
			continue
		}
		violations = append(violations, TestAssertionViolation{
			Path: relPath(root, path),
			Line: fset.Position(fd.Pos()).Line,
			Message: fmt.Sprintf("%s has %d assertion(s) — a test without assertions verifies nothing",
				fd.Name.Name, assertions),
		})
	}
	return violations, checked
}

// isTestFuncName mirrors the testing package's test-function rule: the name
// starts with "Test" and the next rune is not a lowercase letter. TestMain
// is the harness entry point, not a test, and never counts.
func isTestFuncName(name string) bool {
	if !strings.HasPrefix(name, "Test") || name == "TestMain" {
		return false
	}
	if len(name) == len("Test") {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len("Test"):])
	return !unicode.IsLower(r)
}

// countFailCapableCalls counts the calls that can fail the test: fail-
// capable methods on any *testing.T parameter of the function or its
// nested func literals (t.Run subtests), plus bare panic() calls.
func countFailCapableCalls(fd *ast.FuncDecl) int {
	names := map[string]bool{}
	addTestingNames(fd.Type.Params, names)
	count := 0
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			addTestingNames(n.Type.Params, names)
		case *ast.CallExpr:
			if isFailCapableCall(n, names) {
				count++
			}
		}
		return true
	})
	return count
}

// isFailCapableCall reports whether the call invokes a fail-capable method
// through an identifier bound to a *testing.T parameter, or the builtin
// panic.
func isFailCapableCall(call *ast.CallExpr, names map[string]bool) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		x, ok := fn.X.(*ast.Ident)
		return ok && names[x.Name] && failingTestMethods[fn.Sel.Name]
	case *ast.Ident:
		return fn.Name == "panic"
	}
	return false
}

// addTestingNames adds the parameter names typed *testing.T to names.
func addTestingNames(fields *ast.FieldList, names map[string]bool) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if !isTestingTPtr(field.Type) {
			continue
		}
		for _, id := range field.Names {
			if id.Name != "" && id.Name != "_" {
				names[id.Name] = true
			}
		}
	}
}

// isTestingTPtr reports whether expr is the type *testing.T.
func isTestingTPtr(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}
