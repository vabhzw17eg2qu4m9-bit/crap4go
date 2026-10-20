package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runSimilarity runs the subcommand and returns (stdout, stderr, exit).
func runSimilarity(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunSimilarityCommand(args, root, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestSimilarityCheckFlagsViolations(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md": simProseA, "b.md": simProseA, "c.md": simProseC,
	})
	stdout, _, code := runSimilarity(t, root, "--check", "--jaccard", "0.5", "--min-shingles", "10")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	for _, want := range []string{
		"a.md: similar to b.md — near-duplicate content (jaccard 1.00, containment 1.00)",
		"b.md: similar to a.md — near-duplicate content (jaccard 1.00, containment 1.00)",
		"2 files in 1 similar-file clusters (3 scanned)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "c.md:") {
		t.Errorf("unrelated file flagged:\n%s", stdout)
	}
}

func TestSimilarityCheckCleanExitsZero(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA, "c.md": simProseC})
	stdout, _, code := runSimilarity(t, root, "--check", "--jaccard", "0.5", "--min-shingles", "10")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "2 files scanned") || !strings.Contains(stdout, "no similar files") {
		t.Errorf("clean summary missing:\n%s", stdout)
	}
}

// TestSimilarityExplorerAlwaysExitsZero pins the upstream explorer
// contract: the default run is informational and exits 0 even when
// clusters are found.
func TestSimilarityExplorerAlwaysExitsZero(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md": simProseA, "b.md": simProseA, "c.md": simProseC,
	})
	stdout, _, code := runSimilarity(t, root, "--jaccard", "0.5", "--min-shingles", "10")
	if code != 0 {
		t.Errorf("exit = %d, want 0 (explorer is informational)", code)
	}
	for _, want := range []string{
		"similarity: 3 files scanned (0 skipped below 10 shingles), thresholds jaccard >= 0.5 / containment >= 0.95",
		"cluster 1 — 2 files:",
		"a.md (jaccard 1.00, containment 1.00)",
		"2 files in 1 clusters",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

func TestSimilarityJSONReport(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA, "b.md": simProseA})
	stdout, _, code := runSimilarity(t, root, "--format", "json", "--jaccard", "0.5", "--min-shingles", "10")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var decoded struct {
		Scanned    int `json:"scanned"`
		Skipped    int `json:"skipped"`
		Thresholds struct {
			Jaccard     float64 `json:"jaccard"`
			Containment float64 `json:"containment"`
			MinShingles int     `json:"min_shingles"`
		} `json:"thresholds"`
		Pairs []struct {
			A       string  `json:"a"`
			B       string  `json:"b"`
			Jaccard float64 `json:"jaccard"`
		} `json:"pairs"`
		Clusters []struct {
			Files []string `json:"files"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if decoded.Scanned != 2 || decoded.Skipped != 0 {
		t.Errorf("scanned/skipped = %d/%d, want 2/0", decoded.Scanned, decoded.Skipped)
	}
	if decoded.Thresholds.Jaccard != 0.5 || decoded.Thresholds.MinShingles != 10 {
		t.Errorf("thresholds = %+v", decoded.Thresholds)
	}
	if len(decoded.Pairs) != 1 || decoded.Pairs[0].A != "a.md" || decoded.Pairs[0].B != "b.md" || decoded.Pairs[0].Jaccard != 1.0 {
		t.Errorf("pairs = %+v", decoded.Pairs)
	}
	if len(decoded.Clusters) != 1 || len(decoded.Clusters[0].Files) != 2 {
		t.Errorf("clusters = %+v", decoded.Clusters)
	}
}

func TestSimilarityJSONDefaultThresholds(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA})
	stdout, _, _ := runSimilarity(t, root, "--format", "json")
	var decoded struct {
		Thresholds struct {
			Jaccard     float64 `json:"jaccard"`
			Containment float64 `json:"containment"`
			MinShingles int     `json:"min_shingles"`
		} `json:"thresholds"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
	// Upstream gate defaults, unchanged: jaccard 0.8 / containment 0.95 /
	// min_shingles 50.
	if decoded.Thresholds.Jaccard != 0.8 || decoded.Thresholds.Containment != 0.95 || decoded.Thresholds.MinShingles != 50 {
		t.Errorf("default thresholds = %+v", decoded.Thresholds)
	}
}

func TestSimilarityDirArgLimitsScan(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"docs/a.md": simProseA, "docs/b.md": simProseA, "other/c.md": simProseA,
	})
	stdout, _, code := runSimilarity(t, root, "--jaccard", "0.5", "--min-shingles", "10", "docs")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "2 files scanned") || !strings.Contains(stdout, "a.md") {
		t.Errorf("docs dir not scanned exclusively:\n%s", stdout)
	}
}

func TestSimilarityCheckExcludesAndIncludes(t *testing.T) {
	root := writeSimFiles(t, map[string]string{
		"a.md": simProseA, "generated/b.md": simProseA,
	})
	stdout, _, code := runSimilarity(t, root, "--check", "--jaccard", "0.5",
		"--min-shingles", "10", "--exclude", "generated/**")
	if code != 0 {
		t.Errorf("exit = %d, want 0 (excluded twin)", code)
	}
	if !strings.Contains(stdout, "1 files scanned") {
		t.Errorf("exclude not honored:\n%s", stdout)
	}
}

func TestSimilarityUsageErrors(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA})
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--jaccard", "1.5"}, "--jaccard must be between 0 and 1"},
		{[]string{"--containment", "-0.1"}, "--containment must be between 0 and 1"},
		{[]string{"--min-shingles", "0"}, "--min-shingles must be positive"},
		{[]string{"--format", "yaml"}, `--format must be "console" or "json"`},
	}
	for _, tc := range cases {
		stdout, stderr, code := runSimilarity(t, root, tc.args...)
		if code != 1 {
			t.Errorf("%v: exit = %d, want 1", tc.args, code)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: stderr missing %q, got %q", tc.args, tc.want, stderr)
		}
		if stdout != "" {
			t.Errorf("%v: stdout not empty on usage error: %q", tc.args, stdout)
		}
	}
}

func TestSimilarityMissingDirSkippedSilently(t *testing.T) {
	root := writeSimFiles(t, map[string]string{"a.md": simProseA})
	stdout, _, code := runSimilarity(t, root, "--min-shingles", "10", "does-not-exist")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "0 files scanned") {
		t.Errorf("missing dir not skipped silently:\n%s", stdout)
	}
}

func TestSimilarityViolationMessageCountsMoreCounterparts(t *testing.T) {
	prose := map[string]string{
		"a.md": simProseA, "b.md": simProseA, "c.md": simProseA, "d.md": simProseA,
	}
	root := writeSimFiles(t, prose)
	stdout, _, code := runSimilarity(t, root, "--check", "--jaccard", "0.5", "--min-shingles", "10")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	// Four clustered files: each names two counterparts and counts the rest.
	if !strings.Contains(stdout, "a.md: similar to b.md, c.md (+1 more)") {
		t.Errorf("counterpart naming wrong:\n%s", stdout)
	}
	if !strings.Contains(stdout, "4 files in 1 similar-file clusters (4 scanned)") {
		t.Errorf("summary wrong:\n%s", stdout)
	}
}
