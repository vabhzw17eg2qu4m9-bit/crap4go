package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const analyzerSrc = `package sample

func Add(a, b int) int {
	return a + b
}

func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
`

const analyzerProfile = `mode: atomic
example.com/sample/sample.go:3.10,5.2 1 3
example.com/sample/sample.go:7.2,7.10 1 1
example.com/sample/sample.go:8.3,9.4 1 0
example.com/sample/sample.go:10.3,11.4 1 1
`

func writeFixture(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(src, []byte(analyzerSrc), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	cov := filepath.Join(dir, "cover.out")
	if err := os.WriteFile(cov, []byte(analyzerProfile), 0o644); err != nil {
		t.Fatalf("write cov: %v", err)
	}
}

func TestAnalyze_WithCoverage(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir)
	src := filepath.Join(dir, "sample.go")
	cov := filepath.Join(dir, "cover.out")

	metrics, err := Analyze([]string{src}, cov)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("got %d metrics, want 2", len(metrics))
	}

	find := func(name string) MethodMetric {
		for _, m := range metrics {
			if m.MethodName == name {
				return m
			}
		}
		t.Fatalf("method %q missing", name)
		return MethodMetric{}
	}

	add := find("Add") // CC=1, fully covered
	if add.Complexity != 1 {
		t.Errorf("Add CC = %d, want 1", add.Complexity)
	}
	if add.Coverage == nil || *add.Coverage != 1.0 {
		t.Errorf("Add coverage = %v, want 1.0", add.Coverage)
	}
	if add.CrapScore == nil || *add.CrapScore != 1.0 {
		t.Errorf("Add CRAP = %v, want 1.0", add.CrapScore)
	}

	max := find("Max") // CC=2; blocks: (7,7)=1/1 covered, (8,9)=0/1, (10,11)=1/1 => 2/3
	if max.Complexity != 2 {
		t.Errorf("Max CC = %d, want 2", max.Complexity)
	}
	if max.Coverage == nil || math.Abs(*max.Coverage-(2.0/3.0)) > 1e-9 {
		t.Errorf("Max coverage = %v, want 2/3", max.Coverage)
	}
	if max.CrapScore == nil {
		t.Errorf("Max CRAP should not be nil")
	} else {
		// CC=2, cov=2/3 -> 4*(1/3)^3 + 2 = 4/27 + 2 = 2.148...
		want := 4.0*math.Pow(1.0/3.0, 3) + 2
		if math.Abs(*max.CrapScore-want) > 1e-9 {
			t.Errorf("Max CRAP = %v, want %v", *max.CrapScore, want)
		}
	}
}

func TestAnalyze_MissingCoverageErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(src, []byte(analyzerSrc), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.out")
	if _, err := Analyze([]string{src}, missing); err == nil {
		t.Fatal("expected error on missing coverage file")
	}
}

func TestAnalyze_NonMatchingPathIsNA(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(src, []byte(analyzerSrc), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cov := filepath.Join(dir, "cover.out")
	// profile path does not match sample.go basename
	body := "mode: atomic\nexample.com/other/other.go:1.1,5.2 1 1\n"
	if err := os.WriteFile(cov, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	metrics, _ := Analyze([]string{src}, cov)
	for _, m := range metrics {
		if m.Coverage != nil {
			t.Errorf("method %q should have N/A (path mismatch)", m.MethodName)
		}
	}
}

// sanity: the test profile path does match by basename in TestAnalyze_WithCoverage.
func TestLookupCoverage_BasenameMatch(t *testing.T) {
	prof, _ := ParseCoverProfileReader(strings.NewReader(analyzerProfile))
	if fc := lookupCoverage(prof, "/abs/path/sample.go"); fc == nil {
		t.Fatal("expected basename match")
	}
}

// strictLcov covers only Add's statement line; Max's span intersects no DA
// record, and strict.go has no SF entry at all.
const strictLcov = `SF:sample.go
DA:4,1
end_of_record
`

func TestAnalyzeInRoot_StrictNoEntryIsZeroAndNoStatementStaysNA(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "sample.go")
	if err := os.WriteFile(src, []byte(analyzerSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "strict.go")
	if err := os.WriteFile(other, []byte("package sample\n\nfunc Lonely() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := filepath.Join(root, "cover.info")
	if err := os.WriteFile(info, []byte(strictLcov), 0o644); err != nil {
		t.Fatal(err)
	}

	metrics, err := AnalyzeInRoot([]string{src, other}, info, root, true)
	if err != nil {
		t.Fatalf("analyze strict: %v", err)
	}
	byName := map[string]*MethodMetric{}
	for i := range metrics {
		byName[metrics[i].MethodName] = &metrics[i]
	}
	if cov := byName["Add"].Coverage; cov == nil || *cov != 1.0 {
		t.Errorf("Add coverage = %v, want 1.0", cov)
	}
	// Covered file, but no statement record intersects Max's span: stays N/A.
	if cov := byName["Max"].Coverage; cov != nil {
		t.Errorf("Max coverage = %v, want nil (N/A)", *cov)
	}
	// No coverage entry for strict.go: 0.0%, not N/A.
	if cov := byName["Lonely"].Coverage; cov == nil || *cov != 0.0 {
		t.Errorf("Lonely coverage = %v, want 0.0", cov)
	}

	// Without --strict the entry-less file stays N/A.
	metrics, err = AnalyzeInRoot([]string{src, other}, info, root, false)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, m := range metrics {
		if m.MethodName == "Lonely" && m.Coverage != nil {
			t.Errorf("non-strict Lonely coverage = %v, want nil (N/A)", *m.Coverage)
		}
	}
}

// The fail-closed default: a missing coverage file errors without --strict.
func TestAnalyzeInRoot_MissingCoverageErrors(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir)
	src := filepath.Join(dir, "sample.go")
	missing := filepath.Join(dir, "does-not-exist.info")
	if _, err := AnalyzeInRoot([]string{src}, missing, dir, false); err == nil {
		t.Error("expected error on missing coverage file by default")
	}
}

// The fail-closed default: a record-less profile errors without --strict.
func TestAnalyzeInRoot_EmptyCoverageErrors(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir)
	src := filepath.Join(dir, "sample.go")
	empty := filepath.Join(dir, "empty.info")
	if err := os.WriteFile(empty, []byte("TN:nothing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeInRoot([]string{src}, empty, dir, false); err == nil {
		t.Error("expected error on coverage with no records by default")
	}
}
