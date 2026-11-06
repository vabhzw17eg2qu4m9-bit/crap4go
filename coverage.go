package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// coverLineRe matches a single cover-profile data line:
//
//	pkg/path/file.go:startLine.startCol,endLine.endCol numStmt count
//
// This is the same shape emitted by "go test -coverprofile" and parsed by
// golang.org/x/tools/cover (reimplemented here with stdlib only).
var coverLineRe = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

// CoverBlock is one coverage profile entry: the half-open source range and the
// statement count for that block. Count > 0 means the block was executed.
type CoverBlock struct {
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
	NumStmt   int
	Count     int
}

// FileCoverage holds all coverage blocks recorded for a single source file.
type FileCoverage struct {
	Blocks []CoverBlock
}

// ParseCoverProfile reads and parses a Go cover profile from path. The first
// line is "mode: <mode>" and is consumed. Returns a map keyed by the profile's
// recorded file path (which may be module-prefixed, e.g. "example.com/pkg/foo.go").
func ParseCoverProfile(path string) (map[string]*FileCoverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseCoverProfileReader(f)
}

// ParseCoverProfileReader parses a cover profile from any io.Reader. Useful for
// tests that supply inline profile text.
func ParseCoverProfileReader(r io.Reader) (map[string]*FileCoverage, error) {
	profile := make(map[string]*FileCoverage)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if isMetaLine(line) {
			continue
		}
		m := coverLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("malformed cover profile line: %q", line)
		}
		path := m[1]
		startLine, _ := strconv.Atoi(m[2])
		startCol, _ := strconv.Atoi(m[3])
		endLine, _ := strconv.Atoi(m[4])
		endCol, _ := strconv.Atoi(m[5])
		numStmt, _ := strconv.Atoi(m[6])
		count, _ := strconv.Atoi(m[7])
		fc := profile[path]
		if fc == nil {
			fc = &FileCoverage{}
			profile[path] = fc
		}
		fc.Blocks = append(fc.Blocks, CoverBlock{
			StartLine: startLine,
			StartCol:  startCol,
			EndLine:   endLine,
			EndCol:    endCol,
			NumStmt:   numStmt,
			Count:     count,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return profile, nil
}

// isMetaLine reports whether line is blank, a comment, or the "mode:" header —
// the non-data lines skipped by ParseCoverProfileReader.
func isMetaLine(line string) bool {
	if line == "" || line[0] == '#' {
		return true
	}
	return len(line) >= 5 && line[:5] == "mode:"
}

// CoverageForMethod aggregates the blocks of fc whose [StartLine, EndLine]
// intersects [startLine, endLine] and returns the covered/total statement
// fraction. Returns nil when no statements fall in range (coverage unknown for
// that method).
func CoverageForMethod(fc *FileCoverage, startLine, endLine int) *float64 {
	if fc == nil {
		return nil
	}
	var covered, total int
	for _, b := range fc.Blocks {
		if b.StartLine > endLine || b.EndLine < startLine {
			continue
		}
		total += b.NumStmt
		if b.Count > 0 {
			covered += b.NumStmt
		}
	}
	if total == 0 {
		return nil
	}
	fraction := float64(covered) / float64(total)
	return &fraction
}

// loadCoverage reads the --coverage profile: a name ending in .info is an
// lcov tracefile, anything else the native Go cover profile. Relative lcov
// SF: paths resolve against root (absolute paths keep the profile's own key);
// an empty root leaves the keys as recorded.
func loadCoverage(path, root string) (map[string]*FileCoverage, error) {
	if !strings.HasSuffix(path, ".info") {
		return ParseCoverProfile(path)
	}
	profile, err := ParseLcovProfile(path)
	if err != nil || root == "" {
		return profile, err
	}
	resolved := make(map[string]*FileCoverage, len(profile))
	for sfPath, fc := range profile {
		if !filepath.IsAbs(sfPath) {
			sfPath = filepath.Join(root, sfPath)
		}
		resolved[sfPath] = fc
	}
	return resolved, nil
}

// ParseLcovProfile reads and parses an lcov tracefile from path. Returns a map
// keyed by the recorded SF: path (relative paths are the caller's to resolve).
func ParseLcovProfile(path string) (map[string]*FileCoverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseLcovProfileReader(f)
}

// ParseLcovProfileReader parses an lcov tracefile from any io.Reader. Each
// `DA:<line>,<hits>` record is one statement on that source line (the block
// spans the single line); repeated SF: sections for one file sum their hits
// per line. FN/FNDA/BRDA/LF/LH records and blank lines are ignored; anything
// else is a parse error. CRLF tracefiles parse (the \r is trimmed before
// matching).
func ParseLcovProfileReader(r io.Reader) (map[string]*FileCoverage, error) {
	hits := make(map[string]map[int]int)
	file := ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if isLcovMetaLine(line) {
			continue
		}
		var err error
		file, err = addLcovRecords(hits, file, line)
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lcovProfile(hits), nil
}

// addLcovRecords consumes one non-meta lcov line: an SF header selects the
// current file, a DA record adds its hits, anything else is malformed.
// Returns the (possibly updated) current file.
func addLcovRecords(hits map[string]map[int]int, file, line string) (string, error) {
	switch {
	case strings.HasPrefix(line, "SF:"):
		file = line[len("SF:"):]
		if hits[file] == nil {
			hits[file] = make(map[int]int)
		}
	case strings.HasPrefix(line, "DA:"):
		if err := addLcovHits(hits, file, line); err != nil {
			return file, err
		}
	default:
		return file, fmt.Errorf("malformed lcov line: %q", line)
	}
	return file, nil
}

// addLcovHits adds one DA record's hits to the current file's line totals.
func addLcovHits(hits map[string]map[int]int, file, line string) error {
	if file == "" {
		return fmt.Errorf("lcov DA record outside SF section: %q", line)
	}
	lineNo, count, err := parseLcovDA(line)
	if err != nil {
		return err
	}
	hits[file][lineNo] += count
	return nil
}

// lcovProfile materializes the accumulated per-line hit totals as one
// single-line block per DA record, in ascending line order.
func lcovProfile(hits map[string]map[int]int) map[string]*FileCoverage {
	profile := make(map[string]*FileCoverage, len(hits))
	for path, lineHits := range hits {
		fc := &FileCoverage{}
		lines := make([]int, 0, len(lineHits))
		for lineNo := range lineHits {
			lines = append(lines, lineNo)
		}
		sort.Ints(lines)
		for _, lineNo := range lines {
			fc.Blocks = append(fc.Blocks, CoverBlock{
				StartLine: lineNo,
				EndLine:   lineNo,
				NumStmt:   1,
				Count:     lineHits[lineNo],
			})
		}
		profile[path] = fc
	}
	return profile
}

// isLcovMetaLine reports whether line carries no statement coverage: blank
// lines, `end_of_record`, the trace name (TN) and the function/branch
// instrumentation records and summary counters (FN*, BR*, LF, LH).
func isLcovMetaLine(line string) bool {
	return line == "" || line == "end_of_record" || strings.HasPrefix(line, "TN:") ||
		strings.HasPrefix(line, "FN") || strings.HasPrefix(line, "BR") ||
		strings.HasPrefix(line, "LF:") || strings.HasPrefix(line, "LH:")
}

// parseLcovDA extracts the line number and hit count from a DA record. A
// third checksum field, when present, is ignored; anything less than
// line,count is malformed.
func parseLcovDA(line string) (lineNo, count int, err error) {
	fields := strings.Split(line[len("DA:"):], ",")
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("malformed lcov DA record: %q", line)
	}
	lineNo, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("malformed lcov DA record: %q", line)
	}
	count, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("malformed lcov DA record: %q", line)
	}
	return lineNo, count, nil
}
