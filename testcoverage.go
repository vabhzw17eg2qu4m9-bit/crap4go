package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
)

// defaultMinCoverage is the test-coverage gate default, ported from
// crap4dart's CoverageGateConfig.
const defaultMinCoverage = 80.0

// TestCoverageViolation is the aggregate below-threshold report.
type TestCoverageViolation struct {
	Path    string
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter;
// the violation names the coverage input, so only file-scoped markers on it
// could ever apply.
func (v TestCoverageViolation) ignorePath() string { return v.Path }
func (v TestCoverageViolation) ignoreLine() int    { return 0 }

// RunTestCoverageCommand implements `crap4go test-coverage [--min-percent F]
// [--coverage PATH] [--exclude GLOB]... [paths...]`: it computes the total
// statement-hit ratio over the selected files from the coverage profile —
// the same ratio the CRAP attribution uses, aggregated — and fails below
// --min-percent. Missing or record-less coverage fails closed with exit 1
// (the CRAP command's requireCoverage semantics); excluded files drop out of
// the ratio, and a ratio with no statements left passes (upstream treats a
// found==0 aggregate as 100%).
func RunTestCoverageCommand(args []string, root string, stdout, stderr io.Writer) int {
	minPercent, coveragePath, exclude, paths, code := parseTestCoverageFlags(args, stderr)
	if code != 0 {
		return code
	}
	coverage, err := requireCoverage(resolveCoveragePath(coveragePath, root), root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	files, err := selectFiles(false, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	percent := totalCoverage(coverage, files, root, exclude)
	passed := percent >= minPercent
	comparison := ">="
	if !passed {
		comparison = "<"
		fmt.Fprintf(stdout, violationTextFormat, coveragePath,
			fmt.Sprintf("total coverage %s%% < min %s%%", fmtPercent(percent), fmtPercent(minPercent)))
	}
	fmt.Fprintf(stdout, "coverage %s%% %s %s%%\n", fmtPercent(percent), comparison, fmtPercent(minPercent))
	if !passed {
		return 2
	}
	return 0
}

// parseTestCoverageFlags parses the --min-percent/--coverage/--exclude flags
// plus the positional paths; the returned code is 1 on a usage error
// (already reported).
func parseTestCoverageFlags(args []string, stderr io.Writer) (float64, string, []string, []string, int) {
	fs := flag.NewFlagSet("crap4go test-coverage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	minPercent := fs.Float64("min-percent", defaultMinCoverage, "minimum total coverage percent")
	coveragePath := fs.String("coverage", "coverage.out", "coverage profile path (.info selects lcov)")
	excl := &stringSlice{}
	fs.Var(excl, excludeFlagName, excludeFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 0, "", nil, nil, 1
	}
	return *minPercent, *coveragePath, excl.slice(), fs.Args(), 0
}

// totalCoverage aggregates the statement-hit ratio over the selected files
// that do not match an exclude glob. Files with no coverage entry contribute
// nothing; with no statements left the ratio is 100%.
func totalCoverage(coverage map[string]*FileCoverage, files []string, root string, exclude []string) float64 {
	covered, total := 0, 0
	for _, f := range files {
		if matchesAnyGlob(relPath(root, f), exclude) {
			continue
		}
		fc := lookupCoverage(coverage, f)
		if fc == nil {
			continue
		}
		for _, b := range fc.Blocks {
			total += b.NumStmt
			if b.Count > 0 {
				covered += b.NumStmt
			}
		}
	}
	if total == 0 {
		return 100.0
	}
	return float64(covered) / float64(total) * 100
}

// fmtPercent renders a coverage percentage with one decimal place (ported
// from crap4dart CoverageGate._fmt).
func fmtPercent(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}
