package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
)

// The `collapsible-flow` gate, ported from crap4dart 7bdfd64ae1's
// collapsible_flow: control-flow nesting that a semantics-preserving
// mechanical rewrite can flatten. Three checks:
//
//  1. Collapsible if — `if a { if b { X } }` with no `else` on either
//     level merges into `if a && b { X }`. Short-circuit `&&` preserves
//     evaluation order, so the merge is always equivalent (including
//     side-effecting conditions). An inner if carrying its own init
//     statement never merges — the init cannot move into a `&&` operand
//     (`if a && b := f(); b` is invalid Go). Go if bodies are always
//     braced and Go has no pattern conditions, so upstream's `case`
//     exclusion has no host shape; the outer if's own init merges fine
//     (`if x := f(); x && b { X }`).
//  2. Redundant else — `if c { return X } else { Y }` where the
//     then-branch always exits (`return`, `break`, `continue`, a
//     `panic` call, a block ending in one of those, or an if/else pair
//     whose both branches exit): the `else` is redundant — drop the
//     keyword and dedent (gofmt handles the dedent). An inner if
//     WITHOUT an else never counts as exiting: control falls through
//     when its condition is false — dedenting would change behavior.
//     switch and select bodies are not analyzed (case fall-through /
//     channel nondeterminism), so a trailing switch/select never
//     counts; `goto` and `fallthrough` stay outside the jump family
//     (conservative, like upstream's shapes it chooses not to count).
//  3. Chain after terminal then — an `else if` following an
//     always-exiting then-branch. The whole chain wants a guard
//     sequence or a dispatch table; that rewrite is not mechanical, so
//     this check only points at it (no fix-shaped message).
//
// Host N/A: upstream's try/finally exit shapes — Go has no try/finally,
// and defer is function-scoped, so nothing can absorb a branch exit.
//
// Every finding is correct code — this gate trades flatness, not bugs.
// Like string-hazards, the upstream `severity` config key maps to the
// `--severity` flag — warning (default) prints findings and exits 0,
// error exits 2. Findings are ignorable via the shared ignore-marker
// system (kebab-case id `collapsible-flow`).

// CollapsibleFlowViolation is one collapsible-flow finding.
type CollapsibleFlowViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v CollapsibleFlowViolation) ignorePath() string { return v.Path }

func (v CollapsibleFlowViolation) ignoreLine() int { return v.Line }

// collapsibleFlowOptions holds the parsed collapsible-flow CLI
// configuration.
type collapsibleFlowOptions struct {
	paths []string
	fail  bool
}

// RunCollapsibleFlowCommand implements `crap4go collapsible-flow
// [--severity warning|error] [paths...]`: it prints one line per finding
// plus a summary, exits 0 on findings at the default warning severity,
// and 2 with `--severity error` (ported from crap4dart 7bdfd64ae1).
func RunCollapsibleFlowCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseCollapsibleFlowFlags(args, stderr)
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
	violations, constructs := CheckCollapsibleFlow(files, root)
	violations = filterIgnored("collapsible-flow", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d collapsible-flow findings (%d if statements checked)\n", len(violations), constructs)
		if opts.fail {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "%d if statements checked, no collapsible flow\n", constructs)
	return 0
}

// parseCollapsibleFlowFlags parses the --severity flag plus positional
// paths. ok=false means a usage error (already reported to stderr); the
// code is the exit code to return.
func parseCollapsibleFlowFlags(args []string, stderr io.Writer) (collapsibleFlowOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go collapsible-flow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	severity := fs.String("severity", "warning", "finding severity: warning (default, exit 0) or error (exit 2 on findings)")
	if err := fs.Parse(args); err != nil {
		return collapsibleFlowOptions{}, 1, false
	}
	if *severity != "warning" && *severity != "error" {
		fmt.Fprintf(stderr, "invalid --severity %q: want warning or error\n", *severity)
		return collapsibleFlowOptions{}, 1, false
	}
	return collapsibleFlowOptions{paths: fs.Args(), fail: *severity == "error"}, 0, true
}

// CheckCollapsibleFlow scans every parseable file for the collapsible
// control-flow shapes and returns the violations plus the number of if
// statements examined.
func CheckCollapsibleFlow(files []string, root string) ([]CollapsibleFlowViolation, int) {
	var violations []CollapsibleFlowViolation
	constructs := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		found, counted := collapsibleFlow(file, fset, relPath(root, f))
		violations = append(violations, found...)
		constructs += counted
	}
	return violations, constructs
}

// collapsibleFlow walks one file's AST and returns its findings plus the
// number of if statements checked.
func collapsibleFlow(file *ast.File, fset *token.FileSet, path string) ([]CollapsibleFlowViolation, int) {
	var violations []CollapsibleFlowViolation
	add := func(node ast.Node, message string) {
		violations = append(violations, CollapsibleFlowViolation{
			Path:    path,
			Line:    fset.Position(node.Pos()).Line,
			Message: message,
		})
	}
	constructs := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if node, ok := n.(*ast.IfStmt); ok {
			constructs++
			checkCollapsibleIf(node, add)
			checkRedundantElse(node, add)
		}
		return true
	})
	return violations, constructs
}

// checkCollapsibleIf flags `if a { if b { X } }` — no else on either
// level, the then-block's only statement is the inner if. Short-circuit
// && preserves evaluation order, so the merge is always equivalent.
func checkCollapsibleIf(node *ast.IfStmt, add func(ast.Node, string)) {
	if node.Else != nil {
		return
	}
	inner := singleInnerIf(node.Body)
	if inner == nil || inner.Else != nil || inner.Init != nil {
		return
	}
	add(node, "nested if can be merged into a single condition with `&&`")
}

// singleInnerIf returns the inner if of a collapsible then-branch: the
// block's only statement (Go if bodies are always braced). nil when the
// branch has any other shape.
func singleInnerIf(body *ast.BlockStmt) *ast.IfStmt {
	if len(body.List) != 1 {
		return nil
	}
	inner, _ := body.List[0].(*ast.IfStmt)
	return inner
}

// checkRedundantElse flags checks 2 and 3: an always-exiting
// then-branch makes a plain else redundant (drop the keyword, dedent)
// and turns an else if into a chain flattening hint.
func checkRedundantElse(node *ast.IfStmt, add func(ast.Node, string)) {
	if node.Else == nil || !alwaysExits(node.Body) {
		return
	}
	if elseIf, ok := node.Else.(*ast.IfStmt); ok {
		add(elseIf, "else-if follows an always-exiting then branch — flatten the chain into guard clauses or a dispatch table")
		return
	}
	add(node, "redundant else after an always-exiting then branch — dedent the else body")
}

// alwaysExits reports whether stmt always exits its enclosing construct
// (the enclosing if/loop/function). Conservative: an if without an else
// falls through and never counts; switch and select bodies are not
// analyzed, so a trailing switch/select never counts.
func alwaysExits(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.BREAK || s.Tok == token.CONTINUE
	case *ast.ExprStmt:
		return isPanicCall(s.X)
	case *ast.BlockStmt:
		return exitsAsBlock(s)
	case *ast.IfStmt:
		return exitsAsIf(s)
	}
	return false
}

// exitsAsBlock reports whether a non-empty block's last statement exits.
func exitsAsBlock(block *ast.BlockStmt) bool {
	return len(block.List) > 0 && alwaysExits(block.List[len(block.List)-1])
}

// exitsAsIf reports whether an if exits: both branches must exist and
// exit — a bare if falls through when its condition is false (the
// source_instrumenter trap upstream learned the hard way).
func exitsAsIf(s *ast.IfStmt) bool {
	return s.Else != nil && alwaysExits(s.Body) && alwaysExits(s.Else)
}

// isPanicCall reports whether expr is a call to the predeclared panic —
// the only expression statement that cannot return (upstream counts
// throw/rethrow; Go's statement-level counterpart is the panic call).
// A file-local declaration named panic resolves in Obj, so a user
// panic that returns normally is never counted.
func isPanicCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "panic" && ident.Obj == nil
}
