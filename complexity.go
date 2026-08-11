package main

import (
	"go/ast"
	"go/token"
)

// ComplexityOf returns the cyclomatic complexity of a Go function body.
//
// Base value 1, +1 for each occurrence of:
//   - *ast.IfStmt      (if)
//   - *ast.ForStmt     (for)
//   - *ast.RangeStmt   (for ... range)
//   - *ast.CaseClause  (switch case or default)
//   - *ast.CommClause  (select case or default)
//   - *ast.BinaryExpr  with op token.LAND or token.LOR (&& / ||)
//
// Anonymous function literals (*ast.FuncLit) inside the body are traversed and
// their branches count towards the enclosing function (matches crap4java and
// crap4dart with countLambdas=true). Go has no ternary, while, do-while, or
// catch-as-construct.
func ComplexityOf(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}
	cc := 1
	ast.Inspect(body, func(n ast.Node) bool {
		cc += complexityDelta(n)
		return true
	})
	return cc
}

// complexityDelta returns the cyclomatic-complexity contribution of a single
// AST node: 1 for a decision/branch node (if/for/range/case/select-case) or a
// short-circuiting && / || operator, 0 otherwise.
func complexityDelta(n ast.Node) int {
	switch n := n.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
		return 1
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			return 1
		}
	}
	return 0
}
