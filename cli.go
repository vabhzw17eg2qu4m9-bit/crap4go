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

Subcommands (must be the first argument):
  crap4go profile [flags] [paths]  Run instrumented tests; report per-method timing
    --name <pattern>               Run only tests matching the pattern (go test -run)
    --threshold <ms>               Exit 2 when a method's total exceeds this (default off)
    --top <N>                      Console rows shown (default 20)
  crap4go file-naming [paths...]   Flag mechanical file names; exit 2 on violations
  crap4go nesting [paths...]       Flag functions nested deeper than 5; exit 2 on violations
  crap4go class-size [paths...]    Flag types with >25 methods or WMC >80; exit 2
  crap4go weight-of-class [paths...] Flag data-heavy exported structs; exit 2
  crap4go unused-code [paths...]   Flag unexported declarations never referenced; exit 2
  crap4go unused-files [paths...]  Flag packages never imported; exit 2
  crap4go banned-imports [--from GLOB --forbid GLOB --message MSG]... [paths...]
                                   Flag banned imports per from/forbid rule; exit 2
  crap4go magic-constants [paths...] Flag magic literals (hex colors, repeats); exit 2
  crap4go test-assertions [paths...] Flag tests with no fail-capable calls; exit 2
  crap4go folder-structure [dirs...] Flag dirs with loose .go files; exit 2
  crap4go skill                    Print the crap4go profiling skill for AI agents

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

// warnIfCoverageMissing writes a warning when the coverage file is absent,
// with a hint naming the port's own coverage generation command (ported
// from crap4dart 0.8.7).
func warnIfCoverageMissing(resolved, display string, stderr io.Writer) {
	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		fmt.Fprintf(stderr, "Warning: coverage file %s not found. Coverage will be N/A.\n", display)
		fmt.Fprintln(stderr, "Hint: generate coverage first — `go test ./... -coverprofile=coverage.out -covermode=atomic`, or pass --run-tests to do it automatically.")
	}
}

// runWithRoot is Run with an explicit project root, used by tests. The first
// argument, when it exactly matches a subcommand name (profile, skill, or a
// gate from the subcommands table), dispatches to that subcommand; anything
// else — flags, paths — takes the analyze path, byte-for-byte as before the
// subcommands existed.
func runWithRoot(args []string, root string, stdout, stderr io.Writer) int {
	if code, handled := runSubcommand(args, root, stdout, stderr); handled {
		return code
	}
	return runAnalyze(args, root, stdout, stderr)
}

// subcommands maps gate subcommand names to their runners; all share the
// (args, root, stdout, stderr) signature and return the exit code.
var subcommands = map[string]func([]string, string, io.Writer, io.Writer) int{
	"file-naming":      RunFileNamingCommand,
	"nesting":          RunNestingCommand,
	"class-size":       RunClassSizeCommand,
	"weight-of-class":  RunWeightOfClassCommand,
	"unused-code":      RunUnusedCodeCommand,
	"unused-files":     RunUnusedFilesCommand,
	"banned-imports":   RunBannedImportsCommand,
	"magic-constants":  RunMagicConstantsCommand,
	"test-assertions":  RunTestAssertionsCommand,
	"folder-structure": RunFolderStructureCommand,
}

// runSubcommand executes a subcommand when args[0] exactly matches a known
// name (profile, skill, or a gate from the subcommands table); otherwise it
// reports not handled.
func runSubcommand(args []string, root string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	if cmd, ok := subcommands[args[0]]; ok {
		return cmd(args[1:], root, stdout, stderr), true
	}
	switch args[0] {
	case "profile":
		return RunProfileCommand(args[1:], root, stdout, stderr), true
	case "skill":
		return RunSkillCommand(stdout), true
	}
	return 0, false
}

// runAnalyze is the original analyze flow: parse flags, select source files,
// optionally run tests, then emit the CRAP report.
func runAnalyze(args []string, root string, stdout, stderr io.Writer) int {
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
	return emitReport(files, covPath, opts, stdout, stderr)
}

// emitReport analyzes files, prints the sorted report, and enforces the CRAP
// threshold. It returns the process exit code (1 on analyze error, 2 on
// threshold exceeded, 0 otherwise).
func emitReport(files []string, covPath string, opts options, stdout, stderr io.Writer) int {
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
