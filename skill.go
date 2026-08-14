package main

import (
	"fmt"
	"io"
	"strings"
)

// codeFence is a markdown triple-backtick fence. Kept as a constant because
// raw string literals cannot contain backticks, and skillText is full of
// markdown code spans and fences.
const codeFence = "```"

// skillTextParts are joined (with backtick code spans spliced in) to form
// skillText.
var skillTextParts = []string{
	`---
name: crap4go-profiling
description: CPU profiling for Go projects using crap4go. Use when the user wants to find performance bottlenecks, measure function execution time, profile tests, or optimize Go code. Activated by keywords like "profile", "performance", "bottleneck", "slow functions", "optimize timing", "microseconds".
---

# crap4go Profiling Skill

## When to Use

- Find performance bottlenecks in Go code
- Measure per-function execution time (microsecond precision)
- Profile test suites to see which functions are expensive
- Identify frequently-called functions that accumulate cost

## What is crap4go profile?

`,
	"`crap4go profile`",
	` is a source-instrumentation profiler. It copies the
module to a temp dir, wraps every function body with a defer-based timer, runs
`,
	"`go test`",
	` against the instrumented copy, and reports exact per-function
timing. Unlike pprof sampling (statistical), this gives **exact** microsecond
timing for every single call — no missed fast functions.

## Basic Usage

`, codeFence, "bash", `
crap4go profile                     # full test suite
crap4go profile --name TestParser   # only tests matching (go test -run)
crap4go profile --top 10            # limit console table rows
crap4go profile --threshold 10.0    # exit 2 when a total exceeds this
crap4go profile ./pkg/...           # only these packages/paths
`, codeFence, `

## Reading the Report

Full reports are saved to profile-reports/ (profile-<timestamp>.txt and .json).

| Column       | Meaning                                    |
|--------------|--------------------------------------------|
| TOTAL(ms)    | Total time across all calls                |
| %            | Share of total profiling time              |
| CALLS        | Number of invocations                      |
| MEAN(µs)     | Average time per call                      |
| MAX(µs)      | Worst single call                          |
| @60fps(ms)   | Cost if called 60× per second (mean × 60)  |

Rows are sorted by TOTAL descending. FILE:LINE points at the function in the
original (non-instrumented) source. Look for: high TOTAL + high CALLS (called
too often — cache it), high MEAN (expensive call — algorithm issue), high
@60fps (costly on any per-request path).

## How It Works

1. Copies the module to .crap_profile_temp/
2. Every function body gets `,
	"`defer crap4goRecord(\"file|Func\")()`",
	` — a defer-based timer that
   records elapsed µs on exit
3. A generated collector logs each call to a per-process file
   (CRAP_PROFILE_DIR); no cross-process locking needed
4. `,
	"`go test -count=1`",
	` runs against the copy (test cache bypassed)
5. Logs are merged into calls/total/min/max per function and attributed to
   the analyzed method inventory (unmatched entries ignored)
6. The table is printed, full reports written to profile-reports/
7. The temp dir is cleaned up (kept when CRAP_PROFILE_DEBUG is set)

## Limitations

- Test files (*_test.go) are not instrumented — only the code under test
- Unparseable files (e.g. testdata fixtures) are skipped
- Profiling adds per-call overhead; sub-microsecond measurements are noise

## Install as an agent skill

mkdir -p .agents/skills/crap4go-profiling && crap4go skill > .agents/skills/crap4go-profiling/SKILL.md
`,
}

// skillText is the Go-adapted version of crap4dart's profiling skill
// (.agents/skills/crap4dart-profiling/SKILL.md): same structure, adapted to
// go test and .go files.
var skillText = strings.Join(skillTextParts, "")

// RunSkillCommand implements `crap4go skill`: it prints the language-adapted
// profiling skill for AI agents, ending with the one-line install hint.
// Always exits 0.
func RunSkillCommand(stdout io.Writer) int {
	fmt.Fprint(stdout, skillText)
	return 0
}
