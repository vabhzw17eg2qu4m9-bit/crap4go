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
crap4go profile [flags] [paths]  Run instrumented tests; report per-method timing
crap4go file-naming [paths...]   Flag mechanical file names; exit 2 on violations
crap4go nesting [paths...]       Flag functions nested deeper than 5; exit 2 on violations
crap4go class-size [paths...]    Flag named types with >25 methods or WMC >80; exit 2
crap4go weight-of-class [paths...] Flag exported structs with field/member ratio >0.33; exit 2
crap4go unused-code [paths...]   Flag unexported declarations never referenced; exit 2
crap4go unused-files [paths...]  Flag packages never imported; exit 2
crap4go banned-imports [--from GLOB --forbid GLOB --message MSG]... [paths...]
                                 Flag banned imports per from/forbid rule; exit 2
crap4go skill                    Print the crap4go profiling skill for AI agents
```

`--changed` is mutually exclusive with positional paths (usage error if
combined). Unknown flags are usage errors.

**Subcommands:** `profile`, `skill`, and the gate subcommands (`file-naming`,
`nesting`, `class-size`, `weight-of-class`, `unused-code`, `unused-files`,
`banned-imports`) are dispatched on the first argument only, and only on an
exact match. Any other first argument — flags, paths — takes the analyze path
unchanged. Subcommand flags:

```
profile:
  --name <pattern>               Run only tests matching the pattern (go test -run)
  --threshold <ms>               Exit 2 when a method's total exceeds this (default off)
  --top <N>                      Console rows shown (default 20)
```

The upstream `--tags`/`--exclude-tags` and config-file options are skipped:
Go's `go test` has no tag concept, and the port has no config system — all
knobs are CLI flags with upstream defaults.

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

## 9. `profile`

```
crap4go profile [--name <pattern>] [--threshold <ms>] [--top <N>] [paths...]
```

Runs the test suite against instrumented source code and reports per-method
timing data. Source instrumentation: the module is copied to
`.crap_profile_temp/`, every function/method body in the copy is wrapped with
`defer crap4goRecord("<file>|<method>")()` — a defer-based timer that records
elapsed microseconds on exit — plus a small generated collector per package
that appends each call to a per-process log (`go test` runs each package as
its own binary, so no cross-process locking is needed). `go test -count=1`
runs against the copy (`-count=1` bypasses Go's test cache, which would
otherwise skip the instrumented binaries); the temp directory is cleaned up
automatically after the run (kept when `CRAP_PROFILE_DEBUG` is set).

Test selection: `--name` is forwarded to `go test -run`; positional paths are
forwarded verbatim; without paths, `./...` runs. `--top` (default 20) limits
the console table; `--threshold <ms>` (default off) makes the command exit 2
when any method's total time exceeds it.

Method attribution: timing data is matched to the project's method inventory
(same parsing as analyze, keyed by relative file path plus method name).
Timing entries that do not match a project method are ignored. Test files
(`*_test.go`) are not instrumented; unparseable files (e.g. `testdata/`
fixtures) are left unchanged.

### Profile Report

A fixed-width table is written to stdout, sorted by total time descending:

```
TOTAL(ms) | % | CALLS | MEAN(µs) | MAX(µs) | @60fps(ms) | METHOD | FILE:LINE
```

- **total time** — total execution time across all calls
- **calls** — number of invocations
- **mean time** — average time per call
- **max time** — slowest single call
- **@60fps** — estimated cost when called 60× per second (mean × 60, in
  milliseconds), highlighting methods that are cheap per-call but costly on
  hot paths

The full (untruncated) table and a JSON report are also written to
`profile-reports/profile-<timestamp>.txt` and `.json`.

## 10. `file-naming`

```
crap4go file-naming [paths...]
```

Flags Go files whose names indicate a mechanical split instead of a domain
boundary. Selection defaults to the normal analyze rules (non-test,
non-vendor `.go` files under the root). A file is flagged when its stem (name
without the `.go` extension), lower-cased, either ends in digits preceded by
a letter or underscore (`jira_batch1`, `report2`, `day_1`, `configv3`) or
equals a generic dumping-ground stem (`common`, `core`, `general`, `helper`,
`helpers`, `misc`, `shared`, `stuff`, `temp`, `tmp`, `types`, `util`,
`utils`, `utilities`, `utility`, `various`). Whole technical stems ending in
digits (`base64`, `sha256`, `utf8`, `oauth2`, ...) are accepted by default
(the upstream `defaultAllowedStems` list).

Output: one line per violation (`<relative path>: <message>`), then a
summary — `N/M files with mechanical names` or `M files have
domain-meaningful names`. Exit code 2 iff there are violations.

### Gate subcommands (ported from crap4dart 0.5.x)

crap4dart 0.5.0 introduced a quality-gate framework (severity, ignorable,
`crap:ignore` comments, per-gate `entries` overrides, baselines, yaml
config). The Go port has no gate framework: each gate is a standalone
subcommand dispatched like `file-naming`, with upstream default thresholds
hard-coded and violations always failing (exit 2) — severity/ignorable/
entries/baseline do not apply. Gate checks 11.5–11.10 of the crap4dart
spec are ported with Go adaptations as described below.

## 11. `nesting`

```
crap4go nesting [paths...]
```

Flags functions whose maximum block nesting level exceeds 5 (default
ported from crap4dart's `nesting` gate). The function body block counts as
level 1; every nested block or control-flow statement (`if`, `for`,
`switch`, type-switch, `select`, plain block statement) adds one.
Control-statement braces do not add a level on top of the statement
itself; `else if` chains nest one more level per branch (upstream
visitor semantics). Output: one line per violation
(`<relative path>:<line>: <func> nesting=N > max 5`) plus a summary
(`N/M functions nested deeper than 5`). Exit code 2 iff violations.

## 12. `class-size`

```
crap4go class-size [paths...]
```

Go adaptation of crap4dart's `class_size` gate: named types replace
classes, and a type's methods are gathered across the whole package
directory. A named type fails when it has more than 25 methods (with a
body) or a weighted-methods sum — total cyclomatic complexity over all
its methods, counted by the same rules as the analyze command — above 80.
Output: one line per violation (`<type> has N methods > max 25` and/or
`<type> WMC=N > max 80`, at the type's first method) plus a summary.
Exit code 2 iff violations.

## 13. `weight-of-class`

```
crap4go weight-of-class [paths...]
```

Go adaptation of crap4dart's `weight_of_class` gate: exported named
struct types replace public classes; exported methods are gathered across
the whole package directory. A type fails when its ratio of exported
fields to exported members (exported fields + exported methods, exported
embedded types counted as fields) exceeds 0.33. Types without exported
fields are never flagged. Output: one line per violation (`<type> exposes
N public fields of M public members (weight=W)`) plus a summary.
Exit code 2 iff violations.

## 14. `unused-code`

```
crap4go unused-code [paths...]
```

Flags unexported package-level declarations — functions (methods
excluded), types, vars, consts — whose identifier never appears elsewhere
in the same package. References are counted lexically on unresolved ASTs
(all identifier occurrences, declaration included; a declaration is
unused when its identifier occurs exactly once). Non-test files only,
for both declarations and references (upstream `unused_code` gate
semantics). Output: one line per violation
(`<relative path>:<line>: <name> is never referenced`) plus a summary.
Exit code 2 iff violations.

**Partial selection:** an explicit path selection prints
`unused-code: not meaningful for a partial selection` and exits 0 —
a partial file set yields false positives (ported from crap4dart 0.5.1).

## 15. `unused-files`

```
crap4go unused-files [paths...]
```

Go adaptation of crap4dart's `unused_files` gate: packages replace
files. Flags non-main packages in the module (resolved from `go.mod`)
that are never imported by any other analyzed package; `main` packages
are entry points and never flagged (upstream never reports files with a
top-level `main`). Internal imports resolve by stripping the module
path prefix. Output: one line per violation
(`<dir>: package <name> is never imported by any analyzed package`) plus
a summary. Exit code 2 iff violations.

**Partial selection:** an explicit path selection prints
`unused-files: not meaningful for a partial selection` and exits 0
(ported from crap4dart 0.5.1).

## 16. `banned-imports`

```
crap4go banned-imports [--from GLOB --forbid GLOB --message MSG]... [paths...]
```

Enforces architectural boundaries (upstream `banned_imports` gate).
Rules are `--from`/`--forbid` pairs zipped by CLI order; the optional
`--message` is appended per rule. For every file whose project-relative
path matches a rule's `from` glob, each import that matches any `forbid`
glob — matched against the raw import path and, for imports inside the
module, its project-relative package directory — is a violation
(`<relative path>:<line>: import "<path>" is banned for <file> — <message>`).
Globs support `*`, `?` and `**` (across separators); a leading `**/` and
trailing `/**` also match zero directories. With no rules the command
prints `no rules configured` and exits 0; `--from` without `--forbid`
(or more `--message` values than rules) is a usage error (exit 1).
Exit code 2 iff violations.

## 17. `skill`

Prints a Go-adapted version of crap4dart's profiling skill (when to profile,
how the instrumentation works, how to read the report), ending with one line
on installing it as an agent skill. Always exits 0.

## 18. Threshold

The threshold defaults to `8.0` and is overridable with `--threshold`. After
the report is printed, the maximum numeric CRAP is compared against it; if
`max > threshold`, `CRAP threshold exceeded: <max> > <threshold>` is printed
to stderr and the process exits 2. Otherwise the process exits 0. A threshold
of `0` or less is a usage error.

## 19. Exit codes

| Code | Meaning                                                                  |
|------|--------------------------------------------------------------------------|
| `0`  | Success (including empty selection, or max CRAP ≤ threshold).            |
| `1`  | Usage error: bad flags, bad threshold, `--changed` + paths, unreadable.  |
| `2`  | Max numeric CRAP exceeded the threshold; `profile` total exceeded        |
|      | `--threshold`; gate subcommand violations (`file-naming`, `nesting`,     |
|      | `class-size`, `weight-of-class`, `unused-code`, `unused-files`,          |
|      | `banned-imports`). (Also reported on stderr.)                           |

## 20. `--run-tests`

Runs `go test ./... -coverprofile=coverage.out -covermode=atomic` in the
project root, streaming stdout/stderr through. On non-zero exit, the error is
printed to stderr and the process exits 1.

## 21. Non-goals

- No type-checking or build verification — parsing only.
- No HTML/branch coverage reports — only the statement-level cover profile.
- No automatic fixing or refactoring of high-CRAP code.
- No external runtime dependencies (standard library only); no `go.sum`.
- No config file or gate framework; no gate severity/ignorable/entries/
  baseline (crap4dart 0.5.0 framework features) — gates are standalone
  subcommands with hard-coded upstream defaults; no profile
  `--tags`/`--exclude-tags` (no tag concept in `go test`) — all knobs are
  CLI flags.
