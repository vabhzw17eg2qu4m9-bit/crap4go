# crap4go — Specification

A Go port of the CRAP (Change Risk Anti-Patterns) metric tool. It computes
CRAP for every Go function and method by combining cyclomatic complexity with
statement coverage drawn from a `go test` cover profile.

## 1. Purpose

Identify code that is simultaneously complex and under-tested — the riskiest
code to change. Output is a deterministic per-method report; non-zero exit
codes let CI fail builds whose worst CRAP exceeds a configurable threshold.

## 2. Scope

**In scope:** plain Go source (`.go`), top-level functions, methods on named
and pointer receiver types, complexity drawn from `go/ast`, coverage drawn
from a Go cover profile.

**Out of scope:** test files (`*_test.go`) are excluded from analysis;
`vendor/` trees are excluded; generated code is not specially detected;
generics are parsed structurally (no type-parameter-aware scoring); cgo is
parsed by `go/parser` like any Go file.

## 3. CLI

```
crap4go                          Analyze all .go files under ./
crap4go --changed                Analyze git-changed .go files (git status --porcelain)
crap4go <path>...                Analyze explicit files; directories are walked
crap4go --help                   Print usage; exit 0
crap4go --coverage <path>        Override coverage profile (default coverage.out)
crap4go --threshold <num>        Override CRAP threshold (default 8.0)
crap4go --run-tests              Run "go test" before analyzing
```

`--changed` is mutually exclusive with positional paths (usage error if
combined). Unknown flags are usage errors.

**Flag ordering:** built on Go's `flag` package, which stops at the first
non-flag argument. All flags must precede positional paths. `-flag` and
`--flag` are both accepted.

## 4. File selection

- **Default (no args):** recursively walk the project root for `.go` files,
  skipping `*_test.go` and any path under `vendor/`.
- **`--changed`:** run `git -C <root> status --porcelain`, parse each line's
  status + path, follow renames to their target, keep only `.go` non-test
  paths under root, sort and dedupe.
- **Explicit paths:** each file is kept verbatim; each directory is walked via
  the same rules as the default. Paths resolve against the project root when
  relative. Results are deduped and sorted.

If the resulting set is empty, print `No Go files to analyze.` and exit 0.

## 5. Coverage

The default coverage file is `coverage.out` (overridable with `--coverage`).
It is the standard Go cover profile: a `mode: <mode>` header followed by
lines of `pkg/path/file.go:startLine.startCol,endLine.endCol numStmt count`.
Paths are module-prefixed; matching to source files falls back from exact
path to basename.

For each method covering source lines `[startLine, endLine]`, the tool sums
every cover block whose `[blockStart, blockEnd]` intersects the method range:
`covered = Σ NumStmt where Count > 0`, `total = Σ NumStmt`. Coverage is the
ratio `covered/total`. If no block intersects, coverage (and CRAP) are `null`
for that method. If the profile file is missing, a warning is printed to
stderr and coverage is `null` for every method.

## 6. Go parsing

Source is parsed with `go/parser.ParseFile` (mode 0 — no type checking).
For every `*ast.FuncDecl` with a non-nil `Body` (interface method declarations
have no body and are skipped):

- **Name:** `(Foo)Bar` for methods (where `Foo` is the receiver type, pointer
  receivers unwrapped), or `Bar` for plain functions.
- **Line range:** the `Pos()`…`End()` positions of the declaration.
- **Complexity:** computed from the body (see §7).

A parse error for any file is fatal and reported.

## 7. Formula & complexity rules

```
CRAP = CC² × (1 − coverage)³ + CC
```

Cyclomatic complexity base is 1, +1 for each occurrence of:

| Construct                      | `go/ast` type                         |
|--------------------------------|---------------------------------------|
| `if`                           | `*ast.IfStmt`                         |
| `for`                          | `*ast.ForStmt`                        |
| `for … range`                  | `*ast.RangeStmt`                      |
| `switch` `case` / `default`    | `*ast.CaseClause`                     |
| `select` `case` / `default`    | `*ast.CommClause`                     |
| `&&` / `\|\|`                  | `*ast.BinaryExpr` op `LAND`/`LOR`     |

Anonymous function literals (`*ast.FuncLit`) inside a body are traversed and
their branches count towards the enclosing function (matches `crap4java` and
`crap4dart` with `countLambdas=true`). Go has no ternary, `while`, `do-while`,
or `catch`-as-construct, and `recover()` is not counted.

Verified edge cases:
- `CC=5, coverage=1.0  → 5.0`
- `CC=5, coverage=0.0  → 30.0`
- `CC=8, coverage=0.45 → 18.648`
- `CC=3, coverage=null → null`

## 8. Report

A fixed-width table is written to stdout:

```
CRAP Report
===========
<header>
<separator — as many dashes as the header is wide>
<rows>
<blank line>
Max CRAP: <max> (threshold <t>) — <FAILED|passed>
```

Rows are sorted by CRAP descending, with `N/A` (null) entries last; ties break
by File ascending then StartLine ascending. `CC` is right-aligned; `Cov%`
prints as a percentage with one decimal (or `N/A`); `CRAP` prints with one
decimal (or `N/A`). When every CRAP is `N/A`, max is treated as `0.0`.

## 9. Threshold

The threshold defaults to `8.0` and is overridable with `--threshold`. After
the report is printed, the maximum numeric CRAP is compared against it; if
`max > threshold`, `CRAP threshold exceeded: <max> > <threshold>` is printed
to stderr and the process exits 2. Otherwise the process exits 0. A threshold
of `0` or less is a usage error.

## 10. Exit codes

| Code | Meaning                                                                  |
|------|--------------------------------------------------------------------------|
| `0`  | Success (including empty selection, or max CRAP ≤ threshold).            |
| `1`  | Usage error: bad flags, bad threshold, `--changed` + paths, unreadable.  |
| `2`  | Max numeric CRAP exceeded the threshold (also reported on stderr).       |

## 11. `--run-tests`

Runs `go test ./... -coverprofile=coverage.out -covermode=atomic` in the
project root, streaming stdout/stderr through. On non-zero exit, the error is
printed to stderr and the process exits 1.

## 12. Non-goals

- No type-checking or build verification — parsing only.
- No HTML/branch coverage reports — only the statement-level cover profile.
- No automatic fixing or refactoring of high-CRAP code.
- No external runtime dependencies (standard library only); no `go.sum`.
