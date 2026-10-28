# crap4go

[![Quality](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml/badge.svg)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/actions/workflows/quality.yml)
[![version](https://img.shields.io/github/v/release/vabhzw17eg2qu4m9-bit/crap4go?label=version)](https://github.com/vabhzw17eg2qu4m9-bit/crap4go/releases)
![CRAP](badges/crap.svg)
![coverage](badges/coverage.svg)
![memory churn](badges/memory.svg)

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

`profile`, `skill`, `memory`, `reorder`, and the gate subcommands
(`file-naming`,
`nesting`, `complexity`, `class-size`, `weight-of-class`, `unused-code`,
`unused-files`, `banned-imports`, `magic-constants`, `test-assertions`,
`folder-structure`, `anon-struct-signatures`, `scattered-constants`, `duplicates`, `similarity`, `string-hazards`) dispatch on the first
argument only; anything else takes the analyze path above.

```sh
crap4go profile --name TestParser   # run instrumented tests, report per-method timing
crap4go file-naming                 # flag mechanical file names (util.go, batch1.go, ...)
crap4go nesting                     # flag functions nested deeper than 5
crap4go complexity --metric cognitive --max 8   # flag functions over the cognitive limit
crap4go class-size                  # flag types with >25 methods or WMC >80
crap4go weight-of-class             # flag data-heavy exported structs (fields > 33% of members)
crap4go unused-code                 # flag unexported declarations never referenced
crap4go unused-files                # flag packages never imported by analyzed code
crap4go banned-imports --from 'ui/**' --forbid '**/db/**' --message 'UI must not touch storage'
crap4go magic-constants           # flag hex colors outside consts and repeated literals
crap4go test-assertions           # flag tests with no fail-capable calls (t.Error/Fatal/..., panic)
crap4go folder-structure           # flag dirs with loose .go files at the module root
crap4go folder-structure --cap 'internal/*=3'   # per-pattern caps over glob-selected dirs
crap4go anon-struct-signatures     # flag anonymous struct types in function signatures
crap4go scattered-constants        # flag literals repeated across 3+ files, duplicating a named const, or pure value aliases
crap4go duplicates --source ../other-module/lib   # flag copy-pasted blocks across modules
crap4go similarity --check --jaccard 0.6 --containment 0.9   # flag whole-file near-duplicates; exit 2
crap4go mutation --min-score 0.9   # mutation-test covered sites; exit 2 on survivors
crap4go memory --format json       # per-test GC/alloc churn report (informational, exit 0)
crap4go memory --badge badges/memory.svg   # refresh the README churn badge (self-analyze)
crap4go reorder --dry-run          # list files whose top-level decls are out of canonical order; exit 2
crap4go string-hazards             # flag regex anchors in raw regexp sources and literal strings.Replace/Split/Trim patterns (warning severity, exit 0)
crap4go string-hazards --severity error   # same findings, exit 2
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

`memory` (ported from crap4dart 3bae178bc0 and 11551739c8, per-test
GC/alloc attribution) runs the test suite under `go test -v -count=1 -p 1
-exec 'env GODEBUG=gctrace=1'` and attributes every gctrace row to a test
by stream position: gctrace lines and `=== RUN` / `--- PASS` markers
interleave in one ordered output stream, so a GC belongs to the innermost
open test window (subtests nest — the shorter span wins, the upstream
nested-window rule). Only rows INSIDE test windows are trusted: GCs with
no open window (test-binary loading, teardown after the last `--- PASS`,
between suites) land in a separate unattributed bucket — reported, never
polluting test or package totals. Markers match at any indentation —
subtest results print indented one level per depth, and a column-0-only
matcher would leave subtest windows stuck open — and because the go
tool's relay merges the two output streams at arbitrary byte boundaries,
rows and markers are recovered by shape even when a tear glues a marker
into a torn row mid-line (a row fragment is never a row: its churn is
lost, nothing misattributed). Tracing is scoped to the test binaries via
the go tool's `-exec` hook and GODEBUG is stripped from the command
environment, so the go tool's and the compilers' own GCs never pollute
the stream (the role upstream's `isolate.startsWith('test_suite')`
filter plays). Unlike upstream there is no clock-offset machinery: both
markers and GC rows live in one stream, so attribution needs no clock
math (upstream voted one global offset per suite in phase 1 and replaced
it with per-isolate clocks in phase 2; neither has a Go analog).
Metrics: churn = the heap a GC cycle
reclaimed, `max(0, heap-at-GC-start − live-heap-after-GC)` summed over
the attributed rows (upstream's reclaimed new-gen); old-gen growth =
live-heap-after-GC delta vs the previous GC, baseline reset per package
since each package is its own test binary (upstream's oldAfter−oldBefore;
retained growth that never frees is the leak suspect). `--format json`
keeps stdout JSON-only (progress on stderr); `--top <N>` limits the
leaderboards (default 20). `--badge <path>` (+ `--badge-threshold` MB,
default 500) writes a shields-style `memory churn` SVG after the report:
green at or below the threshold, yellow at most twice it, red beyond.
Informational, like upstream: always exits 0
— only usage errors (bad flags, no `go.mod`, no tests executed) exit 1;
failing tests do not change the exit code. Upstream's measured insight
carries over: churn totals are a function of the code and stable across
runs, while GC counts depend on the machine — churn is the future gate
metric, never counts.

The remaining gates are ported from crap4dart 0.5.x–0.9.2 as
subcommands (there is no gate framework: no severity/ignorable/entries/
baseline — violations always fail with exit 2, thresholds keep upstream
defaults; 0.6.0's baseline/severity/config knobs and 0.6.1's internal
refactor do not apply, and 0.5.2's profile fix is Dart-only).
`nesting`
fails functions whose block nesting exceeds 5 (body = level 1); an
`else if` chain is flat — every chained `if` nests at the originating
`if`'s level — while an `if` inside a plain `else` block keeps paying
(ported from crap4dart 7bf7a15).
`complexity` flags functions over a complexity limit (default max 10,
cyclomatic, reported as `CC`); `--metric cognitive` switches to
SonarSource-style cognitive complexity (+1 per flow break, nesting-
weighted increments for structural constructs, one increment per
identical `&&`/`||` sequence, flat `else if` chains, function literals
nest one level deeper without incrementing, `--count-lambdas` on by
default) and `--max N` overrides the limit.
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
nothing. `folder-structure` (from 0.9, upgraded from crap4dart
753c3580) flags directories holding more than their cap of loose `.go`
units directly (default: the module root, `--max-loose-files 0`) —
group them into feature packages. Generated siblings of one source file
(`foo.go` + `foo.pb.go` + `foo_string.go`, `zz_generated*.go`) count
once; dirs accept globs (`internal/*`, hidden/`vendor`/`build` trees
skipped); `--cap 'internal/*=3'` gives a pattern its own cap (first
pattern wins); `--exclude GLOB` drops files.

`anon-struct-signatures` (from crap4dart 4d844a4's record gates, collapsed
into one Go gate) flags anonymous struct types in function signatures —
declarations, methods and function literals, parameters and results,
nested inside slice/map/chan/pointer types and generic type arguments
included. Every inline `struct{...}` in a signature forces consumers to
diff the shape field-by-field and lets "similar but not identical" shapes
drift apart silently; declare it as a named type and reference the name.
`--max-fields N` (default 5) calls a flagged shape oversized in its
message when the field count exceeds it (upstream's record_size cap). The
blessed form is a named type: a struct referenced by name is never
flagged, and unnamed multiple results — Go's core idiom — are never
flagged either. Dart's `.$N` positional field access has no Go
counterpart (nothing to port); anonymous struct values in bodies stay
legal; a named function type blesses nothing — an anonymous struct inside
`type Handler func() struct{ OK bool }` is still flagged.

`scattered-constants` (from crap4dart e766d68's `scattered_constants`)
is the corpus-wide sibling of `magic-constants`: instead of in-file
repeats it keys literal values across the whole selection and reports
two finding kinds. **Constant regression** — a named constant for the
value already exists (Go: top-level `const` or top-level `var` with a
plain literal initializer), so every hardcoded re-introduction flags
immediately, at any occurrence count, even in the declaring file, naming
the constant and its `file:line` to reference; the first declaration
wins. **Scattered literal** — no constant exists and the value appears
in `--min-files` (default 3) distinct files: one violation per
occurrence, the "change 86400 in every service" hazard no per-file gate
can see. Values compare semantically (`1_000`, `0x3E8` and `1000` are
one fact). Never counted: strings under `--min-length` (4), numbers of
magnitude at or below `--min-value` (100), char/imaginary literals,
import paths, struct tags (protocol wire vocabulary), identifier-position
strings (map keys, index operands, case labels), and by default
`*_test.go` files plus files carrying the `// Code generated ... DO NOT
EDIT.` marker. From crap4dart a314b485, a third kind — the **pure value
alias**: an unexported const whose initializer is another constant's
bare identifier (`const kAlias = kBase`) adds a name without
information and is flagged to be inlined; exported declarations
(re-export shims), the blank identifier, computed initializers and
Go's predeclared constants (`true`/`false`/`iota`) stay exempt, and
`--aliases=false` (default on) switches the rule off.

`string-hazards` (from crap4dart 62fec6833's `string_hazards`) flags
string literals whose escaping or pattern usage contradicts what the
author almost certainly meant, straight from the syntax tree. Two of
upstream's three kinds host in Go (Go has no interpolation to escape,
so the escaped-interpolation kind is omitted): a **raw regexp source
with an escaped anchor** — a backtick string passed to
`regexp.Compile`/`MustCompile`/`CompilePOSIX`/`MustCompilePOSIX` ending
in `` `\$` `` or starting with `` `\^` `` matches a literal dollar/caret
where the end/start anchor was almost certainly intended (interpreted
sources are spared — valid Go cannot carry an escaped dollar in them);
and **regex anchors in literal strings-package patterns** —
`strings.Replace`/`ReplaceAll`/`Split`/`SplitN` match their pattern
literally, so a leading `^` or trailing `$` there can only match that
literal character (`strings.TrimPrefix` checks the leading `^`,
`strings.TrimSuffix` the trailing `$` — each only where an anchor
mistake is possible). The first warning-default gate: findings print
but exit 0, upstream's `severity` config key mapped to the
`--severity warning|error` flag, and findings are ignorable via
`// ignore:string-hazards` / the blanket markers.

`duplicates` (from 0.2.0, plus dc64e9c's
per-gate `sources`) tokenizes every scanned file with `go/scanner`
dropping comments and auto-inserted semicolons, finds token windows of
50+ tokens (`--min-tokens`) spanning 5+ lines (`--min-lines`) that appear
at least twice within or across files (Rabin-Karp over the token
stream), and fails files whose duplicated-line percentage is over
`--threshold` (default 1.0). `--source PATH` unions extra directories or
files into the scan — the cross-module mechanism: keep CRAP scoped to the
module, but catch duplication spanning sibling packages. `--exclude GLOB`
(repeatable) skips matching paths; by default `**/*_test.go` and
`vendor/**` (the standard source exclusions) are skipped. Two opt-in
normalizations extend detection to renamed clones: `--ignore-locals`
renames function-local identifiers (receiver name, named params and
results, type parameters, `:=` locals, range variables, function-literal
bindings) consistently in first-use order, keeping the API surface —
called functions and methods, types, field references — visible, so two
locals swapped against each other never match; detection becomes a union
of the raw-lexeme pass and the masked pass, so exact copies are never
lost when enclosing scopes shift placeholder numbering.
`--ignore-literals` replaces string and numeric literals with `$STR`/
`$NUM` placeholders; `--ignore-identifiers` collapses every identifier —
API surface included (Go's predeclared builtins are plain identifiers) —
into one `$ID` placeholder for structure-only matching;
`--ignore-imports` drops import declarations from tokenization;
`--skip-duplicate-files` analyzes a token-identical file once. Windows
are verified by token equality before reporting (rolling-hash collisions
do not count) and occurrences anchor at the maximal extent all of them
share. All knobs are off by default: the default mode reports exact
copy-paste only. `--report clones` switches to PMD-CPD-style reporting —
one violation per non-overlapping clone group (longest first) with every
occurrence location in the message — while the default `percentage`
keeps the per-file duplicated-line report.

`similarity` (ported from crap4dart 2b21966) is file-level near-duplicate
detection over any text corpus: copied docs that drifted apart, twin
templates, stale snapshot copies, generated files duplicating their
sources. Unlike `duplicates` (token-level clone blocks inside Go code),
each finding says "this file is (mostly) a copy of that file" — reuse,
generate or delete, not extract-method. Every scanned file becomes a set
of 8-token shingles of profile-normalized text: markdown files lose
their fenced code blocks, source files lose comments and import
declarations with string/number literals placeholdered (`S`/`N`) so
literal-only differences never block a match (`.go` files tokenize via
`go/scanner`, other code extensions via the language-agnostic shaping),
anything else compares as-is. Pairs cross when Jaccard >= `--jaccard`
(default 0.8) or containment of the smaller shingle set in the larger
one >= `--containment` (default 0.95 — catches a small file copied into
a big one), and transitively crossing pairs merge into clusters
(union-find). Files with fewer than `--min-shingles` shingles (default
50) are skipped — tiny files carry no reliable signal. Positional args
are the directories scanned recursively (default `.`); `--include GLOB`
(default `**/*.md`) and `--exclude GLOB` (repeatable; default
`.git/**`, `build/**`, `coverage/**`, `node_modules/**`, `vendor/**`)
filter by project-relative path. The default run is the upstream
ad-hoc explorer — a console (or `--format json`) cluster report that
always exits 0; `--check` turns it into the gate: one violation line
per clustered file naming its counterparts and strongest metrics,
exit 2 iff there are violations. Explore first, tune the thresholds,
then run with `--check` in CI.

`mutation` (ported from crap4dart c865ec3, adapted from the
unclebob/mutator research) is mutation testing: a declarative operator
table (`arithmetic` `+`↔`-`, `*`↔`/`; `boundary` `>`↔`>=`, `<`↔`<=`,
`>`↔`<`, `>=`↔`<=`; `equality` `==`↔`!=`; `logical` `&&`↔`||`; `boolean`
`true`↔`false`; `constant` `0`↔`1`, `-1`→`0`; `unary` delete `!`) is
matched on the parsed AST — offsets come from go/token positions, never
regex. Sites in unexecuted methods are `uncovered` and excluded from the
score; every covered site is mutated one at a time and `go test ./...`
re-runs per mutant (non-zero exit kills the mutant — compile errors
included; a timeout counts as survived), with the file restored after
each mutant. It fails below `--min-score` (default 0.8) or above
`--max-survivors` (default 0), printing one violation line per surviving
mutant; skips (exit 0) without coverage data or test files. Expensive by
nature (one full test run per mutant) it is opt-in only — the analyze
flow never runs it — with `--timeout-seconds` (60), `--max-mutants` (500)
and `--operators` capping the work.

The digest-incremental cache (ported from crap4dart 16d9c4c, on by
default; `--cache=false` disables) persists per-method outcomes to
`.crap-mutation-cache.json` at the project root: killed mutants of
methods whose exact source span (FNV-1a digest) is unchanged since the
last run are replayed, not re-executed, and reported as `cached` in the
summary; survivors always re-run (the suite may have improved); coverage
gating is re-derived from fresh coverage every run; a config change
(operators, `--test-args`, tool version) wipes the cache; entries of
vanished methods are pruned. The end-of-run integrity check
(crap4dart 8b2e8e8) snapshots every file before the loop and re-verifies
the tree after it — a file that drifted (external edit, killed process
between apply and restore) is re-restored and surfaced as `INTEGRITY` in
the summary plus a stderr warning: treat that run as suspect.
`--test-args "test -run TestFoo ./pkg/..."` scopes the per-mutant
invocation (default `test ./...` — the whole suite); scoping is your
responsibility: a mutant is killed only if the scoped suite would kill
it. `--diff-base <ref>` scopes execution to this change: sites run only
when their containing method intersects the lines `git diff
--unified=0 <ref>` changed — everything outside the diff is neither
executed nor counted, so a per-PR run pays only for the changed methods'
mutants. `--scope-tests` (default false) narrows each mutant's run to
the derived test file of the mutated file (`dir/base.go` → `go test
./dir` via its in-package `base_test.go`), falling back to `--test-args`
when no candidate exists — a scoped kill is monotone and cached like any
kill; a scoped survivor (timeouts included) is re-confirmed once against
the full invocation and only the confirmed outcome is reported and
cached (`test-scoped N (M re-confirmed killed)` in the summary).

### Reordering source

`reorder` (ported from crap4dart 24a6f24f, adapted to Go's file-level
declaration model) rewrites files so top-level declarations follow the
canonical Go order: **imports, consts, vars, types, funcs** — a stable
partition, never an alphabetical sort: declarations of one category keep
their original relative order. The transform is deterministic and
idempotent: the same input always produces the same output,
already-canonical files stay byte-identical whatever their layout, and
reordering an already-reordered file changes nothing. Doc comments and
whole-line comment blocks above a declaration move with it — including a
block separated by a single blank line; two or more blank lines detach
the block, and a detached block between declarations sticks to the
preceding declaration so comments are never dropped — including
multi-line `/* */` blocks (their tail lines are not whole-line
comments, so nothing else claims them). A comment group
starting on a declaration's line (a trailing same-line comment and its
contiguous continuation lines) belongs to that declaration, and
`//line` directives ride with the declaration they precede (the
transform indexes raw, directive-unadjusted positions, so a directive
cannot shift its line table). Grouped
const/var/type blocks move whole — `iota` and struct-field order are
semantics and are never touched — and because the partition preserves
the relative order of functions, several `init()` functions in one file
keep their execution order (Go initializes package-level vars by
dependency order, not source order, so moving whole var declarations
cannot change observable initialization). A file's line-ending style (LF
or CRLF) is preserved. A rewritten LF file is additionally passed
through gofmt's own printer (`go/format`) — reordering can create new
adjacency groups of one-line declarations whose columnar alignment no
longer matches — so the output is gofmt-clean by construction; CRLF
files skip that pass (the printer emits LF only) and keep their
endings; already-canonical files are never reformatted. With
`--dry-run` (`-n`) nothing is written: the
command lists the files that would change and exits 2 when at least one
would (0 otherwise); without it, files are written and the command exits
0. Files that fail to parse are skipped with a note, never fatal.
Selection follows the standard rules (non-test, non-vendor `.go` files
under the current directory unless paths are given; an explicit file
path is honored verbatim, so `reorder foo_test.go` works).

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

## Ignore comments

Gate subcommands drop violations suppressed by ignore comments (ported
from crap4dart 8abb64c):

- `// crap:ignore` — legacy blanket, line scope: suppresses every
  ignorable gate's violation on its line (or the line above).
- `// crap:ignore-file` — legacy blanket, file scope: anywhere in the
  first 5 lines it suppresses every ignorable gate's violations in the
  whole file.
- `// ignore:<gate-id>` — granular, line scope: suppresses only that
  gate's violation on its own line (trailing marker counts) or on the
  code line that ends a run of whole-line ignore comments directly
  above it.
- `// ignore-file:<gate-id>` — granular, file scope: first 5 lines,
  same per-gate semantics.

Dart-lint style: one rule per marker — several rules stack as several
markers, and several markers on one line union their ids. An empty id
(`// ignore:`) is blanket again. Comma lists are not lists:
`// ignore:a,b` names one unknown id and matches nothing, like any
unknown id.

Suppression is opt-in per gate. crap4go has no config file, so the
opt-in is the hard-coded registry in `ignorefilter.go` (the host
adaptation of upstream's `ignorable: true` config flag): the 12 gates
whose violations carry `file:line` — `complexity`, `nesting`,
`class-size`, `weight-of-class`, `unused-code`, `magic-constants`,
`test-assertions`, `scattered-constants`, `duplicates`,
`banned-imports`, `anon-struct-signatures`, `string-hazards` — honor
line and file markers; `file-naming` and `unused-files` (line-less violations) honor
file markers only. Not ignorable: `folder-structure` (violates
directories — no lines to read), `similarity` (a suppressed file is
still named by its cluster peers), `mutation` (count-bearing score
aggregates would desync from the printed list), and the informational
`memory`/`profile`.

Dogfood: memory's `--badge-threshold` default `500` — the accepted
scattered-constants residue (a coincidence with mutation's
`defaultMutationMaxMutants`) — carries a trailing
`// ignore:scattered-constants`, so the documented exception is
enforced by the gate instead of living in prose alone.

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

Badges under `badges/` are regenerated by CI on every push — except
`badges/memory.svg`: the commit-badges workflow refreshes only
`crap.svg` and `coverage.svg`; `memory.svg` is written by the memory
command itself (`crap4go memory --badge badges/memory.svg`) and
committed manually. To refresh the other two locally, build the binary,
run it and the tests, then call `scripts/badge.sh`:

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
  files.go        runtests.go    cli.go         ignorefilter.go
  profile.go      profile_collector.go   filenaming.go   skill.go
  magicconstants.go   testassertions.go   folderstructure.go
  *_test.go       testdata/
```

See `spec.md` for the full language-adapted specification.
