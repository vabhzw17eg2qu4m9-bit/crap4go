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
crap4go magic-constants [paths...] Flag magic literals; exit 2 on violations
crap4go test-assertions [paths...] Flag tests with no fail-capable calls; exit 2
crap4go anon-struct-signatures [--max-fields N] [paths...]
                                 Flag anonymous struct types in function
                                 signatures; exit 2
crap4go scattered-constants [--min-files N] [--min-length N]
                  [--min-value N] [--aliases] [paths...]
                                 Flag literals repeated across distinct files,
                                 duplicating a named const, or pure value
                                 aliases; exit 2
crap4go duplicates [--threshold N] [--min-tokens N] [--min-lines N]
                   [--report percentage|clones] [--exclude GLOB]...
                   [--source PATH]... [--ignore-locals] [--ignore-literals]
                   [--ignore-identifiers] [--ignore-imports]
                   [--skip-duplicate-files] [paths...]
                   Flag files over N% duplicated lines (or report clone
                   groups); exit 2
crap4go mutation [--coverage PATH] [--min-score F] [--max-survivors N]
                                 [--timeout-seconds N] [--max-mutants N]
                                 [--operators LIST] [--exclude GLOB]... [paths...]
                                 Mutation-test covered sites; exit 2 on survivors
crap4go similarity [--format console|json] [--jaccard F] [--containment F]
                   [--min-shingles N] [--include GLOB]... [--exclude GLOB]...
                   [--check] [dirs...]
                                 Scan for near-duplicate files; exit 0 (explorer)
                                 or, with --check, 2 on violations
crap4go skill                    Print the crap4go profiling skill for AI agents
```

`--changed` is mutually exclusive with positional paths (usage error if
combined). Unknown flags are usage errors.

**Subcommands:** `profile`, `skill`, and the gate subcommands (`file-naming`,
`nesting`, `class-size`, `weight-of-class`, `unused-code`, `unused-files`,
`banned-imports`, `magic-constants`, `test-assertions`, `folder-structure`,
`anon-struct-signatures`, `scattered-constants`, `duplicates`, `mutation`, `similarity`,
`string-hazards`, `identical-branches`)
are dispatched on the first argument only, and only on an
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
stderr — with a hint naming the coverage generation command
(`go test ./... -coverprofile=coverage.out -covermode=atomic`, or
`--run-tests`) — and coverage is `null` for every method.

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
or `catch`-as-construct, and `recover()` is not counted. Every `switch` case
counts — expression and type switches share `*ast.CaseClause` in `go/ast` —
and `&&`/`||` inside case expressions count like any other.

The `complexity` gate (§11) can score a second metric,
**cognitive complexity** (`--metric cognitive`): the SonarSource definition
ported from crap4dart 7bf7a15 — +1 per flow break, a nesting increment of
the current depth for structural constructs, one increment per
identical-operator `&&`/`||` sequence, flat `else if` chains, function
literal bodies one level deeper without incrementing, starting at 0. See
§11 for the full rules; CRAP itself is always computed from the cyclomatic
value.

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
`.crap_profile_temp/`, every function/method body in the copy begins with
`crap4goEnter("<file>|<method>")` followed by `defer crap4goExit()` — the
collector owns a per-goroutine call stack of open frames (goroutine ids
parsed from `runtime.Stack`), and the deferred exit reports the call on
every return path (panics included) on the registering goroutine — plus a
small generated collector per package that appends each call to a
per-process log (`go test` runs each package as its own binary, so no
cross-process locking is needed). `go test -count=1`
runs against the copy (`-count=1` bypasses Go's test cache, which would
otherwise skip the instrumented binaries); the temp directory is cleaned up
automatically after the run (kept when `CRAP_PROFILE_DEBUG` is set).

Test selection: `--name` is forwarded to `go test -run`; positional paths
are forwarded remapped into the instrumented copy (project-relative paths
stay unchanged and resolve inside the copy, which is `go test`'s working
directory; absolute paths under the original project root become
`./<rel>` — otherwise `go test` would run the original, non-instrumented
files and produce empty reports, ported from crap4dart 0.9). Without
paths, `./...` runs. Explicit paths are passed exclusively — the default
`./...` selector is appended only when no paths are given (upstream 0.9.3
fixed this shape in crap4dart, where the default `test` directory selector
was appended unconditionally and explicit paths profiled the whole suite
in addition; the Go port never had the bug, verified by
`TestProfileTestArgs`). `--top` (default 20) limits the console table;
`--threshold <ms>` (default off) makes the command exit 2 when any
method's total time exceeds it.

Method attribution: timing data is matched to the project's FULL method
inventory — the whole configured source set, not just the selected paths
(timings come from all instrumented methods; keyed by relative file path
plus method name, same parsing as analyze). Timing entries that do not
match a project method are ignored. Test files (`*_test.go`) are not
instrumented; unparseable files (e.g. `testdata/` fixtures) are left
unchanged.

The collector logs every call immediately — one `<key>\t<inclusive>\t<self>`
line per exit, where self is inclusive minus nested instrumented calls
that completed while the frame was open — to a per-process file (one per
test binary / pid, appends serialized by a mutex), so the upstream 0.9.2
isolate-collision fixes (per-worker temp-file identity around the atomic
rename, reader retry) do not apply — there is no temp-file/rename dance
and no buffering whose flush cadence could lose records; the logs are
read after the test run finishes. Upstream 0.9.5's delta-flush fix is
likewise N/A here by design: the collector never accumulates or re-merges
counters — it appends per-call deltas and the host merges each log
exactly once, so the quadratic calls/total inflation upstream fixed
cannot occur. Upstream 0.9.5's public `flush()` and
flush-on-outermost-exit are N/A for the same reason (every call is
written as it exits, so short runs do not lose their tail), and upstream
commit b789739 (icon/PNG assets) is branding-only, not ported.

### Profile Report

A fixed-width table is written to stdout, sorted by total time descending:

```
TOTAL | SELF | % | CALLS | MEAN(µs) | MAX(µs) | @60fps(ms) | METHOD | FILE:LINE
```

- **total time** — total execution time across all calls, inclusive of
  nested instrumented calls
- **self time** — total time minus the time spent in nested instrumented
  calls that completed while the frame was open (flamegraph self-time
  semantics); per-goroutine frame stacks keep it correct under parallel
  test goroutines
- **calls** — number of invocations
- **mean time** — average time per call; sub-30µs means are marked with
  `~` — the enter/exit instrumentation costs on the order of a microsecond
  per call (a goroutine-id traceback per enter/exit), so such means are
  mostly profiler noise; read the
  CALLS and TOTAL deltas instead (ported from crap4dart 0.9.2)
- **max time** — slowest single call
- **@60fps** — estimated cost when called 60× per second (mean × 60, in
  milliseconds), highlighting methods that are cheap per-call but costly on
  hot paths

TOTAL, SELF and the summary-line total render with adaptive units
(`82.50ms`, `13.89s`, `22.50m`, `13.89h`, always two decimals) so the
columns stay compact at extreme call counts (ported from crap4dart
0.9.5).

The full (untruncated) table and a JSON report are also written to
`profile-reports/profile-<timestamp>.txt` and `.json`; each JSON method
entry carries `totalSelfMicros` alongside the inclusive `totalMicros`.

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

### Gate subcommands (ported from crap4dart 0.5.x–0.9.2)

crap4dart 0.5.0 introduced a quality-gate framework (severity, ignorable,
`crap:ignore` comments, per-gate `entries` overrides, baselines, yaml
config). The Go port has no gate framework: each gate is a standalone
subcommand dispatched like `file-naming`, with upstream default thresholds
hard-coded and violations always failing (exit 2) — severity/ignorable/
entries/baseline do not apply. Gate checks 11.5–11.10 of the crap4dart
spec are ported with Go adaptations as described below; `magic-constants`
comes from 0.6.0 (§11.13) with the 0.7–0.9 precision fixes,
`test-assertions` (§11.15) and `folder-structure` (§11.16) from 0.9, and
`duplicates` (§11.11) from 0.2.0 with 0.2.1's whole-file tokenization and
the per-gate `sources` key from dc64e9c; the CPD-parity clone machinery —
`--report clones`, `--ignore-identifiers`, `--ignore-imports`,
`--skip-duplicate-files`, token-equality window verification and
shared-extent anchoring — ports d6c3ab6. `scattered-constants`
(§11.13.1) comes from e766d68, completing the corpus-wide literal
coverage `magic-constants` cannot see. `mutation` (§11.22) comes from
c865ec3 (the unclebob/mutator research): the operator table, coverage
gating, per-mutant test re-run and survivor violations port directly —
Dart's `/`→`~/` row is dropped (Go integer division already truncates),
`true`/`false` match predefined identifiers in expression position, the
LCOV per-line hit map becomes the block-based cover profile flattened to
per-line flags, and the yaml `enabled: false` knob becomes "subcommand
only" (the analyze flow never runs gates). `similarity` (§23) comes from
2b21966: the upstream pair — opt-in gate (`enabled: false` yaml) plus an
ad-hoc always-exit-0 command — collapses into one subcommand whose
default run is the explorer and whose `--check` flag is the gate.
`string-hazards` (§34) comes from 62fec6833: the first
warning-default gate — upstream's `severity: warning` (findings never
fail the run) maps to the `--severity` flag, warning (default) exits 0,
error exits 2; upstream's escaped-interpolation kind is omitted (Go has
no interpolation) and the ignorable-marker registry (§32) is the
`ignorable: true` mapping. `identical-branches` (§35) comes from
c6b2425add: the copy-paste-edit family, compared by canonical form —
four of upstream's six finding kinds host in Go (no ternary, no
try/catch), and the same `--severity` / ignorable mapping applies.

Not applicable upstream changes: 0.5.2's profile part-of fix is Dart-only;
0.6.0's baseline/severity/config knobs have no counterpart (no gate
framework — §34's `--severity` flag is the first mapping of a severity
knob, arriving with 62fec6833); 0.6.1's internal constants refactor changes no behavior;
0.7.1's export-directive edges do not apply to `unused-files` — Go has no
re-export concept (an import names the implementing package directly);
0.9's broken-goldens/tofu/goldens-guard command (Flutter PNG goldens), the
external Checkstyle-XML gate (gate-framework config), run_tests-by-default
(breaking; the port's pre-commit hook runs the bare tool on staged files —
a full test suite per commit is unacceptable), and the pixel-detector
tuning commits are Flutter/gate-framework features with no Go counterpart.

## 11. `complexity`

```
crap4go complexity [--metric cyclomatic|cognitive] [--max N] [--count-lambdas] [paths...]
```

Port of crap4dart's `complexity` gate with the cognitive-metric option
(upstream 7bf7a15): flags functions whose complexity exceeds `--max`
(default 10, the upstream `max_complexity` default) and returns exit code 2
iff there are violations. `--metric` selects the metric — `cyclomatic`
(default, reported as `CC`) or `cognitive` (SonarSource-style, reported as
`cognitive`); anything else is a usage error. `--count-lambdas` (default
true) excludes function-literal bodies from the enclosing function's score
when false. Output: one line per violation
(`<relative path>:<line>: <func> CC=N > max M` / `cognitive=N > max M`)
plus a summary (`N/M functions over their cyclomatic limit M`).

Cognitive complexity follows the SonarSource definition (v1.2) as the Go
grammar allows: +1 per flow break (`if`, `else if`, `else`, `for`,
`for … range`, every `switch` case and `default`, every `select` comm case
and `default`, and each *sequence* of identical `&&`/`||` operators —
`a && b && c` is one sequence, `a && b || c` is two), plus a nesting
increment of the current depth for every structural construct (everything
but the logical sequences) when it sits inside another one. `else if`
chains stay flat: each chained `if` increments once at the originating
`if`'s depth, while an `if` inside a plain `else` block keeps paying the
nesting penalty. Function literals nest one level deeper without
incrementing themselves; Go has no ternary, `while`, `do-while` or
`catch`-as-construct, and `goto`/labels are not flow breaks.

## 12. `nesting`

```
crap4go nesting [paths...]
```

Flags functions whose maximum block nesting level exceeds 5 (default
ported from crap4dart's `nesting` gate). The function body block counts as
level 1; every nested block or control-flow statement (`if`, `for`,
`switch`, type-switch, `select`, plain block statement) adds one.
Control-statement braces do not add a level on top of the statement
itself. An `else if` chain is flat since 7bf7a15: every chained `if`'s
branches nest at the originating `if`'s level, so a chain of any length
scores 2 — while an `if` inside a plain `else` block keeps paying a level
of its own. Output: one line per violation
(`<relative path>:<line>: <func> nesting=N > max 5`) plus a summary
(`N/M functions nested deeper than 5`). Exit code 2 iff violations.

## 13. `class-size`

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

## 14. `weight-of-class`

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

## 15. `unused-code`

```
crap4go unused-code [paths...]
```

Flags unexported package-level declarations — functions (methods
excluded), types, vars, consts — whose identifier never appears elsewhere
in the same package. References are counted lexically on unresolved ASTs
(all identifier occurrences, declaration included; a declaration is
unused when its identifier occurs exactly once). Declaring a private
declaration never removes its name from the reference set, so cross-class
same-package private access counts (crap4dart 0.7.1 regression covered).
Non-test files only, for both declarations and references (upstream
`unused_code` gate semantics). Output: one line per violation
(`<relative path>:<line>: <name> is never referenced`) plus a summary.
Exit code 2 iff violations.

**Partial selection:** an explicit path selection prints
`unused-code: not meaningful for a partial selection` and exits 0 —
a partial file set yields false positives (ported from crap4dart 0.5.1).

## 16. `unused-files`

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

**Re-exports:** crap4dart 0.7.1 counts `export` directives toward the
import graph; Go has no re-export concept (an import names the
implementing package directory directly), so nothing extra applies.

## 17. `banned-imports`

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
trailing `/**` also match zero directories; glob patterns are compiled to
regex once per pattern and cached, not per matched file (0.8.6 perf
port). With no rules the command
prints `no rules configured` and exits 0; `--from` without `--forbid`
(or more `--message` values than rules) is a usage error (exit 1).
Exit code 2 iff violations.

## 18. `magic-constants`

```
crap4go magic-constants [paths...]
```

Go adaptation of crap4dart's `magic_constants` gate (0.6.0, with the
0.7.2/0.8.3/0.8.4/0.8.6 precision fixes). Two checks:

- **Hex colors:** integer literals whose lexeme matches
  `^0[xX][0-9a-fA-F]{6,8}$` (`0xRRGGBB` / `0xAARRGGBB`) outside named
  constant declarations. Exempt: the line of every literal anywhere
  inside a `const` initializer's full subtree — nested calls, composite
  literals, binary expressions included (everything inside a const
  declaration is a named constant already).
- **Duplicates:** numeric (int/float, by raw lexeme) and string
  (unquoted value) literals whose value repeats at least 3 times in one
  file among its non-const-line occurrences — every such occurrence is
  reported with the filtered count. Occurrences on const lines do not
  count towards the minimum.

String literals in identifier position are never duplicate candidates
(protocol identifiers, not magic constants): direct map-literal keys
(`map[string]int{"k": 1}`), index operands (`obj["key"]`), and switch case
labels (`case "key":`). Values shorter than 4 characters are ignored;
char and imaginary literals have no upstream counterpart and never count.
Output: one line per violation
(`<relative path>:<line>: hex color outside a constant declaration` or
`<relative path>:<line>: literal <value> repeats N times — extract a named
constant`) plus a summary. Exit code 2 iff violations.

## 19. `test-assertions`

```
crap4go test-assertions [paths...]
```

Go adaptation of crap4dart 0.9's `test_assertions` gate (min 1 assertion
per test): flags `Test*` functions in `*_test.go` files whose bodies
contain neither a fail-capable `*testing.T` method call nor a `panic()`
call — a Go test that cannot fail is vacuous ("a test without assertions
assertions verifies nothing"). Counted calls: `Error`, `Errorf`, `Fatal`, `Fatalf`,
`Fail`, `FailNow` invoked through any identifier bound to a `*testing.T`
parameter — the test function's own parameter or a nested func literal's
(`t.Run` subtests) — plus bare `panic()` calls. `TestMain` (the harness
entry point), benchmarks, and fuzz functions are not tests and never
count. Upstream 0.9.4 fixed the body being picked as the `test(...)`
call's LAST argument, so trailing named args (`skip:`, `timeout:`) hid it
and caused false "0 assertions"; N/A in Go — test functions are
declarations with the compiler-enforced `func TestXxx(t *testing.T)`
signature, there is no call-site argument list to pick a body from, and
the gate reads the `FuncDecl` body directly (`t.Run` subtest bodies are
nested func literals and cannot be hidden either; covered by the subtest
case in `testassertions_test.go`). Selection: `*_test.go` files under the positional paths
(directories walked, non-test files dropped) or, without paths, the whole
root; `vendor/` trees are excluded. Output: one line per violation
(`<relative path>:<line>: TestFoo has 0 assertion(s) — a test without
assertions verifies nothing`) plus a summary. Exit code 2 iff violations.

## 20. `folder-structure`

```
crap4go folder-structure [--max-loose-files N] [--cap PATTERN=N]...
                   [--exclude GLOB]... [dirs...]
```

Go adaptation of crap4dart 753c3580's `folder_structure` gate: flags
directories containing more than their cap of logical non-test `.go`
units directly (non-recursive) — a flat-file sprawl that should be
organized into feature packages. Classic form: positional dirs (default:
the module root `.`, the port's default source root) share one cap,
`--max-loose-files 0` by default; a non-directory arg is a usage error.
Counting rules: only a directory's OWN files count — subdirectories are
the organized form and never reach the parent's budget. Generated
siblings of one source file collapse into a single logical unit:
`foo.go`, `foo.pb.go`, `foo.pb.gw.go` and `foo_string.go` share the stem
`foo` (protobuf and stringer output), and wholly generated
`zz_generated*.go` files share one unit per directory — the port's
host-generic generated-sibling set. `--exclude GLOB` (repeatable) drops
matching files from the count. Directory entries — positional dirs and
--cap patterns alike — are literal paths or glob patterns
(`internal/*`, `packages/**`) expanding to every matching directory;
hidden (dot-prefixed), `vendor` and `build` directories are never
enumerated, and a pattern matching nothing selects nothing. Per-pattern
caps: `--cap PATTERN=N` (repeatable) checks every directory matching
PATTERN against N — the CLI form of crap4dart's `config` map; a
directory matched by several patterns uses the FIRST pattern's cap, and
with --cap present the positional dirs and --max-loose-files are not
consulted (upstream precedence rule). Output: one line per violation
(`<dir>: N loose .go files directly in <dir> — group them into feature
packages (max <cap>)`) plus a summary. Exit code 2 iff violations.
Dogfooding: the flat single-package root holds 34 logical units (no
generated files, so collapse is a no-op here), capped at 34 — any new
root-level dump flags at once.

## 21. `duplicates`

```
crap4go duplicates [--threshold N] [--min-tokens N] [--min-lines N]
                   [--report percentage|clones] [--exclude GLOB]...
                   [--source PATH]... [--ignore-locals] [--ignore-literals]
                   [--ignore-identifiers] [--ignore-imports]
                   [--skip-duplicate-files] [paths...]
```

Go adaptation of crap4dart's `duplication` gate (§11.11): detects exact
copy-paste duplicates across Go source files. Every scanned file is
tokenized with `go/scanner` — comments and the scanner's auto-inserted
semicolons are dropped, everything else keeps its lexeme — so copies that
differ in whitespace, layout or comments still match. A duplicated block
is a sequence of at least `--min-tokens` (default 50) tokens spanning at
least `--min-lines` (default 5) source lines that appears at least twice,
within or across files: every file in the scan is indexed together with a
Rabin-Karp rolling hash over the token stream (the port convention for
upstream's hash window). Buckets of at least two windows become candidate
clone groups: each window is verified against the bucket reference by
actual token equality (rolling-hash collisions do not count), and each
occurrence is extended left and right to the maximal extent all
occurrences share — the reference stays anchored at its own window, and
the common length is the window size, so non-reference occurrences anchor
at the shared extent start. Candidates from the passes below compete in
one greedy selection — longest clone first, then most occurrences, then
position — and a group is accepted only when none of its occurrences
overlaps an already-accepted range of its file (the PMD CPD Match
semantics); accepted groups mark their tokens for the percentage report.

Opt-in normalizations extend detection to renamed clones and noisier
sources ("Type-2" renamed clones come from upstream 0599df2 + 94299e0;
the CPD-parity knobs from d6c3ab6). `--ignore-locals` renames
identifiers declared inside an outermost function scope — the receiver
name (a local binding; the fields it selects are API state), named
parameters and named results, type parameters, `:=` locals including
multi-assigns, range variables, and the bindings of function literals
nested anywhere in the body (nested literals share the enclosing scope) —
to `$L1`, `$L2`, ... in first-use order before hashing. Renamed clones
hash identically, while locals swapped against each other keep different
placeholders and never match. The API surface — called functions and
methods, types, field references, package-level identifiers — keeps its
lexeme. Go string literals are single opaque tokens (there is no
interpolation), so nothing else changes under this mode.
`--ignore-literals` replaces string literals with `$STR` and numeric
literals (int, float, imaginary, rune) with `$NUM` before hashing.
`--ignore-identifiers` replaces every identifier with one `$ID`
placeholder before hashing — structure-only matching (CPD
`--ignore-identifiers`); Go has no built-in/pseudo keyword ambiguity, so
masking `token.IDENT` covers the predeclared builtins (`len`, `append`,
`make`, `panic`, ...) the way upstream's mask list does. Keywords,
operators and literals keep their lexemes.
`--ignore-imports` excludes import declarations from tokenization — the
whole declaration (keyword, parens, every spec, blank imports included)
drops out, the boilerplate that otherwise inflates duplication.
`--skip-duplicate-files` analyzes a file whose token stream (by masked
value) equals an earlier file's only once, so a copied file is reported
as one clone group instead of N×N pairs (CPD `--skip-duplicate-files`).
Detection is a union of two passes: the raw-lexeme pass always runs
alongside the masked one when `--ignore-locals` is set, so exact copies
are never lost when enclosing scopes shift placeholder numbering, and
candidates from both passes compete in the same greedy selection. With
every flag off the gate reports exact copy-paste only.

Scan scope: the normal analyze selection (default walk or explicit paths),
unioned with every repeatable `--source` path resolved against the project
root — directories are walked recursively by the standard source rules,
plain files are taken directly when they have the `.go` extension, missing
paths are skipped silently. This is the dc64e9c cross-module mechanism:
the CRAP analysis stays scoped to the module while duplication spans
sibling packages or directories outside it. Files whose project-relative
path matches a repeatable `--exclude` glob (the port's glob convention)
are skipped; the default is the port's standard source exclusions,
`**/*_test.go` and `vendor/**`, replaced when the flag is given. Files
with fewer than `--min-tokens` tokens are skipped from the scan and its
summary.

`--report` selects what violations are reported for. `percentage` (the
default) keeps the historical behavior: a file violates when its
percentage of duplicated lines — distinct lines holding duplicated tokens
over its total line count — is strictly over `--threshold` (default 1.0);
output is one line per violation
(`<path>:<first duplicated line>: <pct>% duplicated lines > <threshold>%`)
plus a summary: pass `N files, P.PP% duplicated lines`, fail
`M/N files over T% duplication`, where N counts the tokenized files.
`clones` reports PMD-CPD-style clone groups: one violation per accepted
non-overlapping group (longest first), with every occurrence location in
the message — `clone: T tokens / L lines, K occurrences:
<path>:<first>-<last>; ...` (the first three locations, then
`(+N more)`) — plus a summary `K clone groups (>= T tokens, >= L lines),
P.PP% duplicated lines` or, on a clean scan, `no clones >= L lines across
N files, P.PP% duplicated lines` — the same project-wide duplicated-line
percentage the percentage report aggregates (distinct lines holding
duplicated tokens over all scanned files' total lines). Exit code 2
iff there are violations; `no files with enough tokens` with exit 0 when
no file clears the token minimum.

## 22. `mutation`

Go adaptation of crap4dart c865ec3's `mutation` gate (§11.22, adapted from
the unclebob/mutator research conclusions):

```
crap4go mutation [--coverage PATH] [--min-score F] [--max-survivors N]
                 [--timeout-seconds N] [--max-mutants N]
                 [--operators LIST] [--exclude GLOB]... [--cache]
                 [--test-args ARGS] [--diff-base REF] [--scope-tests]
                 [paths...]
```

A declarative, category-tagged operator table is matched on parsed
(unresolved) ASTs; replacement offsets come from go/token positions, never
regex:

- `arithmetic`: `+`↔`-`, `*`↔`/` (Dart's `/`→`~/` has no Go counterpart —
  Go `/` on integers already truncates)
- `boundary`: `>`↔`>=`, `<`↔`<=`, `>`↔`<`, `>=`↔`<=`
- `equality`: `==`↔`!=`
- `logical`: `&&`↔`||`
- `boolean`: `true`↔`false` (predefined identifiers in expression position)
- `constant`: integer `0`↔`1`, `-1`→`0` (the inner `1` also yields its own
  `1`→`0` site, as upstream's child recursion does)
- `unary`: delete a `!` prefix

Only executable code is walked: declaration positions (field names, labels,
selector fields) can never be literal sites. Each match is a mutation site
`{file, line, method, category, original, mutant}`.

A site whose containing method has no executed line — or whose line has no
positive cover-profile hit when no method contains it — is `uncovered`:
excluded from the score denominator and never executed. Every covered site
is mutated one at a time (one textual replacement at its exact token span,
the file restored after each mutant) and `go test ./...` re-runs against it:
a non-zero exit kills the mutant — compile-error mutants die the same way
because `go test` refuses to run a broken build — while a timeout counts as
survived (the suite did not kill the mutant deterministically).

The mutation score is `killed / (killed + survived)`; uncovered sites are
excluded. The gate fails — printing one violation line per surviving mutant
(`<path>:<line>: survived <category> mutant `<orig>` -> `<mutant>`[ in
<method>]` plus a summary `mutation score S.S% (K killed, S survived, U
uncovered); survivors by category: <category>=<count>...`) — when the score
is below `--min-score` (default 0.8) or more than `--max-survivors`
(default 0) mutants survive; exit code 2. Without coverage data, without
`*_test.go` files, or when `go` cannot spawn, the gate skips itself with
exit 0. `--timeout-seconds` (default 60) bounds each mutant,
`--max-mutants` (default 500) caps the executed sites deterministically
(file order, then offset), `--operators` selects a comma-separated category
subset, and `--exclude GLOB` (repeatable) skips matching project-relative
paths (default: none — the standard selection already skips `*_test.go` and
`vendor/`). The gate is disabled by default: it never runs in the analyze
flow and only executes when invoked as a subcommand — mutation testing runs
the whole suite once per mutant, so invoke it deliberately on fast suites.

Digest-incremental cache (port of crap4dart 16d9c4c): when `--cache` is on
(the default; `--cache=false` disables), per-method outcomes persist to
`<projectRoot>/.crap-mutation-cache.json` — a versioned JSON envelope
`{version, meta, methods}` keyed by method identity (project-relative file +
method name) and a digest (FNV-1a 64-bit via hash/fnv, zero new
dependencies) of the method's exact AST source span. On later runs, a
covered mutant whose method digest and site identity (method-relative
offset, category, original, mutant) match a previously `killed` outcome is
replayed — counted killed, reported as `cached` in the summary — and never
re-executed; survivors are always re-executed, because the test suite may
have improved. Changed or new methods re-execute fully; coverage gating is
re-derived from the run's fresh coverage profile and is never cached.
Offsets are stored method-relative, so an edit in an earlier method of the
same file invalidates only that method. The `meta` guard (tool version,
enabled operator categories, `test_args`) wipes the cache on any mismatch,
as do a missing, corrupt or outdated-version file. Entries of deleted or
excluded files are pruned after each run.

End-of-run tree integrity check (port of crap4dart 8b2e8e8): the engine
snapshots every file it will mutate before the loop and re-verifies the
whole tree in an outer finally — even when the loop died mid-mutant. A file
whose content drifted from its snapshot (external edit, killed process
between apply and restore) is re-restored and surfaced as
`; INTEGRITY: re-restored after run: <paths>` in the summary plus a stderr
warning: the tree is healed, but the run must be treated as suspect.

`--test-args "test -run TestFoo ./pkg/..."` (default `test ./...` — the
whole suite, matching upstream's `['test']`) is the full per-mutant test
invocation, so consumers can scope per-mutant runs to relevant test paths
on large repos; scoping is the consumer's responsibility — a mutant is
killed only if the scoped suite would kill it. Validation is strict: an
empty (all-whitespace) value is a usage error; args are split on
whitespace.

Diff-scoped execution (port of crap4dart 8228585): under
`--diff-base <ref>` the gate intersects the candidate sites with the
changed lines BEFORE executing, not merely filters reported violations
afterwards: a site runs only when its containing method intersects the
diff (falling back to the site's own line when no method contains it).
Sites outside the diff are neither executed nor counted, so a per-PR run
pays only for the mutants of the changed methods. The changed lines come
from `git diff --unified=0 <ref>` (fixed argv, no shell): each file's
added lines in the new file; deleted files (`/dev/null`), binary files
and pure-removal hunks contribute nothing. An unresolvable ref or a
non-repository project is a fatal error (exit 1, reported on stderr).

`--scope-tests` (default false) derives that per-mutant scoping
automatically from the Go layout: each mutant's invocation is narrowed to
the derived test file(s) of the mutated file (`dir/base.go` →
`dir/base_test.go`, the in-package convention), so the scoped invocation
is `go test ./<dir>` — the mutated file's own package — falling back to
`--test-args` when no candidate test file exists. A scoped kill is
monotone (the scoped invocation is a subset of the full suite) and is
cached as a regular kill: `scope_tests` is deliberately NOT part of the
cache meta guard. A scoped survivor — timeout included — is re-confirmed
once against the full invocation, and only the confirmed outcome is
reported and cached; the summary gains
`; test-scoped N (M re-confirmed killed)` when scoping confirmed kills.

## 23. `similarity`

```
crap4go similarity [--format console|json] [--jaccard F] [--containment F]
                   [--min-shingles N] [--include GLOB]... [--exclude GLOB]...
                   [--check] [dirs...]
```

Go adaptation of crap4dart 2b21966's similarity feature (§11.23):
file-level near-duplicate detection over any text corpus — copied docs
that drifted apart, twin templates differing by a heading, stale
snapshot copies, generated files duplicating their sources. Unlike
`duplicates` (§21, token-level clone blocks inside Go code), each
finding says "this file is (mostly) a copy of that file" — a structural
problem (reuse the shared source, generate one of them, or delete the
stale copy), not an extract-method refactor.

The upstream split — an opt-in gate inside `check` (yaml
`gates.similarity`, `enabled: false` default, kebab-case surface
dirs/include/exclude/min_shingles/jaccard/containment with strict loader
validation) plus an ad-hoc `similarity` command (threshold/include/
exclude overrides, console + JSON, always exits 0) — collapses into one
subcommand, the port's "yaml `enabled: false` becomes subcommand only"
adaptation: the default run IS the upstream command (always exits 0,
informational), and `--check` IS the gate — one violation line per
clustered file naming its counterparts (first two, then `(+N more)`)
and its strongest pair metrics, plus a summary, exit code 2 iff there
are violations, 0 on a clean scan. The upstream config template block
and gate-runner wiring have no counterpart (no config system, no check
pipeline); the yaml keys map to flags — `dirs` to the positional
directories (default `.`), `include`/`exclude` to repeatable flags,
`min_shingles`/`jaccard`/`containment` to value flags, and the loader's
strict validation to flag validation: `--jaccard`/`--containment` must
be in [0,1], `--min-shingles` positive, `--format` console or json —
violations are usage errors (exit 1).

Engine: every scanned file becomes a set of 8-token shingles
(`shingleSize`) of profile-normalized text. The profile comes from the
extension: `.md`/`.markdown`/`.mdx` are markdown — fenced code blocks
are dropped, only prose words compare; the code family (`.dart`, `.js`,
`.ts`, `.java`, `.py`, `.go`, `.rs`, `.c`, ... the upstream extension
list) strips line/block comments and import-family lines, lowercases
identifiers and maps literals to placeholders (`S` strings, `N`
numbers) so clones differing only in literal values still match;
anything else compares as-is (word tokens of 2+ latin/cyrillic
characters, lowercased — markdown uses the same word tokens). The Go
adaptation for `.go` files: the token stream comes from `go/scanner`
(the port convention, as in `duplicates`) — identifiers and keywords
lowercased, string/rune literals → `S`, numbers → `N`, comments and
operators never entering the stream, and whole import declarations
dropped (the Go import family: single and parenthesized forms,
blank/dot/alias specs included). Other code extensions keep the
upstream language-agnostic line shaping (comment/import regexes across
the `//|#|--|;` and import/require/using/... families). Fewer tokens
than one shingle yield an empty set.

A pair crosses when Jaccard (intersection over union) >= `--jaccard`
(default 0.8) OR containment (intersection over the smaller set —
catches a small file largely copied into a big one, where Jaccard stays
low) >= `--containment` (default 0.95). Files with fewer shingles than
`--min-shingles` (default 50) are skipped and counted in the report —
tiny files carry no reliable signal. Crossing pairs merge transitively
into clusters via union-find with path compression; clusters report
largest first, pairs strongest (max Jaccard) first, and every file's
metrics line names the strongest pair it participates in.

Scan scope: the positional directories, resolved against the project
root (absolute paths taken as-is), walked recursively — any file type,
filtered by include then exclude globs on the project-relative path;
missing directories are skipped silently. Defaults are the upstream
gate's: include `**/*.md`, exclude `.git/**`, `build/**`,
`coverage/**`, `node_modules/**` — with `.dart_tool/**` replaced by
`vendor/**` (the Go port's standard exclusion, as in `duplicates`).
Sort order is deterministic: files by path, clusters largest first with
ties in scan order.

Explorer output: a header (`similarity: N files scanned (M skipped
below K shingles), thresholds jaccard >= J / containment >= C`), then
`no similar files` or per-cluster blocks (`cluster I — F files:` with
one `  path (jaccard X.XX, containment Y.YY)` line per member) and a
`F files in C clusters` footer. `--format json` prints only JSON:
`scanned`, `skipped`, `thresholds` (`jaccard`, `containment`,
`min_shingles`), `pairs` (`a`, `b`, `jaccard`, `containment`, rounded
to four decimals), `clusters` (`files`).

Dogfooding (matching upstream's own md-corpus enablement at jaccard
0.6 / containment 0.9): this repo's corpus (README.md, spec.md,
CHANGELOG.md, 3 files, none below the 50-shingle floor) measures clean
at 0.6/0.9 — and still clean at 0.5/0.85 and 0.2/0.3, a wide margin.

## 24. `skill`

Prints a Go-adapted version of crap4dart's profiling skill (when to profile,
how the instrumentation works, how to read the report), ending with one line
on installing it as an agent skill. Always exits 0.

## 25. Threshold

The threshold defaults to `8.0` and is overridable with `--threshold`. After
the report is printed, the maximum numeric CRAP is compared against it; if
`max > threshold`, `CRAP threshold exceeded: <max> > <threshold>` is printed
to stderr and the process exits 2. Otherwise the process exits 0. A threshold
of `0` or less is a usage error.

## 26. Exit codes

| Code | Meaning                                                                  |
|------|--------------------------------------------------------------------------|
| `0`  | Success (including empty selection, or max CRAP ≤ threshold).            |
| `1`  | Usage error: bad flags, bad threshold, `--changed` + paths, unreadable.  |
| `2`  | Max numeric CRAP exceeded the threshold; `profile` total exceeded        |
|      | `--threshold`; gate subcommand violations (`file-naming`, `nesting`,     |
|      | `class-size`, `weight-of-class`, `unused-code`, `unused-files`,          |
|      | `banned-imports`, `magic-constants`, `test-assertions`,                  |
|      | `anon-struct-signatures`, `scattered-constants`, `duplicates`,           |
|      | `folder-structure`, `mutation`, `similarity --check`,                    |
|      | `string-hazards --severity error`,                                       |
|      | `identical-branches --severity error`).                                  |
|      | (Also reported on stderr.)                                              |

## 27. `--run-tests`

Runs `go test ./... -coverprofile=coverage.out -covermode=atomic` in the
project root, streaming stdout/stderr through. On non-zero exit, the error is
printed to stderr and the process exits 1.

## 28. Non-goals

- No type-checking or build verification — parsing only.
- No HTML/branch coverage reports — only the statement-level cover profile.
- No automatic fixing or refactoring of high-CRAP code.
- No external runtime dependencies (standard library only); no `go.sum`.
- No config file or gate framework; no gate severity/entries/baseline
  (crap4dart 0.5.0 framework features) — gates are standalone
  subcommands with hard-coded upstream defaults. The one framework flag
  this port does carry — per-gate `ignorable` — is likewise hard-coded
  (§32), not config; no profile
  `--tags`/`--exclude-tags` (no tag concept in `go test`) — all knobs are
  CLI flags.

## 29. `memory`

```
crap4go memory [--format console|json] [--top <N>] [--badge <path>]
               [--badge-threshold <MB>] [paths...]
```

Ported from crap4dart 3bae178bc0 (memory: per-test GC/alloc attribution
via `--verbose_gc`, phase 1) and 11551739c8 (per-isolate clocks +
unattributed bucket + `--badge`, phase 2). Allocation-volume reporting
follows b7c227d (0.17.0): reports gain `totalAllocatedMb` (JSON), an
`allocated` headline mention and the `alloc MB` column, and the churn
leaderboard is retitled `by allocation volume`. The engine half of
b7c227d (Flutter/VM Service measurement) is N/A here: `go test` is
spawned directly with the tool owning argv and env, so there is no
flag-swallowing middleware to route around. Runs the test suite under GC
tracing and attributes every GC event and its reclaimed memory to tests
and packages, then reports allocation-churn and old-gen-growth hotspots.

Tracing: `go test -v -count=1 -p 1 -exec 'env GODEBUG=gctrace=1'` —
verbose for the `=== RUN` / `--- PASS` attribution markers, `-count=1`
to bypass Go's test cache, `-p 1` so packages run sequentially, and the
go tool's `-exec` hook scoping `GODEBUG=gctrace=1` to the test binaries
only. GODEBUG is stripped from the command environment (an ambient
value, or `go env -w GODEBUG`, would otherwise trace the go tool and
every compile/link subprocess, whose GC rows would pollute the stream).
Positional paths are forwarded to `go test` exclusively; without them
`./...` runs.

Attribution is by stream position — the Go mapping of upstream's
isolate-switch logic: the go tool relays each test binary's combined
output live, so gctrace rows and the verbose markers interleave in one
ordered stream. `=== RUN <name>` opens a window; `--- PASS/FAIL/SKIP:
<name>` and `=== PAUSE <name>` close it (`=== CONT <name>` reopens a
paused `t.Parallel()` window; `=== NAME <name>` raises an open window to
the top of the stack). A GC row lands on the innermost open window —
subtest windows nest inside their parents, so the shorter span wins
(upstream's nested-window rule). A GC row with no open window is
UNATTRIBUTED: only rows INSIDE test windows are trusted, so
test-binary loading GCs (before the first `=== RUN`), teardown GCs
(after the last `--- PASS`) and rows between suites land in the
report's unattributed bucket — reported (console line, JSON
`unattributedGcs`/`unattributedChurnMb`), never polluting test or
package totals (upstream phase 2: rows outside a suite's test windows
never pollute file totals; killing test
`TestBoundaryGcDoesNotLeakIntoNextTest` — a teardown GC after the last
`--- PASS` must not leak into the last test or the package entry).
A `?  <pkg>  [no test files]` summary discards pending rows entirely:
no test binary ran for that package, so they are not even suite churn
(`TestDiscardBeforeNoTestFiles`). Because `-p 1` runs one test binary
at a time, a pending bucket can never span two binaries. Upstream's
phase-2 seq-continuation guard (a row continuing the previous isolate's
GC numbering returns to it) has no Go analog: there is one stream and
the window stack already routes rows; a row after `--- PASS` but before
the next `=== RUN` is unattributed by construction (killing test
`TestBoundaryGcDoesNotLeakIntoNextTest`).

Markers are matched at ANY indentation, and nesting comes from the
RUN/close stack, not from indentation: subtest result lines print
indented four spaces per depth (`    --- PASS: TestX/sub (0.00s)`,
after the parent's unindented result — verified go1.27.2 shapes), and a
column-0-only matcher would leave every subtest window stuck open,
leaking all later GCs of the package into the innermost stuck subtest
(killing test `TestIndentedSubtestBoundary`).

The two output streams are merged by the go tool's relay at arbitrary
byte boundaries, so under load a gctrace row can tear mid-row or get
glued onto a marker (both observed in real captures). Rows and markers
are therefore recovered by SHAPE anywhere in a physical line: a complete
row followed by glued marker text yields both; a row torn mid-numbers is
a fragment — its churn is honestly lost and it must never parse as a row
or swallow a glued marker; a result word without its duration is dropped
(Killing tests in `TestTornRowsKeepChurnAndMarkers`).

There is no clock-offset machinery: upstream's phase 1 voted one global
offset per suite from (last GC − last test end) at the 90th percentile
because the Dart VM's GC clock and the JSON reporter's wall clock are
different clocks, and its phase 2 (11551739c8) replaced that vote with
per-suite windows anchored at the isolate-clock zero — the GC `start`
column counts from ISOLATE start, so the suite event's `time` is each
suite's window origin. The Go port needed neither: markers and GC rows
share one stream and one clock, so attribution is pure stream position
(phase 1 never grew offset code, so phase 2's clock rework has nothing
to port) and the JSON omits `clockOffsetSeconds`.

Metrics (documented divergence from upstream's new/old-space split —
gctrace prints one heap triple `heapStart->heapEnd->heapLiveAfter MB`
per GC):

- churn = Σ max(0, heapStart − heapLiveAfter) over the attributed rows:
  the volume the GC cycle reclaimed, the honest allocated-MB proxy
  (upstream: `reclaimedNewMb = newBeforeMb − newAfterMb`). gctrace
  exposes no cumulative allocation counter out-of-process (one engine,
  one runtime), so `totalAllocatedMb` (upstream b7c227d's exact-bytes
  total on the Flutter engine) reports the churn value on Go; per-entry
  JSON is unchanged.
- old-gen growth = Σ (heapLiveAfter − previous GC's heapLiveAfter) over
  the attributed rows, baseline 0 at each test binary's first GC — the
  baseline resets on every package flush because each package runs as
  its own process (upstream: `oldAfterMb − oldBeforeMb`; Go's trace has
  no old-gen field, so retained-heap growth across GCs is the analog —
  positive and never freed = leak suspect).

Output: the console format prints a summary line (`, allocated N MB`
appended when the churn total is above zero), the unattributed bucket
line (`unattributed (outside test windows): N GCs, X MB`) and two
leaderboards (top N by allocation volume — `alloc MB` column holding
the churn proxy — top N by old-gen growth, `--top` default 20, entries
without GC activity excluded); `--format json` writes one JSON document
to stdout (`testCount`, `gcCount`, `totalChurnMb`, `totalAllocatedMb`,
`totalOldGrowthMb`, `unattributedGcs`, `unattributedChurnMb`,
`packages[]`, `tests[]` — labels `label`/`package`/`gcs`/`churnNewMb`/
`oldGrowthMb`, values rounded to one decimal) and keeps stdout
JSON-only, progress on stderr.

Badges: `--badge <path>` writes a shields-style SVG badge
(`memory churn | <N> MB`) after the report — the suite churn (every
parsed row, bucket rows included) rounded to whole MB; green at or
below `--badge-threshold` MB (default 500), yellow at most twice it,
red beyond (killing tests `TestBadgeColorForThresholds` for the
boundaries, `TestRunMemoryCommandBadge` for the file). The repo's
`badges/memory.svg` tracks crap4go's own suite: it is written by
`crap4go memory --badge badges/memory.svg` (manual/self-analyze) and
committed — the CI commit-badges workflow refreshes only
`badges/crap.svg` and `badges/coverage.svg`, never `memory.svg`.

Exit codes: informational — always 0 (failing tests do not change it);
usage errors exit 1 (bad flags or values, no `go.mod` in the project
root, no tests executed).

Dogfooding (crap4go's own suite, `memory --badge badges/memory.svg`):
294 tests (equal to the suite's result-marker count, subtests included);
three runs measured 3 GCs / 9.0 MB, 4 GCs / 12.0 MB, 3 GCs / 9.0 MB —
every row reclaims the same 3.0 MB, so per-test churn is stable modulo
which window happens to catch the machine-timed GC (the leaderboards are
ties that swap; the middle run's extra row fired under load). The
unattributed bucket was empty in every run — with `-p 1` a light test
binary GCs only inside its tests, which is exactly the trusted case.
Upstream's measured insight holds: churn per row is deterministic (a
function of the code) while GC counts are machine-dependent — churn is
the future gate metric, never counts.

## 30. `anon-struct-signatures`

```
crap4go anon-struct-signatures [--max-fields N] [paths...]
```

Go adaptation of crap4dart 4d844a4's record gates (wave 6: named fields,
no inline record types, arity cap), collapsed into one gate per the port's
one-command convention. Dart records do not exist in Go; the honest
host-language analog of an anonymous record shape in a signature is the
anonymous struct type, and upstream's inline-record and unnamed-fields
kinds collapse into it: every `struct{...}` written inside a function
signature is inline (no named declaration to point at) and its fields are
shape-diffed positionally by consumers. Upstream's third kind — positional
field access (`.$1`, `.$2`) — is N/A: Go has no positional field access
(t[0] is universal indexing and would fire on every slice), so nothing is
ported for it.

**Flagged:** an anonymous struct type (`*ast.StructType`) reachable from a
signature's parameter or result types — `FuncDecl.Type`, `FuncLit.Type`,
and function-typed `TypeSpec` declarations (a named function type blesses
nothing, mirroring upstream's "function-type typedef blesses nothing": the
anonymous shape inside `type Handler func() struct{ OK bool }` is still
flagged). Types nested inside slice, map, chan, pointer and generic type
arguments are included (one walk per type expression). Only the outermost
anonymous struct of a nested group is reported — an inner one is fixed by
the same named-type extraction.

**Never flagged (blessed forms and N/A kinds):**

- Named struct types referenced by name (`type Point struct{...}` then
  `func dist(a, b Point) float64`) — the named declaration form upstream
  blesses; the gate sees an identifier, not a shape.
- Unnamed multiple results (`func div(a, b int) (int, int)`) — Go's core
  idiom; positional shapes in a result list are plain named types, not
  anonymous structs, so the unnamed-fields kind has nothing to attach to.
- Anonymous struct values in bodies (`v := struct{ X int }{1}`) — record
  values stay legal upstream too; only signature shapes are scanned.
- Interface method signatures are not scanned (no scanned counterpart
  upstream); receivers are not scanned (the receiver type names the
  method's owner, it is not part of the call signature).

`--max-fields N` (default 5) folds upstream's `record_size` cap into the
same finding: a flagged anonymous struct with more than N fields says so
in its message. Values below 1 are a usage error (exit 1).

Output: one line per finding —
`<relative path>:<line>: anonymous struct type in signature: declare it as
a named type and reference it by name`, or `anonymous struct type with N
fields (max M) in signature: ...` for oversized shapes — plus a summary
(`N signatures checked, no anonymous struct types in signatures` on a
clean run, `M anonymous struct types in K signatures` otherwise). Exit
code 2 iff there are violations, 0 on a clean or empty selection, 1 on
usage errors (bad flags, `--max-fields < 1`).

Killing tests: `TestRun_AnonStructFlagged` (nested-composite walk — drop
the descent into slice/map/generic type arguments and the nested case
passes), `TestRun_MaxFieldsBoundary` (`--max-fields 2`: a two-field shape
must carry the plain message, a three-field shape the oversized one — an
`>=` off-by-one flips both). The blessed forms are pinned by
`TestRun_NamedTypesBlessed` and `TestRun_UnnamedMultipleResultsLegal`
(even at `--max-fields 1`); `TestRun_FuncTypeAliasBlessesNothing` and
`TestRun_AnonStructValuesLegal` pin the alias and body-value rules.

Dogfooding: on crap4go's own source the gate flagged 4 shapes — the
`map[string]struct{}` set idiom in similarity.go signatures — promoted to
the named `shingleSet` type in the same commit; the gate now passes with
443 signatures checked.

## 31. `scattered-constants`

```
crap4go scattered-constants [--min-files N] [--min-length N] [--min-value N] [paths...]
```

Go adaptation of crap4dart e766d68's `scattered_constants` gate
(§11.13.1, extended by a314b485): the corpus-wide sibling of
`magic-constants`. Where magic-constants counts in-file repeats only —
a literal used once per file in K files is invisible to it —
scattered-constants scans the whole selection, keys literal values
semantically, and reports three finding kinds:

- **Constant regression.** The corpus already declares a named constant
  for the value: every hardcoded re-introduction — a bare literal or a
  duplicate declaration, at any occurrence count, even in the declaring
  file — flags immediately (no `--min-files` wait), naming the constant
  and its `file:line` to reference. The first declaration wins when
  several share a value; only the winning declaration itself is exempt
  from its own rule.
- **Scattered literal.** No constant exists and the value appears in at
  least `--min-files` distinct files (default 3): one violation per
  occurrence — the "change 86400 in every service" hazard that in-file
  repeat counting cannot see. The message carries the scope (files and
  occurrences), the extract-and-verify fix shape, and the verification
  clause: all occurrences must mean the same thing.
- **Pure value alias** (from crap4dart a314b485's `aliases` rule). An
  unexported const whose initializer is another constant's bare
  identifier — `const kAlias = kBase` — adds a name without
  information; reference the shared constant directly. Exported
  declarations are exempt (re-export shims are a deliberate API shape),
  the blank identifier is exempt (`const _ = kY` introduces no name to
  lose), and computed initializers are never aliases. Go adaptation:
  `true`, `false` and `iota` are Go's predeclared constant identifiers
  — boolean literals and the enum machinery — so initializing from them
  is computed, not aliasing (Dart parses those as literal AST nodes;
  Go's grammar makes them identifiers); an iota-block continuation has
  no initializer and nothing to alias. `--aliases` (default on)
  switches the rule off with `--aliases=false`; the flag exists only on
  this subcommand — every other gate's flag set rejects it, mirroring
  upstream's strict unknown-key validation.

**Registry (Go adaptation).** Upstream registers `const` declarations.
Go's referencable named-value forms are top-level `const` (single or
block) and top-level `var` with a plain string/numeric initializer —
both register. Function-local `const` declarations cannot be referenced
across files: they count toward the scattered minimum but never anchor a
reference. Computed initializers (`const s = a + "b"`) register nothing;
their inner literals are ordinary sites. The registry obeys the same
value filters as the sites — a below-threshold value registers nothing,
because no site for it can ever exist to regress. Upstream a314b485 had
to retrofit exactly this (its Dart corpus collector bypassed `minValue`
and the decl-initializer path skipped the guards); this port's shared
filter has enforced both since e766d68, and
`corpus_honors_min_value` plus `decl_initializer_min_value_boundary`
lock them.

**Semantic comparison:** strings key by unquoted value; numbers key by
value, so digit separators (`1_000`), bases (`0x3E8`) and float
spellings (`1000.50` vs `1000.5`) do not split a fact.

**Never counted:** strings shorter than `--min-length` (default 4),
numbers with magnitude at or below `--min-value` (default 100 — 0, 1,
10, 100 happen everywhere), char and imaginary literals (no upstream
counterpart), import paths (structural references — Go import paths are
string tokens in the AST, so the walk skips `ImportSpec`), string
literals in identifier position (map keys, index operands, case labels —
the same protocol-vocabulary rule as magic-constants), and struct tags
(Go adaptation: tags hold protocol wire names and must stay literals).
Excluded by default: `*_test.go` files (upstream's `test/**`) and
generated files — those carrying the `// Code generated ... DO NOT
EDIT.` marker comment (Go's generated-code convention, the analog of
upstream's `**.g.dart` exclude globs). Excluded files leave both the
registry and the counts entirely.

Output: one line per violation —
`<relative path>:<line>: hardcoded <value> duplicates constant `K`
(<file>:<line>) — reference the constant instead of repeating the
literal`, `<relative path>:<line>: literal <value> repeats in N files
(M occurrences) — extract a shared constant and reference it from every
site; verify all occurrences mean the same thing first`, or
`<relative path>:<line>: alias `<name>` adds no information — reference
`<target>` directly` — plus a summary. Exit code 2 iff there are
violations, 0 on a clean or empty selection, 1 on usage errors (bad
flags, `--min-files < 2`, `--min-length < 1`, `--min-value < 0`).

Killing tests (scatteredconstants_test.go):
`in-file_body_repeats_never_flag` in one and two files (the core
distinct-file semantic — an in-file repeat count would flag the six
occurrences), `two_distinct_body_literals_stay_clean` plus
`three_distinct_files_flag_per_occurrence` (the 2-vs-3 `--min-files`
boundary — an `>=` off-by-one flips both),
`registry_rule_fires_on_one_file_with_no_min-files_wait`,
`re-introduction_flags_even_in_the_declaring_file` and
`duplicate_local_const_flags` (the constant-regression rule: no
min-files wait, winner self-exemption, duplicate declarations caught),
`first_declaration_wins_on_shared_values`,
`digit_separators_and_number_bases_are_one_fact` (semantic comparison
through the registry), the exemption quartet (protocol positions,
import paths, struct tags, small values across three files each),
`corpus_honors_min_value` (a small-int const cannot anchor regressions —
dropping the minValue guard flags the two bare uses) and
`decl_initializer_min_value_boundary` (const-decl initializers through
the same guard: a below-threshold triple stays clean, an
above-threshold triple is winner plus two regressions), plus the
aliases set (a314b485): `private_alias_flagged` (top-level and
function-local), the `alias_exemptions` quartet (exported shim,
computed initializer, predeclared true/false/iota, blank identifier),
`aliases_flag_off`, `parse_scattered_flags_aliases_default` (default
on), and `run_scattered_constants_aliases_flag` (command wiring:
default on, `--aliases=false` off, another gate's flag set rejects the
flag).

Dogfooding: the first run flagged 63 scattered literals — the
copy-pasted gate-subcommand vocabulary (`"%s:%d: %s\n"`, `"%s: %s\n"`,
`No Go files to check.`, the `--exclude` flag registration with its help
text, `_test.go`, `0o644`, the `go test` subcommand/package vocabulary,
the `--threshold` flag name) consolidated into shared constants in
cli.go, files.go and runtests.go in the same commit. The regression rule
then caught memory.go hardcoding similaritygate's `console` format
default twice — both now reference `defaultSimFormat`. Accepted
remainder (1 finding): memory's `--badge-threshold` default 500 is a
coincidence with mutation's `defaultMutationMaxMutants = 500` —
different facts (MB vs mutant count) sharing a value, and the gate's
verification clause puts that call with the human (upstream's
self-config accepts the same class).

The a314b485 port re-ran the dogfood with the aliases rule active: 4
findings before (the 500 residue plus a new `vendor` scatter — 3 walk
skip sites in files.go, testassertions.go and folderstructure.go; all
pre-existed on main, and the codebase holds no pure value aliases to
inline). `vendor` consolidated into `vendorDirName` (files.go's shared
file vocabulary, referenced by all three walkers). After: 1 finding —
the documented 500 remainder.

## 32. Ignore comments

Ported from crap4dart 8abb64c (`ignore comments: gate-specific
granularity`, #21) together with the legacy blanket system it extends.
Every gate subcommand (§10–§31) drops suppressed violations from its
output and exit-code aggregation through one shared filter
(`ignorefilter.go`) — no per-gate copies.

```
// crap:ignore             legacy blanket, line scope
// crap:ignore-file        legacy blanket, file scope (first 5 lines)
// ignore:<gate-id>        granular, line scope
// ignore-file:<gate-id>   granular, file scope (first 5 lines)
```

Semantics (upstream-verbatim; `<gate-id>` is this port's subcommand
name — `magic-constants`, not `magic_constants`):

- **One rule per marker, Dart lint style.** Each granular marker names
  exactly one gate; several rules stack as several markers, and several
  markers on one line union their id sets (the id ends at whitespace or
  the next comment start — regexes `//\s*ignore:([^\s/]*)` and
  `//\s*ignore-file:([^\s/]*)`).
- **Empty id is blanket.** `// ignore:` and `// ignore-file:` suppress
  every ignorable gate, matching the legacy markers' scope.
- **Comma lists are inert.** `// ignore:a,b` captures one id `a,b`,
  which is simply unknown — it matches nothing, like any unknown id
  (`// ignore:typo`). Unknown ids never error.
- **Line scope** covers the violation's own line (a trailing marker
  counts) and the code line that ends a run of whole-line ignore
  comments directly above it — stacked `// ignore: a` / `// ignore: b`
  lines all apply; the walk stops at the first non-marker line. A line
  is a whole-line marker iff its trimmed form starts with `// ignore:`
  or `// ignore-file:`. Legacy `// crap:ignore` suppresses wherever it
  appears in that window.
- **File scope** reads only the first 5 lines. The legacy blanket is
  the literal `crap:ignore-file`; a line-scoped `// crap:ignore` in the
  leading lines grants no file scope. Violations without a line number
  respond to file markers only.
- **Opt-in per gate.** Upstream gates carry `ignorable: true` in the
  YAML config, default off. This port has no config file (§28), so the
  opt-in is the hard-coded `ignorableGates` registry. Unreadable or
  empty files are never suppressed.

**Ignorable mapping.** The 13 line-anchored gates — `complexity`,
`nesting`, `class-size`, `weight-of-class`, `unused-code`,
`magic-constants`, `test-assertions`, `scattered-constants`,
`duplicates`, `banned-imports`, `anon-struct-signatures`,
`string-hazards`, `identical-branches` — honor line
and file markers. `file-naming` and `unused-files` violations carry no
line, so they respond to file markers only (their upstream
counterparts are ignorable-capable the same way). Not ignorable, with
reasons: `folder-structure` violates directories — no readable lines,
so no marker can ever name one (upstream's flag table admits it too,
to the same null effect); `similarity` reports clusters — a suppressed
file is still named by its peers' counterpart messages; `mutation`
prints count-bearing score/summary aggregates that filtering would
desync from the printed list; `memory`/`profile` are informational and
never fail. The `duplicates` summary line keeps its pre-filter count
(upstream likewise passes `summary` through untouched).

Killing tests (ignorefilter_test.go): `granular_marker_silences_only_
its_gate` — the three-violation-line shape, `var dead = 0xFF00AA`
violating magic-constants (hex outside const), unused-code (dead
unexported var) and scattered-constants (value already registered as
`Brand`) on ONE line; `// ignore:magic-constants` silences exactly its
rule, the other two still exit 2. `stacked_markers_above_apply_walk_
stops` (two whole-line markers above both apply; a non-marker comment
between stops the walk). `file_scope_first_five_lines` (separate
markers union, empty-id blanket, line 5 counts, line 6 does not).
`legacy_parity` (`// crap:ignore` trailing, `// crap:ignore-file`
leading, and no file scope from a leading line marker).
`comma_inert_unknown_noop`. `lineless_violations_file_scope_only`
(file-naming: file marker suppresses, line marker cannot).
`opt_in_registry` (a non-ignorable gate is never filtered, even by
blanket markers; unreadable files stay flagged).

Dogfooding: the memory `--badge-threshold` default `500` — §31's
accepted residue (coincidence with `defaultMutationMaxMutants`, MB vs
mutant count) — now carries a trailing
`// ignore:scattered-constants` on memory.go's flag registration.
Before: `memory.go:587: hardcoded 500 duplicates constant
`defaultMutationMaxMutants``, exit 2. After: exit 0. The exception is
enforced by the gate instead of prose.

## 33. The `reorder` command

`reorder [--dry-run] [paths...]` (ported from crap4dart 24a6f24f,
adapted to Go's declaration model) shall rewrite each listed Go file
(default selection: the standard rules — non-test, non-vendor `.go`
files under the current directory; an explicit file path is honored
verbatim) so its top-level declarations appear in the canonical order:
imports, constants, variables, types, functions. Go has no class
bodies, so unlike the Dart original (whose canonical scope is the class
member list) the canonical scope here is the whole file's declaration
list — the de-facto Go convention — and the Dart private-method
category has no analog: Go's convention does not split
exported/unexported at file scope, so functions are one category.

Members within one category shall keep their original relative order (a
stable partition — the command never sorts alphabetically). Doc
comments and whole-line comment blocks above a declaration shall move
with it — including a block separated from the declaration by a single
blank line; two or more blank lines detach the block, and a detached
block between two declarations shall stick to the preceding one, so a
rewrite never drops a comment (a deliberate deviation: upstream's gap
rewrite silently discards unattached middle blocks; Go code carries too
many section comments for that). Multi-line `/* */` blocks attach
forward when directly above a declaration (the parser's doc group);
detached from the declaration below, their tail lines are not
whole-line comments, so nothing claims them and the gap glue keeps
them with the preceding declaration. A comment group starting on a
declaration's line — a trailing same-line comment and its contiguous
continuation lines — belongs to that declaration, wherever the parser
would report it. `//line` directives ride with the declaration they
precede, and the transform shall index raw, directive-unadjusted
positions (`PositionFor(_, false)`): a cgo- or generator-style
directive shifts the parser's adjusted line numbers away from the raw
file, and indexing the line table with them is out of range (a crash
would violate the never-fatal contract). Build tags, the package doc
and everything before the first declaration stay put (the first
declaration's walk never goes above the package clause), as does
everything after the last declaration.

The enum rule maps to Go's grouped declarations: a parenthesized
`const`/`var`/`type` block moves whole — its internal spec order
(`iota` arithmetic, parallel assignments) and struct field order are
semantics and are never touched. Package-level var initialization is
dependency-driven (Go spec), not source order, so moving whole var
declarations cannot change observable initialization; the stable
partition preserves the relative order of functions, so several
`init()` functions in one file keep their execution order.

The transform shall be deterministic and idempotent: the same input
shall produce the same output, already-canonical files shall be left
byte-identical (whatever their layout — declarations sharing a line via
explicit semicolons and odd spacing are preserved as written), and
reordering an already-reordered file shall not change it. Only the
rewritten span is normalized: slices join with one blank line in the
file's own line-ending style (LF or CRLF, preserved), and a rewritten
LF file is then passed through gofmt's own printer (`go/format.Source`)
so the output is gofmt-clean by construction — reordering can create
new adjacency groups of one-line declarations whose columnar body
alignment no longer matches; CRLF files skip that pass (the printer
emits LF only) and keep their line endings. Already-canonical files are
never reformatted. With `--dry-run`
(`-n`) the command shall write nothing, list the files that would
change, and exit 2 when at least one would (0 otherwise); without it,
files are written and the command exits 0. Files that fail to parse
shall be skipped with a note and shall not fail the run.

Killing tests (reorder_test.go): `TestReorderCanonicalSequence` /
`TestReorderStableWithinCategory` (order + stable partition),
`TestReorderCanonicalByteIdentical` (canonical files untouched whatever
their layout), `TestReorderGroupedBlocksMoveWhole` (iota block whole),
`TestReorderInitRelativeOrderPreserved` (init() order),
`TestReorderDocCommentsMoveWithDecl` / `TestReorderOneBlankAttaches` /
`TestReorderTwoBlanksDetachToPreceding` (one-blank attach, two-blank
detach, no comment dropped), `TestReorderTrailingSameLineCommentRides`,
`TestReorderPreambleNeverEaten` (build tag + package doc), 
`TestReorderSameLineDecls` (explicit-semicolon shared lines),
`TestReorderCRLFPreserved` + `TestReorderCRLFCommandRoundTrip`
(line-ending preservation), `TestReorderMultiLineBlockDocMoves`,
`TestReorderLineDirectiveNoPanic` (raw positions under `//line`
directives — the directive rides with its declaration, a leading one
stays in the preamble), `TestReorderMultiLineBlockInGapNotDropped`
(two-blank-detached multi-line block glues to the preceding
declaration, never dropped), `TestReorderFirstDeclOneBlankAttach`
(comment one blank above the first declaration rides with it; floor is
the package clause line),
`TestReorderParseErrorsAndTinyFiles`, the `reorderFixtures` invariants
battery and `TestReorderRealRepoFiles` (idempotence + token-multiset
preservation + parseability over a fixture corpus and real repository
files), and the command-level `TestRunReorderCommand*` (dry-run exit 2
without writing, write + exit 0, second run already-canonical,
parse-error skip is not fatal, default selection excludes test/vendor
files while explicit paths are honored).

Dogfooding: `go run . reorder .` reorders the port's own source; the
second run is a no-op (repo-scale idempotence proof), `gofmt -l`
reports nothing afterwards, and the suite stays green.

## 34. `string-hazards`

```
crap4go string-hazards [--severity warning|error] [paths...]
```

Go adaptation of crap4dart 62fec6833's `string_hazards` gate: flags
string literals whose escaping or pattern usage contradicts what the
author almost certainly meant, from the syntax tree only (parse errors
skip, like every gate). Upstream has three finding kinds; Go hosts two —
Go has no string interpolation, so upstream's escaped-interpolation
shape (its `\$name` guarded by sibling-interpolation evidence) has
nothing to escape and is omitted with no false-positive risk. Severity
is the first warning-default mapping in the port: upstream defaults
`gates.string_hazards.severity: warning` (findings print but the run
passes), and with no gate framework the port maps that config key to
the `--severity` flag — `warning` (default) prints findings and exits
0, `error` exits 2 on findings; any other value is a usage error (exit
1). The ignorable-marker system (§32) is the quiet-down mechanism:
`string-hazards` is in the `ignorableGates` opt-in registry, so
`// ignore:string-hazards`, `// ignore-file:string-hazards` and the
blanket markers suppress findings.

**Raw regexp with an escaped anchor** (upstream kind 2). A raw
(backtick) string passed as the source argument of
`regexp.Compile`, `regexp.MustCompile`, `regexp.CompilePOSIX` or
`regexp.MustCompilePOSIX` (any alias-blind receiver named `regexp`, the
port's textual stdlib-call convention) that ends with `\$` or starts
with `\^` is flagged: the backslash survives into the pattern, so it
matches a literal dollar/caret where the end/start anchor was almost
certainly intended (a plain `$`/`^` is the anchor). Interpreted string
sources are spared — in valid Go they cannot carry an escaped dollar,
the exact moral of upstream's spare of non-raw `RegExp` sources (a
parser-accepted invalid escape unquotes to an error and is skipped).
The check is value-shaped on the literal's own text: `\\$`-style
spellings fall under it just as upstream's `endsWith(r'\$')` does.
Upstream's `const`/`new RegExp(...)` instance-creation arm has no Go
counterpart (no constructor form takes a source); non-literal sources
are never judged, like upstream.

**Regex anchors in literal string patterns** (upstream kind 3).
`strings.Replace`, `strings.ReplaceAll`, `strings.Split` and
`strings.SplitN` match their pattern argument (the second argument) as
literal text, so a leading `^` or trailing `$` can only ever match that
literal character — a regexp was probably intended. `strings.TrimPrefix`
checks the leading `^` only and `strings.TrimSuffix` the trailing `$`
only: a prefix's match position is the string start, so an end anchor
in a prefix (or a start anchor in a suffix) cannot be an anchor mistake
— the position-aware narrowing of upstream's check-both-on-one-literal
rule to the functions whose semantics make the mistake possible. The
judgment is value-based after unquoting (raw and interpreted literals
alike — unlike Dart, a Go interpreted string can end in a plain `$`).
Upstream's adjacent-string arm (first part checked for `^`, last for
`$`) has no Go counterpart: concatenations are binary expressions, not
literals, and skip like upstream's non-literal arguments. Only
`strings`-package receivers are matched (same textual convention);
`foo.Replace(...)` on other receivers stays quiet — unlike upstream,
which flags any receiver's `.split(...)`/`.replace*(...)`, because Go
method sets make same-named methods on foreign types common and the
stdlib functions are the regex-confusion surface.

Output: one line per finding —
`<relative path>:<line>: <message>` (messages adapted verbatim from
upstream: `raw regexp pattern ends with '\$' — in a raw string that is
a literal '$' character, not the end anchor; did you mean '$'?`, the
`\^`/start-anchor twin, and `literal pattern starts with '^' — string
patterns match literally, so this can only match a literal '^'
character; a regexp was probably intended` / the trailing-`$` twin) —
plus a summary (`N string hazard(s) in M files`, or `M files free of
string hazards`). Exit code 0 on findings at the default warning
severity, 2 with `--severity error`, 0 on a clean or empty selection,
1 on usage errors (bad flags, unknown severity).

Killing tests (stringhazards_test.go), all non-vacuous — every fixture
triggers outside the harness, and mutation-removing a check fails its
test: `raw_regexp_trailing_escaped_dollar` (MustCompile, end-anchor
message), `raw_regexp_leading_escaped_caret` (Compile, start anchor),
`raw_regexp_posix_variant` (MustCompilePOSIX wiring), the spared
quartet `raw_regexp_plain_anchors` / `regexp_interpreted_source`
(non-raw spare) / `regexp_non_literal_source` / `regexp_aliased_
receiver` (receiver-match guard), `replace_anchored_patterns` (Replace
`^` + ReplaceAll `$`), `split_anchored_separators` (Split/SplitN),
`trim_anchored_positions` (TrimPrefix `^` flagged, TrimPrefix `$`
spared, TrimSuffix the mirror image — the position table), 
`replace_both_anchors` (two findings on one literal), 
`replace_clean_patterns` (plain patterns, non-literals, foreign
receiver), and the command-level `TestRun_StringHazards*` (warning
default exits 0 with findings printed, `--severity error` exits 2,
unknown severity exits 1, granular and blanket ignore markers silence
findings even at `--severity error`).

Dogfooding: `go run . string-hazards .` over the port's own source
reports `38 files free of string hazards` — the repo's raw regexps use
plain anchors (`^0[xX]...$`, glob patterns) and its strings.Replace/
TrimPrefix calls carry plain literals, so nothing to fix and nothing to
ignore.

## 35. `identical-branches`

```
crap4go identical-branches [--severity warning|error] [paths...]
```

Go adaptation of crap4dart c6b2425add's `identical_branches` gate: the
copy-paste-edit bug family — syntactically identical pieces inside one
construct where a second, slightly different piece was intended. All
pieces compare by **canonical form**: whitespace-, comment- and
parenthesis-independent (parenthesized operands unwrap; leaves render
through `go/printer`, so comments never enter a key), operands of
commutative operators (`==`, `!=`, `&&`, `||`, `+`, `*`, `&`, `|`, `^`)
sort, and same-operator nests of the associative operators (`&&`, `||`,
`+`, `*`, `&`, `|`, `^`) flatten — `a == b` matches `b == a` and
`a && b` matches `b && a`. Non-commutative operators (`<`, `>`, `<=`,
`>=`, `-`, `/`, `%`, shifts, ...) keep their operand order and tree
shape: `a - (b - c)` never equals `(a - b) - c`. Unaries canonicalize
as `op(operand)`.

Four of upstream's six finding kinds host in Go:

1. **Duplicate conditions** — two links of one `if`/`else if` chain
   whose conditions compare equal; every later branch is unreachable.
   An `else if` link is part of the chain rooted above it (only chain
   heads scan); an `if` nested inside an `else` *block* starts its own
   chain; `switch` is excluded — the compiler already rejects duplicate
   constant cases.
2. **Duplicate branch bodies** — two bodies in one chain whose
   statements compare equal by token text; a single-statement block
   unwraps to its statement, so brace style cannot hide the repeat and
   the violation points at the statement's line (Go has no naked
   statement form, so upstream's block-equals-naked rule lands entirely
   in the unwrap).
3. **Duplicate and absorbed operands** — one `&&`/`||` chain whose
   flattened operand list contains a repeated canonical operand
   (`a || a` collapses to `a`; `f() || f()` double-calls by accident),
   or an operand that also appears standalone as another top-level
   operand's conjunct (for `||`) or disjunct (for `&&`) — the
   absorption law (`a || b && a` collapses to `a`). Only the outermost
   link of a same-operator chain reports; parenthesized links are
   transparent (`a || (a || b)` is one chain).
4. **Duplicate positional arguments** — one call whose argument list
   contains a repeated canonical argument (`f(w, h, w)` — the
   wrong-variable bug; reported as `same as argument N`). All-literal
   argument lists are exempt — uniform-value APIs like
   `rect(8, 8, 8, 4)` legitimately repeat — and a literal (numeric,
   string or char literal, or the predeclared `nil`/`true`/`false`) is
   never reported as the repeat. Go has no named arguments, so
   upstream's named-argument exemption is vacuous here.

Host N/A (upstream shapes other hosts port, documented like their
omissions): **identical ternary branches** (`c ? x : x`) — Go has no
ternary; **duplicate catch bodies** in one `try` — Go has no
try/catch. Only pieces of the *same* construct compare; identical
conditions or bodies in separate `if` statements are legal (the state
may change between them).

Output: one line per finding —
`<relative path>:<line>: <message>` (messages adapted verbatim from
upstream: `duplicate condition in if/else-if chain: 'x == 0' (same as
line 6)`, `duplicate branch body in if/else-if chain: 'return "x"'
(same as line 5)`, `duplicate operand in '||' chain: 'a' repeats — the
chain collapses to the single operand`, `operand of '&&' is implied by
another operand (absorption): 'a' also appears standalone — the extra
terms are dead`, `duplicate argument in call to 'measure': 'width'
(same as argument 1)`) — plus a summary (`N identical-branch findings
(M constructs checked)`, or `M constructs checked, no identical-branch
findings`; constructs are chains, outermost boolean chains and calls
with 2+ arguments). Exit code 0 on findings at the default warning
severity, 2 with `--severity error`, 0 on a clean or empty selection,
1 on usage errors (bad flags, unknown severity). Severity mirrors
string-hazards (§34): upstream's `severity: warning` default (the
equivalence assumes side-effect-free operands, which pure syntax cannot
prove) maps to the `--severity` flag. Findings are ignorable via
`// ignore:identical-branches`, `// ignore-file:identical-branches`
and the blanket markers (§32).

Killing tests (identicalbranches_test.go), all non-vacuous — every
flagged fixture really enters a construct scan (the helper fails on
zero constructs) and mutation-removing a check fails its test:
`dup_condition_chain` / `dup_condition_twice` (condition repeats, line
and `same as line` pinning), `dup_condition_canonical` (operand
reorder + parentheses cannot hide a repeat — kills the sort/unwrap),
`dup_condition_whitespace` (formatting cannot hide a repeat),
`non_commutative_conditions` (`<` and `-` never reorder — kills an
over-eager sort), `dup_body_chain` / `dup_body_else_equals_then` /
`dup_body_block_equals_naked` (bodies; kills the unwrap — without it
violations point at braces and brace style hides repeats),
`dup_operand_or` (also asserts exactly one finding — kills the
outermost-only rule), `dup_operand_and` / `dup_operand_call` /
`absorbed_operand` / `absorbed_operand_or` (absorption both ways),
`nested_distinct_chain`, `reordered_commutative_operands` (canonical
sort collapses `(a && b) || (b && a)`), `dup_arg_call` /
`all_literal_args` / `literal_never_repeat` / `predeclared_literals` /
`distinct_args` (call arguments; kills the literal skip),
`callee_name_shapes` (callee naming across plain/method/generic/
parenthesized/computed call shapes),
`separate_if_conditions` / `nested_ifs_separate_chains` /
`switch_excluded` / `separate_if_bodies` / `else_block_if_is_own_chain`
(scope), and the command-level `TestRun_IdenticalBranches*` (warning
default exits 0 with findings printed, clean summary line,
`--severity error` exits 2, unknown severity exits 1, granular and
blanket ignore markers silence findings even at `--severity error`).

Dogfooding: `go run . identical-branches .` over the port's own source
(2070 constructs) first flagged eight argument repeats. The badge
renderer's `svgEscape(label)`/`svgEscape(message)` repeats became named
locals (`el`, `em` — the Go way to name the slot, upstream's
named-argument exemption); the remaining repeats are the SVG template's
deliberate structure (the width in both the attribute and the viewBox,
the plate geometry `x = lw`, label/message rendered in aria-label,
title and plates, and mutation-diff's degenerate single-line
`[line, line]` range) — each carries a reason-carrying
`// ignore:identical-branches` marker. The re-run reports
`2070 constructs checked, no identical-branch findings`.
