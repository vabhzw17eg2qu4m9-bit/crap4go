package main

import "testing"

// cognitiveOf parses src as package main and returns the cognitive
// complexity of the function named name (upstream cognitive_test's
// cognitiveOf helper, in the port's parseBody convention).
func cognitiveOf(t *testing.T, src, name string, countLambdas bool) int {
	t.Helper()
	return CognitiveOf(parseBody(t, src, name), countLambdas)
}

func TestCognitive_LinearCodeScoresZero(t *testing.T) {
	src := `package main
func f() { x := 1; _ = x }`
	if got := cognitiveOf(t, src, "f", true); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestCognitive_FlatIfScoresOne(t *testing.T) {
	src := `package main
func f(x int) string {
	if x > 0 {
		return "pos"
	}
	return "other"
}`
	if got := cognitiveOf(t, src, "f", true); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestCognitive_IfInsideLoopPaysNesting(t *testing.T) {
	// Sonar example: for = +1, if inside = +1 + nesting 1.
	src := `package main
func f(max int) int {
	total := 0
	for i := 1; i <= max; i++ {
		if i%2 == 0 {
			total += i
		}
	}
	return total
}`
	if got := cognitiveOf(t, src, "f", true); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestCognitive_ElseIfChainFlatNestedElsePays(t *testing.T) {
	chain := `package main
func classify(x int) string {
	if x < 0 {
		return "neg"
	} else if x == 0 {
		return "zero"
	} else if x < 10 {
		return "small"
	}
	return "other"
}`
	nested := `package main
func classify(x int) string {
	if x < 0 {
		return "neg"
	} else {
		if x == 0 {
			return "zero"
		} else {
			if x < 10 {
				return "small"
			}
		}
	}
	return "other"
}`
	if got := cognitiveOf(t, chain, "classify", true); got != 3 {
		t.Errorf("flat chain: got %d, want 3 (one increment per if)", got)
	}
	// if +1, else +1, if +2, else +2, if +3.
	if got := cognitiveOf(t, nested, "classify", true); got != 9 {
		t.Errorf("nested else: got %d, want 9", got)
	}
}

func TestCognitive_LogicalSequences(t *testing.T) {
	same := `package main
func f(a, b, c bool) bool { return a && b && c }`
	mixed := `package main
func f(a, b, c bool) bool { return a && b || c }`
	// One identical-operator sequence.
	if got := cognitiveOf(t, same, "f", true); got != 1 {
		t.Errorf("a && b && c: got %d, want 1", got)
	}
	// Two sequences (one &&, one ||).
	if got := cognitiveOf(t, mixed, "f", true); got != 2 {
		t.Errorf("a && b || c: got %d, want 2", got)
	}
}

func TestCognitive_SwitchAndSelectMembers(t *testing.T) {
	sw := `package main
func f(x int) string {
	switch x {
	case 0:
		return "zero"
	default:
		return "other"
	}
}`
	sel := `package main
func f(c chan int) int {
	select {
	case x := <-c:
		return x
	default:
		return 0
	}
}`
	if got := cognitiveOf(t, sw, "f", true); got != 2 {
		t.Errorf("switch case+default: got %d, want 2", got)
	}
	if got := cognitiveOf(t, sel, "f", true); got != 2 {
		t.Errorf("select comm case+default: got %d, want 2", got)
	}
}

func TestCognitive_LogicalOpsInCaseGuard(t *testing.T) {
	// ||/&& patterns count inside case expressions (the Go analog of
	// upstream's ||/&& pattern counting in switch members).
	src := `package main
func f(a, b bool) string {
	switch true {
	case a && b:
		return "ab"
	default:
		return "other"
	}
}`
	if got := cognitiveOf(t, src, "f", true); got != 3 {
		t.Fatalf("got %d, want 3 (case + && sequence + default)", got)
	}
}

func TestCognitive_FuncLitsNestWithoutIncrement(t *testing.T) {
	src := `package main
func f(a, b bool) {
	g := func(x bool) {
		if x {
			println("y")
		}
	}
	g(a && b)
}`
	// The func literal itself does not increment; its if sits at depth 1
	// (+2) and the && sequence adds +1.
	if got := cognitiveOf(t, src, "f", true); got != 3 {
		t.Errorf("count-lambdas: got %d, want 3", got)
	}
	// With lambdas excluded the if inside g does not count at all.
	if got := cognitiveOf(t, src, "f", false); got != 1 {
		t.Errorf("no-lambdas: got %d, want 1", got)
	}
}

func TestCognitive_LoopCondWalkedIfCondNot(t *testing.T) {
	// Mirrors upstream's CognitiveCalculator: loop conditions are
	// traversed, if conditions are not.
	forSrc := `package main
func f(n int, ok bool) {
	for n < 10 && ok {
		n++
	}
}`
	ifSrc := `package main
func f(n int, ok bool) {
	if n > 0 && ok {
		n--
	}
}`
	if got := cognitiveOf(t, forSrc, "f", true); got != 2 {
		t.Errorf("for cond &&: got %d, want 2 (for + && sequence)", got)
	}
	if got := cognitiveOf(t, ifSrc, "f", true); got != 1 {
		t.Errorf("if cond &&: got %d, want 1 (upstream skips if conditions)", got)
	}
}

func TestCognitive_LabelsNotFlowBreaks(t *testing.T) {
	// goto and labels are not flow breaks (upstream lists none); the
	// labeled loop's own structure still counts.
	src := `package main
func f(n int) {
L:
	for n < 10 {
		n++
		if n == 5 {
			break L
		}
	}
}`
	// for +1, if inside +1+1 = 3; the label itself adds nothing.
	if got := cognitiveOf(t, src, "f", true); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestCognitive_NilBody(t *testing.T) {
	if got := CognitiveOf(nil, true); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}
