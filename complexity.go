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
		switch n := n.(type) {
		case *ast.IfStmt:
			cc++
		case *ast.ForStmt:
			cc++
		case *ast.RangeStmt:
			cc++
		case *ast.CaseClause:
			cc++
		case *ast.CommClause:
			cc++
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				cc++
			}
		}
		return true
	})
	return cc
}
