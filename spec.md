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
crap4go skill                    Print the crap4go profiling skill for AI agents
```

`--changed` is mutually exclusive with positional paths (usage error if
combined). Unknown flags are usage errors.

**Subcommands:** `profile`, `skill`, and the gate subcommands (`file-naming`,
`nesting`, `class-size`, `weight-of-class`, `unused-code`, `unused-files`,
`banned-imports`, `magic-constants`, `test-assertions`, `folder-structure`,
`duplicates`, `mutation`)
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
shared-extent anchoring — ports d6c3ab6. `mutation` (§11.22) comes from
c865ec3 (the unclebob/mutator research): the operator table, coverage
gating, per-mutant test re-run and survivor violations port directly —
Dart's `/`→`~/` row is dropped (Go integer division already truncates),
`true`/`false` match predefined identifiers in expression position, the
LCOV per-line hit map becomes the block-based cover profile flattened to
per-line flags, and the yaml `enabled: false` knob becomes "subcommand
only" (the analyze flow never runs gates).
Not applicable upstream changes: 0.5.2's profile part-of fix is Dart-only;
0.6.0's baseline/severity/config knobs have no counterpart (no gate
framework); 0.6.1's internal constants refactor changes no behavior;
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
crap4go folder-structure [dirs...]
```

Go adaptation of crap4dart 0.9's `folder_structure` gate (max 0 loose
files): flags directories containing more than 0 non-test `.go` files
directly (non-recursive) — a flat-file sprawl that should be organized
into feature packages. Default directory: the module root (`.`) — the
port's default source root; positional args select other directories
(resolved against the root; a non-directory arg is a usage error).
Output: one line per violation
(`<dir>: N loose .go files directly in <dir> — group them into feature
packages (max 0)`) plus a summary. Exit code 2 iff violations.

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
`(+N more)`) — plus a summary `K clone groups (>= T tokens, >= L lines)`
or, on a clean scan, `no clones >= L lines across N files`. Exit code 2
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

## 23. `skill`

Prints a Go-adapted version of crap4dart's profiling skill (when to profile,
how the instrumentation works, how to read the report), ending with one line
on installing it as an agent skill. Always exits 0.

## 24. Threshold

The threshold defaults to `8.0` and is overridable with `--threshold`. After
the report is printed, the maximum numeric CRAP is compared against it; if
`max > threshold`, `CRAP threshold exceeded: <max> > <threshold>` is printed
to stderr and the process exits 2. Otherwise the process exits 0. A threshold
of `0` or less is a usage error.

## 25. Exit codes

| Code | Meaning                                                                  |
|------|--------------------------------------------------------------------------|
| `0`  | Success (including empty selection, or max CRAP ≤ threshold).            |
| `1`  | Usage error: bad flags, bad threshold, `--changed` + paths, unreadable.  |
| `2`  | Max numeric CRAP exceeded the threshold; `profile` total exceeded        |
|      | `--threshold`; gate subcommand violations (`file-naming`, `nesting`,     |
|      | `class-size`, `weight-of-class`, `unused-code`, `unused-files`,          |
|      | `banned-imports`, `magic-constants`, `test-assertions`,                  |
|      | `duplicates`, `folder-structure`, `mutation`). (Also reported on        |
|      | stderr.)                                                                |

## 26. `--run-tests`

Runs `go test ./... -coverprofile=coverage.out -covermode=atomic` in the
project root, streaming stdout/stderr through. On non-zero exit, the error is
printed to stderr and the process exits 1.

## 27. Non-goals

- No type-checking or build verification — parsing only.
- No HTML/branch coverage reports — only the statement-level cover profile.
- No automatic fixing or refactoring of high-CRAP code.
- No external runtime dependencies (standard library only); no `go.sum`.
- No config file or gate framework; no gate severity/ignorable/entries/
  baseline (crap4dart 0.5.0 framework features) — gates are standalone
  subcommands with hard-coded upstream defaults; no profile
  `--tags`/`--exclude-tags` (no tag concept in `go test`) — all knobs are
  CLI flags.
