package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
)

// similarityCommandFlags, ported from crap4dart 2b21966's similarity
// command + gate: thresholds, glob selection, output format and the
// --check switch that turns the explorer into the enforcing gate.

// defaultSimFormat is the console report; --format json emits the
// machine-readable shape.
const defaultSimFormat = "console"

// similarityFormat selects the explorer output format.
type similarityFormat int

const (
	simFormatConsole similarityFormat = iota
	simFormatJSON
)

// parseSimilarityFormat parses a --format value: "console" or "json".
func parseSimilarityFormat(value string) (similarityFormat, bool) {
	switch value {
	case "console":
		return simFormatConsole, true
	case "json":
		return simFormatJSON, true
	}
	return 0, false
}

// similarityCommandOptions holds the parsed `similarity` subcommand
// configuration.
type similarityCommandOptions struct {
	similarityOptions
	format similarityFormat
	check  bool
}

// RunSimilarityCommand implements `crap4go similarity [--format
// console|json] [--jaccard F] [--containment F] [--min-shingles N]
// [--include GLOB]... [--exclude GLOB]... [--check] [dirs...]`: it scans
// the directories for near-duplicate files and reports the clusters.
//
// The default run is the upstream ad-hoc explorer (console/json report,
// always exits 0); --check is the upstream gate role — one violation
// line per clustered file naming its counterparts and strongest metrics,
// exit code 2 iff there are violations (the port has no check pipeline:
// the yaml `enabled: false` knob becomes this explicit switch).
func RunSimilarityCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseSimilarityFlags(args, stderr)
	if !ok {
		return code
	}
	report := analyzeSimilarity(root, opts.similarityOptions)
	if opts.check {
		return reportSimilarityGate(report, opts.similarityOptions, stdout)
	}
	if opts.format == simFormatJSON {
		printSimilarityJSON(report, opts.similarityOptions, stdout)
	} else {
		printSimilarityConsole(report, opts.similarityOptions, stdout)
	}
	// The explorer is informational: always exit 0 (ported from the
	// upstream similarity command's contract).
	return 0
}

// reportSimilarityGate prints the gate report: one violation per
// clustered file (counterparts + strongest metrics), then the summary.
// Exit code 2 iff there are violations.
func reportSimilarityGate(report SimilarityReport, opts similarityOptions, stdout io.Writer) int {
	if report.Clean() {
		fmt.Fprintf(stdout, "%d files scanned, no similar files (jaccard %s, containment %s)\n",
			report.Scanned, formatSimFloat(opts.jaccard), formatSimFloat(opts.containment))
		return 0
	}
	for _, path := range report.ClusteredPaths() {
		var cluster SimilarityCluster
		for _, c := range report.Clusters {
			if simClusterContains(c, path) {
				cluster = c
				break
			}
		}
		fmt.Fprintf(stdout, violationTextFormat, path, similarityViolationMessage(path, cluster, report))
	}
	fmt.Fprintf(stdout, "%d files in %d similar-file clusters (%d scanned)\n",
		len(report.ClusteredPaths()), len(report.Clusters), report.Scanned)
	return 2
}

// similarityViolationMessage is the violation message for one clustered
// path: its counterparts (first two, then a count) and the strongest
// pair metrics, ported from the upstream gate.
func similarityViolationMessage(path string, cluster SimilarityCluster, report SimilarityReport) string {
	var others []string
	for _, f := range cluster.Files {
		if f != path {
			others = append(others, f)
		}
	}
	named := others
	more := 0
	if len(others) > 2 {
		named = others[:2]
		more = len(others) - 2
	}
	morePart := ""
	if more > 0 {
		morePart = fmt.Sprintf(" (+%d more)", more)
	}
	jac, con, _ := report.MetricsFor(path)
	return fmt.Sprintf("similar to %s%s — near-duplicate content (jaccard %.2f, containment %.2f); reuse the shared source, generate one of them, or delete the stale copy",
		strings.Join(named, ", "), morePart, jac, con)
}

func simClusterContains(c SimilarityCluster, path string) bool {
	for _, f := range c.Files {
		if f == path {
			return true
		}
	}
	return false
}

// printSimilarityConsole prints the explorer console report.
func printSimilarityConsole(report SimilarityReport, opts similarityOptions, stdout io.Writer) {
	fmt.Fprintf(stdout, "similarity: %d files scanned (%d skipped below %d shingles), thresholds jaccard >= %s / containment >= %s\n",
		report.Scanned, report.Skipped, opts.minShingles,
		formatSimFloat(opts.jaccard), formatSimFloat(opts.containment))
	if report.Clean() {
		fmt.Fprintln(stdout, "no similar files")
		return
	}
	for i, cluster := range report.Clusters {
		fmt.Fprintf(stdout, "\ncluster %d — %d files:\n", i+1, len(cluster.Files))
		for _, file := range cluster.Files {
			jac, con, _ := report.MetricsFor(file)
			fmt.Fprintf(stdout, "  %s (jaccard %.2f, containment %.2f)\n", file, jac, con)
		}
	}
	fmt.Fprintf(stdout, "\n%d files in %d clusters\n", len(report.ClusteredPaths()), len(report.Clusters))
}

// similarityJSONThresholds echoes the effective thresholds.
type similarityJSONThresholds struct {
	Jaccard     float64 `json:"jaccard"`
	Containment float64 `json:"containment"`
	MinShingles int     `json:"min_shingles"`
}

// similarityJSONPair is one threshold-crossing pair.
type similarityJSONPair struct {
	A           string  `json:"a"`
	B           string  `json:"b"`
	Jaccard     float64 `json:"jaccard"`
	Containment float64 `json:"containment"`
}

// similarityJSONCluster is one cluster: its sorted member files.
type similarityJSONCluster struct {
	Files []string `json:"files"`
}

// similarityJSON is the JSON report shape (ported from the upstream
// command's JSON output).
type similarityJSON struct {
	Scanned    int                      `json:"scanned"`
	Skipped    int                      `json:"skipped"`
	Thresholds similarityJSONThresholds `json:"thresholds"`
	Pairs      []similarityJSONPair     `json:"pairs"`
	Clusters   []similarityJSONCluster  `json:"clusters"`
}

// printSimilarityJSON prints the JSON report (stdout stays JSON-only).
func printSimilarityJSON(report SimilarityReport, opts similarityOptions, stdout io.Writer) {
	out := similarityJSON{
		Scanned: report.Scanned,
		Skipped: report.Skipped,
		Thresholds: similarityJSONThresholds{
			Jaccard:     simRound4(opts.jaccard),
			Containment: simRound4(opts.containment),
			MinShingles: opts.minShingles,
		},
		Pairs:    []similarityJSONPair{},
		Clusters: []similarityJSONCluster{},
	}
	for _, pair := range report.Pairs {
		out.Pairs = append(out.Pairs, similarityJSONPair{
			A: pair.A, B: pair.B,
			Jaccard:     simRound4(pair.Jaccard),
			Containment: simRound4(pair.Containment),
		})
	}
	for _, cluster := range report.Clusters {
		out.Clusters = append(out.Clusters, similarityJSONCluster{Files: cluster.Files})
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		// Marshaling plain data cannot fail; keep stdout JSON-only anyway.
		return
	}
	fmt.Fprintln(stdout, string(encoded))
}

// simRound4 rounds a metric to four decimals (the upstream JSON
// precision, toStringAsFixed(4)).
func simRound4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// formatSimFloat renders a threshold the way Dart interpolates a num:
// minimal digits (%g), e.g. 0.5, 0.8, 0.95.
func formatSimFloat(v float64) string {
	return fmt.Sprintf("%g", v)
}

// parseSimilarityFlags parses the similarity flags plus the positional
// scan directories. ok=false means a usage error (already reported to
// stderr); the code is the exit code to return.
func parseSimilarityFlags(args []string, stderr io.Writer) (similarityCommandOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go similarity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := similarityCommandOptions{
		similarityOptions: similarityOptions{
			include:     defaultSimInclude,
			exclude:     defaultSimExclude,
			jaccard:     defaultSimJaccard,
			containment: defaultSimContainment,
			minShingles: defaultSimMinShingles,
		},
		format: simFormatConsole,
	}
	fs.Float64Var(&opts.jaccard, "jaccard", defaultSimJaccard, "report pairs with jaccard >= this (0-1)")
	fs.Float64Var(&opts.containment, "containment", defaultSimContainment, "report pairs with containment >= this (0-1)")
	fs.IntVar(&opts.minShingles, "min-shingles", defaultSimMinShingles, "skip files with fewer shingles")
	includes := &stringSlice{}
	excludes := &stringSlice{}
	fs.Var(includes, "include", "glob of files to analyze (repeatable)")
	fs.Var(excludes, excludeFlagName, excludeFlagUsage)
	format := fs.String("format", defaultSimFormat, `output format: "console" or "json"`)
	check := fs.Bool("check", false, "gate mode: one violation per clustered file, exit 2 on violations")
	if err := fs.Parse(args); err != nil {
		return similarityCommandOptions{}, 1, false
	}
	opts.check = *check
	parsedFormat, ok := parseSimilarityFormat(*format)
	if !ok {
		fmt.Fprintln(stderr, `--format must be "console" or "json"`)
		return similarityCommandOptions{}, 1, false
	}
	opts.format = parsedFormat
	if !validateSimilarityOptions(&opts.similarityOptions, stderr) {
		return similarityCommandOptions{}, 1, false
	}
	if len(includes.slice()) > 0 {
		opts.include = includes.slice()
	}
	if len(excludes.slice()) > 0 {
		opts.exclude = excludes.slice()
	}
	opts.dirs = fs.Args()
	if len(opts.dirs) == 0 {
		opts.dirs = []string{"."}
	}
	return opts, 0, true
}

// validateSimilarityOptions enforces the upstream loader's value
// validation: thresholds in [0,1], a positive shingle floor.
func validateSimilarityOptions(opts *similarityOptions, stderr io.Writer) bool {
	if opts.jaccard < 0 || opts.jaccard > 1 {
		fmt.Fprintln(stderr, "--jaccard must be between 0 and 1")
		return false
	}
	if opts.containment < 0 || opts.containment > 1 {
		fmt.Fprintln(stderr, "--containment must be between 0 and 1")
		return false
	}
	if opts.minShingles < 1 {
		fmt.Fprintln(stderr, "--min-shingles must be positive")
		return false
	}
	return true
}
