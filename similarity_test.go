package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shared prose fixtures, ported from crap4dart 2b21966's test fixtures.
// Measured with the ported engine (shingleSize 8): proseA yields 26
// shingles, proseB 27, proseC 29 — every fixture here clears the tests'
// min-shingles 10 floor, so every fixture genuinely enters comparison.
const (
	// simProseA is baseline prose.
	simProseA = "alpha bravo charlie delta echo foxtrot golf hotel india juliet " +
		"kilo lima mike november oscar papa quebec romeo sierra tango uniform " +
		"victor whiskey xray yankee zulu alpha bravo charlie delta echo " +
		"foxtrot golf hotel india juliet kilo lima mike november oscar papa"

	// simProseB is a near-duplicate of simProseA: two scattered words
	// differ (jaccard measured at 0.51).
	simProseB = "alpha bravo charlie delta echo foxtrot golf hotel india juliet " +
		"kilo lima mike november oscar papa quebec SIERRA tango uniform " +
		"victor whiskey xray yankee zulu alpha bravo charlie delta echo " +
		"foxtrot golf hotel india juliet kilo lima mike november delta papa"

	// simProseC is unrelated prose sharing almost nothing with simProseA
	// (jaccard measured at 0.00).
	simProseC = "the quick brown fox jumps over the lazy dog while the sleepy cat " +
		"naps under the warm sun dreaming about fish and mice and cream " +
		"the dog barks loudly at nothing in particular every single night"
)

// writeSimFiles writes files (relative path → content) under a fresh
// temp root and returns the root.
func writeSimFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// analyzeSim is analyzeSimilarity with the tests' working thresholds and
// the shingle floor the measured fixtures clear.
func analyzeSim(root string, mutate func(*similarityOptions)) SimilarityReport {
	opts := similarityOptions{
		dirs:        []string{"."},
		include:     []string{"**/*.md"},
		exclude:     []string{".git/**"},
		jaccard:     0.5,
		containment: 0.85,
		minShingles: 10,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return analyzeSimilarity(root, opts)
}

func TestSimilarityProfileForExtensions(t *testing.T) {
	cases := []struct {
		path string
		want similarityProfile
	}{
		{"docs/a.md", profileMarkdown},
		{"docs/a.markdown", profileMarkdown},
		{"docs/a.mdx", profileMarkdown},
		{"lib/a.dart", profileCode},
		{"src/a.java", profileCode},
		{"cmd/a.go", profileCode},
		{"data.csv", profileRaw},
		{"README", profileRaw},
	}
	for _, tc := range cases {
		if got := similarityProfileFor(tc.path); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestNormalizeMarkdownDropsFences(t *testing.T) {
	normalized := normalizeMarkdown(
		"# Title\n\nProse line one.\n```dart\nfinal hidden = 1; // inside a fence\n```\nProse line two.\n")
	if !containsAll(normalized, "Prose") || containsAll(normalized, "hidden") {
		t.Errorf("fence content leaked: %q", normalized)
	}
}

func TestNormalizeCodeStripsCommentsAndImports(t *testing.T) {
	normalized := normalizeCode(
		"import \"package:x/x.dart\";\nlibrary foo;\n// a line comment\nvoid main() {\n  /* block */ final a = 1;\n}\n")
	if containsAll(normalized, "import", "line comment") {
		t.Errorf("noise kept: %q", normalized)
	}
	if !containsAll(normalized, "final a = 1;") {
		t.Errorf("code lost: %q", normalized)
	}
}

func TestNormalizeCodeRejoinsAfterBlockComment(t *testing.T) {
	normalized := normalizeCode("/* one\ntwo */ final kept = true;\n")
	if !containsAll(normalized, "final kept = true;") || containsAll(normalized, "one") {
		t.Errorf("block comment handling broke: %q", normalized)
	}
}

func TestTokensForCodePlaceholders(t *testing.T) {
	tokens := tokensFor("final width = 640;\nfinal name = \"abc\";\n", profileCode)
	for _, want := range []string{"final", "width", "N", "name", "S"} {
		found := false
		for _, tok := range tokens {
			if tok == want {
				found = true
			}
		}
		if !found {
			t.Errorf("token %q missing from %v", want, tokens)
		}
	}
	for _, tok := range tokens {
		if tok == "640" || tok == "abc" {
			t.Errorf("raw literal %q leaked into %v", tok, tokens)
		}
	}
}

func TestTokensForProseLowercaseTwoPlus(t *testing.T) {
	got := tokensFor("The Quick brown fox A x1", profileMarkdown)
	want := []string{"the", "quick", "brown", "fox", "x1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSimilarityGoTokensDropsImportsPlaceholdersLiterals(t *testing.T) {
	src := "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {\n\tWidth := 640\n\ts := \"abc\"\n\tfmt.Println(Width, s)\n}\n"
	got := similarityGoTokens(src)
	want := []string{"package", "main", "func", "main", "width", "N", "s", "S", "fmt", "println", "width", "s"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestShinglesBelowOneShingleYieldEmpty(t *testing.T) {
	if got := shinglesForPath("too short", "f.txt"); len(got) != 0 {
		t.Errorf("got %d shingles, want 0", len(got))
	}
}

func TestNineTokensYieldTwoShingles(t *testing.T) {
	content := "one two three four five six seven eight nine"
	a := shinglesForPath(content, "a.txt")
	b := shinglesForPath(content, "b.txt")
	if len(a) != 2 {
		t.Errorf("got %d shingles, want 2 (9 tokens)", len(a))
	}
	if len(a) != len(b) || jaccard(a, b) != 1.0 {
		t.Errorf("identical content diverged: %d vs %d", len(a), len(b))
	}
}

func TestJaccardAndContainmentMath(t *testing.T) {
	mk := func(items ...string) map[string]struct{} {
		set := map[string]struct{}{}
		for _, i := range items {
			set[i] = struct{}{}
		}
		return set
	}
	a := mk("x", "y", "z")
	b := mk("x", "y", "q")
	big := mk("x", "y", "z", "q", "r", "s")
	if got := jaccard(a, b); diff(got, 2.0/4.0) {
		t.Errorf("jaccard(a,b) = %f, want 0.5", got)
	}
	if got := jaccard(a, a); got != 1.0 {
		t.Errorf("jaccard(a,a) = %f, want 1", got)
	}
	if got := jaccard(a, mk()); got != 0 {
		t.Errorf("jaccard(a,{}) = %f, want 0", got)
	}
	if got := containment(big, a); got != 1.0 {
		t.Errorf("containment(big,a) = %f, want 1", got)
	}
	if got := containment(a, b); diff(got, 2.0/3.0) {
		t.Errorf("containment(a,b) = %f, want 0.667", got)
	}
	if got := containment(a, mk()); got != 0 {
		t.Errorf("containment(a,{}) = %f, want 0", got)
	}
}

func diff(a, b float64) bool { return a < b-1e-9 || a > b+1e-9 }

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}

func TestExactDuplicatesCluster(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA, "b.md": simProseA})
	report := analyzeSim(root, nil)
	if report.Scanned != 2 {
		t.Errorf("scanned = %d, want 2", report.Scanned)
	}
	if len(report.Clusters) != 1 || len(report.Clusters[0].Files) != 2 {
		t.Fatalf("clusters = %+v, want one cluster of 2", report.Clusters)
	}
	if len(report.Pairs) != 1 || report.Pairs[0].Jaccard != 1.0 {
		t.Errorf("pairs = %+v, want a single jaccard-1.0 pair", report.Pairs)
	}
	paths := report.ClusteredPaths()
	if fmt.Sprint(paths) != fmt.Sprint([]string{"a.md", "b.md"}) {
		t.Errorf("clustered paths = %v", paths)
	}
}

func TestNearDuplicateCrossesUnrelatedDoesNot(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md": simProseA, "b.md": simProseB, "c.md": simProseC,
	})
	report := analyzeSim(root, nil)
	if len(report.Pairs) != 1 {
		t.Fatalf("pairs = %+v, want exactly the a/b pair", report.Pairs)
	}
	pair := report.Pairs[0]
	if pair.A != "a.md" || pair.B != "b.md" {
		t.Errorf("pair = %s/%s, want a.md/b.md", pair.A, pair.B)
	}
	// k=8 shingles: two scattered word changes cost ~16 of ~26 shingles —
	// the near-duplicate lands at 0.51, well above the 0.5 threshold,
	// while the unrelated pair measured 0.00.
	if pair.Jaccard <= 0.4 {
		t.Errorf("jaccard = %f, want > 0.4", pair.Jaccard)
	}
	for _, c := range report.Clusters {
		if c.simHas("c.md") {
			t.Errorf("unrelated c.md clustered: %+v", c)
		}
	}
}

// TestTransitiveChainMergesIntoOneCluster is the union-find transitivity
// killer: only (a, ab) and (ab, c) cross (measured jaccard 0.42 and
// 0.47, containment 1.0 each) while (a, c) measures 0.00/0.00. The one
// three-file cluster can only exist if union transitively merges the
// two direct pairs.
func TestTransitiveChainMergesIntoOneCluster(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":  simProseA,
		"ab.md": simProseA + "\n\n" + simProseC,
		"c.md":  simProseC,
	})
	report := analyzeSim(root, func(o *similarityOptions) {
		o.jaccard, o.containment = 0.4, 0.9
	})
	if len(report.Pairs) != 2 {
		t.Fatalf("pairs = %+v, want exactly (a,ab) and (ab,c)", report.Pairs)
	}
	for _, pair := range report.Pairs {
		if pair.A == "a.md" && pair.B == "c.md" {
			t.Errorf("a/c crossed thresholds directly: %+v", pair)
		}
	}
	if len(report.Clusters) != 1 || len(report.Clusters[0].Files) != 3 {
		t.Fatalf("clusters = %+v, want one cluster of 3 (transitive merge)", report.Clusters)
	}
}

// TestContainmentCatchesSmallCopiedIntoBig kills metric mutations: the
// pair crosses on containment (measured 1.0) while jaccard (measured
// 0.28) stays below 0.9 — swapping the metrics or dropping containment
// fails this test.
func TestContainmentCatchesSmallCopiedIntoBig(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"small.md": simProseA,
		"big.md":   simProseA + "\n\n" + simProseC + "\n\n" + simProseB,
	})
	report := analyzeSim(root, func(o *similarityOptions) {
		o.jaccard, o.containment = 0.9, 0.85
	})
	if len(report.Pairs) != 1 {
		t.Fatalf("pairs = %+v, want exactly one", report.Pairs)
	}
	pair := report.Pairs[0]
	if pair.A != "big.md" || pair.B != "small.md" {
		t.Errorf("pair = %s/%s, want big.md/small.md", pair.A, pair.B)
	}
	if pair.Containment <= 0.85 {
		t.Errorf("containment = %f, want > 0.85", pair.Containment)
	}
	if pair.Jaccard >= 0.9 {
		t.Errorf("jaccard = %f, want < 0.9", pair.Jaccard)
	}
}

// TestMarkdownFencesDoNotInflateMatches kills normalization drops: the
// two files differ only in large fenced blocks; normalized (fences
// gone) they measure jaccard 1.0, while raw-byte tokenization measures
// 0.43 — below the 0.5 threshold this test uses.
func TestMarkdownFencesDoNotInflateMatches(t *testing.T) {
	fenceX := "```go\nfmt.Println(\"alphaOne\", 11111)\nfmt.Println(\"bravoTwo\", 22222)\n" +
		"fmt.Println(\"charlieThree\", 33333)\nfmt.Println(\"deltaFour\", 44444)\n```\n"
	fenceY := "```go\nfmt.Println(\"echoFive\", 55555)\nfmt.Println(\"foxtrotSix\", 66666)\n" +
		"fmt.Println(\"golfSeven\", 77777)\nfmt.Println(\"hotelEight\", 88888)\n```\n"
	root := writeSimFiles(t, map[string]string{
		"a.md": fenceX + simProseA,
		"b.md": fenceY + simProseA,
	})
	report := analyzeSim(root, nil)
	if len(report.Pairs) != 1 || report.Pairs[0].Jaccard != 1.0 {
		t.Errorf("pairs = %+v, want one jaccard-1.0 pair (fences normalized away)", report.Pairs)
	}
	// And a fence-only markdown file carries no signal at all.
	if got := shinglesForPath(fenceX, "fences.md"); len(got) != 0 {
		t.Errorf("fence-only markdown kept %d shingles", len(got))
	}
}

// TestGoLiteralsAndImportsDontBlockMatch pins the Go adaptation: two
// files identical except import paths and literal values measure
// jaccard 1.0 — imports are dropped whole and literals placeholdered.
func TestGoLiteralsAndImportsDontBlockMatch(t *testing.T) {
	body := "func run(w int) int {\n\ttotal := 0\n\tfor i := 0; i < w; i++ {\n\t\ttotal += i * 2\n\t}\n\treturn total\n}\n"
	goA := "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n" + body
	goB := "package main\n\nimport (\n\t\"log\"\n\t\"io\"\n\t\"strconv\"\n)\n\n" + body
	goB = strings.ReplaceAll(goB, "total += i * 2", "total += i * 3")
	root := writeSimFiles(t, map[string]string{"a.go": goA, "b.go": goB})
	// Literal 2 → 3 is the only body difference: token N/N placeholdered.
	report := analyzeSim(root, func(o *similarityOptions) {
		o.include = []string{"**/*.go"}
		o.minShingles = 10
	})
	if report.Scanned != 2 {
		t.Fatalf("scanned = %d, want 2 (measured: both files clear the floor)", report.Scanned)
	}
	if len(report.Pairs) != 1 || report.Pairs[0].Jaccard != 1.0 {
		t.Errorf("pairs = %+v, want one jaccard-1.0 pair", report.Pairs)
	}
}

func TestMetricsForReturnsStrongestPair(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md": simProseA, "b.md": simProseA, "c.md": simProseC,
	})
	report := analyzeSim(root, nil)
	jac, con, ok := report.MetricsFor("a.md")
	if !ok || jac != 1.0 || con != 1.0 {
		t.Errorf("metricsFor(a.md) = (%f, %f, %v)", jac, con, ok)
	}
	if _, _, ok := report.MetricsFor("c.md"); ok {
		t.Errorf("metricsFor(c.md) reported metrics for an unclustered file")
	}
}

func TestSkippedBelowShingleFloor(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"tiny.md": "too short",
		"a.md":    simProseA,
		"c.md":    simProseC,
	})
	report := analyzeSim(root, func(o *similarityOptions) { o.minShingles = 20 })
	if report.Scanned != 2 || report.Skipped != 1 {
		t.Errorf("scanned = %d, skipped = %d, want 2/1", report.Scanned, report.Skipped)
	}
}

func TestExcludeGlobDropsFiles(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":           simProseA,
		"generated/b.md": simProseA,
	})
	report := analyzeSim(root, func(o *similarityOptions) { o.exclude = []string{"generated/**"} })
	if report.Scanned != 1 || !report.Clean() {
		t.Errorf("scanned = %d, clean = %v, want 1/true", report.Scanned, report.Clean())
	}
}

func TestIncludeGlobLimitsScan(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":  simProseA,
		"a.txt": simProseA,
	})
	report := analyzeSim(root, func(o *similarityOptions) { o.include = []string{"*.txt"} })
	if report.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", report.Scanned)
	}
}

func TestCleanCorpusReportsClean(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA, "c.md": simProseC})
	report := analyzeSim(root, nil)
	if !report.Clean() || len(report.Clusters) != 0 {
		t.Errorf("clean corpus reported %+v", report)
	}
}

func (c SimilarityCluster) simHas(path string) bool {
	for _, f := range c.Files {
		if f == path {
			return true
		}
	}
	return false
}
