package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"io"
	"sort"
	"strings"
)

// The `identical-branches` gate, ported from crap4dart c6b2425add's
// identical_branches: the copy-paste-edit bug family — syntactically
// identical pieces inside one construct where a second, slightly
// different piece was intended. All pieces compare by canonical form
// (whitespace-, comment- and parenthesis-independent, operands of
// commutative operators sorted, same-operator nests flattened), so
// `a == b` matches `b == a` and `a && b` matches `b && a`.
//
// Upstream's six checks; four host in Go:
//
//  1. Duplicate conditions in one if/else-if chain — every later branch
//     is unreachable (linked ifs and switch excluded: the compiler
//     already rejects duplicate constant cases, and else-if links are
//     one chain, not separate if statements).
//  2. Duplicate branch bodies in one chain — an else equal to its then;
//     a single-statement block equals the naked statement and the
//     violation points at the unwrapped statement's line.
//  3. Duplicate operands in one &&/|| chain (`a || a`) plus absorbed
//     operands (`a || b && a` collapses to `a`); only the outermost
//     link of a same-operator chain reports.
//  4. Duplicate positional arguments in one call — all-literal argument
//     lists are exempt (uniform-value APIs legitimately repeat) and a
//     literal is never reported as the repeat. Go has no named
//     arguments, so upstream's named-argument exemption is vacuous.
//
// Host N/A (documented, like upstream's shapes that other hosts lack):
// identical ternary branches — Go has no ternary; duplicate catch
// bodies — Go has no try/catch.
//
// Only pieces of the *same* construct are compared; identical
// conditions in separate if statements are legal (the state may change
// between them).
//
// Default severity is warning: equivalence assumes side-effect-free
// operands, which pure syntax cannot prove. Like string-hazards, the
// upstream `severity` config key maps to the `--severity` flag —
// warning (default) prints findings and exits 0, error exits 2. Findings
// are ignorable via the shared ignore-marker system (kebab-case id
// `identical-branches`).

// flattenableOps are the operators whose same-operator nests may be
// flattened: `a && b && c` compares equal regardless of grouping.
// Other operators keep their tree shape because flattening would merge
// semantically different expressions (`a - (b - c)`).
var flattenableOps = map[token.Token]bool{
	token.LAND: true,
	token.LOR:  true,
	token.ADD:  true,
	token.MUL:  true,
	token.AND:  true,
	token.OR:   true,
	token.XOR:  true,
}

// commutativeOps are the operators whose operand order may be
// canonicalized (sorted), so `a == b` matches `b == a`.
var commutativeOps = map[token.Token]bool{
	token.EQL:  true,
	token.NEQ:  true,
	token.LAND: true,
	token.LOR:  true,
	token.ADD:  true,
	token.MUL:  true,
	token.AND:  true,
	token.OR:   true,
	token.XOR:  true,
}

// IdenticalBranchesViolation is one identical-branch finding.
type IdenticalBranchesViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v IdenticalBranchesViolation) ignorePath() string { return v.Path }

func (v IdenticalBranchesViolation) ignoreLine() int { return v.Line }

// identicalBranchesOptions holds the parsed identical-branches CLI
// configuration.
type identicalBranchesOptions struct {
	paths []string
	fail  bool
}

// RunIdenticalBranchesCommand implements `crap4go identical-branches
// [--severity warning|error] [paths...]`: it prints one line per finding
// plus a summary, exits 0 on findings at the default warning severity,
// and 2 with `--severity error` (ported from crap4dart c6b2425add).
func RunIdenticalBranchesCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseIdenticalBranchesFlags(args, stderr)
	if !ok {
		return code
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, noGoFilesNotice)
		return 0
	}
	violations, constructs := CheckIdenticalBranches(files, root)
	violations = filterIgnored("identical-branches", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d identical-branch findings (%d constructs checked)\n", len(violations), constructs)
		if opts.fail {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "%d constructs checked, no identical-branch findings\n", constructs)
	return 0
}

// parseIdenticalBranchesFlags parses the --severity flag plus positional
// paths. ok=false means a usage error (already reported to stderr); the
// code is the exit code to return.
func parseIdenticalBranchesFlags(args []string, stderr io.Writer) (identicalBranchesOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go identical-branches", flag.ContinueOnError)
	fs.SetOutput(stderr)
	severity := fs.String("severity", "warning", "finding severity: warning (default, exit 0) or error (exit 2 on findings)")
	if err := fs.Parse(args); err != nil {
		return identicalBranchesOptions{}, 1, false
	}
	if *severity != "warning" && *severity != "error" {
		fmt.Fprintf(stderr, "invalid --severity %q: want warning or error\n", *severity)
		return identicalBranchesOptions{}, 1, false
	}
	return identicalBranchesOptions{paths: fs.Args(), fail: *severity == "error"}, 0, true
}

// CheckIdenticalBranches scans every parseable file for the
// copy-paste-edit family and returns the violations plus the number of
// constructs (chains, boolean chains, calls) examined.
func CheckIdenticalBranches(files []string, root string) ([]IdenticalBranchesViolation, int) {
	var violations []IdenticalBranchesViolation
	constructs := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		found, counted := identicalBranches(file, fset, relPath(root, f))
		violations = append(violations, found...)
		constructs += counted
	}
	return violations, constructs
}

// identicalBranches walks one file's AST and returns its findings plus
// the number of constructs checked.
func identicalBranches(file *ast.File, fset *token.FileSet, path string) ([]IdenticalBranchesViolation, int) {
	var violations []IdenticalBranchesViolation
	add := func(line int, message string) {
		violations = append(violations, IdenticalBranchesViolation{
			Path:    path,
			Line:    line,
			Message: message,
		})
	}
	linkedIfs, linkedChains := linkedNodes(file)
	constructs := 0
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.IfStmt:
			// Only chain heads start a scan: an else-if link is part
			// of the chain rooted above it.
			if !linkedIfs[node] {
				constructs++
				scanIfChain(fset, node, add)
			}
		case *ast.BinaryExpr:
			// Only the outermost link of a same-operator chain checks
			// operand duplicates; the flatten step sees the whole
			// chain. Parenthesized links are transparent.
			if (node.Op == token.LAND || node.Op == token.LOR) && !linkedChains[node] {
				constructs++
				checkBooleanChain(fset, node, add)
			}
		case *ast.CallExpr:
			constructs += checkCallArgs(fset, node, add)
		}
		return true
	})
	return violations, constructs
}

// linkedNodes pre-computes which if statements are else-if links of
// another if and which &&/|| expressions are inner links of a
// same-operator chain, so the main walk processes only chain heads and
// outermost boolean chains.
func linkedNodes(file *ast.File) (map[*ast.IfStmt]bool, map[ast.Node]bool) {
	linkedIfs := map[*ast.IfStmt]bool{}
	linkedChains := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.IfStmt:
			if elseIf, ok := node.Else.(*ast.IfStmt); ok {
				linkedIfs[elseIf] = true
			}
		case *ast.BinaryExpr:
			markLinkedChains(node, linkedChains)
		}
		return true
	})
	return linkedIfs, linkedChains
}

// markLinkedChains records node's same-operator operands as inner chain
// links: only the outermost link of a &&/|| chain checks operand
// duplicates, and parenthesized links are transparent.
func markLinkedChains(node *ast.BinaryExpr, linked map[ast.Node]bool) {
	if node.Op != token.LAND && node.Op != token.LOR {
		return
	}
	for _, operand := range [2]ast.Expr{node.X, node.Y} {
		if top, ok := unwrapParens(operand).(*ast.BinaryExpr); ok && top.Op == node.Op {
			linked[top] = true
		}
	}
}

// chainOperand is a top-level chain operand: canonical key plus its
// source node.
type chainOperand struct {
	key  string
	node ast.Expr
}

// scanIfChain compares conditions and bodies within one if/else-if
// chain: every later occurrence of a seen condition or body is a
// finding. Linked ifs and switch are excluded by the caller.
func scanIfChain(fset *token.FileSet, head *ast.IfStmt, add func(int, string)) {
	conditions := map[string]int{}
	bodies := map[string]int{}
	current := head
	for current != nil {
		body := unwrapBody(current.Body)
		key, line := nodeText(fset, body), fset.Position(body.Pos()).Line
		reportRepeat(bodies, key, line,
			"duplicate branch body in if/else-if chain: '"+snippet(fset, body)+"'", add)
		cond := canonicalExpr(fset, current.Cond)
		reportRepeat(conditions, cond, fset.Position(current.Cond.Pos()).Line,
			"duplicate condition in if/else-if chain: '"+snippet(fset, current.Cond)+"'", add)
		switch elseNode := current.Else.(type) {
		case *ast.IfStmt:
			current = elseNode
		case *ast.BlockStmt:
			body := unwrapBody(elseNode)
			key, line := nodeText(fset, body), fset.Position(body.Pos()).Line
			reportRepeat(bodies, key, line,
				"duplicate branch body in if/else-if chain: '"+snippet(fset, body)+"'", add)
			current = nil
		default:
			current = nil
		}
	}
}

// checkBooleanChain flags a repeated operand in one &&/|| chain
// (`a || a` — the operand collapses to itself; `f() || f()` double-calls
// by accident) and, when all operands are distinct, absorbed operands.
func checkBooleanChain(fset *token.FileSet, node *ast.BinaryExpr, add func(int, string)) {
	refs := chainOperands(fset, node.Op, node, nil)
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.key] {
			add(fset.Position(node.Pos()).Line,
				fmt.Sprintf("duplicate operand in '%s' chain: '%s' repeats — the chain collapses to the single operand",
					node.Op, ref.key))
			return
		}
		seen[ref.key] = true
	}
	checkAbsorption(fset, node.Op, refs, add)
}

// checkAbsorption applies the absorption law: an operand of the chain
// that is also a conjunct (for `||`) or disjunct (for `&&`) of a sibling
// operand makes the sibling's extra terms dead — `a || b && a` collapses
// to `a`. Same law both ways: `a && (b || a)` collapses to `a`.
func checkAbsorption(fset *token.FileSet, op token.Token, refs []chainOperand, add func(int, string)) {
	standalone := map[string]bool{}
	for _, ref := range refs {
		standalone[ref.key] = true
	}
	nested := token.LOR
	if op == token.LOR {
		nested = token.LAND
	}
	for _, ref := range refs {
		for _, conjunct := range chainOperands(fset, nested, ref.node, nil) {
			if conjunct.key == ref.key || !standalone[conjunct.key] {
				continue
			}
			add(fset.Position(conjunct.node.Pos()).Line,
				fmt.Sprintf("operand of '%s' is implied by another operand (absorption): '%s' also appears standalone — the extra terms are dead",
					op, snippet(fset, conjunct.node)))
			return
		}
	}
}

// chainOperands collects the top-level operands of an op chain rooted at
// root, as canonical keys paired with their source nodes. A non-chain
// root yields the root itself. Parenthesized operands are transparent.
func chainOperands(fset *token.FileSet, op token.Token, root ast.Expr, out []chainOperand) []chainOperand {
	expr := unwrapParens(root)
	if be, ok := expr.(*ast.BinaryExpr); ok && flattenableOps[op] && be.Op == op {
		out = chainOperands(fset, op, be.X, out)
		return chainOperands(fset, op, be.Y, out)
	}
	return append(out, chainOperand{key: canonicalExpr(fset, expr), node: expr})
}

// checkCallArgs flags a repeated positional argument in one call
// (`f(w, h, w)` — the wrong-variable bug) and reports whether the call
// counted as a construct. All-literal argument lists are exempt
// (uniform-value APIs like rect(8, 8, 8, 4) legitimately repeat) and a
// literal is never reported as the repeat: literal arguments are skipped
// entirely, which covers both. Go has no named arguments.
func checkCallArgs(fset *token.FileSet, call *ast.CallExpr, add func(int, string)) int {
	if len(call.Args) < 2 {
		return 0
	}
	first := map[string]int{}
	for i, arg := range call.Args {
		if isLiteralExpr(arg) {
			continue
		}
		key := canonicalExpr(fset, arg)
		if at, ok := first[key]; ok {
			add(fset.Position(arg.Pos()).Line,
				fmt.Sprintf("duplicate argument in call to '%s': '%s' (same as argument %d)",
					calleeName(fset, call.Fun), snippet(fset, arg), at+1))
			continue
		}
		first[key] = i
	}
	return 1
}

// unwrapParens returns expr with any surrounding parentheses removed.
func unwrapParens(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

// canonicalExpr returns the canonical form of expr: whitespace-,
// comment- and parenthesis-independent, with operands of commutative
// operators sorted and same-operator nests of associative operators
// flattened.
func canonicalExpr(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	writeCanonicalExpr(fset, unwrapParens(expr), &buf)
	return buf.String()
}

// writeCanonicalExpr renders one unwrapped expression canonically:
// binaries as `op(operand,...)` with flattened same-operator operands,
// unaries as `op(operand)`, leaves as their printer text.
func writeCanonicalExpr(fset *token.FileSet, expr ast.Expr, buf *bytes.Buffer) {
	if be, ok := expr.(*ast.BinaryExpr); ok {
		var operands []string
		flattenCanonical(fset, be.Op, be.X, be.Y, &operands)
		if commutativeOps[be.Op] {
			sort.Strings(operands)
		}
		fmt.Fprintf(buf, "%s(%s)", be.Op, strings.Join(operands, ","))
		return
	}
	if ue, ok := expr.(*ast.UnaryExpr); ok {
		fmt.Fprintf(buf, "%s(", ue.Op)
		writeCanonicalExpr(fset, unwrapParens(ue.X), buf)
		buf.WriteString(")")
		return
	}
	buf.WriteString(nodeText(fset, expr))
}

// flattenCanonical collects the canonical operands of an op chain.
// Same-operator nests are flattened for the associative operators;
// other operators keep their tree shape via their own canonical form.
func flattenCanonical(fset *token.FileSet, op token.Token, x, y ast.Expr, out *[]string) {
	for _, operand := range [2]ast.Expr{x, y} {
		expr := unwrapParens(operand)
		if be, ok := expr.(*ast.BinaryExpr); ok && flattenableOps[op] && be.Op == op {
			flattenCanonical(fset, op, be.X, be.Y, out)
			continue
		}
		*out = append(*out, canonicalExpr(fset, expr))
	}
}

// unwrapBody returns a branch body with single-statement blocks
// unwrapped to their statement, so `{ return x }` equals `return x` and
// the violation points at the code, not the brace.
func unwrapBody(body ast.Stmt) ast.Stmt {
	stmt := body
	for {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok || len(block.List) != 1 {
			break
		}
		stmt = block.List[0]
	}
	return stmt
}

// isLiteralExpr reports whether expr is a literal (numeric, string or
// char literal, or the predeclared nil/true/false) — repeated literals
// are exempt from the argument check and never reported as the repeat.
func isLiteralExpr(expr ast.Expr) bool {
	switch e := unwrapParens(expr).(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return e.Name == "nil" || e.Name == "true" || e.Name == "false"
	}
	return false
}

// calleeName returns the last name segment of a call's function
// expression (methods, plain functions, generic instantiations); any
// other shape — conversions like `[]byte(s)` — falls back to its
// printer text.
func calleeName(fset *token.FileSet, fun ast.Expr) string {
	switch f := unwrapParens(fun).(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr:
		return calleeName(fset, f.X)
	case *ast.IndexListExpr:
		return calleeName(fset, f.X)
	}
	return snippet(fset, fun)
}

// reportRepeat records key's first occurrence line or reports a repeat
// at the violation site: `message (same as line N)`.
func reportRepeat(seen map[string]int, key string, line int, message string, add func(int, string)) {
	if first, ok := seen[key]; ok {
		add(line, fmt.Sprintf("%s (same as line %d)", message, first))
		return
	}
	seen[key] = line
}

// nodeText renders a node with go/printer: whitespace- and
// comment-independent token text (comments live in the file's comment
// list, not in the node).
func nodeText(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	printer.Fprint(&buf, fset, n)
	return buf.String()
}

// snippet truncates a node's text to a 40-rune preview for messages.
func snippet(fset *token.FileSet, n ast.Node) string {
	text := []rune(nodeText(fset, n))
	if len(text) > 40 {
		return string(text[:37]) + "..."
	}
	return string(text)
}
