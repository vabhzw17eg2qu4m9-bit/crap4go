package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Fixtures are full Go files; `package p` is line 1. Line numbers in the
// assertions pin the violation sites — every flagged fixture carries a
// real collapsible-flow shape, every clean fixture really enters an if
// scan (collapsibleStrings fails on zero constructs).

// mergeIf carries a collapsible if: the then-block's only statement is
// an else-less inner if — merges into `if a && b` — flagged on line 4.
const mergeIf = `package p

func f(a, b bool) {
	if a {
		if b {
			println("x")
		}
	}
}
`

// innerElseBlocksMerge: the inner if has an else — merging would drop it.
const innerElseBlocksMerge = `package p

func f(a, b bool) {
	if a {
		if b {
			println("x")
		} else {
			println("y")
		}
	}
}
`

// outerElseBlocksMerge: the outer if has an else — the inner if is not
// the branch's only shape.
const outerElseBlocksMerge = `package p

func f(a, b bool) {
	if a {
		if b {
			println("x")
		}
	} else {
		println("z")
	}
}
`

// extraStatementBeforeBlocksMerge: a statement before the inner if makes
// the then-branch something a condition merge cannot express.
const extraStatementBeforeBlocksMerge = `package p

func f(a, b bool) {
	if a {
		println("before")
		if b {
			println("x")
		}
	}
}
`

// extraStatementAfterBlocksMerge: the same with a trailing statement.
const extraStatementAfterBlocksMerge = `package p

func f(a, b bool) {
	if a {
		if b {
			println("x")
		}
		println("after")
	}
}
`

// innerInitBlocksMerge: the inner if carries its own init — the init
// cannot move into a `&&` operand (`if a && v, ok := m[k]; ok` is
// invalid Go), so this never merges.
const innerInitBlocksMerge = `package p

func f(a bool, m map[string]int, k string) {
	if a {
		if v, ok := m[k]; ok {
			println(v)
		}
	}
}
`

// outerInitMerges: the outer if's init survives the merge (`if v, ok :=
// m[k]; ok && v > 1` is valid Go) — flagged on line 4.
const outerInitMerges = `package p

func f(m map[string]int, k string) {
	if v, ok := m[k]; ok {
		if v > 1 {
			println(v)
		}
	}
}
`

// fallthroughPlainThen: the then-branch is a plain statement — nothing
// exits, the else stays.
const fallthroughPlainThen = `package p

func f(c bool) string {
	if c {
		println("work")
	} else {
		return "b"
	}
	return "c"
}
`

// fallthroughBareInnerIf — the source_instrumenter trap: the
// then-branch ends with an if WITHOUT an else, which falls through when
// its condition is false — the else must stay.
const fallthroughBareInnerIf = `package p

func f(node any) string {
	if node != nil {
		s := node
		if s == "" {
			return ""
		}
	} else {
		return "other"
	}
	return "x"
}
`

// halfExitingPairBlocks: the then-branch ends in an if/else pair whose
// else does NOT exit — the pair falls through, so the OUTER else stays
// (kills an else-exists-only terminal check). The middle if's own else
// is genuinely redundant (its then returns) — flagged on line 5 only.
const halfExitingPairBlocks = `package p

func f(c bool) string {
	if c {
		if c {
			return "a"
		} else {
			println("b")
		}
	} else {
		return "other"
	}
	return "z"
}
`

// redundantElseReturn: the then-branch returns — the else is redundant,
// dedent its body — flagged on line 4.
const redundantElseReturn = `package p

func f(c bool) string {
	if c {
		return "a"
	} else {
		return "b"
	}
}
`

// switchNotAnalyzed: the then-branch ends in a switch — every case
// returns, but switch bodies are not analyzed (case fall-through), so
// the else is never called redundant.
const switchNotAnalyzed = `package p

func f(c bool, x int) string {
	if c {
		switch x {
		case 1:
			return "one"
		default:
			return "other"
		}
	} else {
		return "b"
	}
	return "z"
}
`

// selectNotAnalyzed: the then-branch ends in a select — same
// conservative rule.
const selectNotAnalyzed = `package p

func f(c bool, ch chan int) string {
	if c {
		select {
		case v := <-ch:
			return "v"
		}
	} else {
		return "b"
	}
	return "z"
}
`

// typeSwitchNotAnalyzed: the then-branch ends in a type switch.
const typeSwitchNotAnalyzed = `package p

func f(c bool, v any) string {
	if c {
		switch x := v.(type) {
		case int:
			return "i"
		default:
			return "d"
		}
	} else {
		return "b"
	}
	return "z"
}
`

// bothBranchesExitChain: the inner if/else pair exits, so the block
// ending in it exits too — both the inner if-chain (line 5) and the
// outer if whose then-block ends with it (line 4) are flagged.
const bothBranchesExitChain = `package p

func f(c bool, x int) string {
	if x > 0 {
		if c {
			return "a"
		} else {
			return "b"
		}
	} else {
		return "c"
	}
}
`

// chainHint: the else-if follows an always-exiting then — a flattening
// hint at the else-if's line, not a fix-shaped message.
const chainHint = `package p

func f(x int) string {
	if x < 0 {
		return "neg"
	} else if x == 0 {
		return "zero"
	}
	return "pos"
}
`

// plainElseNotChain: a plain else after a terminal then is flagged as
// redundant (dedent), not as a chain.
const plainElseNotChain = `package p

func f(c bool) string {
	if c {
		return "a"
	} else {
		if c {
			return "x"
		}
		return "y"
	}
}
`

// canonicalClean produces no findings: separate guard ifs, a then-branch
// with extra statements around its inner if.
const canonicalClean = `package p

func f(x int, c bool) string {
	if x < 0 {
		return "neg"
	}
	if c {
		println("a")
		if x == 0 {
			println("b")
		}
	}
	return "done"
}
`

// localPanicShadows: a file-local panic that returns normally is never
// counted as exiting — kills a name-only panic check.
const localPanicShadows = `package p

func panic(msg string) {}

func f(c bool) string {
	if c {
		panic("mine")
	} else {
		return "b"
	}
	return "z"
}
`

// jumpFixture wraps one body statement in a loop so break and continue
// have a construct to leave; the violation sits on the if line (5).
func jumpFixture(body string) string {
	return "package p\n\nfunc f(c bool) {\n\tfor {\n\t\tif c {\n\t\t\t" + body + "\n\t\t} else {\n\t\t\tprintln(\"x\")\n\t\t}\n\t}\n}\n"
}

// collapsibleStrings runs CheckCollapsibleFlow over one fixture file and
// returns "line:message" strings. It fails on zero constructs — a clean
// fixture must never pass vacuously.
func collapsibleStrings(t *testing.T, src string) []string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root+"/flow.go", src)
	violations, constructs := CheckCollapsibleFlow([]string{root + "/flow.go"}, root)
	if constructs == 0 {
		t.Fatalf("constructs = 0, fixture entered no if scan:\n%s", src)
	}
	var got []string
	for _, v := range violations {
		if v.Path != "flow.go" {
			t.Errorf("path = %q, want flow.go", v.Path)
		}
		got = append(got, fmt.Sprintf("%d:%s", v.Line, v.Message))
	}
	return got
}

func TestCollapsibleFlowMerge(t *testing.T) {
	matchViolations(t, collapsibleStrings(t, mergeIf), []string{
		"4:nested if can be merged into a single condition with `&&`",
	})
	if got := collapsibleStrings(t, outerInitMerges); len(got) != 1 || !strings.HasPrefix(got[0], "4:") {
		t.Errorf("violations = %v, want one at line 4 — the outer if's init survives the merge", got)
	}
	for _, blocked := range []string{
		innerElseBlocksMerge,
		outerElseBlocksMerge,
		extraStatementBeforeBlocksMerge,
		extraStatementAfterBlocksMerge,
		innerInitBlocksMerge,
	} {
		if got := collapsibleStrings(t, blocked); len(got) != 0 {
			t.Errorf("violations = %v, want none for:\n%s", got, blocked)
		}
	}
}

func TestCollapsibleFlowRedundantElse(t *testing.T) {
	matchViolations(t, collapsibleStrings(t, redundantElseReturn), []string{
		"4:redundant else after an always-exiting then branch — dedent the else body",
	})
	// break, continue and panic count as exiting.
	for _, body := range []string{"break", "continue", `panic("boom")`} {
		matchViolations(t, collapsibleStrings(t, jumpFixture(body)), []string{
			"5:redundant else after an always-exiting then branch — dedent the else body",
		})
	}
	// A block ending in an exiting if-chain counts as exiting: the
	// inner if-chain and the outer if whose then-block ends with it
	// are both flagged, outer first.
	matchViolations(t, collapsibleStrings(t, bothBranchesExitChain), []string{
		"4:redundant else after an always-exiting then branch — dedent the else body",
		"5:redundant else after an always-exiting then branch — dedent the else body",
	})
	// A file-local panic that returns normally is never an exit.
	if got := collapsibleStrings(t, localPanicShadows); len(got) != 0 {
		t.Errorf("violations = %v, want none — the shadowed panic can return", got)
	}
}

func TestCollapsibleFlowFallthrough(t *testing.T) {
	for _, trap := range []string{fallthroughPlainThen, fallthroughBareInnerIf} {
		if got := collapsibleStrings(t, trap); len(got) != 0 {
			t.Errorf("violations = %v, want none — the then-branch falls through:\n%s", got, trap)
		}
	}
	// An if/else pair whose else does not exit falls through: only the
	// middle if's own redundant else is flagged, never the outer one.
	matchViolations(t, collapsibleStrings(t, halfExitingPairBlocks), []string{
		"5:redundant else after an always-exiting then branch — dedent the else body",
	})
}

func TestCollapsibleFlowSwitchSelectNotAnalyzed(t *testing.T) {
	for _, ending := range []string{switchNotAnalyzed, selectNotAnalyzed, typeSwitchNotAnalyzed} {
		if got := collapsibleStrings(t, ending); len(got) != 0 {
			t.Errorf("violations = %v, want none — switch/select bodies are not analyzed", got)
		}
	}
}

func TestCollapsibleFlowChainHint(t *testing.T) {
	matchViolations(t, collapsibleStrings(t, chainHint), []string{
		"6:else-if follows an always-exiting then branch — flatten the chain into guard clauses or a dispatch table",
	})
	// A plain else after a terminal then is flagged as redundant, not
	// as a chain.
	matchViolations(t, collapsibleStrings(t, plainElseNotChain), []string{
		"4:redundant else after an always-exiting then branch — dedent the else body",
	})
	if got := collapsibleStrings(t, canonicalClean); len(got) != 0 {
		t.Errorf("violations = %v, want none for canonical code", got)
	}
}

func TestRun_CollapsibleFlowWarningDefaultExitsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/flow.go", mergeIf)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"collapsible-flow"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 at the default warning severity (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"flow.go:4: nested if can be merged into a single condition with `&&`",
		"1 collapsible-flow findings (",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_CollapsibleFlowCleanSummary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/clean.go", canonicalClean)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"collapsible-flow"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "if statements checked, no collapsible flow"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_CollapsibleFlowSeverityErrorExitsTwo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/flow.go", redundantElseReturn)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"collapsible-flow", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --severity error", code)
	}
}

func TestRun_CollapsibleFlowSeverityUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"collapsible-flow", "--severity", "fatal"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for an unknown severity (stderr=%s)", code, errOut.String())
	}
}

func TestRun_CollapsibleFlowIgnorable(t *testing.T) {
	root := t.TempDir()
	single := strings.Replace(mergeIf,
		"\tif a {\n\t\tif b {",
		"\t// ignore:collapsible-flow\n\tif a {\n\t\tif b {", 1)
	writeFile(t, root+"/flow.go", single)
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"collapsible-flow", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the granular ignore marker (stderr=%s)", code, errOut.String())
	}
	writeFile(t, root+"/flow.go", strings.Replace(mergeIf,
		"\tif a {\n\t\tif b {",
		"\t// crap:ignore\n\tif a {\n\t\tif b {", 1))
	out.Reset()
	if code := runWithRoot([]string{"collapsible-flow", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the blanket ignore marker", code)
	}
}
