package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"strconv"
	"strings"
)

// The `string-hazards` gate, ported from crap4dart 62fec6833's
// string_hazards. Upstream flags three string-literal hazard shapes; Go
// hosts two of them:
//
//  1. Escaped interpolation — N/A: Go has no string interpolation to
//     escape, so there is nothing to flag and no false positive to guard.
//  2. Raw regexp with an escaped anchor — a raw (backtick) string passed
//     to regexp.Compile/MustCompile/CompilePOSIX/MustCompilePOSIX ending
//     with `\$` (or starting with `\^`): the backslash survives into the
//     pattern, so it matches a literal dollar/caret where the end/start
//     anchor was almost certainly intended. Interpreted strings are
//     spared: in valid Go they cannot carry an escaped dollar, matching
//     upstream's spare of non-raw RegExp sources.
//  3. Regex anchors in literal string patterns — strings.Replace/
//     ReplaceAll/Split/SplitN and TrimPrefix/TrimSuffix match their
//     pattern argument as literal text, so a leading `^` or trailing `$`
//     can only ever match that literal character. Replace/ReplaceAll/
//     Split/SplitN check both anchors; TrimPrefix the leading `^` (its
//     match position is the string start) and TrimSuffix the trailing
//     `$` — a prefix cannot misplace an end anchor, nor a suffix a start
//     anchor. Non-literal arguments are skipped; Go has no adjacent-string
//     form, and concatenated expressions skip like upstream's non-literals.
//
// Upstream default severity is warning (findings do not fail the run);
// with no gate framework this port maps the upstream `severity` config
// key to the `--severity` flag: `warning` (default) prints findings and
// exits 0, `error` exits 2. Gate findings are ignorable via the shared
// ignore-marker system (kebab-case id `string-hazards`).

// regexpCompileFuncs are the stdlib regexp constructors taking a pattern
// source as their first argument (upstream: RegExp factory and const/new
// creations; Go has no constructor form taking a source).
var regexpCompileFuncs = map[string]bool{
	"Compile":          true,
	"MustCompile":      true,
	"CompilePOSIX":     true,
	"MustCompilePOSIX": true,
}

// literalPatternFuncs maps the strings-package functions whose pattern
// argument matches literally to the anchors that can never do anchor duty
// there: index 0 is the leading `^`, index 1 the trailing `$`.
var literalPatternFuncs = map[string][2]bool{
	"Replace":    {true, true},
	"ReplaceAll": {true, true},
	"Split":      {true, true},
	"SplitN":     {true, true},
	"TrimPrefix": {true, false},
	"TrimSuffix": {false, true},
}

// StringHazardsViolation is one string-literal hazard.
type StringHazardsViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v StringHazardsViolation) ignorePath() string { return v.Path }

func (v StringHazardsViolation) ignoreLine() int { return v.Line }

// stringHazardsOptions holds the parsed string-hazards CLI configuration.
type stringHazardsOptions struct {
	paths []string
	fail  bool
}

// RunStringHazardsCommand implements `crap4go string-hazards
// [--severity warning|error] [paths...]`: it prints one line per string
// hazard plus a summary, exits 0 on findings at the default warning
// severity, and 2 with `--severity error` (ported from crap4dart
// 62fec6833).
func RunStringHazardsCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseStringHazardsFlags(args, stderr)
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
	violations, checked := CheckStringHazards(files, root)
	violations = filterIgnored("string-hazards", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d string hazard(s) in %d files\n", len(violations), checked)
		if opts.fail {
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "%d files free of string hazards\n", checked)
	return 0
}

// parseStringHazardsFlags parses the --severity flag plus positional
// paths. ok=false means a usage error (already reported to stderr); the
// code is the exit code to return.
func parseStringHazardsFlags(args []string, stderr io.Writer) (stringHazardsOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go string-hazards", flag.ContinueOnError)
	fs.SetOutput(stderr)
	severity := fs.String("severity", "warning", "finding severity: warning (default, exit 0) or error (exit 2 on findings)")
	if err := fs.Parse(args); err != nil {
		return stringHazardsOptions{}, 1, false
	}
	if *severity != "warning" && *severity != "error" {
		fmt.Fprintf(stderr, "invalid --severity %q: want warning or error\n", *severity)
		return stringHazardsOptions{}, 1, false
	}
	return stringHazardsOptions{paths: fs.Args(), fail: *severity == "error"}, 0, true
}

// CheckStringHazards scans every parseable file for string-literal
// hazards and returns the violations plus the number of files checked.
func CheckStringHazards(files []string, root string) ([]StringHazardsViolation, int) {
	var violations []StringHazardsViolation
	checked := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		checked++
		violations = append(violations, stringHazards(file, fset, relPath(root, f))...)
	}
	return violations, checked
}

// stringHazards walks one file's AST and returns its hazards.
func stringHazards(file *ast.File, fset *token.FileSet, path string) []StringHazardsViolation {
	var violations []StringHazardsViolation
	add := func(n ast.Node, message string) {
		violations = append(violations, StringHazardsViolation{
			Path:    path,
			Line:    fset.Position(n.Pos()).Line,
			Message: message,
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			checkStringHazardsCall(call, add)
		}
		return true
	})
	return violations
}

// checkStringHazardsCall dispatches one call to the shape checks: raw
// regexp sources to the anchor check, strings-package pattern arguments
// to the literal-anchor check. Everything else is quiet.
func checkStringHazardsCall(call *ast.CallExpr, add func(ast.Node, string)) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	if pkg.Name == "regexp" && regexpCompileFuncs[sel.Sel.Name] {
		checkRawRegexpAnchor(litArg(call.Args), add)
		return
	}
	checkStringsPatternCall(pkg.Name, sel.Sel.Name, call.Args, add)
}

// checkStringsPatternCall applies the literal-anchor check to a
// strings-package call with a pattern argument; everything else is quiet.
func checkStringsPatternCall(pkg, fn string, args []ast.Expr, add func(ast.Node, string)) {
	if pkg != "strings" || len(args) < 2 {
		return
	}
	if anchors, ok := literalPatternFuncs[fn]; ok {
		checkLiteralPatternAnchors(litArg(args[1:2]), anchors, add)
	}
}

// litArg returns args[0] when it is a string literal, nil otherwise
// (non-literal sources and patterns are never flagged).
func litArg(args []ast.Expr) *ast.BasicLit {
	if len(args) == 0 {
		return nil
	}
	lit, ok := args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}
	return lit
}

// checkRawRegexpAnchor flags a raw string regexp source ending with `\$`
// or starting with `\^`; interpreted strings are spared (in valid Go they
// cannot carry an escaped dollar).
func checkRawRegexpAnchor(lit *ast.BasicLit, add func(ast.Node, string)) {
	if lit == nil || lit.Value[0] != '`' {
		return
	}
	value := lit.Value[1 : len(lit.Value)-1]
	switch {
	case strings.HasSuffix(value, `\$`):
		add(lit, `raw regexp pattern ends with '\$' — in a raw string that is a literal '$' character, not the end anchor; did you mean '$'?`)
	case strings.HasPrefix(value, `\^`):
		add(lit, `raw regexp pattern starts with '\^' — in a raw string that is a literal '^' character, not the start anchor; did you mean '^'?`)
	}
}

// checkLiteralPatternAnchors flags a literal pattern argument carrying
// anchors that can only match literal characters in the strings-package
// function's literal matching.
func checkLiteralPatternAnchors(lit *ast.BasicLit, anchors [2]bool, add func(ast.Node, string)) {
	if lit == nil {
		return
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return // invalid escape spellings parse but never run — not a hazard
	}
	if anchors[0] && strings.HasPrefix(value, "^") {
		add(lit, `literal pattern starts with '^' — string patterns match literally, so this can only match a literal '^' character; a regexp was probably intended`)
	}
	if anchors[1] && strings.HasSuffix(value, "$") {
		add(lit, `literal pattern ends with '$' — string patterns match literally, so this can only match a literal '$' character; a regexp was probably intended`)
	}
}
