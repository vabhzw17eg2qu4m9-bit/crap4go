package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vacuousTests declares one test with no fail-capable call and one with a
// t.Fatal — only the vacuous one is flagged.
const vacuousTests = `package p

import "testing"

func TestVacuous(t *testing.T) {
	result := Slow(3)
	_ = result
}

func TestAsserting(t *testing.T) {
	if Slow(3) <= 0 {
		t.Fatal("positive sum expected")
	}
}
`

// assertionShapes covers every counted assertion shape: renamed testing.T
// parameter, subtest closures, Errorf, and panic.
const assertionShapes = `package p

import "testing"

func TestRenamedParam(x *testing.T) {
	x.Errorf("boom")
}

func TestSubtest(t *testing.T) {
	t.Run("inner", func(t *testing.T) {
		t.Error("inner boom")
	})
}

func TestPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			panic("expected a panic")
		}
	}()
	Explode()
}
`

// nonTestFuncs shows TestMain, benchmarks and lowercase helpers are not
// counted as tests (TestMain is the harness entry point).
const nonTestFuncs = `package p

import "testing"

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func BenchmarkSlow(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Slow(1)
	}
}

func helperNoT() int { return 1 }
`

func TestCheckTestAssertions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/slow_test.go", vacuousTests)
	violations, checked := CheckTestAssertions([]string{root + "/slow_test.go"}, root)
	if checked != 2 {
		t.Fatalf("checked = %d, want 2", checked)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want 1", violations)
	}
	v := violations[0]
	if v.Path != "slow_test.go" || v.Line != 5 {
		t.Errorf("violation at %s:%d, want slow_test.go:5", v.Path, v.Line)
	}
	want := "TestVacuous has 0 assertion(s) — a test without assertions verifies nothing"
	if v.Message != want {
		t.Errorf("message = %q, want %q", v.Message, want)
	}
}

func TestCheckTestAssertionsShapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/shapes_test.go", assertionShapes)
	violations, checked := CheckTestAssertions([]string{root + "/shapes_test.go"}, root)
	if checked != 3 {
		t.Fatalf("checked = %d, want 3", checked)
	}
	if len(violations) != 0 {
		t.Errorf("asserting tests flagged: %v", violations)
	}
}

func TestCheckTestAssertionsNonTestsSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main_test.go", nonTestFuncs)
	violations, checked := CheckTestAssertions([]string{root + "/main_test.go"}, root)
	if checked != 0 {
		t.Fatalf("checked = %d, want 0 (TestMain/benchmark/helper skipped)", checked)
	}
	if len(violations) != 0 {
		t.Errorf("non-test functions flagged: %v", violations)
	}
}

func TestRun_TestAssertionsViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/slow_test.go", vacuousTests)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"slow_test.go:5: TestVacuous has 0 assertion(s) — a test without assertions verifies nothing",
		"1/2 tests without assertions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_TestAssertionsClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/shapes_test.go", assertionShapes)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "3 tests assert their expectations"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestAssertionsSkipsNonTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/main.go", "package p\n\nfunc Slow(n int) int { return n }\n")
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestAssertionsExplicitPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a_test.go", vacuousTests)
	writeFile(t, filepath.Join(root, "sub", "b_test.go"), assertionShapes)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions", "./sub"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if strings.Contains(out.String(), "TestVacuous") {
		t.Errorf("file outside selection checked:\n%s", out.String())
	}
	if want := "3 tests assert their expectations"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_TestAssertionsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions", "/nonexistent/path"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRun_TestAssertionsSkipsVendor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "vendor", "x", "v_test.go"), vacuousTests)
	if _, err := os.Stat(filepath.Join(root, "vendor", "x", "v_test.go")); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"test-assertions"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if strings.Contains(out.String(), "TestVacuous") {
		t.Errorf("vendor test file checked:\n%s", out.String())
	}
}
