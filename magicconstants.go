package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"regexp"
	"sort"
	"strconv"
)

// Magic-constants defaults, ported from crap4dart's magic_constants gate.
const (
	minLiteralRepeats = 3
	minLiteralLength  = 4
)

// hexColorRE matches 0xRRGGBB / 0xAARRGGBB integer lexemes.
var hexColorRE = regexp.MustCompile(`^0[xX][0-9a-fA-F]{6,8}$`)

// MagicConstantsViolation is one magic literal occurrence.
type MagicConstantsViolation struct {
	Path    string
	Line    int
	Message string
}

// RunMagicConstantsCommand implements `crap4go magic-constants [paths...]`:
// it flags hex color integer literals outside const declarations and
// numeric/string literals repeating at least minLiteralRepeats times in one
// file, prints one line per violation plus a summary, and returns exit code
// 2 iff there are violations (ported from crap4dart 0.6.0).
func RunMagicConstantsCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectFiles(false, args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckMagicConstants(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d magic constant(s) in %d files\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d files free of magic constants\n", checked)
	return 0
}

// CheckMagicConstants scans every parseable file for magic literals and
// returns the violations plus the number of files checked.
func CheckMagicConstants(files []string, root string) ([]MagicConstantsViolation, int) {
	var violations []MagicConstantsViolation
	checked := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		checked++
		sites := collectLiterals(file, fset)
		violations = append(violations, literalViolations(sites, relPath(root, f))...)
	}
	return violations, checked
}

// literalSites holds, for one file, the lines of hex color literals, the
// occurrence lines per literal value, and the const-initializer lines that
// are hex-exempt.
type literalSites struct {
	hexLines      []int
	valueLines    map[string][]int
	constantLines map[int]bool
}

// collectLiterals walks file once, recording literal sites and marking
// const-initializer lines (upstream marks the initializer's own line plus
// the lines of a direct call initializer's arguments).
func collectLiterals(file *ast.File, fset *token.FileSet) *literalSites {
	sites := &literalSites{valueLines: map[string][]int{}, constantLines: map[int]bool{}}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			if n.Tok == token.CONST {
				markConstantLines(n, fset, sites.constantLines)
			}
		case *ast.BasicLit:
			sites.record(n, fset)
		}
		return true
	})
	return sites
}

// markConstantLines marks the start line of every value in a const
// declaration plus the start lines of a direct call's arguments.
func markConstantLines(decl *ast.GenDecl, fset *token.FileSet, lines map[int]bool) {
	for _, spec := range decl.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, value := range vs.Values {
			lines[fset.Position(value.Pos()).Line] = true
			if call, ok := value.(*ast.CallExpr); ok {
				for _, arg := range call.Args {
					lines[fset.Position(arg.Pos()).Line] = true
				}
			}
		}
	}
}

// record notes one basic literal's line: integer lexemes matching hexColorRE
// become hex candidates, and every string/numeric value at least
// minLiteralLength long becomes a repeat candidate.
func (s *literalSites) record(lit *ast.BasicLit, fset *token.FileSet) {
	line := fset.Position(lit.Pos()).Line
	if lit.Kind == token.INT && hexColorRE.MatchString(lit.Value) {
		s.hexLines = append(s.hexLines, line)
	}
	if value, ok := literalValue(lit); ok && len(value) >= minLiteralLength {
		s.valueLines[value] = append(s.valueLines[value], line)
	}
}

// literalValue returns the repeat-check value of a literal: the unquoted
// string for strings, the raw lexeme for integers and floats. Char and
// imaginary literals have no upstream counterpart and never count.
func literalValue(lit *ast.BasicLit) (string, bool) {
	switch lit.Kind {
	case token.STRING:
		if value, err := strconv.Unquote(lit.Value); err == nil {
			return value, true
		}
		return lit.Value, true
	case token.INT, token.FLOAT:
		return lit.Value, true
	}
	return "", false
}

// literalViolations turns one file's collected sites into violations: hex
// colors on non-constant lines first, then every occurrence of a value
// repeating at least minLiteralRepeats times (values visited in sorted order
// for deterministic output).
func literalViolations(s *literalSites, path string) []MagicConstantsViolation {
	var violations []MagicConstantsViolation
	for _, line := range s.hexLines {
		if !s.constantLines[line] {
			violations = append(violations, MagicConstantsViolation{
				Path: path, Line: line,
				Message: "hex color outside a constant declaration",
			})
		}
	}
	for _, value := range sortedKeys(s.valueLines) {
		lines := s.valueLines[value]
		if len(lines) < minLiteralRepeats {
			continue
		}
		for _, line := range lines {
			violations = append(violations, MagicConstantsViolation{
				Path: path, Line: line,
				Message: fmt.Sprintf("literal %s repeats %d times — extract a named constant", value, len(lines)),
			})
		}
	}
	return violations
}

// sortedKeys returns m's keys in sorted order.
func sortedKeys(m map[string][]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
