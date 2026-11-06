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
  crap4go --exclude GLOB           Skip files whose project-relative path matches GLOB
                                   (repeatable)
  crap4go --diff-base REF          Analyze only functions with added/changed lines since
                                   git REF (new-file paths; renames use the new path)
  crap4go --strict                 Report 0.0% (instead of N/A) for a selected file without
                                   a coverage entry

Unit-test coverage is required: a missing or empty coverage profile fails
with exit 1 and no report — generate it with
'go test ./... -coverprofile=coverage.out -covermode=atomic', or pass
--run-tests to do it automatically.

Subcommands (must be the first argument):
  crap4go profile [flags] [paths]  Run instrumented tests; report per-method timing
    --name <pattern>               Run only tests matching the pattern (go test -run)
    --threshold <ms>               Exit 2 when a method's total exceeds this (default off)
    --top <N>                      Console rows shown (default 20)
  crap4go file-naming [paths...]   Flag mechanical file names; exit 2 on violations
  crap4go nesting [paths...]       Flag functions nested deeper than 5; exit 2 on violations
  crap4go complexity [--metric cyclomatic|cognitive] [--max N] [--count-lambdas]
                    [paths...]
                                   Flag functions over the complexity limit
                                   (default max 10, cyclomatic); exit 2 on violations
  crap4go class-size [paths...]    Flag types with >25 methods or WMC >80; exit 2
  crap4go weight-of-class [paths...] Flag data-heavy exported structs; exit 2
  crap4go unused-code [paths...]   Flag unexported declarations never referenced; exit 2
  crap4go unused-files [paths...]  Flag packages never imported; exit 2
  crap4go banned-imports [--from GLOB --forbid GLOB --message MSG]... [paths...]
                                   Flag banned imports per from/forbid rule; exit 2
  crap4go duplicates [--threshold N] [--min-tokens N] [--min-lines N]
                    [--exclude GLOB]... [--source PATH]...
                    [--ignore-locals] [--ignore-literals] [paths...]
                                   Flag files over N% duplicated lines; exit 2
  crap4go similarity [--format console|json] [--jaccard F] [--containment F]
                    [--min-shingles N] [--include GLOB]... [--exclude GLOB]...
                    [--check] [dirs...]
                                   Scan dirs for near-duplicate files and print
                                   the clusters; exits 0 (explorer) unless
                                   --check: then exit 2 on violations
  crap4go magic-constants [paths...] Flag magic literals (hex colors, repeats); exit 2
  crap4go test-assertions [paths...] Flag tests with no fail-capable calls; exit 2
  crap4go folder-structure [--max-loose-files N] [--cap PATTERN=N]...
                    [--exclude GLOB]... [dirs...]
                                   Flag dirs with loose .go files; --cap
                                   gives a pattern its own cap (first wins);
                                   exit 2 on violations
  crap4go anon-struct-signatures [--max-fields N] [paths...]
                                   Flag anonymous struct types in function
                                   signatures; exit 2
  crap4go scattered-constants [--min-files N] [--min-length N]
                    [--min-value N] [--aliases] [paths...]
                                   Flag literals appearing in at least N
                                   distinct files (corpus-wide, one violation
                                   per occurrence), hardcoded re-uses of a
                                   named const, and pure value aliases
                                   (--aliases=false keeps them); exit 2
  crap4go mutation [--coverage PATH] [--min-score F] [--max-survivors N]
                    [--timeout-seconds N] [--max-mutants N]
                    [--operators LIST] [--exclude GLOB]... [--cache]
                    [--test-args ARGS] [--diff-base REF] [--scope-tests]
                    [paths...]
                                   Mutation-test covered sites; exit 2 on survivors
  crap4go memory [--format console|json] [--top N] [paths...]
                                   Run tests under GODEBUG=gctrace=1; report per-test
                                   allocation churn and old-gen growth (informational,
                                   always exits 0)
  crap4go reorder [--dry-run] [paths...]
                                   Rewrite files so top-level declarations follow the
                                   canonical order (imports, consts, vars, types, funcs;
                                   stable within kind, comments ride along); --dry-run
                                   writes nothing and exits 2 when anything would change
  crap4go string-hazards [--severity warning|error] [paths...]
                                   Flag escaped anchors in raw regexp sources
                                   and anchors in literal strings.Replace/
                                   Split/Trim patterns; warning severity by
                                   default (exit 0), --severity error exits 2
  crap4go identical-branches [--severity warning|error] [paths...]
                                   Flag the copy-paste-edit family — duplicate
                                   conditions/bodies in one if/else-if chain,
                                   duplicate ||/&& chain operands, absorbed
                                   operands, duplicate call arguments — all by
                                   canonical form; warning severity by
                                   default (exit 0), --severity error exits 2
  crap4go collapsible-flow [--severity warning|error] [paths...]
                                   Flag collapsible control-flow nesting:
                                   if a { if b {} } mergeable into a && b,
                                   redundant else after an always-exiting
                                   then, else-if chains after a terminal
                                   then; warning severity by default
                                   (exit 0), --severity error exits 2
  crap4go skill                    Print the crap4go profiling skill for AI agents

Note: Go's flag package stops parsing at the first non-flag argument, so all
flags must appear before positional paths. Both -flag and --flag are accepted.
`

// Shared gate-subcommand output and flag vocabulary — the copy-pasted
// formats and strings the scattered-constants dogfood consolidated.
const (
	violationLocationFormat = "%s:%d: %s\n"
	violationTextFormat     = "%s: %s\n"
	noGoFilesNotice         = "No Go files to check."
	excludeFlagName         = "exclude"
	excludeFlagUsage        = "glob of project-relative paths to skip (repeatable)"
	thresholdFlagName       = "threshold"
)

// subcommands maps gate subcommand names to their runners; all share the
// (args, root, stdout, stderr) signature and return the exit code.
var subcommands = map[string]func([]string, string, io.Writer, io.Writer) int{
	"file-naming":            RunFileNamingCommand,
	"nesting":                RunNestingCommand,
	"complexity":             RunComplexityCommand,
	"class-size":             RunClassSizeCommand,
	"weight-of-class":        RunWeightOfClassCommand,
	"unused-code":            RunUnusedCodeCommand,
	"unused-files":           RunUnusedFilesCommand,
	"banned-imports":         RunBannedImportsCommand,
	"magic-constants":        RunMagicConstantsCommand,
	"duplicates":             RunDuplicatesCommand,
	"test-assertions":        RunTestAssertionsCommand,
	"folder-structure":       RunFolderStructureCommand,
	"mutation":               RunMutationCommand,
	"similarity":             RunSimilarityCommand,
	"memory":                 RunMemoryCommand,
	"anon-struct-signatures": RunAnonStructSignaturesCommand,
	"scattered-constants":    RunScatteredConstantsCommand,
	"reorder":                RunReorderCommand,
	"string-hazards":         RunStringHazardsCommand,
	"identical-branches":     RunIdenticalBranchesCommand,
	"collapsible-flow":       RunCollapsibleFlowCommand,
}

// options holds the parsed, validated CLI configuration.
type options struct {
	changed      bool
	coveragePath string
	threshold    float64
	runTests     bool
	diffBase     string
	strict       bool
	excludes     []string
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
	threshold := fs.Float64(thresholdFlagName, 8.0, "CRAP threshold")
	runTests := fs.Bool("run-tests", false, "run go test before analyzing")
	strict := fs.Bool("strict", false, "report 0.0% instead of N/A for selected files without coverage entries")
	var excludes stringSlice
	fs.Var(&excludes, excludeFlagName, excludeFlagUsage)
	diffBase := fs.String("diff-base", "", "git `ref`: analyze only functions changed since it")

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
	if code, ok := validateAnalyzeOptions(*changed, fs.Args(), *diffBase, stdout, stderr); !ok {
		return options{}, code, false
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
		diffBase:     *diffBase,
		strict:       *strict,
		excludes:     excludes.slice(),
		posArgs:      fs.Args(),
	}, 0, true
}

// validateAnalyzeOptions reports the analyze command's mutual-exclusion
// usage errors: --changed with positional paths, and --diff-base with
// --changed. ok=false means the usage was already reported; code is the exit
// code to return.
func validateAnalyzeOptions(changed bool, posArgs []string, diffBase string, stdout, stderr io.Writer) (int, bool) {
	if changed && len(posArgs) > 0 {
		fmt.Fprintln(stderr, "--changed cannot be combined with file arguments")
		fmt.Fprint(stdout, usage)
		return 1, false
	}
	if diffBase != "" && changed {
		fmt.Fprintln(stderr, "--diff-base cannot be combined with --changed")
		fmt.Fprint(stdout, usage)
		return 1, false
	}
	return 0, true
}

// resolveCoveragePath makes a relative coverage path absolute under root.
func resolveCoveragePath(path, root string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
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
// optionally run tests, then emit the CRAP report. --exclude drops selected
// files; --diff-base narrows the selection to the functions whose span
// intersects an added or changed line of the ref's diff.
func runAnalyze(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseOptions(args, stdout, stderr)
	if !ok {
		return code
	}
	files, diff, err := selectAnalyzeFiles(opts, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to analyze.")
		return 0
	}
	return runAnalyzeCoverage(opts, files, diff, root, stdout, stderr)
}

// selectAnalyzeFiles resolves the file selection: the active mode (changed,
// explicit paths, or the full walk), minus exclude globs, minus files with no
// added or changed lines under --diff-base. The returned diff is nil without
// --diff-base (a full run).
func selectAnalyzeFiles(opts options, root string) ([]string, map[string]map[int]bool, error) {
	files, err := selectFiles(opts.changed, opts.posArgs, root)
	if err != nil {
		return nil, nil, err
	}
	files = excludeAnalyzeFiles(files, root, opts.excludes)
	diff, err := analyzeDiffFor(opts, root)
	if err != nil {
		return nil, nil, err
	}
	if diff != nil {
		files = restrictToDiffFiles(files, root, diff)
	}
	return files, diff, nil
}

// runAnalyzeCoverage runs the tests when asked, attributes coverage, applies
// the diff's function selection, and emits the report.
func runAnalyzeCoverage(opts options, files []string, diff map[string]map[int]bool, root string, stdout, stderr io.Writer) int {
	if opts.runTests {
		if err := RunTests(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	covPath := resolveCoveragePath(opts.coveragePath, root)
	metrics, err := AnalyzeInRoot(files, covPath, root, opts.strict)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if diff != nil {
		return emitDiffReport(metrics, opts, root, diff, stdout, stderr)
	}
	return emitReport(metrics, opts, stdout, stderr)
}

// emitDiffReport applies the --diff-base function selection and emits the
// report; a selection with no surviving functions prints the empty-selection
// message and exits 0.
func emitDiffReport(metrics []MethodMetric, opts options, root string, diff map[string]map[int]bool, stdout, stderr io.Writer) int {
	metrics = keepDiffFunctions(metrics, root, diff)
	if len(metrics) == 0 {
		fmt.Fprintln(stdout, "No Go files to analyze.")
		return 0
	}
	return emitReport(metrics, opts, stdout, stderr)
}

// emitReport prints the sorted report and enforces the CRAP threshold. It
// returns the process exit code (2 on threshold exceeded, 0 otherwise).
func emitReport(metrics []MethodMetric, opts options, stdout, stderr io.Writer) int {
	SortMetrics(metrics)
	fmt.Fprint(stdout, FormatReport(metrics, opts.threshold))

	if max := maxCrap(metrics); max > opts.threshold {
		fmt.Fprintf(stderr, "CRAP threshold exceeded: %.1f > %.1f\n", max, opts.threshold)
		return 2
	}
	return 0
}

// excludeAnalyzeFiles drops files whose project-relative path matches any
// exclude glob (the gates' convention).
func excludeAnalyzeFiles(files []string, root string, patterns []string) []string {
	if len(patterns) == 0 {
		return files
	}
	var kept []string
	for _, f := range files {
		if !matchesAnyGlob(relPath(root, f), patterns) {
			kept = append(kept, f)
		}
	}
	return kept
}

// analyzeDiffFor maps the added or changed lines of --diff-base; nil without
// one (a full run). The error is the mutation plumbing's exit-1 git error.
func analyzeDiffFor(opts options, root string) (map[string]map[int]bool, error) {
	if opts.diffBase == "" {
		return nil, nil
	}
	return mutationDiffLines(root, opts.diffBase)
}

// restrictToDiffFiles keeps only selected files whose project-relative path
// carries added or changed lines in the diff (new-file paths, so renames
// match under their new name).
func restrictToDiffFiles(files []string, root string, diff map[string]map[int]bool) []string {
	var kept []string
	for _, f := range files {
		if len(diff[relPath(root, f)]) > 0 {
			kept = append(kept, f)
		}
	}
	return kept
}

// keepDiffFunctions retains only the functions whose [StartLine, EndLine]
// span intersects an added or changed line of the diff.
func keepDiffFunctions(metrics []MethodMetric, root string, diff map[string]map[int]bool) []MethodMetric {
	var kept []MethodMetric
	for _, m := range metrics {
		if diffIntersects(diff[relPath(root, m.File)], m.StartLine, m.EndLine) {
			kept = append(kept, m)
		}
	}
	return kept
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
