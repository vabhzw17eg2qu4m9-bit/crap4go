package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Analyze reads each Go source file, extracts methods, attributes coverage
// from coveragePath, and computes CRAP scores. A missing or record-less
// coverage profile is an error: unit-test coverage is required (fail closed).
// Any other coverage-read or source-parse failure is returned as an error.
func Analyze(filePaths []string, coveragePath string) ([]MethodMetric, error) {
	return AnalyzeInRoot(filePaths, coveragePath, "", false)
}

// zeroCoverage is the strict-mode coverage fraction for a selected file with
// no coverage entry.
var zeroCoverage = 0.0

// AnalyzeInRoot is Analyze with the project root and strict mode. root
// resolves relative lcov SF: paths to absolute keys (an empty root keeps the
// profile's own keys). A missing coverage file or a profile with no coverage
// records is an error in every mode; under strict, a selected file with no
// coverage entry reports coverage 0.0% for all its methods instead of
// unknown — while a function with no statements in a covered file stays N/A.
func AnalyzeInRoot(filePaths []string, coveragePath, root string, strict bool) ([]MethodMetric, error) {
	coverage, err := requireCoverage(coveragePath, root)
	if err != nil {
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
			cov := methodCoverage(fc, m, strict)
			metrics = append(metrics, MethodMetric{
				MethodName: m.Name,
				File:       path,
				StartLine:  m.StartLine,
				EndLine:    m.EndLine,
				Complexity: m.Complexity,
				Coverage:   cov,
				CrapScore:  CrapScore(m.Complexity, cov),
			})
		}
	}
	return metrics, nil
}

// coverageHint names the port's own coverage generation command; the
// fail-closed errors reuse it so every failure is actionable.
const coverageHint = "Hint: generate coverage first — `go test ./... -coverprofile=coverage.out -covermode=atomic`, or pass --run-tests to do it automatically."

// requireCoverage reads the coverage profile, failing closed: a missing file
// or a profile with no coverage records is an error naming the generation
// command — a CRAP score without unit-test coverage is meaningless.
func requireCoverage(coveragePath, root string) (map[string]*FileCoverage, error) {
	coverage, err := loadCoverage(coveragePath, root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("unit-test coverage is required: coverage file %s not found. %s", coveragePath, coverageHint)
		}
		return nil, err
	}
	if coverageRecords(coverage) == 0 {
		return nil, fmt.Errorf("unit-test coverage is required: no coverage records in %s. %s", coveragePath, coverageHint)
	}
	return coverage, nil
}

// methodCoverage attributes a method's coverage from its file's entry. Under
// strict, a file with no coverage entry reports 0.0% instead of unknown; a
// function with no statements in a covered file stays N/A in every mode.
func methodCoverage(fc *FileCoverage, m MethodDescriptor, strict bool) *float64 {
	cov := CoverageForMethod(fc, m.StartLine, m.EndLine)
	if strict && cov == nil && fc == nil {
		return &zeroCoverage
	}
	return cov
}

// coverageRecords counts the statement records across the profile: strict
// mode treats a profile with none (an empty native profile or an lcov
// tracefile with no DA records) as empty input.
func coverageRecords(coverage map[string]*FileCoverage) int {
	total := 0
	for _, fc := range coverage {
		total += len(fc.Blocks)
	}
	return total
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
//
// The comparison constants on unequal-operand paths are equivalent-mutant
// territory: `*a > *b` executes only when the scores differ, and tieBreak's
// `a.File < b.File` only when the file paths differ — on unequal operands
// `>` and `>=` (resp. `<` and `<=`) coincide, so those boundary mutants are
// unobservable (verified: each mutant passes the full suite). The final
// `a.StartLine < b.StartLine` comparison IS load-bearing: sort.SliceStable
// keeps entries with identical keys in input order only while the comparator
// stays irreflexive, so mutating it to `<=` reorders identical-key entries.
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
