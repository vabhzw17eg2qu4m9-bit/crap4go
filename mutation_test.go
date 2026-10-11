package main

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mutationFixture exercises every operator category plus the expression
// positions that must NOT match (selectors, labels, non-0/1 ints, strings).
const mutationFixture = `package mut

import "fmt"

type T struct{ n int }

func Top(x, y int, p, q, ok bool) int {
	if x > y && p || !ok {
		return 0
	}
	_ = true && false
	_ = q
	return x + y - 1
}

func (t T) Val(ch chan int) int {
	defer func() { _ = recover() }()
	m := map[string]int{"a": 0}
	v := <-ch
	if v == 1 {
		m["a"] = -1
	}
	go func() { _ = v }()
	select {
	case ch <- v:
	default:
	}
	s := []int{0, 1, 2}
	_ = s[0:1]
	fmt.Println(m, t.n)
	return 2
}

func Loop(items []int, tag interface{}) int {
	total := 0
done:
	for {
		break done
	}
	for _, it := range items {
		total += it
	}
	for i := 0; i < 1; i++ {
		switch i {
		case 0:
			total--
		default:
		}
	}
	if n, ok := tag.(int); ok {
		total += n
	}
	switch tag.(type) {
	case string:
		total = -1
	}
	if total != 1 {
		return -2
	}
	return total + 0
}
`

// mutationAllCategories is the all-on detector configuration.
func mutationAllCategories() map[string]bool {
	cats := map[string]bool{}
	for _, c := range mutationOperatorCategories {
		cats[c] = true
	}
	return cats
}

// mutationFixtureSites parses mutationFixture and detects sites.
func mutationFixtureSites(t *testing.T, categories map[string]bool) (string, []MutationSite) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(path, []byte(mutationFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, mutationFixture, 0)
	if err != nil {
		t.Fatal(err)
	}
	methods, err := ExtractMethods(path, []byte(mutationFixture))
	if err != nil {
		t.Fatal(err)
	}
	return path, detectMutationSites(path, fset, f, methods, categories)
}

// lineOf returns the 1-based line holding needle in src.
func lineOf(src, needle string) int {
	return 1 + strings.Count(src[:strings.Index(src, needle)], "\n")
}

func TestMutationSitesDetectAllCategories(t *testing.T) {
	_, sites := mutationFixtureSites(t, mutationAllCategories())
	counts := map[string]int{}
	for _, s := range sites {
		counts[s.Category]++
		if s.Offset < 0 || s.Length <= 0 || s.Line <= 0 {
			t.Fatalf("bad site span: %v", s)
		}
	}
	want := map[string]int{
		"arithmetic": 3, "boundary": 4, "equality": 2, "logical": 3,
		"boolean": 2, "constant": 18, "unary": 1,
	}
	for category, n := range want {
		if counts[category] != n {
			t.Errorf("category %s: got %d sites, want %d (all: %v)", category, counts[category], n, sites)
		}
	}
	if len(sites) != 33 {
		t.Errorf("got %d sites, want 33", len(sites))
	}
}

func TestMutationSiteSpansAndMethods(t *testing.T) {
	_, sites := mutationFixtureSites(t, mutationAllCategories())
	find := func(category, original, mutant string) MutationSite {
		t.Helper()
		for _, s := range sites {
			if s.Category == category && s.Original == original && s.Mutant == mutant {
				return s
			}
		}
		t.Fatalf("no %s site %s -> %s in %v", category, original, mutant, sites)
		return MutationSite{}
	}
	plus := find("arithmetic", "+", "-")
	if want := lineOf(mutationFixture, "return x + y - 1"); plus.Line != want {
		t.Errorf("+ site line %d, want %d", plus.Line, want)
	}
	if plus.Method != "Top" {
		t.Errorf("+ site method %q, want Top", plus.Method)
	}
	if got, want := string(plus.applyTo([]byte(mutationFixture))), "return x - y - 1"; !strings.Contains(got, want) {
		t.Errorf("mutated source missing %q:\n%s", want, got)
	}
	bang := find("unary", "!", "")
	if bang.Method != "Top" || bang.Line != lineOf(mutationFixture, "|| !ok") {
		t.Errorf("! site misplaced: %v", bang)
	}
	if got := string(bang.applyTo([]byte(mutationFixture))); !strings.Contains(got, "p || ok") {
		t.Errorf("! deletion failed:\n%s", got)
	}
	minusOne := find("constant", "-1", "0")
	if minusOne.Method != "(T)Val" || minusOne.Line != lineOf(mutationFixture, `m["a"] = -1`) {
		t.Errorf("-1 site misplaced: %v", minusOne)
	}
	if got := string(minusOne.applyTo([]byte(mutationFixture))); !strings.Contains(got, `m["a"] = 0`) {
		t.Errorf("-1 -> 0 failed:\n%s", got)
	}
	if m := find("boolean", "true", "false"); m.Method != "Top" {
		t.Errorf("true site method %q, want Top", m.Method)
	}
	if m := find("equality", "==", "!="); m.Method != "(T)Val" {
		t.Errorf("== site method %q, want (T)Val", m.Method)
	}
}

func TestMutationSitesSkipNonExpressions(t *testing.T) {
	_, sites := mutationFixtureSites(t, mutationAllCategories())
	for _, s := range sites {
		if s.Original == "2" || s.Original == "%" || s.Original == "<-" {
			t.Errorf("matched non-table token: %v", s)
		}
	}
}

func TestMutationOperatorsFilterByCategory(t *testing.T) {
	_, sites := mutationFixtureSites(t, map[string]bool{"boundary": true})
	if len(sites) != 4 {
		t.Fatalf("got %d sites, want 4 boundary rows: %v", len(sites), sites)
	}
	for _, s := range sites {
		if s.Category != "boundary" {
			t.Errorf("non-boundary site leaked through: %v", s)
		}
	}
}

func TestMutationCoveredSitesFilterByCoverage(t *testing.T) {
	dir := t.TempDir()
	src := "package mut\n\nfunc Hot(x int) int {\n\treturn x + 1\n}\n\nfunc Cold(x int) int {\n\treturn x + 1\n}\n"
	hot := filepath.Join(dir, "hot.go")
	if err := os.WriteFile(hot, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := strings.Join([]string{
		"mode: set",
		// Hot executed, Cold never: both functions live in hot.go.
		fmt.Sprintf("example.com/m/hot.go:3.21,4.14 1 1"),
		fmt.Sprintf("example.com/m/hot.go:6.21,7.14 1 0"),
		"",
	}, "\n")
	cov, err := ParseCoverProfileReader(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := mutationCoveredSites([]string{hot}, dir, cov, mutationAllCategories(), 500, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sites := scan.sites
	if len(sites) != 2 || scan.uncovered != 2 {
		t.Fatalf("got %d covered sites (%v), %d uncovered; want 2 covered, 2 uncovered", len(sites), sites, scan.uncovered)
	}
	for _, s := range sites {
		if s.Method != "Hot" {
			t.Errorf("covered site from %s leaked: %v", s.Method, s)
		}
	}
}

func TestMutationEngineMutatesOneAtATimeAndRestores(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "calc.go")
	src := "package mut\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	methods, _ := ExtractMethods(file, []byte(src))
	sites := detectMutationSites(file, fset, f, methods, mutationAllCategories())
	if len(sites) != 1 {
		t.Fatalf("got %d sites, want 1: %v", len(sites), sites)
	}
	var seen []string
	runner := func(root string, timeout time.Duration, testArgs []string) (mutantOutcome, error) {
		mutated, err := os.ReadFile(file)
		if err != nil {
			t.Error(err)
			return mutantKilled, nil
		}
		seen = append(seen, string(mutated))
		return mutantKilled, nil
	}
	result, err := runMutationEngine(dir, sites, time.Second, defaultMutationTestArgs, nil, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.killedCount() != 1 || result.score() != 1.0 {
		t.Errorf("bad result: %d killed, score %g", result.killedCount(), result.score())
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "a - b") {
		t.Errorf("runner did not observe the mutant: %q", seen)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != src {
		t.Errorf("file not restored:\n%s", after)
	}
}

func TestMutationRunResultScoreAndCategories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(file, []byte("package mut\n\nfunc A(x int) bool {\n\treturn x > 0 && x < 9\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sites := []MutationSite{
		{File: file, Line: 3, Category: "logical", Original: "&&", Mutant: "||"},
		{File: file, Line: 3, Category: "constant", Original: "0", Mutant: "1"},
		{File: file, Line: 3, Category: "boundary", Original: ">", Mutant: ">="},
	}
	sequence := []mutantOutcome{mutantSurvived, mutantTimeout, mutantKilled}
	i := 0
	runner := func(string, time.Duration, []string) (mutantOutcome, error) {
		out := sequence[i]
		i++
		return out, nil
	}
	result, err := runMutationEngine(".", sites, time.Second, defaultMutationTestArgs, nil, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.killedCount() != 1 || result.survivedCount() != 2 {
		t.Errorf("got %d killed / %d survived, want 1 / 2", result.killedCount(), result.survivedCount())
	}
	if want := 1.0 / 3.0; result.score() != want {
		t.Errorf("score %g, want %g", result.score(), want)
	}
	cats := result.survivorsByCategory()
	if len(cats) != 2 || cats["logical"] != 1 || cats["constant"] != 1 {
		t.Errorf("survivors by category %v, want logical=1 constant=1", cats)
	}
	if got, want := mutationCategorySummary(cats), "constant=1, logical=1"; got != want {
		t.Errorf("category summary %q, want %q", got, want)
	}
}

// writeMutationProject writes a runnable module: go.mod plus the given files.
func writeMutationProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/mut\n\ngo 1.21\n"
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// generateMutationCoverage runs the project's tests with a cover profile,
// exactly as the real workflow would.
func generateMutationCoverage(t *testing.T, root string) {
	t.Helper()
	requireGoBin(t)
	cmd := exec.Command("go", "test", "./...", "-coverprofile=coverage.out", "-covermode=atomic")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
}

func requireGoBin(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
}

// mutationGateProject is a module whose Sign mutants are all killed by its
// tests, plus a Dead method no test executes (its sites stay uncovered).
func mutationGateProject(t *testing.T) string {
	t.Helper()
	root := writeMutationProject(t, map[string]string{
		"sign.go": "package mut\n\nfunc Sign(x int) string {\n\tif x < 0 {\n\t\treturn \"neg\"\n\t}\n\treturn \"pos\"\n}\n\nfunc Dead(x int) int {\n\treturn x + 0\n}\n",
		"sign_test.go": `package mut

import "testing"

func TestSign(t *testing.T) {
	if Sign(-1) != "neg" {
		t.Error("Sign(-1) != neg")
	}
	if Sign(1) != "pos" {
		t.Error("Sign(1) != pos")
	}
	if Sign(0) != "pos" {
		t.Error("Sign(0) != pos")
	}
}
`,
	})
	generateMutationCoverage(t, root)
	return root
}

func TestMutationCommandKillsEverything(t *testing.T) {
	root := mutationGateProject(t)
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "mutation score 100.0% (3 killed, 0 survived, 2 uncovered, 0 cached)"; !strings.Contains(out.String(), want) {
		t.Errorf("summary missing %q:\n%s", want, out.String())
	}
}

func TestMutationCommandReportsSurvivors(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"calc.go": "package mut\n\nfunc Add(a, b int) int {\n\tif a > 100 {\n\t\treturn 0\n\t}\n\treturn a + b\n}\n",
		"calc_test.go": `package mut

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Error("Add(1,2) != 3")
	}
}
`,
	})
	generateMutationCoverage(t, root)
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	stdout := out.String()
	if !strings.Contains(stdout, "calc.go:") || !strings.Contains(stdout, "survived boundary mutant `>` -> `>=`") {
		t.Errorf("survivor violation missing from:\n%s", stdout)
	}
	if !strings.Contains(stdout, "mutation score 66.7% (2 killed, 1 survived, 1 uncovered, 0 cached)") {
		t.Errorf("summary missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "survivors by category: boundary=1") {
		t.Errorf("category breakdown missing:\n%s", stdout)
	}
	// The mutant is restored: the source is byte-identical after the run.
	src, err := os.ReadFile(filepath.Join(root, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "a > 100") {
		t.Errorf("source not restored:\n%s", src)
	}
}

func TestMutationCommandMaxMutantsCapsExecution(t *testing.T) {
	root := mutationGateProject(t)
	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--max-mutants", "1", "sign.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "mutation score 100.0% (1 killed, 0 survived, 0 uncovered, 0 cached)"; !strings.Contains(out.String(), want) {
		t.Errorf("summary missing %q:\n%s", want, out.String())
	}
}

func TestMutationCommandExcludeSkipsFiles(t *testing.T) {
	root := mutationGateProject(t)
	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"--exclude", "sign.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "no mutation sites"; !strings.Contains(out.String(), want) {
		t.Errorf("summary missing %q:\n%s", want, out.String())
	}
}

func TestMutationCommandSkipsWithoutCoverage(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"a.go": "package mut\n\nfunc A() int { return 0 }\n",
	})
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s", code, out.String())
	}
	if want := "skip mutation: no coverage data at coverage.out"; !strings.Contains(out.String(), want) {
		t.Errorf("skip message missing:\n%s", out.String())
	}
}

func TestMutationCommandSkipsWithoutTestFiles(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"a.go":         "package mut\n\nfunc A() int { return 0 }\n",
		"coverage.out": "mode: set\n",
	})
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s", code, out.String())
	}
	if want := "skip mutation: no test files"; !strings.Contains(out.String(), want) {
		t.Errorf("skip message missing:\n%s", out.String())
	}
}

func TestMutationCommandSkipsWhenGoUnavailable(t *testing.T) {
	root := mutationGateProject(t)
	t.Setenv("PATH", "")
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "skip mutation: go test unavailable"; !strings.Contains(out.String(), want) {
		t.Errorf("skip message missing:\n%s", out.String())
	}
}

func TestMutationCommandUncoveredOnlyPasses(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"literal.go": "package mut\n\nfunc Two() int { return 2 }\n",
		"literal_test.go": `package mut

import "testing"

func TestTwo(t *testing.T) {
	if Two() != 2 {
		t.Error("Two() != 2")
	}
}
`,
	})
	generateMutationCoverage(t, root)
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if want := "no mutation sites"; !strings.Contains(out.String(), want) {
		t.Errorf("summary missing %q:\n%s", want, out.String())
	}
}

func TestMutationFlagValidation(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--min-score", "1.5"}, "must be valid"},
		{[]string{"--max-mutants", "0"}, "must be valid"},
		{[]string{"--operators", "bogus"}, `unknown operator category "bogus"`},
	}
	for _, tc := range cases {
		var out, errOut strings.Builder
		code := RunMutationCommand(tc.args, t.TempDir(), &out, &errOut)
		if code != 1 {
			t.Errorf("%v: exit %d, want 1", tc.args, code)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: stderr %q, want it to contain %q", tc.args, errOut.String(), tc.want)
		}
	}
}

func TestMutationCommandFailsOnMissingFile(t *testing.T) {
	root := mutationGateProject(t)
	var out, errOut strings.Builder
	code := RunMutationCommand([]string{"missing.go"}, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "missing.go") {
		t.Errorf("stderr should name the unreadable file:\n%s", errOut.String())
	}
}

func TestMutationCommandFailsOnMalformedCoverage(t *testing.T) {
	root := writeMutationProject(t, map[string]string{
		"a.go":         "package mut\n\nfunc A() int { return 0 }\n",
		"coverage.out": "this is not a cover profile\n",
		"a_test.go":    "package mut\n",
	})
	var out, errOut strings.Builder
	code := RunMutationCommand(nil, root, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if errOut.String() == "" {
		t.Error("malformed profile should report an error")
	}
}

func TestMutantRunnerClassifiesRealGoTest(t *testing.T) {
	requireGoBin(t)
	t.Run("survived", func(t *testing.T) {
		root := writeMutationProject(t, map[string]string{
			"a.go": "package mut\n\nfunc A() int { return 1 }\n",
			"a_test.go": `package mut

import "testing"

func TestA(t *testing.T) {
	if A() != 1 {
		t.Error("nope")
	}
}
`,
		})
		outcome, err := mutantTestRunnerOS(root, 30*time.Second, defaultMutationTestArgs)
		if err != nil || outcome != mutantSurvived {
			t.Errorf("got %v, %v; want survived", outcome, err)
		}
	})
	t.Run("killed", func(t *testing.T) {
		root := writeMutationProject(t, map[string]string{
			"a.go": "package mut\n\nfunc A() int { return 1 }\n",
			"a_test.go": `package mut

import "testing"

func TestA(t *testing.T) {
	if A() != 2 {
		t.Error("nope")
	}
}
`,
		})
		outcome, err := mutantTestRunnerOS(root, 30*time.Second, defaultMutationTestArgs)
		if err != nil || outcome != mutantKilled {
			t.Errorf("got %v, %v; want killed", outcome, err)
		}
	})
	t.Run("compile error is killed", func(t *testing.T) {
		root := writeMutationProject(t, map[string]string{
			"a.go":      "package mut\n\nvar Broken int = \"not an int\"\n",
			"a_test.go": "package mut\n",
		})
		outcome, err := mutantTestRunnerOS(root, 30*time.Second, defaultMutationTestArgs)
		if err != nil || outcome != mutantKilled {
			t.Errorf("got %v, %v; want killed", outcome, err)
		}
	})
	t.Run("timeout counts as survived", func(t *testing.T) {
		root := writeMutationProject(t, map[string]string{
			"a.go": "package mut\n\nfunc A() int { return 1 }\n",
			"a_test.go": `package mut

import (
	"testing"
	"time"
)

func TestA(t *testing.T) {
	time.Sleep(10 * time.Second)
}
`,
		})
		outcome, err := mutantTestRunnerOS(root, 2*time.Second, defaultMutationTestArgs)
		if err != nil || outcome != mutantTimeout {
			t.Errorf("got %v, %v; want timeout", outcome, err)
		}
	})
	t.Run("spawn failure is unavailable", func(t *testing.T) {
		t.Setenv("PATH", "")
		_, err := mutantTestRunnerOS(t.TempDir(), time.Second, defaultMutationTestArgs)
		var execErr *exec.Error
		if !errors.As(err, &execErr) {
			t.Errorf("err %v, want *exec.Error", err)
		}
	})
}

func TestMutationGateDisabledByDefault(t *testing.T) {
	root := mutationGateProject(t)
	sign := filepath.Join(root, "sign.go")
	before, err := os.ReadFile(sign)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := runWithRoot([]string{"-coverage", "coverage.out"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("analyze exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "mutation") {
		t.Errorf("analyze output mentions mutation:\n%s", out.String())
	}
	after, err := os.ReadFile(sign)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("analyze flow mutated the source")
	}
}
