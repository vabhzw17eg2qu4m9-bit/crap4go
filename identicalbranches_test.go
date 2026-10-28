package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Fixtures are full Go files; `package p` is line 1. Line numbers in the
// assertions pin the violation sites — every flagged fixture carries a
// real copy-paste-edit shape, every clean fixture really enters a
// construct scan (branchStrings fails on zero constructs).

// dupConditionChain carries a copy-pasted condition: both else-if links
// test x == 0, so the second is unreachable — flagged on line 8.
const dupConditionChain = `package p

func classify(x int) string {
	if x < 0 {
		return "neg"
	} else if x == 0 {
		return "zero"
	} else if x == 0 {
		return "zero again"
	}
	return "other"
}
`

// dupConditionTwice repeats one condition across three links — two
// violations, each pointing at the later occurrence (lines 6 and 8).
const dupConditionTwice = `package p

func classify(x int) string {
	if x == 0 {
		return "a"
	} else if x == 0 {
		return "b"
	} else if x == 0 {
		return "c"
	}
	return "d"
}
`

// dupConditionCanonical matches across operand reorder and parentheses:
// `x && a == b` vs `a == b && (x)` — the canonical form sorts
// commutative operands and unwraps parens — flagged on line 6.
const dupConditionCanonical = `package p

func check(a, b int, x bool) bool {
	if x && a == b {
		return true
	} else if a == b && (x) {
		return false
	}
	return true
}
`

// dupConditionWhitespace: `x==0` vs `x  ==  0` — token-canonical
// comparison ignores formatting — flagged on line 6.
const dupConditionWhitespace = `package p

func classify(x int) string {
	if x==0 {
		return "a"
	} else if x  ==  0 {
		return "b"
	}
	return "c"
}
`

// nonCommutativeConditions: `a < b` vs `b < a` and `a-b == 1` vs
// `b-a == 1` are semantically different — never reordered, never
// flagged, while the chain still scans.
const nonCommutativeConditions = `package p

func classify(a, b int) string {
	if a < b {
		return "lt"
	} else if b < a {
		return "gt"
	} else if a-b == 1 {
		return "next"
	} else if b-a == 1 {
		return "prev"
	}
	return "eq"
}
`

// dupBodyChain: both then-bodies return "x" — one arm was supposed to
// be edited; flagged on line 7 (same as line 5).
const dupBodyChain = `package p

func f(a, b bool) string {
	if a {
		return "x"
	} else if b {
		return "x"
	}
	return "y"
}
`

// dupBodyElseEqualsThen: the final else arm duplicates the then arm —
// flagged on line 7.
const dupBodyElseEqualsThen = `package p

func f(a bool) string {
	if a {
		return "x"
	} else {
		return "x"
	}
	return "y"
}
`

// dupBodyBlockEqualsNaked: `{ return "x" }` equals the naked
// `return "x"` — single-statement blocks unwrap, so the brace style
// cannot hide the repeat — flagged on line 6.
const dupBodyBlockEqualsNaked = `package p

func f(a, b bool) string {
	if a {
		return "x"
	} else if b { return "x" }
	return "y"
}
`

// separateIfBodies: identical bodies in two separate if statements are
// legal — only pieces of one construct compare.
const separateIfBodies = `package p

func f(a, b bool) string {
	if a {
		return "x"
	}
	if b {
		return "x"
	}
	return "y"
}
`

// dupOperandOr: `a || a || b` — the chain collapses to `a || b`.
// Reported once: only the outermost link of a same-operator chain is
// checked (line 4).
const dupOperandOr = `package p

func f(a, b bool) bool {
	return a || a || b
}
`

// dupOperandAnd: `a && a`.
const dupOperandAnd = `package p

func f(a bool) bool {
	return a && a
}
`

// dupOperandCall: `ready() || ready()` — an accidental double call,
// caught by the canonical form of the call expressions.
const dupOperandCall = `package p

func ready() bool { return true }

func f() bool {
	return ready() || ready()
}
`

// distinctOperands pass: `a || b || c` — nothing collapses.
const distinctOperands = `package p

func f(a, b, c bool) bool {
	return a || b || c
}
`

// absorbedOperand: `a && (b || a)` — a is implied by `b || a`
// (absorption law), flagged at the absorbed conjunct's line 4.
const absorbedOperand = `package p

func f(a, b bool) bool {
	return a && (b || a)
}
`

// absorbedOperandOr: the `||` direction — `a || b && a` collapses to
// `a`; flagged at the conjunct's line 4.
const absorbedOperandOr = `package p

func f(a, b bool) bool {
	return a || b && a
}
`

// nestedDistinctChain: `a && (b || c)` — a nested different-operator
// chain without overlap is clean.
const nestedDistinctChain = `package p

func f(a, b, c bool) bool {
	return a && (b || c)
}
`

// reorderedCommutativeOperands collapse: `(a && b) || (b && a)` — both
// operands canonicalize to `&&(a,b)` — flagged on line 4.
const reorderedCommutativeOperands = `package p

func f(a, b bool) bool {
	return (a && b) || (b && a)
}
`

// dupArgCall: measure(width, height, width) — the wrong-variable bug;
// flagged on line 7 (same as argument 1).
const dupArgCall = `package p

func measure(w, h, d int) int { return w + h + d }

func f(width, height int) int {
	return measure(width, height, width)
}
`

// allLiteralArgs: rect(8, 8, 8, 4) — a uniform-value API legitimately
// repeats literals; all-literal lists are exempt.
const allLiteralArgs = `package p

func rect(a, b, c, d int) []int { return []int{a, b, c, d} }

func f() []int {
	return rect(8, 8, 8, 4)
}
`

// literalNeverRepeat: repeated literals next to a non-literal stay
// quiet — only identifier/expression repeats are suspect.
const literalNeverRepeat = `package p

func box(a, b, c int) int { return a + b + c }

func f(x int) int {
	return box(x, 8, 8)
}
`

// predeclaredLiterals: nil/true/false count as literals — repeats of
// them in one call never flag.
const predeclaredLiterals = `package p

func connect(p *int, opts ...bool) int { return len(opts) + *p }

func f(p *int) int {
	return connect(p, nil, nil, true, false)
}
`

// distinctArgs pass: measure(a, b).
const distinctArgs = `package p

func measure(w, h int) int { return w + h }

func f(a, b int) int {
	return measure(a, b)
}
`

// separateIfConditions: the same condition in two separate if
// statements is legal — the state may change between them.
const separateIfConditions = `package p

func classify(x int) string {
	label := "other"
	if x == 0 {
		label = "zero"
	}
	if x == 0 {
		label = "zero"
	}
	return label
}
`

// nestedIfsSeparateChains: an if nested inside another's body starts
// its own chain, not a link of the outer one — all conditions and
// bodies here are distinct within their chains.
const nestedIfsSeparateChains = `package p

func classify(x, y int) string {
	if x == 0 {
		if y == 0 {
			return "origin"
		}
		return "x-zero"
	} else if y == 0 {
		return "y-zero"
	}
	return "other"
}
`

// switchExcluded: identical case bodies across one switch never
// compare — switch is out of the gate's scope (the compiler already
// rejects duplicate constant cases). A clean if chain in the same file
// keeps the construct count honest.
const switchExcluded = `package p

func classify(x int) string {
	switch x {
	case 0:
		return "zero"
	case 1:
		return "zero"
	}
	return "other"
}

func other(x int) string {
	if x == 0 {
		return "zero"
	} else if x < 10 {
		return "small"
	}
	return "other"
}
`

// elseBlockIfIsOwnChain: an if inside an else BLOCK starts its own
// chain — and that chain's duplicate condition is flagged on line 9.
const elseBlockIfIsOwnChain = `package p

func classify(x int) string {
	if x > 0 {
		return "pos"
	} else {
		if x == 0 {
			return "zero"
		} else if x == 0 {
			return "zero!"
		}
	}
	return "neg"
}
`

// calleeNameShapes: messages name the callee across call shapes —
// plain function (dupArgCall), method selector, generic instantiation
// with one and two type arguments, parenthesized function expression,
// and a computed function (snippet fallback).
const calleeNameShapes = `package p

func pair[T any](a, b, c T) T { return a }

func mapping[K comparable, V any](a, b K) V { var zero V; return zero }

type sink struct{}

func (sink) put(a, b, c int) int { return a }

func factory() func(int, int, int) int { return pair[int] }

func f(x int) int {
	return pair[int](x, x, 0)
}

func g(x int) int {
	return (pair[int])(x, x, 0)
}

func h(k1 string) int {
	return mapping[string, int](k1, k1)
}

func m(x int) int {
	return sink{}.put(x, x, 0)
}

func p2(x int) int {
	return factory()(x, x, 0)
}
`

// branchStrings runs the identical-branches gate over one fixture and
// returns its violations as `line:message` strings in file order. A
// fixture that triggers no construct scan fails the test — detection
// must never pass vacuously.
func branchStrings(t *testing.T, src string) []string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root+"/branches.go", src)
	violations, constructs := CheckIdenticalBranches([]string{root + "/branches.go"}, root)
	if constructs == 0 {
		t.Fatalf("constructs = 0, fixture entered no construct scan:\n%s", src)
	}
	var got []string
	for _, v := range violations {
		if v.Path != "branches.go" {
			t.Errorf("path = %q, want branches.go", v.Path)
		}
		got = append(got, fmt.Sprintf("%d:%s", v.Line, v.Message))
	}
	return got
}

func TestIdenticalBranchesConditions(t *testing.T) {
	matchViolations(t, branchStrings(t, dupConditionChain), []string{
		`8:duplicate condition in if/else-if chain: 'x == 0' (same as line 6)`,
	})
	matchViolations(t, branchStrings(t, dupConditionTwice), []string{
		`6:duplicate condition in if/else-if chain`,
		`8:duplicate condition in if/else-if chain`,
	})
	// Canonical form: operand reorder and parentheses cannot hide the
	// repeat (mutation-killer for the sort/unwrap steps).
	if got := branchStrings(t, dupConditionCanonical); len(got) != 1 || !strings.Contains(got[0], "6:") {
		t.Errorf("violations = %v, want one at line 6", got)
	}
	if got := branchStrings(t, dupConditionWhitespace); len(got) != 1 {
		t.Errorf("violations = %v, want one — formatting must not hide a duplicate", got)
	}
	// Non-commutative operators keep their operand order.
	if got := branchStrings(t, nonCommutativeConditions); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

func TestIdenticalBranchesBodies(t *testing.T) {
	matchViolations(t, branchStrings(t, dupBodyChain), []string{
		`7:duplicate branch body in if/else-if chain: 'return "x"' (same as line 5)`,
	})
	matchViolations(t, branchStrings(t, dupBodyElseEqualsThen), []string{
		`7:duplicate branch body in if/else-if chain`,
	})
	// Single-statement block equals the naked statement.
	if got := branchStrings(t, dupBodyBlockEqualsNaked); len(got) != 1 {
		t.Errorf("violations = %v, want one — braces must not hide a duplicate body", got)
	}
	if got := branchStrings(t, separateIfBodies); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

func TestIdenticalBranchesOperands(t *testing.T) {
	matchViolations(t, branchStrings(t, dupOperandOr), []string{
		`4:duplicate operand in '||' chain: 'a' repeats — the chain collapses to the single operand`,
	})
	if got := branchStrings(t, dupOperandAnd); len(got) != 1 {
		t.Errorf("violations = %v, want one", got)
	}
	if got := branchStrings(t, dupOperandCall); len(got) != 1 {
		t.Errorf("violations = %v, want one — a double invocation is a duplicate operand", got)
	}
	if got := branchStrings(t, distinctOperands); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
	// Only the outermost link reports: a three-link chain with one
	// repeat yields exactly one finding.
	if got := branchStrings(t, dupOperandOr); len(got) != 1 {
		t.Errorf("violations = %v, want exactly one — inner links of a same-operator chain must not re-report", got)
	}
	matchViolations(t, branchStrings(t, absorbedOperand), []string{
		`4:operand of '&&' is implied by another operand (absorption): 'a' also appears standalone — the extra terms are dead`,
	})
	matchViolations(t, branchStrings(t, absorbedOperandOr), []string{
		`4:operand of '||' is implied by another operand (absorption)`,
	})
	if got := branchStrings(t, nestedDistinctChain); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
	// Canonical sort: (a && b) and (b && a) are the same operand.
	if got := branchStrings(t, reorderedCommutativeOperands); len(got) != 1 {
		t.Errorf("violations = %v, want one", got)
	}
}

func TestIdenticalBranchesCalls(t *testing.T) {
	matchViolations(t, branchStrings(t, dupArgCall), []string{
		`6:duplicate argument in call to 'measure': 'width' (same as argument 1)`,
	})
	if got := branchStrings(t, allLiteralArgs); len(got) != 0 {
		t.Errorf("violations = %v, want none — uniform-value APIs repeat literals", got)
	}
	if got := branchStrings(t, literalNeverRepeat); len(got) != 0 {
		t.Errorf("violations = %v, want none — literals are never the reported repeat", got)
	}
	if got := branchStrings(t, predeclaredLiterals); len(got) != 0 {
		t.Errorf("violations = %v, want none — nil/true/false are literals", got)
	}
	if got := branchStrings(t, distinctArgs); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

func TestIdenticalBranchesCalleeNames(t *testing.T) {
	matchViolations(t, branchStrings(t, calleeNameShapes), []string{
		`14:duplicate argument in call to 'pair': 'x' (same as argument 1)`,
		`18:duplicate argument in call to 'pair': 'x' (same as argument 1)`,
		`22:duplicate argument in call to 'mapping': 'k1' (same as argument 1)`,
		`26:duplicate argument in call to 'put': 'x' (same as argument 1)`,
		`30:duplicate argument in call to 'factory()': 'x' (same as argument 1)`,
	})
}

func TestIdenticalBranchesScope(t *testing.T) {
	if got := branchStrings(t, separateIfConditions); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
	if got := branchStrings(t, nestedIfsSeparateChains); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
	if got := branchStrings(t, switchExcluded); len(got) != 0 {
		t.Errorf("violations = %v, want none — switch is excluded", got)
	}
	matchViolations(t, branchStrings(t, elseBlockIfIsOwnChain), []string{
		`9:duplicate condition in if/else-if chain: 'x == 0' (same as line 7)`,
	})
}

func TestRun_IdenticalBranchesWarningDefaultExitsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/branches.go", dupConditionChain)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"identical-branches"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 at the default warning severity (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"branches.go:8: duplicate condition in if/else-if chain: 'x == 0' (same as line 6)",
		"1 identical-branch findings (",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_IdenticalBranchesCleanSummary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/clean.go", separateIfConditions)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"identical-branches"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "constructs checked, no identical-branch findings"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_IdenticalBranchesSeverityErrorExitsTwo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/branches.go", dupOperandAnd)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"identical-branches", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --severity error", code)
	}
}

func TestRun_IdenticalBranchesSeverityUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"identical-branches", "--severity", "fatal"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for an unknown severity (stderr=%s)", code, errOut.String())
	}
}

func TestRun_IdenticalBranchesIgnorable(t *testing.T) {
	root := t.TempDir()
	single := strings.Replace(dupConditionChain,
		"\t} else if x == 0 {\n\t\treturn \"zero again\"",
		"\t// ignore:identical-branches\n\t} else if x == 0 {\n\t\treturn \"zero again\"", 1)
	writeFile(t, root+"/branches.go", single)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"identical-branches", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the granular ignore marker (stderr=%s)", code, errOut.String())
	}
	writeFile(t, root+"/branches.go", strings.Replace(dupConditionChain,
		"\t} else if x == 0 {\n\t\treturn \"zero again\"",
		"\t// crap:ignore\n\t} else if x == 0 {\n\t\treturn \"zero again\"", 1))
	out.Reset()
	if code := runWithRoot([]string{"identical-branches", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the blanket ignore marker", code)
	}
}
