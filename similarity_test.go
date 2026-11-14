package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
func analyzeSim(t *testing.T, root string, mutate func(*similarityOptions)) SimilarityReport {
	t.Helper()
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
	report, err := analyzeSimilarity(root, opts)
	if err != nil {
		t.Fatalf("analyzeSimilarity: %v", err)
	}
	return report
}

// testHash is FNV-1a 64 over whole tokens, each terminated by the
// shingle separator — the same byte stream shingleHash consumes. It is
// both the fixture set builder and the exact-algorithm pin for
// shingleHash.
func testHash(tokens ...string) uint64 {
	var hash uint64 = fnvOffsetBasis64
	for _, tok := range tokens {
		for i := range tok {
			hash = (hash ^ uint64(tok[i])) * fnvPrime64
		}
		hash = (hash ^ shingleSeparator) * fnvPrime64
	}
	return hash
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
	mk := func(items ...string) shingleSet {
		set := shingleSet{}
		for _, i := range items {
			set[testHash(i)] = struct{}{}
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

// TestShingleHashSeparatorAndFNV pins the ad4f27b hash contract: tokens
// are hash units each terminated by the separator, so ["ab", "c"] and
// ["a", "bc"] hash differently (the joined-string bug), and the hash is
// exactly 64-bit FNV-1a over the separator-terminated token stream.
func TestShingleHashSeparatorAndFNV(t *testing.T) {
	tail := []string{"t3", "t4", "t5", "t6", "t7", "t8"}
	abC := shingleHash(append([]string{"ab", "c"}, tail...), 0)
	aBc := shingleHash(append([]string{"a", "bc"}, tail...), 0)
	if abC == aBc {
		t.Errorf("separator unit broken: [ab c] and [a bc] hash identically (%d)", abC)
	}
	if got, want := shingleHash([]string{"ab", "c", "t3", "t4", "t5", "t6", "t7", "t8"}, 0),
		testHash("ab", "c", "t3", "t4", "t5", "t6", "t7", "t8"); got != want {
		t.Errorf("shingleHash = %d, want plain FNV-1a 64 over separator-terminated tokens (%d)", got, want)
	}
	// The same window at a different start hashes identically (the set
	// key is the hash, not the position).
	toks := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india"}
	if shingleHash(toks, 0) == shingleHash(toks, 1) {
		t.Errorf("distinct windows hashed identically")
	}
	s1 := shinglesForPath("alpha bravo charlie delta echo foxtrot golf hotel india", "a.txt")
	if len(s1) != 2 {
		t.Fatalf("shingle count = %d, want 2", len(s1))
	}
	if _, ok := s1[shingleHash(toks, 0)]; !ok {
		t.Fatalf("shingle set lost the first window hash")
	}
	if _, ok := s1[shingleHash(toks, 1)]; !ok {
		t.Errorf("shingle set lost the second window hash")
	}
}

// TestSimilarityPairsEqualsAllPairs is the ad4f27b candidate-equivalence
// oracle: the inverted-index candidate scan must find exactly the pairs
// the naive all-pairs loop found — same pair set, same exact scores —
// on a mixed corpus (duplicates, a transitive chain, Go code, a
// below-floor file carrying no shingles).
func TestSimilarityPairsEqualsAllPairs(t *testing.T) {
	contents := map[string]string{
		"a.md":    simProseA,
		"b.md":    simProseA,
		"ab.md":   simProseA + "\n\n" + simProseC,
		"c.md":    simProseC,
		"tiny.md": "too short",
		"x.go":    "package main\n\nfunc run(w int) int {\n\ttotal := 0\n\tfor i := 0; i < w; i++ {\n\t\ttotal += i * 2\n\t}\n\treturn total\n}\n",
		"y.go":    "package main\n\nfunc run(w int) int {\n\ttotal := 0\n\tfor i := 0; i < w; i++ {\n\t\ttotal += i * 3\n\t}\n\treturn total\n}\n",
	}
	var files []similarityFile
	for path, content := range contents {
		files = append(files, similarityFile{path: path, shingles: shinglesForPath(content, path)})
	}
	got := similarityPairs(files, 0.4, 0.85)
	want := naiveAllPairs(files, 0.4, 0.85)
	if len(got) != len(want) {
		t.Fatalf("candidate scan found %d pairs, all-pairs found %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
	}
	byKey := map[string]SimilarityPair{}
	for _, p := range want {
		byKey[p.A+"|"+p.B] = p
	}
	for _, p := range got {
		w, ok := byKey[p.A+"|"+p.B]
		if !ok {
			t.Errorf("candidate scan invented pair %+v", p)
			continue
		}
		if p.Jaccard != w.Jaccard || p.Containment != w.Containment {
			t.Errorf("pair %s|%s scores drifted: (%f, %f) vs all-pairs (%f, %f)",
				p.A, p.B, p.Jaccard, p.Containment, w.Jaccard, w.Containment)
		}
	}
	// The oracle must have found the real duplicates, or it is vacuous.
	if _, ok := byKey["a.md|b.md"]; !ok {
		t.Fatalf("oracle found no a.md/b.md pair — corpus broken: %+v", want)
	}
}

// naiveAllPairs is the pre-ad4f27b all-pairs loop, kept here as the
// equivalence oracle for similarityPairs.
func naiveAllPairs(files []similarityFile, jaccardThreshold, containmentThreshold float64) []SimilarityPair {
	var pairs []SimilarityPair
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			a, b := files[i].shingles, files[j].shingles
			if len(a) == 0 || len(b) == 0 {
				continue
			}
			shared := intersectionCount(a, b)
			jac := float64(shared) / float64(len(a)+len(b)-shared)
			con := float64(shared) / float64(min(len(a), len(b)))
			if jac < jaccardThreshold && con < containmentThreshold {
				continue
			}
			pairs = append(pairs, SimilarityPair{A: files[i].path, B: files[j].path, Jaccard: jac, Containment: con})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Jaccard > pairs[j].Jaccard })
	return pairs
}

// TestPostingsOverLimitSkippedAsBoilerplate is the ad4f27b postings cap:
// a shingle hash shared by more than 100 files is boilerplate and its
// 101-file identical block produces NO pairs, while a genuine duplicate
// pair in the same corpus is still found through its low-posting
// shingles.
func TestPostingsOverLimitSkippedAsBoilerplate(t *testing.T) {
	boiler := map[string]string{}
	names := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		name := fmt.Sprintf("boiler%03d.md", i)
		names = append(names, name)
		boiler[name] = simProseC
	}
	root := writeSimFiles(t, boiler)
	report := analyzeSim(t, root, nil)
	if !report.Clean() {
		t.Errorf("101 identical boilerplate files clustered: %+v", report.Pairs)
	}

	boiler["a.md"], boiler["b.md"] = simProseA, simProseA
	root = writeSimFiles(t, boiler)
	report = analyzeSim(t, root, nil)
	if len(report.Pairs) != 1 || report.Pairs[0].A != "a.md" || report.Pairs[0].B != "b.md" {
		t.Errorf("real duplicate lost among boilerplate: %+v", report.Pairs)
	}
}

// TestScanCapFailsLoudly is the ad4f27b memory guard: a scope matching
// more than 20000 files must fail with the actionable error — through
// the analyzer (error) and through the command (exit 1, the port's
// usage-error convention, message on stderr naming the narrowing).
func TestScanCapFailsLoudly(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < similarityMaxScanFiles+1; i++ {
		name := filepath.Join(root, fmt.Sprintf("big%05d.md", i))
		if err := os.WriteFile(name, []byte("cap fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := analyzeSimilarity(root, similarityOptions{
		dirs: []string{"."}, include: []string{"**/*.md"}, exclude: []string{".git/**"},
		jaccard: 0.8, containment: 0.95, minShingles: 50,
	})
	if err == nil {
		t.Fatal("analyzeSimilarity accepted a scope above the cap")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("above the %d-file safety cap", similarityMaxScanFiles)) ||
		!strings.Contains(err.Error(), "narrow include/dirs or extend exclude") {
		t.Errorf("error lacks the actionable guidance: %v", err)
	}
	stdout, stderr, code := runSimilarity(t, root, "--check")
	if code != 1 {
		t.Errorf("exit = %d, want 1 (usage-error convention)", code)
	}
	if strings.Contains(stdout+stderr, "0 files scanned") || !strings.Contains(stderr, "safety cap") {
		t.Errorf("stderr must carry the cap message, stdout stay clean:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// TestWorktreesExcludedByDefault pins the ad4f27b default-exclude
// addition: a duplicate inside .worktrees/ never enters the default
// scan (fleet worktree checkouts would otherwise flood the report).
func TestWorktreesExcludedByDefault(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":               simProseA,
		"b.md":               simProseA,
		".worktrees/wt/a.md": simProseA,
	})
	stdout, _, code := runSimilarity(t, root, "--check", "--jaccard", "0.5", "--min-shingles", "10")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if strings.Contains(stdout, ".worktrees") {
		t.Errorf("worktree file scanned: %s", stdout)
	}
	if !strings.Contains(stdout, "2 files in 1 similar-file clusters (2 scanned)") {
		t.Errorf("unexpected report:\n%s", stdout)
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
	report := analyzeSim(t, root, nil)
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
	report := analyzeSim(t, root, nil)
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
	report := analyzeSim(t, root, func(o *similarityOptions) {
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
	report := analyzeSim(t, root, func(o *similarityOptions) {
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
	report := analyzeSim(t, root, nil)
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
	report := analyzeSim(t, root, func(o *similarityOptions) {
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
	report := analyzeSim(t, root, nil)
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
	report := analyzeSim(t, root, func(o *similarityOptions) { o.minShingles = 20 })
	if report.Scanned != 2 || report.Skipped != 1 {
		t.Errorf("scanned = %d, skipped = %d, want 2/1", report.Scanned, report.Skipped)
	}
}

func TestExcludeGlobDropsFiles(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":           simProseA,
		"generated/b.md": simProseA,
	})
	report := analyzeSim(t, root, func(o *similarityOptions) { o.exclude = []string{"generated/**"} })
	if report.Scanned != 1 || !report.Clean() {
		t.Errorf("scanned = %d, clean = %v, want 1/true", report.Scanned, report.Clean())
	}
}

func TestIncludeGlobLimitsScan(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md":  simProseA,
		"a.txt": simProseA,
	})
	report := analyzeSim(t, root, func(o *similarityOptions) { o.include = []string{"*.txt"} })
	if report.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", report.Scanned)
	}
}

func TestCleanCorpusReportsClean(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA, "c.md": simProseC})
	report := analyzeSim(t, root, nil)
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
