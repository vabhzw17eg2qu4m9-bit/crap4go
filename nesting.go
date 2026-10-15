package main

import (
	"fmt"
	"go/ast"
	"io"
)

// maxNesting is the deepest allowed block nesting (ported from crap4dart's
// NestingGate default). The function body block counts as level 1; every
// nested block or control-flow statement adds one.
const maxNesting = 5

// NestingViolation is one function nested deeper than maxNesting.
type NestingViolation struct {
	Path    string
	Line    int
	Message string
}

// RunNestingCommand implements `crap4go nesting [paths...]`: it flags
// functions whose maximum block nesting exceeds maxNesting, prints one line
// per violation plus a summary, and returns exit code 2 iff there are
// violations. Selection defaults to the normal analyze rules.
func RunNestingCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectFiles(false, args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckNesting(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d functions nested deeper than %d\n", len(violations), checked, maxNesting)
		return 2
	}
	fmt.Fprintf(stdout, "%d functions within nesting %d\n", checked, maxNesting)
	return 0
}

// CheckNesting evaluates every declared function with a body and returns the
// nesting violations plus the number of functions checked.
func CheckNesting(files []string, root string) ([]NestingViolation, int) {
	var violations []NestingViolation
	checked := 0
	for _, f := range files {
		checked += nestingFile(f, root, &violations)
	}
	return violations, checked
}

// nestingFile flags one file's top-level functions nested deeper than
// maxNesting, appends violations to out, and returns the functions checked.
func nestingFile(f, root string, out *[]NestingViolation) int {
	file, fset, err := parseGoFile(f)
	if err != nil {
		return 0
	}
	checked := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		checked++
		if depth := stmtNesting(fd.Body); depth > maxNesting {
			*out = append(*out, NestingViolation{
				Path:    relPath(root, f),
				Line:    fset.Position(fd.Pos()).Line,
				Message: fmt.Sprintf("%s nesting=%d > max %d", funcDisplayName(fd), depth, maxNesting),
			})
		}
	}
	return checked
}

// stmtNesting returns the nesting depth contributed by statement s: 1 for a
// block or control-flow statement plus the deepest nesting inside it, 0 for
// any other statement.
func stmtNesting(s ast.Stmt) int {
	level, children := nestingParts(s)
	deepest := 0
	for _, c := range children {
		if d := stmtNesting(c); d > deepest {
			deepest = d
		}
	}
	return level + deepest
}

// nestingParts returns the nesting level s contributes (1 for a block or
// control-flow statement, 0 otherwise) and the statements nested directly
// inside it. Control-statement bodies are flattened: the braces of an if/
// for/switch body do not add a level on top of the statement itself; only
// standalone block statements do.
func nestingParts(s ast.Stmt) (int, []ast.Stmt) {
	switch s := s.(type) {
	case *ast.BlockStmt:
		return 1, s.List
	case *ast.IfStmt:
		return 1, ifChildren(s)
	case *ast.ForStmt:
		return 1, s.Body.List
	case *ast.RangeStmt:
		return 1, s.Body.List
	case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		return 1, caseBodies(clauseBlock(s))
	}
	return 0, nil
}

// ifChildren returns the statements nested inside an if statement: its
// then-body plus its else side. An else-if chain is flat — every chained
// if's branches nest at the originating if's level — while an if inside a
// plain else block keeps paying a level of its own (ported from
// crap4dart 7bf7a15's flat-else-if NestingGate).
func ifChildren(s *ast.IfStmt) []ast.Stmt {
	children := append([]ast.Stmt{}, s.Body.List...)
	for e := s.Else; e != nil; {
		if block, ok := e.(*ast.BlockStmt); ok {
			children = append(children, block.List...)
			break
		}
		chain, ok := e.(*ast.IfStmt)
		if !ok {
			children = append(children, e)
			break
		}
		children = append(children, chain.Body.List...)
		e = chain.Else
	}
	return children
}

// clauseBlock returns the body block of a switch, type-switch or select
// statement, or nil for any other statement.
func clauseBlock(s ast.Stmt) *ast.BlockStmt {
	switch s := s.(type) {
	case *ast.SwitchStmt:
		return s.Body
	case *ast.TypeSwitchStmt:
		return s.Body
	case *ast.SelectStmt:
		return s.Body
	}
	return nil
}

// caseBodies flattens the statement lists of a switch/select body's clauses.
func caseBodies(body *ast.BlockStmt) []ast.Stmt {
	var out []ast.Stmt
	if body == nil {
		return out
	}
	for _, c := range body.List {
		switch c := c.(type) {
		case *ast.CaseClause:
			out = append(out, c.Body...)
		case *ast.CommClause:
			out = append(out, c.Body...)
		}
	}
	return out
}
