package main

import (
	"os"
	"path/filepath"
	"sort"
)

// Analyze reads each Go source file, extracts methods, attributes coverage
// from coveragePath, and computes CRAP scores. If coveragePath does not exist,
// coverage is treated as unknown for every method (no error returned). Any
// other coverage-read or source-parse failure is returned as an error.
func Analyze(filePaths []string, coveragePath string) ([]MethodMetric, error) {
	coverage, err := ParseCoverProfile(coveragePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	metrics := make([]MethodMetric, 0, len(filePaths))
	for _, path := range filePaths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		methods, err := ExtractMethods(path, src)
		if err != nil {
			return nil, err
		}
		fc := lookupCoverage(coverage, path)
		for _, m := range methods {
			cov := CoverageForMethod(fc, m.StartLine, m.EndLine)
			metrics = append(metrics, MethodMetric{
				MethodName: m.Name,
				File:       path,
				StartLine:  m.StartLine,
				Complexity: m.Complexity,
				Coverage:   cov,
				CrapScore:  CrapScore(m.Complexity, cov),
			})
		}
	}
	return metrics, nil
}

// lookupCoverage finds the coverage entry for a source file. Cover-profile
// paths are module-prefixed (e.g. "example.com/pkg/foo.go"); match first by
// exact path, then by basename, then by trailing path segment.
func lookupCoverage(coverage map[string]*FileCoverage, sourcePath string) *FileCoverage {
	if coverage == nil {
		return nil
	}
	if fc, ok := coverage[sourcePath]; ok {
		return fc
	}
	base := filepath.Base(sourcePath)
	for profilePath, fc := range coverage {
		if filepath.Base(profilePath) == base {
			return fc
		}
	}
	return nil
}

// SortMetrics orders metrics by CRAP score descending with N/A entries last.
// Ties break by File ascending, then StartLine ascending, for stable reports.
func SortMetrics(m []MethodMetric) {
	sort.SliceStable(m, func(i, j int) bool {
		a, b := m[i].CrapScore, m[j].CrapScore
		switch {
		case a == nil && b == nil:
			return tieBreak(m[i], m[j])
		case a == nil:
			return false
		case b == nil:
			return true
		case *a != *b:
			return *a > *b
		default:
			return tieBreak(m[i], m[j])
		}
	})
}

func tieBreak(a, b MethodMetric) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.StartLine < b.StartLine
}
