package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleProfile = `mode: atomic
crap4go/sample.go:5.10,5.20 1 3
crap4go/sample.go:10.5,10.10 1 0
crap4go/sample.go:12.3,14.5 2 2
crap4go/sample.go:30.1,38.2 5 5
`

func TestParseCoverProfileReader_ModeAndBlocks(t *testing.T) {
	prof, err := ParseCoverProfileReader(strings.NewReader(sampleProfile))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fc, ok := prof["crap4go/sample.go"]
	if !ok {
		t.Fatalf("missing sample.go in profile: %v", prof)
	}
	if len(fc.Blocks) != 4 {
		t.Fatalf("got %d blocks, want 4", len(fc.Blocks))
	}
	want := CoverBlock{StartLine: 5, StartCol: 10, EndLine: 5, EndCol: 20, NumStmt: 1, Count: 3}
	if fc.Blocks[0] != want {
		t.Fatalf("block[0] = %+v, want %+v", fc.Blocks[0], want)
	}
}

func TestParseCoverProfileReader_CommentSkipped(t *testing.T) {
	prof, err := ParseCoverProfileReader(strings.NewReader("mode: atomic\n# a comment\ncrap4go/x.go:1.1,2.2 1 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(prof) != 1 {
		t.Fatalf("expected 1 file, got %d", len(prof))
	}
}

func TestParseCoverProfileReader_Malformed(t *testing.T) {
	_, err := ParseCoverProfileReader(strings.NewReader("mode: atomic\nnot a profile line\n"))
	if err == nil {
		t.Fatal("expected error on malformed line")
	}
}

func TestCoverageForMethod_PartialFraction(t *testing.T) {
	prof, _ := ParseCoverProfileReader(strings.NewReader(sampleProfile))
	fc := prof["crap4go/sample.go"]
	// lines 9..14 intersect block (10..10, count 0, stmt 1) and (12..14, count 2, stmt 2)
	// covered = 0 + 2 = 2; total = 1 + 2 = 3; fraction = 2/3
	got := CoverageForMethod(fc, 9, 14)
	if got == nil {
		t.Fatal("expected non-nil coverage")
	}
	if math.Abs(*got-(2.0/3.0)) > 1e-9 {
		t.Fatalf("got %v, want 2/3", *got)
	}
}

func TestCoverageForMethod_NoOverlap(t *testing.T) {
	prof, _ := ParseCoverProfileReader(strings.NewReader(sampleProfile))
	fc := prof["crap4go/sample.go"]
	// lines 100..110 do not intersect any block
	if got := CoverageForMethod(fc, 100, 110); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
}

func TestCoverageForMethod_NilFileCoverage(t *testing.T) {
	if got := CoverageForMethod(nil, 1, 10); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
}

// sampleLcov exercises every non-statement record type the reader must
// ignore: trace name, function records (FN/FNDA/FNF/FNH), branch records
// (BRDA/BRF/BRH) and the LF/LH summary counters.
const sampleLcov = `TN:sample
SF:crap4go/sample.go
FN:5,30,Sample
FNDA:3,Sample
FNF:1
FNH:1
BRDA:10,0,0,1
BRDA:10,0,1,0
BRF:2
BRH:1
DA:5,3
DA:10,0
DA:12,2
LF:3
LH:2
end_of_record
`

func TestParseLcovProfileReader_BlocksPerDA(t *testing.T) {
	prof, err := ParseLcovProfileReader(strings.NewReader(sampleLcov))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fc, ok := prof["crap4go/sample.go"]
	if !ok {
		t.Fatalf("missing sample.go in profile: %v", prof)
	}
	if len(fc.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(fc.Blocks))
	}
	want := []CoverBlock{
		{StartLine: 5, EndLine: 5, NumStmt: 1, Count: 3},
		{StartLine: 10, EndLine: 10, NumStmt: 1, Count: 0},
		{StartLine: 12, EndLine: 12, NumStmt: 1, Count: 2},
	}
	for i, w := range want {
		if fc.Blocks[i] != w {
			t.Errorf("block[%d] = %+v, want %+v", i, fc.Blocks[i], w)
		}
	}
}

func TestParseLcovProfileReader_DuplicateSFSumsHits(t *testing.T) {
	prof, err := ParseLcovProfileReader(strings.NewReader(
		"SF:x.go\nDA:5,2\nDA:6,0\nend_of_record\nSF:x.go\nDA:5,1\nDA:7,4\nend_of_record\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fc := prof["x.go"]
	want := []CoverBlock{
		{StartLine: 5, EndLine: 5, NumStmt: 1, Count: 3},
		{StartLine: 6, EndLine: 6, NumStmt: 1, Count: 0},
		{StartLine: 7, EndLine: 7, NumStmt: 1, Count: 4},
	}
	if len(fc.Blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(fc.Blocks), len(want), fc.Blocks)
	}
	for i, w := range want {
		if fc.Blocks[i] != w {
			t.Errorf("block[%d] = %+v, want %+v", i, fc.Blocks[i], w)
		}
	}
}

func TestParseLcovProfileReader_CRLF(t *testing.T) {
	prof, err := ParseLcovProfileReader(strings.NewReader(
		strings.ReplaceAll(sampleLcov, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(prof) != 1 || len(prof["crap4go/sample.go"].Blocks) != 3 {
		t.Fatalf("CRLF trace misparsed: %+v", prof)
	}
}

func TestParseLcovProfileReader_Malformed(t *testing.T) {
	if _, err := ParseLcovProfileReader(strings.NewReader("SF:x.go\nnot an lcov line\n")); err == nil {
		t.Error("expected error on malformed line")
	}
	if _, err := ParseLcovProfileReader(strings.NewReader("SF:x.go\nDA:x,y\n")); err == nil {
		t.Error("expected error on non-numeric DA record")
	}
	if _, err := ParseLcovProfileReader(strings.NewReader("SF:x.go\nDA:5\n")); err == nil {
		t.Error("expected error on DA record without hit count")
	}
}

func TestParseLcovProfileReader_DAOutsideSF(t *testing.T) {
	if _, err := ParseLcovProfileReader(strings.NewReader("DA:5,1\n")); err == nil {
		t.Error("expected error on DA record outside SF section")
	}
}

func TestLoadCoverage_ResolvesRelativeLcovPaths(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, "cover.info")
	lcov := "SF:src/sample.go\nDA:5,1\nend_of_record\nSF:/abs/other.go\nDA:2,0\nend_of_record\n"
	if err := os.WriteFile(info, []byte(lcov), 0o644); err != nil {
		t.Fatal(err)
	}
	prof, err := loadCoverage(info, root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if fc, ok := prof[filepath.Join(root, "src", "sample.go")]; !ok || len(fc.Blocks) != 1 {
		t.Errorf("relative SF not resolved against root: %+v", prof)
	}
	if fc, ok := prof["/abs/other.go"]; !ok || len(fc.Blocks) != 1 {
		t.Errorf("absolute SF key not kept as recorded: %+v", prof)
	}

	// The native cover profile still dispatches by extension.
	native := filepath.Join(root, "cover.out")
	if err := os.WriteFile(native, []byte(sampleProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	prof, err = loadCoverage(native, root)
	if err != nil {
		t.Fatalf("load native: %v", err)
	}
	if _, ok := prof["crap4go/sample.go"]; !ok {
		t.Errorf("native profile not parsed: %+v", prof)
	}
}

func TestCoverageForMethod_IgnoresDAOutsideFunction(t *testing.T) {
	prof, err := ParseLcovProfileReader(strings.NewReader("SF:x.go\nDA:1,1\nDA:4,1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := CoverageForMethod(prof["x.go"], 3, 5)
	if got == nil || *got != 1.0 {
		t.Errorf("coverage = %v, want 1.0 (the DA line outside the span must not count)", got)
	}
}
