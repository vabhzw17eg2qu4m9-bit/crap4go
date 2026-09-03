# crap4go

[![Quality](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml/badge.svg)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml)
[![version](https://img.shields.io/github/v/release/vabhzw17eg2qu4m9-bit/crap4go?label=version)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/releases)
![CRAP](badges/crap.svg)
![coverage](badges/coverage.svg)

**crap4go** computes the **CRAP (Change Risk Anti-Patterns) metric** for Go
code by combining each function's **cyclomatic complexity** with its
**statement coverage** from a `go test` cover profile. It is a port of
[`crap4java`](https://github.com/stoyanr/crap4java) (and `crap4dart`); the CLI,
formula, complexity rules, report format, and exit codes are identical across
the ports.

## The CRAP formula

```
CRAP = CC² × (1 − coverage)³ + CC
```

- `CC` — cyclomatic complexity (integer ≥ 1)
- `coverage` — statement-coverage fraction in `[0.0, 1.0]`
- When coverage is unknown, CRAP is `null` (reported as `N/A`).

A function with high complexity and low coverage scores high CRAP — it is both
hard to understand and under-verified, the riskiest combination for change.

## Install

```sh
go install crap4go@latest      # adds $GOPATH/bin/crap4go
# or, from a clone:
git clone <this-repo> crap4go
cd crap4go && go build -o crap4go .
```

No external module dependencies — standard library only.

## CLI usage

```sh
crap4go                          # analyze every .go file under ./
crap4go --changed                # analyze git-changed .go files only
crap4go <path>...                # analyze explicit files / directories
crap4go --help                   # print help, exit 0
crap4go --coverage <path>        # override coverage profile (default coverage.out)
crap4go --threshold <num>        # override CRAP threshold (default 8.0)
crap4go --run-tests              # run "go test" with coverage before analyzing
```

| Flag          | Default        | Description                                              |
|---------------|----------------|----------------------------------------------------------|
| `--changed`   | off            | Analyze only files reported changed by `git status`.     |
| `--coverage`  | `coverage.out` | Path to a Go cover profile.                              |
| `--threshold` | `8.0`          | Max numeric CRAP allowed before exit code 2.              |
| `--run-tests` | off            | Run `go test ./... -coverprofile -covermode=atomic`.     |
| `--help`      | —              | Print usage and exit 0.                                  |

### Subcommands

`profile`, `skill`, and the gate subcommands (`file-naming`, `nesting`,
`class-size`, `weight-of-class`, `unused-code`, `unused-files`,
`banned-imports`, `magic-constants`, `test-assertions`,
`folder-structure`) dispatch on the first argument only; anything else
takes the analyze path above.

```sh
crap4go profile --name TestParser   # run instrumented tests, report per-method timing
crap4go file-naming                 # flag mechanical file names (util.go, batch1.go, ...)
crap4go nesting                     # flag functions nested deeper than 5
crap4go class-size                  # flag types with >25 methods or WMC >80
crap4go weight-of-class             # flag data-heavy exported structs (fields > 33% of members)
crap4go unused-code                 # flag unexported declarations never referenced
crap4go unused-files                # flag packages never imported by analyzed code
crap4go banned-imports --from 'ui/**' --forbid '**/db/**' --message 'UI must not touch storage'
crap4go magic-constants           # flag hex colors outside consts and repeated literals
crap4go test-assertions           # flag tests with no fail-capable calls (t.Error/Fatal/..., panic)
crap4go folder-structure           # flag dirs with loose .go files at the module root
crap4go skill                     # print the profiling skill for AI agents
```

`profile` copies the module to a temp dir, wraps every function body with an
enter/exit pair reported to a generated collector, runs `go test` against the
copy, and prints a timing table (`TOTAL SELF % CALLS MEAN(µs) MAX(µs)
@60fps(ms) METHOD FILE:LINE`, sorted by total descending; TOTAL is inclusive
time and SELF excludes nested profiled calls — flamegraph self-time; TOTAL
and SELF render with adaptive units `82.50ms` / `13.89s` / `22.50m` /
`13.89h`; sub-30µs means are marked `~` — instrumentation overhead
dominates there, so read the CALLS/TOTAL deltas instead). `--top <N>` limits
the console rows (default 20);
`--threshold <ms>` exits 2 when any method's total exceeds it. Full reports
are written to `profile-reports/`. `file-naming` reports files whose stems
are generic dumping-grounds (`util.go`, `helpers.go`, ...) or carry numeric
suffixes (`batch1.go`, `configv2.go`), exiting 2 on violations; technical
stems like `base64.go` or `sha256.go` are accepted.

The remaining gates are ported from crap4dart 0.5.x–0.9.2 as
subcommands (there is no gate framework: no severity/ignorable/entries/
baseline — violations always fail with exit 2, thresholds keep upstream
defaults; 0.6.0's baseline/severity/config knobs and 0.6.1's internal
refactor do not apply, and 0.5.2's profile fix is Dart-only).
`nesting`
fails functions whose block nesting exceeds 5 (body = level 1).
`class-size` fails named types with more than 25 methods or a
weighted-methods sum (total cyclomatic complexity) above 80, methods
gathered across the package. `weight-of-class` fails exported struct
types whose exported fields make up more than 33% of exported members.
`unused-code` flags unexported package-level declarations never
referenced elsewhere in their package; `unused-files` flags non-main
packages never imported by analyzed code — both skip with exit 0 on an
explicit path selection (not meaningful for a partial selection; Go has
no re-export concept, so 0.7.1's export edges do not apply).
`banned-imports` takes repeatable
`--from GLOB --forbid GLOB [--message MSG]` rules; for every file matching
`from`, imports matching `forbid` (raw path or module-relative directory)
are violations; with no rules it passes. `magic-constants` (from 0.6.0
plus the 0.7–0.9 precision fixes)
flags hex color integer literals (`0xRRGGBB`/`0xAARRGGBB`) outside const
declarations and numeric or string literals repeating 3+ times in one
file among their non-const-line occurrences (values shorter than 4
characters ignored; string literals in identifier position — map-literal
keys, index operands, switch case labels — never count).
`test-assertions` (from 0.9) flags `Test*` functions in `*_test.go`
files that cannot fail the test: no fail-capable `*testing.T` method
call (`Error`/`Errorf`/`Fatal`/`Fatalf`/`Fail`/`FailNow`, including via
subtest closures) and no `panic()` — a test without assertions verifies
nothing. `folder-structure` (from 0.9) flags directories holding more
than 0 loose `.go` files directly (default: the module root) — group
them into feature packages.

### Flag ordering

`crap4go` uses Go's standard `flag` package, which **stops parsing at the first
non-flag argument**. Put every flag **before** any positional path:

```sh
crap4go --coverage cover.out --threshold 4 ./pkg/...   # ✅ flags first
crap4go ./pkg --coverage cover.out                     # ❌ --coverage ignored
```

Both `-flag` and `--flag` are accepted. `--changed` is mutually exclusive with
positional paths (usage error if combined).

## Coverage format

`coverage.out` is the profile emitted by:

```sh
go test ./... -coverprofile=coverage.out -covermode=atomic
```

The first line is `mode: atomic`; each subsequent line is:

```
pkg/path/file.go:startLine.startCol,endLine.endCol numStmt count
```

Paths in the profile are module-prefixed (e.g. `example.com/pkg/foo.go`);
`crap4go` matches them to source files by exact path, then by basename.
If the profile is missing, a warning is printed to stderr — with a hint
to run `go test ./... -coverprofile=coverage.out -covermode=atomic` (or
pass `--run-tests`) — and every method's coverage/CRAP is reported as
`N/A`.

## Report

```text
CRAP Report
===========
Method                         File                                  CC    Cov%     CRAP
----------------------------------------------------------------------------------------
(Calc)Compute                  sample.go                               4  100.0%      4.0
Max                            sample.go                               2   66.7%      2.1
Add                            sample.go                               1  100.0%      1.0
Grade                          sample.go                               4    N/A       N/A

Max CRAP: 4.0 (threshold 8.0) — passed
```

Rows are sorted by CRAP descending with `N/A` entries last; ties break by file,
then start line. `Cov%` and `CRAP` print with one decimal. The summary line
prints the maximum numeric CRAP and the verdict against the configured
threshold.

## Exit codes

| Code | Meaning                                                                   |
|------|---------------------------------------------------------------------------|
| `0`  | Success (including empty selection, or max CRAP ≤ threshold).             |
| `1`  | CLI usage error (bad flags, bad threshold, `--changed` + paths, etc.).    |
| `2`  | Max numeric CRAP exceeded the threshold (also prints to stderr).          |

## Development

After cloning, enable the pre-commit hook once:

```sh
git config core.hooksPath githooks
```

The hook builds `crap4go` (if the binary is missing) and runs it on your staged
`.go` files at `--threshold 8.0`; a max CRAP above the threshold blocks the
commit (exit code 2 from the tool).

Badges under `badges/` are regenerated by CI on every push. To refresh them
locally, build the binary, run it and the tests, then call `scripts/badge.sh`:

```sh
go build -o crap4go .
./crap4go --coverage coverage.out | tail -1   # → Max CRAP: <n> ...
go test -coverprofile=coverage.out -covermode=atomic ./...  # → coverage: X% of statements
bash scripts/badge.sh CRAP <max> <color> badges/crap.svg
bash scripts/badge.sh coverage <pct>% <color> badges/coverage.svg
```

## Project layout

```
crap4go/
  go.mod          main.go        crap.go        complexity.go
  parser.go       coverage.go    analyzer.go    report.go
  files.go        runtests.go    cli.go
  profile.go      profile_collector.go   filenaming.go   skill.go
  magicconstants.go   testassertions.go   folderstructure.go
  *_test.go       testdata/
```

See `spec.md` for the full language-adapted specification.
