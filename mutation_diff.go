package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// mutationDiffLines maps every file changed between ref and the working tree
// to the set of its added/changed lines in the NEW file, from
// `git diff --unified=0 <ref>` (fixed argv, no shell). Port of upstream
// crap4dart 8228585's DiffLineMap produced by the --diff/--diff-base plumbing.
func mutationDiffLines(root, ref string) (map[string]map[int]bool, error) {
	cmd := exec.Command("git", "-C", root, "diff", "--unified=0", ref)
	out, err := cmd.Output()
	if err != nil {
		message := err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			message = strings.TrimSpace(string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git diff %s: %s", ref, message)
	}
	return parseDiffLines(string(out)), nil
}

// parseDiffLines parses a unified diff: the `+++ b/<path>` header (the line
// right after `--- `) selects the file, each context-free `@@ ... @@` hunk
// header contributes its +side line range. Deleted files (/dev/null), binary
// files and pure-removal hunks contribute nothing.
func parseDiffLines(diff string) map[string]map[int]bool {
	changed := map[string]map[int]bool{}
	file := ""
	afterMinusHeader := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			afterMinusHeader = true
		case isDiffFileHeader(line, afterMinusHeader):
			afterMinusHeader = false
			file = diffHeaderPath(line, changed)
		case strings.HasPrefix(line, "@@ "):
			recordDiffHunk(changed, file, line)
		default:
			afterMinusHeader = false
		}
	}
	return changed
}

// isDiffFileHeader reports whether line is the `+++ b/<path>` header right
// after a `--- ` header — an added content line can start with `+++ ` too.
func isDiffFileHeader(line string, afterMinusHeader bool) bool {
	return afterMinusHeader && strings.HasPrefix(line, "+++ ")
}

// diffHeaderPath selects the file a `+++ ` header names: a deleted file
// (/dev/null) has no new lines and resets the current file; every other
// header starts an (initially empty) changed-line set, so mode-only diffs
// carry no changed lines.
func diffHeaderPath(line string, changed map[string]map[int]bool) string {
	path := strings.TrimSpace(line[4:])
	if path == "/dev/null" {
		return ""
	}
	file := strings.TrimPrefix(path, "b/")
	changed[file] = map[int]bool{}
	return file
}

// recordDiffHunk adds a hunk's +side line range to the file's changed lines.
func recordDiffHunk(changed map[string]map[int]bool, file, header string) {
	if file == "" {
		return // no current file: nothing to attribute the range to
	}
	start, count, ok := parseDiffHunk(header)
	if !ok {
		return
	}
	for line := start; line < start+count; line++ {
		changed[file][line] = true
	}
}

// parseDiffHunk extracts the +side range of a unified hunk header:
// `@@ -l,s +l,c @@` → start l, count c.
func parseDiffHunk(header string) (start, count int, ok bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, false
	}
	return parseDiffRange(strings.TrimPrefix(fields[2], "+"))
}

// parseDiffRange parses the +side range `l[,c]` of a hunk header: an omitted
// count means one line; a zero count (pure removals) is not a changed line.
func parseDiffRange(rangeText string) (start, count int, ok bool) {
	startText, countText, hasCount := strings.Cut(rangeText, ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 1 {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(countText); err != nil || count < 1 {
			return 0, 0, false
		}
	}
	return start, count, true
}

// mutationInDiff reports whether site's containing method intersects the
// changed lines (method-level scoping: a site runs only when its method
// intersects the diff), falling back to the site's own line when it sits
// outside every method. A nil diff (no --diff-base) keeps everything.
func mutationInDiff(diff map[string]map[int]bool, rel string, site MutationSite, methods []MethodDescriptor) bool {
	if diff == nil {
		return true
	}
	if m := mutationMethodAt(methods, site.Line); m != nil {
		return diffIntersects(diff[rel], m.StartLine, m.EndLine)
	}
	return diffIntersects(diff[rel], site.Line, site.Line)
}

// diffIntersects reports whether any line in [start, end] changed.
func diffIntersects(lines map[int]bool, start, end int) bool {
	for line := start; line <= end; line++ {
		if lines[line] {
			return true
		}
	}
	return false
}

// mutationScopedTestArgs derives the scoped test invocation for a mutated
// file from the Go layout convention: tests live in-package next to the
// source (dir/base.go → dir/base_test.go), so the narrowest faithful
// invocation is the file's own package (`go test ./dir`). Returns nil when
// no candidate test file exists — the caller falls back to the full
// invocation (--test-args).
func mutationScopedTestArgs(root, file string) []string {
	rel := relPath(root, file)
	dir := filepath.Dir(rel)
	candidate := filepath.Join(dir, strings.TrimSuffix(filepath.Base(rel), ".go")+testFileSuffix)
	if _, err := os.Stat(filepath.Join(root, candidate)); err != nil {
		return nil
	}
	if dir == "." {
		return []string{goTestSubcommand, "."}
	}
	return []string{goTestSubcommand, "./" + filepath.ToSlash(dir)}
}
