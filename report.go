package main

import (
	"fmt"
	"strings"
)

// FormatReport renders the metrics as the contract's tabular CRAP report:
//
//	CRAP Report
//	===========
//	<header>
//	<separator of dashes>
//	<rows sorted by caller>
//	<blank line>
//	Max CRAP: <max> (threshold <t>) — <FAILED|passed>
//
// threshold is the configured CRAP threshold used for the verdict line. Callers
// must sort metrics before formatting (SortMetrics).
func FormatReport(metrics []MethodMetric, threshold float64) string {
	var b strings.Builder
	b.WriteString("CRAP Report\n")
	b.WriteString("===========\n")
	header := fmt.Sprintf("%-30s %-35s %4s %7s %8s", "Method", "File", "CC", "Cov%", "CRAP")
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("-", len(header)))
	b.WriteByte('\n')
	for _, m := range metrics {
		b.WriteString(formatRow(m))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	max := maxCrap(metrics)
	verdict := "passed"
	if max > threshold {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "Max CRAP: %.1f (threshold %.1f) — %s\n", max, threshold, verdict)
	return b.String()
}

func formatRow(m MethodMetric) string {
	covField := "  N/A "
	if m.Coverage != nil {
		covField = fmt.Sprintf("%5.1f%%", *m.Coverage*100)
	}
	crapField := "     N/A"
	if m.CrapScore != nil {
		crapField = fmt.Sprintf("%8.1f", *m.CrapScore)
	}
	return fmt.Sprintf("%-30s %-35s %4d %7s %8s",
		m.MethodName, m.File, m.Complexity, covField, crapField)
}
