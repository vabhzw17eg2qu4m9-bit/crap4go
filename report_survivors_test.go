package main

import (
	"strings"
	"testing"
)

func TestFormatReport_ThresholdTiePasses(t *testing.T) {
	// max == threshold must not fail the verdict: the comparison is strict.
	metrics := []MethodMetric{
		{MethodName: "atLimit", File: "a.go", CrapScore: ptr(8.0)},
	}
	out := FormatReport(metrics, 8.0)
	if !strings.Contains(out, "Max CRAP: 8.0 (threshold 8.0) — passed") {
		t.Errorf("tie with threshold must pass, got:\n%s", out)
	}
}

func TestFormatReport_CoveragePercentExact(t *testing.T) {
	// Coverage renders as a percent scaled by 100 with exactly one decimal.
	metrics := []MethodMetric{
		{MethodName: "partial", File: "a.go", Complexity: 1, Coverage: ptr(0.829), CrapScore: ptr(1.0)},
		{MethodName: "full", File: "b.go", Complexity: 1, Coverage: ptr(1.0), CrapScore: ptr(1.0)},
	}
	out := FormatReport(metrics, 8.0)
	for _, want := range []string{"82.9%", "100.0%"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in report:\n%s", want, out)
		}
	}
}
