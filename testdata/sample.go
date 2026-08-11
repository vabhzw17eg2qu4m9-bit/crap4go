// Package sample is a fixture for the end-to-end CLI test. Do not modify the
// line layout without regenerating testdata/cover.out.
//
// Expected per-method cyclomatic complexity (see complexity.go rules):
//   - Add:     CC = 1   (base only)
//   - Max:     CC = 2   (base + if)
//   - Grade:   CC = 4   (base + 3 case clauses incl. default)
//   - Compute: CC = 4   (base + for + if + &&)
package sample

func Add(a, b int) int { // line 11
	return a + b
}

func Max(a, b int) int { // line 15
	if a > b {
		return a
	}
	return b
}

func Grade(score int) string { // line 22
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	default:
		return "C"
	}
}

type Calc struct{}

func (c *Calc) Compute(n int) int { // line 35
	sum := 0
	for i := 0; i < n; i++ {
		if i%2 == 0 && i > 0 {
			sum += i
		}
	}
	return sum
}
