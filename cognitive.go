package main

import (
	"go/ast"
	"go/token"
)

// cognitiveWalker accumulates the cognitive score with the current nesting
// depth; lambdas controls whether function-literal bodies are entered.
type cognitiveWalker struct {
	depth   int
	score   int
	lambdas bool
}

// CognitiveOf returns the cognitive complexity of a Go function body under
// the SonarSource definition (white paper "Cognitive Complexity", v1.2) —
// the Go counterpart of crap4dart's CognitiveCalculator (ported from
// crap4dart 7bf7a15).
//
// Basic increment, +1 per flow break:
//   - *ast.IfStmt   (if; a chained else-if adds +1 per chained if, a plain
//     else block adds +1 on its own)
//   - *ast.ForStmt  (all classic for forms)
//   - *ast.RangeStmt (for ... range)
//   - *ast.CaseClause (every switch case or default; go/ast shares this
//     node between expression and type switches)
//   - *ast.CommClause (every select comm case or default)
//   - each *sequence* of identical && / || operators: `a && b && c` is one
//     sequence (+1), `a && b || c` is two (+2)
//
// Nesting increment: every structural construct above (everything but the
// logical-operator sequences) adds its current depth N on top of the basic
// increment when it sits inside another structural construct. Depth grows
// with if branches (then and plain else), loop bodies, switch/select
// members and function-literal bodies — function literals themselves do
// not increment, like nested methods in the SonarSource definition.
//
// Go grammar notes: there is no ternary, while/do-while or
// catch-as-construct, goto and labels are not flow breaks (upstream lists
// none of them), and named functions cannot be declared inside another
// function, so upstream's "nested named functions are skipped" holds
// vacuously. Mirroring upstream's CognitiveCalculator, an if statement's
// condition is deliberately not traversed (upstream overrides
// visitIfStatement without a super call), while loop conditions, switch
// expressions and case expressions are.
func CognitiveOf(body *ast.BlockStmt, countLambdas bool) int {
	if body == nil {
		return 0
	}
	c := &cognitiveWalker{lambdas: countLambdas}
	c.stmts(body.List)
	return c.score
}

// bump adds the basic-plus-nesting increment for one structural construct.
func (c *cognitiveWalker) bump() {
	c.score += 1 + c.depth
}

// deeper runs f with the nesting depth increased by one.
func (c *cognitiveWalker) deeper(f func()) {
	c.depth++
	f()
	c.depth--
}

func (c *cognitiveWalker) stmts(list []ast.Stmt) {
	for _, s := range list {
		c.stmt(s)
	}
}

func (c *cognitiveWalker) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.IfStmt:
		c.ifStmt(s)
	case *ast.BlockStmt:
		c.stmts(s.List) // bare block: no flow break, no nesting
	case *ast.LabeledStmt:
		c.stmt(s.Stmt) // labels and goto are not flow breaks
	default:
		c.compoundStmt(s)
	}
}

// compoundStmt scores the switch family — expression switch, type switch,
// select — whose clause bodies nest; loops and leaf statements flow on to
// flowStmt.
func (c *cognitiveWalker) compoundStmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.SwitchStmt:
		c.walkNode(s.Init)
		c.walkNode(s.Tag)
		c.cases(s.Body)
	case *ast.TypeSwitchStmt:
		c.walkNode(s.Assign)
		c.cases(s.Body)
	case *ast.SelectStmt:
		c.cases(s.Body)
	default:
		c.flowStmt(s)
	}
}

// flowStmt counts the loop statements — the remaining flow breaks — and
// treats every other statement as a leaf whose cognitive content lives in
// the expressions below it.
func (c *cognitiveWalker) flowStmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.ForStmt:
		c.bump()
		c.walkNode(s.Init)
		c.walkNode(s.Cond)
		c.walkNode(s.Post)
		c.deeper(func() { c.stmts(s.Body.List) })
	case *ast.RangeStmt:
		c.bump()
		c.walkNode(s.X)
		c.deeper(func() { c.stmts(s.Body.List) })
	default:
		c.walkNode(s)
	}
}

// ifStmt scores an if: +1+depth, the then branch one level deeper, a plain
// else as its own +1+depth one level deeper — while an else-if chain stays
// flat, each chained if incrementing once at the originating if's depth.
func (c *cognitiveWalker) ifStmt(s *ast.IfStmt) {
	c.bump()
	c.deeper(func() { c.stmts(s.Body.List) })
	switch e := s.Else.(type) {
	case *ast.IfStmt:
		c.ifStmt(e)
	case *ast.BlockStmt:
		c.bump()
		c.deeper(func() { c.stmts(e.List) })
	}
}

// cases scores a switch/select body: every clause (case or default) is a
// flow break costing +1+depth; its case expressions count at the current
// depth, its body one level deeper.
func (c *cognitiveWalker) cases(body *ast.BlockStmt) {
	for _, st := range body.List {
		c.bump()
		switch cl := st.(type) {
		case *ast.CaseClause:
			for _, e := range cl.List {
				c.walkNode(e)
			}
			c.deeper(func() { c.stmts(cl.Body) })
		case *ast.CommClause:
			c.walkNode(cl.Comm)
			c.deeper(func() { c.stmts(cl.Body) })
		}
	}
}

// walkNode descends into an expression (or a leaf statement) for the only
// cognitive content below it: logical-operator sequences and function-
// literal bodies, the latter walked one level deeper.
func (c *cognitiveWalker) walkNode(n ast.Node) {
	if n == nil {
		return
	}
	var parents []ast.Node
	ast.Inspect(n, func(cur ast.Node) bool {
		if cur == nil {
			parents = parents[:len(parents)-1]
			return false
		}
		if x, ok := cur.(*ast.FuncLit); ok {
			if c.lambdas {
				c.deeper(func() { c.stmts(x.Body.List) })
			}
			return false // body scored above; no further descent
		}
		c.countSequence(parents, cur)
		parents = append(parents, cur)
		return true
	})
}

// countSequence adds +1 when cur is the root of a new sequence of identical
// operators: `a && b && c` is one sequence (+1), `a && b || c` is two (+2).
func (c *cognitiveWalker) countSequence(parents []ast.Node, cur ast.Node) {
	b, ok := cur.(*ast.BinaryExpr)
	if !ok || (b.Op != token.LAND && b.Op != token.LOR) {
		return
	}
	if p, ok := seqParent(parents); ok && p == b.Op {
		return // inside an identical-operator sequence, not its root
	}
	c.score++
}

// seqParent reports the logical operator (&& or ||) of the directly
// enclosing binary expression, if any.
func seqParent(parents []ast.Node) (token.Token, bool) {
	if len(parents) == 0 {
		return token.ILLEGAL, false
	}
	if p, ok := parents[len(parents)-1].(*ast.BinaryExpr); ok {
		if p.Op == token.LAND || p.Op == token.LOR {
			return p.Op, true
		}
	}
	return token.ILLEGAL, false
}
