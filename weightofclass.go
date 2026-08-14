package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"path/filepath"
)

// maxWeight is the highest allowed ratio of exported fields to exported
// members (fields + methods), ported from crap4dart's WeightOfClassGate
// default. Go adaptation: exported named struct types replace public classes;
// methods are gathered across the whole package directory. Types without
// exported fields are never flagged (data-only structs are legitimate).
const maxWeight = 0.33

// WeightViolation is one data-heavy exported struct type.
type WeightViolation struct {
	Path    string
	Line    int
	Message string
}

// RunWeightOfClassCommand implements `crap4go weight-of-class [paths...]`:
// it flags exported named struct types whose ratio of exported fields to
// exported members exceeds maxWeight, prints one line per violation plus a
// summary, and returns exit code 2 iff there are violations.
func RunWeightOfClassCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectFiles(false, args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckWeightOfClass(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d types over weight %.2f\n", len(violations), checked, maxWeight)
		return 2
	}
	fmt.Fprintf(stdout, "%d types within weight %.2f\n", checked, maxWeight)
	return 0
}

// WeightStats is the exported-member profile of one named struct type.
type WeightStats struct {
	Name            string
	Path            string
	Line            int
	Fields          int
	ExportedMethods int
}

// CheckWeightOfClass evaluates every exported named struct type and returns
// the weight violations plus the number of types with exported fields
// checked.
func CheckWeightOfClass(files []string, root string) ([]WeightViolation, int) {
	stats := map[string]*WeightStats{}
	var order []string
	methods := map[string]int{}
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		pkgDir := relPath(root, filepath.Dir(f))
		countExportedMethods(file, pkgDir, methods)
		recordStructStats(file, fset, relPath(root, f), pkgDir, stats, &order)
	}
	return weightViolations(stats, order, methods), len(order)
}

// recordStructStats records WeightStats for every exported named struct
// type declared in file, appending keys to order on first sight.
func recordStructStats(file *ast.File, fset *token.FileSet, path, pkgDir string, stats map[string]*WeightStats, order *[]string) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			recordStructSpec(spec, fset, path, pkgDir, stats, order)
		}
	}
}

// recordStructSpec records WeightStats for one exported named struct type
// specification (any other spec is ignored).
func recordStructSpec(spec ast.Spec, fset *token.FileSet, path, pkgDir string, stats map[string]*WeightStats, order *[]string) {
	ts, ok := spec.(*ast.TypeSpec)
	if !ok || !ts.Name.IsExported() {
		return
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return
	}
	key := pkgDir + "." + ts.Name.Name
	if _, seen := stats[key]; !seen {
		stats[key] = &WeightStats{
			Name:   ts.Name.Name,
			Path:   path,
			Line:   fset.Position(ts.Pos()).Line,
			Fields: exportedFieldCount(st),
		}
		*order = append(*order, key)
	}
}

// countExportedMethods adds the number of exported methods per receiver type
// in file to the methods map (keyed like CheckClassSize's stats).
func countExportedMethods(file *ast.File, pkgDir string, methods map[string]int) {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || !fd.Name.IsExported() {
			continue
		}
		methods[pkgDir+"."+receiverTypeName(fd)]++
	}
}

// exportedFieldCount counts a struct's exported fields, including exported
// embedded types.
func exportedFieldCount(st *ast.StructType) int {
	n := 0
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			if embeddedExported(field.Type) {
				n++
			}
			continue
		}
		for _, name := range field.Names {
			if name.IsExported() {
				n++
			}
		}
	}
	return n
}

// embeddedExported reports whether an embedded field expression names an
// exported type, unwrapping pointers.
func embeddedExported(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.StarExpr:
		return embeddedExported(e.X)
	case *ast.Ident:
		return e.IsExported()
	case *ast.SelectorExpr:
		return e.Sel.IsExported()
	}
	return false
}

// weightViolations flags every recorded type whose exported-field ratio
// exceeds maxWeight; types without exported fields are never checked.
func weightViolations(stats map[string]*WeightStats, order []string, methods map[string]int) []WeightViolation {
	var out []WeightViolation
	for _, key := range order {
		s := stats[key]
		if s.Fields == 0 {
			continue
		}
		members := s.Fields + methods[key]
		if weight := float64(s.Fields) / float64(members); weight > maxWeight {
			out = append(out, WeightViolation{
				Path: s.Path,
				Line: s.Line,
				Message: fmt.Sprintf("%s exposes %d public fields of %d public members (weight=%.2f)",
					s.Name, s.Fields, members, weight),
			})
		}
	}
	return out
}
