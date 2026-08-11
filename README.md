# crap4go

[![Quality](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml/badge.svg)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml)
[![version](https://img.shields.io/github/v/tag/vabhzw17eg2qu4m9-bit/crap4go?label=version)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/releases)
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
If the profile is missing, a warning is printed to stderr and every method's
coverage/CRAP is reported as `N/A`.

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
  *_test.go       testdata/
```

See `spec.md` for the full language-adapted specification.
