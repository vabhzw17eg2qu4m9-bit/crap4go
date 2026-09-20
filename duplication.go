package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Duplication gate defaults (ported from crap4dart §11.11 + dc64e9c).
const (
	defaultDupThreshold = 1.0
	defaultDupMinTokens = 50
	defaultDupMinLines  = 5
)

// defaultDupExcludes mirrors the port's standard source exclusions (the
// same set FindSourceFiles skips: test files and vendor/), applied on top
// of the scan set so --source additions follow the same rules.
var defaultDupExcludes = []string{"**/*_test.go", "vendor/**"}

// dupToken is one normalized token: its lexeme and source line. Comments
// and auto-inserted semicolons never enter the stream; everything else
// keeps its lexeme, so copies with different whitespace or comments still
// match.
type dupToken struct {
	value string
	line  int
	dup   bool
}

// dupFile is one file's token stream plus its total source line count.
type dupFile struct {
	path       string
	tokens     []dupToken
	totalLines int
}

// DuplicationViolation is one file whose duplicated line percentage
// exceeds the threshold.
type DuplicationViolation struct {
	Path    string
	Line    int
	Message string
}

// duplicationResult aggregates the scan: violations, the number of
// tokenized files and the duplicated-line percentage across all of them.
type duplicationResult struct {
	violations []DuplicationViolation
	files      int
	percent    float64
}

// dupOptions holds the parsed `duplicates` subcommand configuration.
type dupOptions struct {
	threshold float64
	minTokens int
	minLines  int
	excludes  []string
	sources   []string
	paths     []string
}

// RunDuplicatesCommand implements `crap4go duplicates [--threshold N]
// [--min-tokens N] [--min-lines N] [--exclude GLOB]... [--source PATH]...
// [paths...]`: it tokenizes every scanned file, marks sliding token
// windows that appear at least twice (>= min-tokens tokens, spanning >=
// min-lines lines) within or across files, and flags files whose
// duplicated line percentage exceeds --threshold. Exit code 2 iff there
// are violations.
func RunDuplicatesCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseDuplicatesFlags(args, stderr)
	if !ok {
		return code
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	files = unionDupSources(files, opts.sources, root)
	files = excludeDupFiles(files, root, opts.excludes)
	result, scanned := CheckDuplicates(files, root, opts)
	if scanned == 0 {
		fmt.Fprintln(stdout, "no files with enough tokens")
		return 0
	}
	for _, v := range result.violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(result.violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d files over %g%% duplication\n", len(result.violations), scanned, opts.threshold)
		return 2
	}
	fmt.Fprintf(stdout, "%d files, %.2f%% duplicated lines\n", scanned, result.percent)
	return 0
}

// parseDuplicatesFlags parses the duplicates flags plus positional paths.
// ok=false means a usage error (already reported to stderr) or help
// printed; the code is the exit code to return.
func parseDuplicatesFlags(args []string, stderr io.Writer) (dupOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go duplicates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := dupOptions{
		threshold: defaultDupThreshold,
		minTokens: defaultDupMinTokens,
		minLines:  defaultDupMinLines,
	}
	fs.Float64Var(&opts.threshold, "threshold", defaultDupThreshold, "flag files over this % of duplicated lines")
	fs.IntVar(&opts.minTokens, "min-tokens", defaultDupMinTokens, "minimum tokens in a duplicated block")
	fs.IntVar(&opts.minLines, "min-lines", defaultDupMinLines, "minimum source lines in a duplicated block")
	excludes := &stringSlice{}
	sources := &stringSlice{}
	fs.Var(excludes, "exclude", "glob of project-relative paths to skip (repeatable)")
	fs.Var(sources, "source", "extra file or directory unioned into the scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return dupOptions{}, 1, false
	}
	if opts.minTokens < 1 || opts.minLines < 1 {
		fmt.Fprintln(stderr, "--min-tokens and --min-lines must be positive")
		return dupOptions{}, 1, false
	}
	opts.excludes = excludes.slice()
	if len(opts.excludes) == 0 {
		opts.excludes = defaultDupExcludes
	}
	opts.sources = sources.slice()
	opts.paths = fs.Args()
	return opts, 0, true
}

// unionDupSources resolves each --source path against root and unions the
// results into files: directories are scanned recursively for Go source
// (the standard selection rules), plain files are taken directly when they
// have the .go extension, missing paths are skipped silently. The union is
// deduped and sorted.
func unionDupSources(files []string, sources []string, root string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
	}
	for _, source := range sources {
		abs := source
		if !filepath.IsAbs(source) {
			abs = filepath.Join(root, source)
		}
		for _, f := range expandDupSource(abs) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files
}

// expandDupSource resolves one --source path: a directory is walked for Go
// source via FindSourceFiles, a file is kept when it has the .go
// extension, and anything unreadable yields nothing (skipped silently).
func expandDupSource(abs string) []string {
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		files, err := FindSourceFiles(abs)
		if err != nil {
			return nil
		}
		return files
	}
	if strings.HasSuffix(abs, ".go") {
		return []string{abs}
	}
	return nil
}

// excludeDupFiles drops files whose project-relative path matches any
// exclude glob.
func excludeDupFiles(files []string, root string, patterns []string) []string {
	var kept []string
	for _, f := range files {
		if !matchesAnyGlob(relPath(root, f), patterns) {
			kept = append(kept, f)
		}
	}
	return kept
}

// matchesAnyGlob reports whether name matches any of the glob patterns.
func matchesAnyGlob(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, name) {
			return true
		}
	}
	return false
}

// CheckDuplicates tokenizes the file set, detects duplicated token windows
// and builds the result. It returns the result plus the number of files in
// the scan (those with at least minTokens tokens); files with fewer are
// skipped from the scan entirely.
func CheckDuplicates(files []string, root string, opts dupOptions) (duplicationResult, int) {
	scanned := scanDupFiles(files, opts.minTokens)
	if len(scanned) == 0 {
		return duplicationResult{}, 0
	}
	detectDuplicates(scanned, opts.minTokens, opts.minLines)
	return buildDuplicationResult(scanned, root, opts.threshold), len(scanned)
}

// scanDupFiles reads and tokenizes every file, keeping those with at least
// minTokens tokens (unreadable files are skipped).
func scanDupFiles(files []string, minTokens int) []dupFile {
	var scanned []dupFile
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		tokens := tokenizeGo(src)
		if len(tokens) < minTokens {
			continue
		}
		scanned = append(scanned, dupFile{path: path, tokens: tokens, totalLines: dupTotalLines(src)})
	}
	return scanned
}

// tokenizeGo scans src into normalized tokens with go/scanner: comments
// are skipped by the default scan mode, auto-inserted semicolons
// (SEMICOLON with literal "\n") are dropped, real semicolons and all other
// tokens keep their lexeme.
func tokenizeGo(src []byte) []dupToken {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil /* errors ignored */, 0)
	var tokens []dupToken
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return tokens
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		value := tok.String()
		if lit != "" {
			value = lit
		}
		tokens = append(tokens, dupToken{value: value, line: file.Line(pos)})
	}
}

// dupTotalLines counts source lines the way the upstream gate does: a
// trailing newline does not start a new line; empty content has none.
func dupTotalLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := bytes.Count(content, []byte("\n"))
	if !bytes.HasSuffix(content, []byte("\n")) {
		n++
	}
	return n
}

// dupPos is a token window's position inside the scan's file list.
type dupPos struct {
	file  int
	token int
}

// dupHashBase is the Rabin-Karp rolling base (upstream's 2^64/φ constant).
const dupHashBase = 0x9e3779b97f4a7c15

// detectDuplicates indexes every valid minTokens-token window of every
// file under a rolling hash, then marks the tokens of all windows whose
// hash occurred at least twice — within or across files.
func detectDuplicates(files []dupFile, minTokens, minLines int) {
	occurrences := map[uint64][]dupPos{}
	for i := range files {
		indexDupFile(&files[i], i, minTokens, minLines, occurrences)
	}
	for _, positions := range occurrences {
		if len(positions) < 2 {
			continue
		}
		for _, pos := range positions {
			tokens := files[pos.file].tokens
			for i := pos.token; i < pos.token+minTokens && i < len(tokens); i++ {
				tokens[i].dup = true
			}
		}
	}
}

// indexDupFile slides a Rabin-Karp window of minTokens token codes over
// the file and records each window that spans at least minLines lines.
// uint64 arithmetic wraps naturally, giving the mod-2^64 semantics.
func indexDupFile(f *dupFile, fileIdx, minTokens, minLines int, occurrences map[uint64][]dupPos) {
	n := len(f.tokens)
	if n < minTokens {
		return
	}
	codes := make([]uint64, n)
	lines := make([]int, n)
	for i, t := range f.tokens {
		codes[i] = dupHash(t.value)
		lines[i] = t.line
	}
	pow := powDupBase(minTokens - 1)
	var hash uint64
	for i := range minTokens {
		hash = hash*dupHashBase + codes[i]
	}
	recordDupWindow(occurrences, hash, fileIdx, 0, lines, minTokens, minLines)
	for start := 1; start+minTokens <= n; start++ {
		hash -= codes[start-1] * pow
		hash = hash*dupHashBase + codes[start+minTokens-1]
		recordDupWindow(occurrences, hash, fileIdx, start, lines, minTokens, minLines)
	}
}

// recordDupWindow indexes a window only when it spans at least minLines
// source lines.
func recordDupWindow(occurrences map[uint64][]dupPos, hash uint64, fileIdx, start int, lines []int, minTokens, minLines int) {
	if lines[start+minTokens-1]-lines[start]+1 < minLines {
		return
	}
	occurrences[hash] = append(occurrences[hash], dupPos{file: fileIdx, token: start})
}

// powDupBase returns dupHashBase^exp via binary exponentiation (uint64
// overflow supplies the mod 2^64).
func powDupBase(exp int) uint64 {
	result, base := uint64(1), uint64(dupHashBase)
	for exp > 0 {
		if exp&1 == 1 {
			result *= base
		}
		base *= base
		exp >>= 1
	}
	return result
}

// dupHash is FNV-1a: a stable per-lexeme code for the rolling hash.
func dupHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range []byte(s) {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

// buildDuplicationResult computes each file's duplicated line percentage
// and the scan-wide aggregate. A file violates when its percentage is
// strictly over the threshold; the violation line is the first duplicated
// line.
func buildDuplicationResult(files []dupFile, root string, threshold float64) duplicationResult {
	result := duplicationResult{files: len(files)}
	dupLines, totalLines := 0, 0
	for _, f := range files {
		dupSet := dupLineSet(f)
		totalLines += f.totalLines
		dupLines += len(dupSet)
		if v, ok := dupViolation(f, len(dupSet), root, threshold); ok {
			result.violations = append(result.violations, v)
		}
	}
	if totalLines > 0 {
		result.percent = float64(dupLines) / float64(totalLines) * 100
	}
	return result
}

// dupLineSet collects the distinct source lines holding duplicated tokens.
func dupLineSet(f dupFile) map[int]bool {
	set := map[int]bool{}
	for _, t := range f.tokens {
		if t.dup {
			set[t.line] = true
		}
	}
	return set
}

// dupViolation builds the file's violation when it is over the threshold.
func dupViolation(f dupFile, dupLines int, root string, threshold float64) (DuplicationViolation, bool) {
	if f.totalLines == 0 || dupLines == 0 {
		return DuplicationViolation{}, false
	}
	percent := float64(dupLines) / float64(f.totalLines) * 100
	if percent <= threshold {
		return DuplicationViolation{}, false
	}
	return DuplicationViolation{
		Path:    relPath(root, f.path),
		Line:    minDupLine(f),
		Message: fmt.Sprintf("%.2f%% duplicated lines > %g%%", percent, threshold),
	}, true
}

// minDupLine returns the first line holding a duplicated token.
func minDupLine(f dupFile) int {
	first := 0
	for line := range dupLineSet(f) {
		if first == 0 || line < first {
			first = line
		}
	}
	return first
}
