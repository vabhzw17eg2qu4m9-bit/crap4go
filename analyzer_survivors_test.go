package main

import "testing"

func TestSortMetrics_IdenticalKeysKeepInputOrder(t *testing.T) {
	// The comparator must stay irreflexive: entries with identical
	// (score, file, start line) keys keep their input order.
	metrics := []MethodMetric{
		{MethodName: "first", File: "f.go", StartLine: 7, CrapScore: ptr(5)},
		{MethodName: "second", File: "f.go", StartLine: 7, CrapScore: ptr(5)},
	}
	SortMetrics(metrics)
	if metrics[0].MethodName != "first" || metrics[1].MethodName != "second" {
		t.Errorf("identical-key order changed: got %s, %s",
			metrics[0].MethodName, metrics[1].MethodName)
	}
}
