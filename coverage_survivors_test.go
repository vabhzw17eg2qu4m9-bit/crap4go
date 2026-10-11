package main

import (
	"strings"
	"testing"
)

func TestCoverageForMethod_TouchingBoundariesIntersect(t *testing.T) {
	// A block sharing exactly one line with the method range intersects:
	// the [startLine, endLine] match is inclusive on both ends.
	prof, err := ParseCoverProfileReader(strings.NewReader(`mode: atomic
example.com/x/x.go:3.1,5.2 2 1
example.com/x/x.go:5.3,9.4 2 0
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fc := prof["example.com/x/x.go"]

	// Method 1..5: the second block starts exactly at endLine 5, is
	// unexecuted, and must still count: covered 2 of 4 statements.
	if got := CoverageForMethod(fc, 1, 5); got == nil || *got != 0.5 {
		t.Errorf("method (1,5): got %v, want 0.5", got)
	}

	// Method 5..9: the first block ends exactly at startLine 5 and must
	// still count: covered 2 of 4 statements.
	if got := CoverageForMethod(fc, 5, 9); got == nil || *got != 0.5 {
		t.Errorf("method (5,9): got %v, want 0.5", got)
	}
}

func TestParseCoverProfileReader_BareModeHeaderSkipped(t *testing.T) {
	// A header of exactly "mode:" (length 5) is still the mode line.
	prof, err := ParseCoverProfileReader(strings.NewReader("mode:\ncrap4go/x.go:1.1,2.2 1 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(prof) != 1 {
		t.Fatalf("expected 1 file, got %d", len(prof))
	}
}
