package main

import (
	"math"
	"testing"
)

// val dereferences a *float64 for assertion, failing if it is nil.
func val(t *testing.T, got *float64, label string) float64 {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: expected non-nil score", label)
	}
	return *got
}

func floatPtr(v float64) *float64 { return &v }

func TestCrapScore_FullCoverage(t *testing.T) {
	// CC=5, coverage=1.0 -> 25*0 + 5 = 5.0
	got := CrapScore(5, floatPtr(1.0))
	if v := val(t, got, "full-coverage"); v != 5.0 {
		t.Fatalf("got %v, want 5.0", v)
	}
}

func TestCrapScore_ZeroCoverage(t *testing.T) {
	// CC=5, coverage=0.0 -> 25*1 + 5 = 30.0
	got := CrapScore(5, floatPtr(0.0))
	if v := val(t, got, "zero-coverage"); v != 30.0 {
		t.Fatalf("got %v, want 30.0", v)
	}
}

func TestCrapScore_PartialCoverage(t *testing.T) {
	// CC=8, coverage=0.45 -> 64*(0.55)^3 + 8 = 18.648 (within 0.01)
	got := CrapScore(8, floatPtr(0.45))
	if v := val(t, got, "partial-coverage"); math.Abs(v-18.648) > 0.01 {
		t.Fatalf("got %v, want ~18.648", v)
	}
}

func TestCrapScore_NilCoverage(t *testing.T) {
	// CC=3, coverage=nil -> nil
	if got := CrapScore(3, nil); got != nil {
		t.Fatalf("got %v, want nil", *got)
	}
}
