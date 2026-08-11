package main

import (
	"math"
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
