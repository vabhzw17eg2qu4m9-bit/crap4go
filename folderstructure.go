package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxLooseFiles is the default maximum number of loose logical units allowed
// directly in one directory (ported from crap4dart's folder_structure gate).
const maxLooseFiles = 0

// generatedSuffixes are stripped from a file name (after .go, repeatedly) to
// find its logical stem — the Go generated-sibling set: protobuf (.pb.go,
// .pb.gw.go) and stringer (_string.go) output shares its source file's unit.
var generatedSuffixes = [...]string{".pb.gw", ".pb", "_string"}

// generatedPrefix marks wholly generated files (zz_generated.deepcopy.go and
// friends): every such file in a directory collapses into one unit.
const generatedPrefix = "zz_generated"

// folderCap is one --cap PATTERN=N entry: a directory pattern with its own
// cap (the CLI form of crap4dart's folder_structure `config` map).
type folderCap struct {
	pattern string
	cap     int
}

// folderCheck is one directory to check with its effective cap.
type folderCheck struct {
	dir string
	cap int
}

// FolderStructureViolation is one directory with loose-file sprawl.
type FolderStructureViolation struct {
	Dir     string
	Message string
}

// RunFolderStructureCommand implements `crap4go folder-structure
// [--max-loose-files N] [--cap PATTERN=N]... [--exclude GLOB]... [dirs...]`:
// it flags directories containing more than their cap of logical non-test
// .go units directly (non-recursive) — a flat-file sprawl that should be
// organized into feature packages. Without positional dirs, the module
// root's direct children are checked (Go adaptation of crap4dart's
// folder_structure gate). Counting: generated siblings of one source file
// (foo.go, foo.pb.go, foo_string.go, zz_generated*.go) collapse into one
// logical unit; files matching --exclude don't count; subdirectories are
// the organized form and never count. Positional dirs may be glob patterns
// (internal/*) expanding to every matching directory — hidden
// (dot-prefixed), vendor and build directories are never enumerated. With
// --cap, each pattern selects and caps its own directories (first pattern
// wins on overlap) and positional dirs and --max-loose-files are not
// consulted, mirroring crap4dart's `config` map.
func RunFolderStructureCommand(args []string, root string, stdout, stderr io.Writer) int {
	caps, maxLoose, exclude, paths, code := parseFolderFlags(args, stderr)
	if code != 0 {
		return code
	}
	checks, err := folderChecks(caps, maxLoose, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var violations []FolderStructureViolation
	for _, check := range checks {
		if units := countUnits(root, check.dir, exclude); units > check.cap {
			violations = append(violations, FolderStructureViolation{
				Dir: relPath(root, check.dir),
				Message: fmt.Sprintf("%d loose .go files directly in %s — group them into feature packages (max %d)",
					units, relPath(root, check.dir), check.cap),
			})
		}
	}
	for _, v := range violations {
		fmt.Fprintf(stdout, violationTextFormat, v.Dir, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d directory(ies) with loose-file sprawl\n", len(violations))
		return 2
	}
	fmt.Fprintf(stdout, "%d directories organized into packages\n", len(checks))
	return 0
}

// parseFolderFlags parses the --max-loose-files/--cap/--exclude flags plus
// the positional dirs; the returned code is 1 on a usage error (already
// reported), 0 otherwise.
func parseFolderFlags(args []string, stderr io.Writer) (caps []folderCap, maxLoose int, exclude, paths []string, code int) {
	flags := flag.NewFlagSet("crap4go folder-structure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&maxLoose, "max-loose-files", maxLooseFiles, "max logical units directly in each checked dir")
	capList := &capSlice{}
	flags.Var(capList, "cap", "per-pattern cap PATTERN=N; the pattern selects dirs (repeatable)")
	excl := &stringSlice{}
	flags.Var(excl, excludeFlagName, excludeFlagUsage)
	if err := flags.Parse(args); err != nil {
		return nil, 0, nil, nil, 1
	}
	return capList.caps, maxLoose, excl.slice(), flags.Args(), 0
}

// capSlice is a repeatable --cap PATTERN=N flag value preserving CLI order
// (order decides which pattern wins on overlap).
type capSlice struct {
	caps []folderCap
}

func (c *capSlice) String() string {
	if c == nil {
		return ""
	}
	parts := make([]string, len(c.caps))
	for i, fc := range c.caps {
		parts[i] = fc.pattern + "=" + strconv.Itoa(fc.cap)
	}
	return strings.Join(parts, ",")
}

func (c *capSlice) Set(v string) error {
	pattern, num, ok := strings.Cut(v, "=")
	if !ok || pattern == "" {
		return fmt.Errorf("--cap wants PATTERN=N, got %q", v)
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return fmt.Errorf("--cap wants an integer N in PATTERN=N, got %q", v)
	}
	c.caps = append(c.caps, folderCap{pattern: pattern, cap: n})
	return nil
}

// folderChecks resolves the directories to check with their caps. The caps
// form (--cap given) checks every directory matching a pattern against that
// pattern's cap, the first pattern winning on overlap; the classic form
// checks the positional dirs (default: the module root) against the shared
// maxLoose.
func folderChecks(caps []folderCap, maxLoose int, paths []string, root string) ([]folderCheck, error) {
	if len(caps) > 0 {
		return perPatternChecks(caps, root)
	}
	dirs, err := selectGateDirs(paths, root)
	if err != nil {
		return nil, err
	}
	checks := make([]folderCheck, len(dirs))
	for i, dir := range dirs {
		checks[i] = folderCheck{dir: dir, cap: maxLoose}
	}
	return checks, nil
}

// perPatternChecks maps every directory matching a --cap pattern to that
// pattern's cap; the first pattern mentioning a directory wins on overlap.
func perPatternChecks(caps []folderCap, root string) ([]folderCheck, error) {
	chosen := map[string]int{}
	var dirs []string
	for _, c := range caps {
		matched, err := expandDirArg(c.pattern, root)
		if err != nil {
			return nil, err
		}
		for _, dir := range matched {
			if _, seen := chosen[dir]; !seen {
				chosen[dir] = c.cap
				dirs = append(dirs, dir)
			}
		}
	}
	sort.Strings(dirs)
	checks := make([]folderCheck, len(dirs))
	for i, dir := range dirs {
		checks[i] = folderCheck{dir: dir, cap: chosen[dir]}
	}
	return checks, nil
}

// selectGateDirs returns the existing directories to check: the positional
// args resolved against root, defaulting to the module root itself. Each
// arg is a literal path or a glob pattern (see expandDirArg).
func selectGateDirs(args []string, root string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	var dirs []string
	for _, arg := range args {
		matched, err := expandDirArg(arg, root)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, matched...)
	}
	return dirs, nil
}

// expandDirArg resolves one dirs/caps entry to existing directories: a path
// that exists passes through literally (glob-free behavior unchanged);
// otherwise an entry with glob metacharacters expands to every matching
// directory under root — a pattern matching nothing selects nothing, a
// missing literal path is a usage error.
func expandDirArg(arg, root string) ([]string, error) {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, arg)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", arg)
		}
		return []string{path}, nil
	}
	if !strings.ContainsAny(arg, "*?[") {
		return nil, err
	}
	return globDirs(strings.TrimPrefix(arg, "./"), root)
}

// globDirs returns every directory under root whose project-relative path
// matches the glob pattern, sorted. Hidden (dot-prefixed), vendor and build
// directories are never enumerated — structure gates have no business
// walking .git or vendored trees.
func globDirs(pattern, root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == root {
			return nil
		}
		if skipDirName(d.Name()) {
			return filepath.SkipDir
		}
		if globMatch(pattern, relPath(root, p)) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}

// skipDirName reports whether a directory is never enumerated by glob
// expansion: hidden (dot-prefixed), vendor and build trees.
func skipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "build"
}

// countUnits counts the distinct logical non-test .go units directly inside
// dir (non-recursive): files are grouped by stem, so generated siblings do
// not inflate the count; files matching an exclude glob are dropped;
// files in subdirectories are the organized form and never count.
func countUnits(root, dir string, exclude []string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	stems := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !countableGoFile(e.Name()) {
			continue
		}
		if len(exclude) > 0 && matchesAnyGlob(relPath(root, filepath.Join(dir, e.Name())), exclude) {
			continue
		}
		stems[logicalStem(e.Name())] = true
	}
	return len(stems)
}

// countableGoFile reports whether the file name is a non-test .go file and
// thus participates in the unit count.
func countableGoFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, testFileSuffix)
}

// logicalStem reduces a non-test .go file name to its logical unit:
// zz_generated*-prefixed files are wholly generated and share one unit per
// directory; otherwise generated suffixes (.pb.gw, .pb, _string — protobuf,
// grpc-gateway and stringer output) are stripped repeatedly, so foo.go,
// foo.pb.go, foo.pb.gw.go and foo_string.go are one unit "foo".
func logicalStem(name string) string {
	stem := strings.TrimSuffix(name, ".go")
	if strings.HasPrefix(stem, generatedPrefix) {
		return generatedPrefix
	}
	for changed := true; changed; {
		changed = false
		for _, suffix := range generatedSuffixes {
			if strings.HasSuffix(stem, suffix) {
				stem = strings.TrimSuffix(stem, suffix)
				changed = true
			}
		}
	}
	return stem
}
