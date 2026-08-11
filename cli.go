package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// usage is the help text printed for --help (and on usage errors).
const usage = `Usage:
  crap4go                          Analyze all .go files under ./
  crap4go --changed                Analyze git-changed .go files
  crap4go <path>...                Analyze explicit files or directories
  crap4go --help                   Print this help message
  crap4go --coverage <path>        Override the coverage profile path (default coverage.out)
  crap4go --threshold <num>        Override the CRAP threshold (default 8.0)
  crap4go --run-tests              Run "go test" before analyzing

Note: Go's flag package stops parsing at the first non-flag argument, so all
flags must appear before positional paths. Both -flag and --flag are accepted.
`

// options holds the parsed, validated CLI configuration.
type options struct {
	changed      bool
	coveragePath string
	threshold    float64
	runTests     bool
	posArgs      []string
}

// Run is the program entry point. It parses args, selects source files, runs
// tests if requested, analyzes, and prints the report. Returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runWithRoot(args, root, stdout, stderr)
}

// parseOptions parses and validates CLI flags. It returns the resolved options
// plus an exit code and an ok flag: when ok is false the caller should return
// code immediately (help printed, or a usage/validation error reported).
func parseOptions(args []string, stdout, stderr io.Writer) (options, int, bool) {
	fs := flag.NewFlagSet("crap4go", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stdout, usage) }

	help := fs.Bool("help", false, "print help and exit")
	changed := fs.Bool("changed", false, "analyze git-changed files only")
	coveragePath := fs.String("coverage", "coverage.out", "coverage profile path")
	threshold := fs.Float64("threshold", 8.0, "CRAP threshold")
	runTests := fs.Bool("run-tests", false, "run go test before analyzing")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return options{}, 0, false
		}
		return options{}, 1, false
	}
	if *help {
		fmt.Fprint(stdout, usage)
		return options{}, 0, false
	}
	if *changed && len(fs.Args()) > 0 {
		fmt.Fprintln(stderr, "--changed cannot be combined with file arguments")
		fmt.Fprint(stdout, usage)
		return options{}, 1, false
	}
	if *threshold <= 0 {
		fmt.Fprintf(stderr, "threshold must be positive, got %g\n", *threshold)
		return options{}, 1, false
	}
	return options{
		changed:      *changed,
		coveragePath: *coveragePath,
		threshold:    *threshold,
		runTests:     *runTests,
		posArgs:      fs.Args(),
	}, 0, true
}

// resolveCoveragePath makes a relative coverage path absolute under root.
func resolveCoveragePath(path, root string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// warnIfCoverageMissing writes a warning when the coverage file is absent.
func warnIfCoverageMissing(resolved, display string, stderr io.Writer) {
	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		fmt.Fprintf(stderr, "Warning: coverage file %s not found. Coverage will be N/A.\n", display)
	}
}

// runWithRoot is Run with an explicit project root, used by tests.
func runWithRoot(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseOptions(args, stdout, stderr)
	if !ok {
		return code
	}

	files, err := selectFiles(opts.changed, opts.posArgs, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to analyze.")
		return 0
	}

	if opts.runTests {
		if err := RunTests(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	covPath := resolveCoveragePath(opts.coveragePath, root)
	warnIfCoverageMissing(covPath, opts.coveragePath, stderr)

	metrics, err := Analyze(files, covPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	SortMetrics(metrics)
	fmt.Fprint(stdout, FormatReport(metrics, opts.threshold))

	if max := maxCrap(metrics); max > opts.threshold {
		fmt.Fprintf(stderr, "CRAP threshold exceeded: %.1f > %.1f\n", max, opts.threshold)
		return 2
	}
	return 0
}

// selectFiles expands the selection per the active mode: --changed delegates
// to git; otherwise explicit positional args are expanded against root.
func selectFiles(changed bool, posArgs []string, root string) ([]string, error) {
	if changed {
		return ChangedFiles(root)
	}
	if len(posArgs) > 0 {
		return ExpandPaths(posArgs, root)
	}
	return FindSourceFiles(root)
}
