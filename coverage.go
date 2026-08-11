package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

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

// coverLineRe matches a single cover-profile data line:
//
//	pkg/path/file.go:startLine.startCol,endLine.endCol numStmt count
//
// This is the same shape emitted by "go test -coverprofile" and parsed by
// golang.org/x/tools/cover (reimplemented here with stdlib only).
var coverLineRe = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

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
		if line == "" || line[0] == '#' {
			continue
		}
		if len(line) >= 5 && line[:5] == "mode:" {
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
