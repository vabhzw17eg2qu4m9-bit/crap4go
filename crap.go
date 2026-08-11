package main

// MethodDescriptor describes a single Go function or method discovered by
// parsing source. StartLine and EndLine are 1-based, inclusive.
type MethodDescriptor struct {
	Name       string
	StartLine  int
	EndLine    int
	Complexity int
}

// MethodMetric is the analyzed result for one method: its complexity plus
// optionally its coverage fraction and CRAP score. A nil Coverage or CrapScore
// pointer means the value is unavailable (reported as "N/A").
type MethodMetric struct {
	MethodName string
	File       string
	StartLine  int
	Complexity int
	Coverage   *float64
	CrapScore  *float64
}

// CrapScore implements the CRAP metric:
//
//	CRAP = CC^2 * (1 - coverage)^3 + CC
//
// coverage is a fraction in [0.0, 1.0]. A nil coverage yields a nil score
// (CRAP is unknown when coverage is unknown). Verified edge cases:
//
//	CC=5, coverage=1.0  -> 5.0
//	CC=5, coverage=0.0  -> 30.0
//	CC=8, coverage=0.45 -> 18.648
//	CC=3, coverage=nil  -> nil
func CrapScore(cc int, coverage *float64) *float64 {
	if coverage == nil {
		return nil
	}
	c := float64(cc)
	uncovered := 1.0 - *coverage
	score := c*c*uncovered*uncovered*uncovered + c
	return &score
}

// maxCrap returns the largest numeric CRAP score in metrics, or 0.0 when there
// are none (all N/A). Nil scores are ignored.
func maxCrap(metrics []MethodMetric) float64 {
	max := 0.0
	for _, m := range metrics {
		if m.CrapScore != nil && *m.CrapScore > max {
			max = *m.CrapScore
		}
	}
	return max
}
