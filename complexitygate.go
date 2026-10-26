package main

import (
	"flag"
	"fmt"
	"go/ast"
	"io"
)

// Complexity-gate defaults, ported from crap4dart's ComplexityGate.
const maxComplexity = 10

// Complexity metric names (ported from crap4dart's ComplexityMetric):
// cyclomatic is the path count that also feeds CRAP, cognitive the
// SonarSource-style nesting-weighted score.
const (
	metricCyclomatic = "cyclomatic"
	metricCognitive  = "cognitive"
)

// ComplexityViolation is one function over its complexity limit.
type ComplexityViolation struct {
	Path    string
	Line    int
	Message string
}

// RunComplexityCommand implements `crap4go complexity [--metric M] [--max N]
// [--count-lambdas=false] [paths...]`: it flags functions whose complexity —
// cyclomatic (default) or cognitive — exceeds --max, prints one line per
// violation plus a summary, and returns exit code 2 iff there are violations
// (ported from crap4dart's complexity gate with the cognitive-metric option).
func RunComplexityCommand(args []string, root string, stdout, stderr io.Writer) int {
	metric, max, lambdas, paths, code := parseComplexityFlags(args, stderr)
	if code != 0 {
		return code
	}
	files, err := selectFiles(false, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, noGoFilesNotice)
		return 0
	}
	violations, checked := CheckComplexity(files, root, metric, max, lambdas)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d functions over their %s limit %d\n", len(violations), checked, metric, max)
		return 2
	}
	fmt.Fprintf(stdout, "%d functions within their %s limit %d\n", checked, metric, max)
	return 0
}

// parseComplexityFlags parses the complexity gate's flags plus the
// positional paths; the returned code is 1 on a usage error (already
// reported to stderr).
func parseComplexityFlags(args []string, stderr io.Writer) (string, int, bool, []string, int) {
	fs := flag.NewFlagSet("crap4go complexity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	metric := fs.String("metric", metricCyclomatic, "complexity metric: cyclomatic or cognitive")
	max := fs.Int("max", maxComplexity, "maximum allowed complexity per function")
	lambdas := fs.Bool("count-lambdas", true, "count branches inside function literals towards the enclosing function")
	if err := fs.Parse(args); err != nil {
		return "", 0, false, nil, 1
	}
	if *metric != metricCyclomatic && *metric != metricCognitive {
		fmt.Fprintf(stderr, "crap4go complexity: invalid --metric %q: expected %q or %q\n", *metric, metricCyclomatic, metricCognitive)
		return "", 0, false, nil, 1
	}
	return *metric, *max, *lambdas, fs.Args(), 0
}

// CheckComplexity evaluates every declared function with a body against max
// under the given metric and returns the violations plus the number of
// functions checked.
func CheckComplexity(files []string, root, metric string, max int, countLambdas bool) ([]ComplexityViolation, int) {
	var violations []ComplexityViolation
	checked := 0
	for _, f := range files {
		checked += complexityFile(f, root, metric, max, countLambdas, &violations)
	}
	return violations, checked
}

// complexityFile scores one file's top-level functions against max, appends
// violations to out, and returns the number of functions checked.
func complexityFile(f, root, metric string, max int, countLambdas bool, out *[]ComplexityViolation) int {
	file, fset, err := parseGoFile(f)
	if err != nil {
		return 0
	}
	checked := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		checked++
		if value := complexityValue(fd.Body, metric, countLambdas); value > max {
			*out = append(*out, ComplexityViolation{
				Path:    relPath(root, f),
				Line:    fset.Position(fd.Pos()).Line,
				Message: fmt.Sprintf("%s %s=%d > max %d", funcDisplayName(fd), complexityName(metric), value, max),
			})
		}
	}
	return checked
}

// complexityValue computes one function body's complexity under metric.
func complexityValue(body *ast.BlockStmt, metric string, countLambdas bool) int {
	if metric == metricCognitive {
		return CognitiveOf(body, countLambdas)
	}
	return ComplexityOf(body, countLambdas)
}

// complexityName is the metric label used in violation messages: CC for the
// cyclomatic path count, cognitive for the SonarSource score (ported from
// upstream MetricCalculator.metricName).
func complexityName(metric string) string {
	if metric == metricCognitive {
		return metricCognitive
	}
	return "CC"
}
