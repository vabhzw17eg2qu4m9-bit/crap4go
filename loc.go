package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
)

// defaultMaxLines is the loc gate default, ported from crap4dart's
// LocGateConfig.
const defaultMaxLines = 800

// LocViolation is one file over the line limit.
type LocViolation struct {
	Path    string
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter;
// loc violations carry no line, so only file-scoped markers suppress them.
func (v LocViolation) ignorePath() string { return v.Path }
func (v LocViolation) ignoreLine() int    { return 0 }

// RunLocCommand implements `crap4go loc [--max-lines N] [--exclude GLOB]...
// [paths...]`: it flags files longer than --max-lines, prints one line per
// violation plus a summary, and returns exit code 2 iff there are violations
// (ported from crap4dart's loc gate with the config flattened to flags).
// Selection defaults to the normal analyze rules.
func RunLocCommand(args []string, root string, stdout, stderr io.Writer) int {
	maxLines, exclude, paths, code := parseLocFlags(args, stderr)
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
	violations, checked := CheckLoc(files, root, maxLines, exclude)
	violations = filterIgnored("loc", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationTextFormat, v.Path, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d files over their limit\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d files within %d lines\n", checked, maxLines)
	return 0
}

// parseLocFlags parses the --max-lines/--exclude flags plus the positional
// paths; the returned code is 1 on a usage error (already reported).
func parseLocFlags(args []string, stderr io.Writer) (int, []string, []string, int) {
	fs := flag.NewFlagSet("crap4go loc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxLines := fs.Int("max-lines", defaultMaxLines, "max lines per file")
	excl := &stringSlice{}
	fs.Var(excl, excludeFlagName, excludeFlagUsage)
	if err := fs.Parse(args); err != nil {
		return 0, nil, nil, 1
	}
	return *maxLines, excl.slice(), fs.Args(), 0
}

// CheckLoc evaluates every file that does not match an exclude glob and
// returns the over-limit violations plus the number of files checked.
func CheckLoc(files []string, root string, maxLines int, exclude []string) ([]LocViolation, int) {
	var violations []LocViolation
	checked := 0
	for _, f := range files {
		rel := relPath(root, f)
		if matchesAnyGlob(rel, exclude) {
			continue
		}
		checked++
		lines, err := countLines(f)
		if err != nil {
			continue
		}
		if lines > maxLines {
			violations = append(violations, LocViolation{
				Path:    rel,
				Message: fmt.Sprintf("%d lines > max %d", lines, maxLines),
			})
		}
	}
	return violations, checked
}

// countLines counts a file's lines: a trailing newline terminates the last
// line (ported from crap4dart LocGate._lineCount).
func countLines(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(content) == 0 {
		return 0, nil
	}
	newlines := bytes.Count(content, []byte("\n"))
	if content[len(content)-1] == '\n' {
		return newlines, nil
	}
	return newlines + 1, nil
}
