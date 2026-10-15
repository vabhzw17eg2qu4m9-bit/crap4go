package main

import (
	"bytes"
	"strings"
	"testing"
)

// chainClassify is a 4-branch else-if chain (upstream
// complexity_metric_gate_test's fixture shape): cyclomatic 5, cognitive 4.
const chainClassify = `package p

func Classify(x int) string {
	if x < 0 {
		return "neg"
	} else if x == 0 {
		return "zero"
	} else if x < 10 {
		return "small"
	} else if x < 100 {
		return "medium"
	}
	return "large"
}
`

// lambdaFixture carries an if inside a function literal: cyclomatic 2,
// cognitive 2 with lambdas counted, 1/1 without.
const lambdaFixture = `package p

func F(a bool) {
	g := func(x bool) {
		if x {
			println("y")
		}
	}
	g(a)
}
`

func TestRun_ComplexityCognitiveMetric(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/chain.go", chainClassify)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"complexity", "--metric", "cognitive", "--max", "2"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"Classify cognitive=4 > max 2", "1/1 functions over their cognitive limit 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_ComplexityCyclomaticDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/f.go", "package p\n\nfunc F(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
	var out, errOut bytes.Buffer
	// No --metric: cyclomatic is the default and reports as CC.
	code := runWithRoot([]string{"complexity", "--max", "1"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"F CC=2 > max 1", "1/1 functions over their cyclomatic limit 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_ComplexityCleanPass(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/f.go", "package p\n\nfunc F(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"complexity"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 functions within their cyclomatic limit 10"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_ComplexityInvalidMetric(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"complexity", "--metric", "sonar"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if want := `invalid --metric "sonar": expected "cyclomatic" or "cognitive"`; !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr missing %q:\n%s", want, errOut.String())
	}
}

func TestRun_ComplexityCountLambdasFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/f.go", lambdaFixture)
	var out, errOut bytes.Buffer
	// Default (count): the if inside the literal counts, CC=2 over max 1.
	code := runWithRoot([]string{"complexity", "--max", "1"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("default exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	// Excluded: the literal's branches are skipped, CC=1 passes.
	out.Reset()
	code = runWithRoot([]string{"complexity", "--max", "1", "--count-lambdas=false"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("no-lambdas exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
}

func TestRun_ComplexityNoFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"complexity"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}
