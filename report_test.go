package main

import (
	"strings"
	"testing"
)

func ptr(v float64) *float64 { return &v }

func TestFormatReport_HeaderAndSeparator(t *testing.T) {
	out := FormatReport(nil, 8.0)
	lines := strings.Split(out, "\n")
	if lines[0] != "CRAP Report" {
		t.Errorf("line 0 = %q, want %q", lines[0], "CRAP Report")
	}
	if lines[1] != "===========" {
		t.Errorf("line 1 = %q, want %q", lines[1], "===========")
	}
	wantHeader := "Method                         File                                  CC    Cov%     CRAP"
	if lines[2] != wantHeader {
		t.Errorf("header = %q\n  want = %q", lines[2], wantHeader)
	}
	if len(lines[3]) != len(wantHeader) || strings.Trim(lines[3], "-") != "" {
		t.Errorf("separator wrong: %q (len %d, want %d)", lines[3], len(lines[3]), len(wantHeader))
	}
}

func TestFormatReport_RowsAndSummary(t *testing.T) {
	metrics := []MethodMetric{
		{MethodName: "lowCov", File: "a.go", StartLine: 10, Complexity: 5, Coverage: ptr(0.45), CrapScore: ptr(18.648)},
		{MethodName: "noData", File: "b.go", StartLine: 20, Complexity: 2, Coverage: nil, CrapScore: nil},
	}
	out := FormatReport(metrics, 8.0)
	lines := strings.Split(out, "\n")

	// numeric desc, N/A last: lowCov row before noData row.
	rowLow := lines[4]
	rowNA := lines[5]
	if !strings.HasPrefix(rowLow, "lowCov") {
		t.Errorf("expected lowCov first, got %q", rowLow)
	}
	if !strings.HasPrefix(rowNA, "noData") {
		t.Errorf("expected noData (N/A) last, got %q", rowNA)
	}
	if !strings.Contains(rowNA, "N/A") {
		t.Errorf("N/A row should show N/A: %q", rowNA)
	}
	if !strings.Contains(rowLow, "18.6") {
		t.Errorf("lowCov row should show 18.6 CRAP: %q", rowLow)
	}

	// summary line uses em-dash, FAILED when max>threshold.
	summary := lines[len(lines)-2]
	if !strings.Contains(summary, "Max CRAP: 18.6 (threshold 8.0) — FAILED") {
		t.Errorf("summary mismatch: %q", summary)
	}
}

func TestFormatReport_PassedVerdict(t *testing.T) {
	metrics := []MethodMetric{
		{MethodName: "ok", File: "a.go", Complexity: 1, Coverage: ptr(1.0), CrapScore: ptr(1.0)},
	}
	out := FormatReport(metrics, 8.0)
	if !strings.Contains(out, "Max CRAP: 1.0 (threshold 8.0) — passed") {
		t.Errorf("expected passed verdict, got:\n%s", out)
	}
}

func TestFormatReport_AllNA_MaxZero(t *testing.T) {
	metrics := []MethodMetric{
		{MethodName: "x", File: "a.go", Complexity: 3, Coverage: nil, CrapScore: nil},
	}
	out := FormatReport(metrics, 8.0)
	if !strings.Contains(out, "Max CRAP: 0.0 (threshold 8.0) — passed") {
		t.Errorf("expected max 0.0, got:\n%s", out)
	}
}

func TestSortMetrics_DescWithNALastAndTieBreak(t *testing.T) {
	metrics := []MethodMetric{
		{MethodName: "na1", File: "z.go", StartLine: 5, CrapScore: nil},
		{MethodName: "na2", File: "a.go", StartLine: 1, CrapScore: nil},
		{MethodName: "big", File: "b.go", StartLine: 9, CrapScore: ptr(20)},
		{MethodName: "small", File: "c.go", StartLine: 2, CrapScore: ptr(5)},
		{MethodName: "tie1", File: "a.go", StartLine: 3, CrapScore: ptr(5)},
		{MethodName: "tie2", File: "a.go", StartLine: 1, CrapScore: ptr(5)},
	}
	SortMetrics(metrics)
	got := make([]string, len(metrics))
	for i, m := range metrics {
		got[i] = m.MethodName
	}
	want := []string{"big", "tie2", "tie1", "small", "na2", "na1"}
	// big=20 first; tie2/tie1 (file a.go, line 1 then 3); small=5 (file c.go); na2/na1 (file a.go, then z.go)
	if !equalStrings(got, want) {
		t.Errorf("order = %v\n want = %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
