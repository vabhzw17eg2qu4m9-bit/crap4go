package main

import (
	"fmt"
	"go/ast"
	"io"
	"path/filepath"
)

// UnusedCodeViolation is one unexported package-level declaration never
// referenced elsewhere in its package.
type UnusedCodeViolation struct {
	Path    string
	Line    int
	Message string
}

// RunUnusedCodeCommand implements `crap4go unused-code [paths...]`: it flags
// unexported package-level declarations (funcs, types, vars, consts) whose
// identifier never appears elsewhere in the same package. References are
// counted lexically on unresolved ASTs, non-test files only. Because a
// partial file set yields false positives, an explicit path selection prints
// a skip message and exits 0 (ported from crap4dart 0.5.1).
func RunUnusedCodeCommand(args []string, root string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stdout, "unused-code: not meaningful for a partial selection")
		return 0
	}
	files, err := selectFiles(false, nil, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckUnusedCode(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d unexported declarations unused\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d unexported declarations used\n", checked)
	return 0
}

// unusedDecl is one unexported package-level declaration site.
type unusedDecl struct {
	path   string
	pkgDir string
	line   int
	name   string
}

// CheckUnusedCode groups the files by package directory and returns the
// unexported package-level declarations whose identifier occurs exactly once
// (the declaration itself), plus the number of declarations checked.
func CheckUnusedCode(files []string, root string) ([]UnusedCodeViolation, int) {
	var decls []unusedDecl
	counts := map[string]map[string]int{}
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		pkgDir := relPath(root, filepath.Dir(f))
		pkgCounts, ok := counts[pkgDir]
		if !ok {
			pkgCounts = map[string]int{}
			counts[pkgDir] = pkgCounts
		}
		for _, name := range packageDeclNames(file) {
			decls = append(decls, unusedDecl{
				path:   relPath(root, f),
				pkgDir: pkgDir,
				line:   fset.Position(name.Pos()).Line,
				name:   name.Name,
			})
		}
		countIdents(file, pkgCounts)
	}
	var violations []UnusedCodeViolation
	for _, d := range decls {
		if counts[d.pkgDir][d.name] <= 1 {
			violations = append(violations, UnusedCodeViolation{
				Path:    d.path,
				Line:    d.line,
				Message: fmt.Sprintf("%s is never referenced", d.name),
			})
		}
	}
	return violations, len(decls)
}

// countIdents adds every identifier occurrence in file to counts (lexical
// references on the unresolved AST, matching crap4dart's UnusedCodeGate).
func countIdents(file *ast.File, counts map[string]int) {
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			counts[id.Name]++
		}
		return true
	})
}

// packageDeclNames returns the unexported package-level names (functions,
// types, vars, consts; methods excluded) declared by file.
func packageDeclNames(file *ast.File) []*ast.Ident {
	var names []*ast.Ident
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if n := funcDeclName(d); n != nil {
				names = append(names, n)
			}
		case *ast.GenDecl:
			names = append(names, genDeclNames(d)...)
		}
	}
	return names
}

// funcDeclName returns the name of an unexported package-level function
// declaration, or nil for methods and non-candidates.
func funcDeclName(d *ast.FuncDecl) *ast.Ident {
	if d.Recv == nil && candidate(d.Name) {
		return d.Name
	}
	return nil
}

// candidate reports whether an identifier is an unused-code candidate:
// unexported and not the blank identifier.
func candidate(n *ast.Ident) bool {
	return !n.IsExported() && n.Name != "_"
}

// genDeclNames returns the unexported names declared by a type or value
// (var/const) declaration group; the blank identifier is never a candidate.
func genDeclNames(d *ast.GenDecl) []*ast.Ident {
	var names []*ast.Ident
	for _, spec := range d.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			if candidate(spec.Name) {
				names = append(names, spec.Name)
			}
		case *ast.ValueSpec:
			for _, n := range spec.Names {
				if candidate(n) {
					names = append(names, n)
				}
			}
		}
	}
	return names
}
