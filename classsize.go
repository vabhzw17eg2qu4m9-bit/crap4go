package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"path/filepath"
)

// Class-size limits, ported from crap4dart's ClassSizeGate defaults. Go
// adaptation: named types replace classes; methods are gathered across the
// whole package directory.
const (
	maxClassMethods = 25
	maxWmc          = 80
)

// ClassSizeViolation is one named type over the method-count or WMC limit.
type ClassSizeViolation struct {
	Path    string
	Line    int
	Message string
}

// RunClassSizeCommand implements `crap4go class-size [paths...]`: it flags
// named types with more than maxClassMethods methods or a weighted-methods
// sum (total cyclomatic complexity over the type's methods) above maxWmc,
// prints one line per violation plus a summary, and returns exit code 2 iff
// there are violations.
func RunClassSizeCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectFiles(false, args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckClassSize(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d violations in %d types over %d methods/WMC %d\n",
			len(violations), checked, maxClassMethods, maxWmc)
		return 2
	}
	fmt.Fprintf(stdout, "%d types within %d methods/WMC %d\n", checked, maxClassMethods, maxWmc)
	return 0
}

// ClassSizeStats is the method count and weighted-methods sum of one named
// type within a package.
type ClassSizeStats struct {
	Name    string
	Path    string
	Line    int
	Methods int
	WMC     int
}

// CheckClassSize gathers methods per named type across whole packages and
// returns the size violations plus the number of types checked.
func CheckClassSize(files []string, root string) ([]ClassSizeViolation, int) {
	stats := map[string]*ClassSizeStats{}
	var order []string
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		collectMethodStats(file, fset, relPath(root, f), relPath(root, filepath.Dir(f)), stats, &order)
	}
	var violations []ClassSizeViolation
	for _, key := range order {
		violations = append(violations, statsViolations(stats[key])...)
	}
	return violations, len(stats)
}

// collectMethodStats adds every method declaration in file to the receiver
// type's stats, appending new type keys to order on first sight.
func collectMethodStats(file *ast.File, fset *token.FileSet, path, pkgDir string, stats map[string]*ClassSizeStats, order *[]string) {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		addMethodStats(fd, fset, path, pkgDir, stats, order)
	}
}

// addMethodStats records one method's presence and complexity on its
// receiver type's stats.
func addMethodStats(fd *ast.FuncDecl, fset *token.FileSet, path, pkgDir string, stats map[string]*ClassSizeStats, order *[]string) {
	key := pkgDir + "." + receiverTypeName(fd)
	s, seen := stats[key]
	if !seen {
		s = &ClassSizeStats{
			Name: receiverTypeName(fd),
			Path: path,
			Line: fset.Position(fd.Pos()).Line,
		}
		stats[key] = s
		*order = append(*order, key)
	}
	s.Methods++
	s.WMC += ComplexityOf(fd.Body)
}

// statsViolations returns the limit violations of one type's stats.
func statsViolations(s *ClassSizeStats) []ClassSizeViolation {
	var out []ClassSizeViolation
	if s.Methods > maxClassMethods {
		out = append(out, ClassSizeViolation{
			Path:    s.Path,
			Line:    s.Line,
			Message: fmt.Sprintf("%s has %d methods > max %d", s.Name, s.Methods, maxClassMethods),
		})
	}
	if s.WMC > maxWmc {
		out = append(out, ClassSizeViolation{
			Path:    s.Path,
			Line:    s.Line,
			Message: fmt.Sprintf("%s WMC=%d > max %d", s.Name, s.WMC, maxWmc),
		})
	}
	return out
}
