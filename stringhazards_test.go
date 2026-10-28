package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// The backtick never survives inside a raw string literal, so every
// fixture embedding a raw regexp source splices it in with `bt`.

const bt = "`"

// rawRegexpTrailingEscapedDollar carries a raw regexp source ending with
// `\$` — the backslash survives into the pattern, so it matches a literal
// dollar where the end anchor was almost certainly intended (upstream
// shape 2). Flagged on line 6.
const rawRegexpTrailingEscapedDollar = `package p

import "regexp"

func token() *regexp.Regexp {
	return regexp.MustCompile(` + bt + `[0-9a-f]{12}\$` + bt + `)
}
`

// rawRegexpLeadingEscapedCaret carries a raw regexp source starting with
// `\^` — flagged on line 6, covering the Compile variant and the start
// anchor message.
const rawRegexpLeadingEscapedCaret = `package p

import "regexp"

func token() *regexp.Regexp {
	return regexp.Compile(` + bt + `\^abc` + bt + `)
}
`

// rawRegexpPosixVariant flags CompilePOSIX/MustCompilePOSIX like the
// plain constructors — flagged on line 6.
const rawRegexpPosixVariant = `package p

import "regexp"

func token() *regexp.Regexp {
	return regexp.MustCompilePOSIX(` + bt + `\^abc` + bt + `)
}
`

// rawRegexpPlainAnchors uses plain `$` and `^` anchors in raw sources —
// the correct spelling, never flagged.
const rawRegexpPlainAnchors = `package p

import "regexp"

func ok() (*regexp.Regexp, *regexp.Regexp) {
	return regexp.MustCompile(` + bt + `[0-9a-f]{12}$` + bt + `), regexp.Compile(` + bt + `^abc` + bt + `)
}
`

// regexpInterpretedSource is spared: interpreted strings cannot carry an
// escaped dollar in valid Go, so a trailing `$` there is a real anchor
// (upstream's spare of non-raw RegExp sources).
const regexpInterpretedSource = `package p

import "regexp"

func token() *regexp.Regexp {
	return regexp.MustCompile("[0-9a-f]{12}$")
}
`

// regexpNonLiteralSource skips the check — only literal sources are
// judged.
const regexpNonLiteralSource = "package p\n\nimport \"regexp\"\n\nvar pattern = " + bt + `x\$` + bt + `

func token() *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
`

// regexpAliasedReceiver stays quiet — the gate matches the regexp
// package, not just any MustCompile method.
const regexpAliasedReceiver = `package p

type builder struct{}

func (builder) MustCompile(src string) *fake { return nil }

type fake struct{}

func build() *fake {
	return builder{}.MustCompile(` + bt + `x\$` + bt + `)
}
`

// replaceAnchoredPatterns carries the shape-3 bugs: Replace with a
// leading `^` (line 6), ReplaceAll with a trailing `$` (line 7).
const replaceAnchoredPatterns = `package p

import "strings"

func rewrite(s string) string {
	s = strings.Replace(s, "^~", "", 1)
	return strings.ReplaceAll(s, "abc$", "")
}
`

// splitAnchoredSeparators flags Split and SplitN like the Replace family
// (lines 6 and 7).
const splitAnchoredSeparators = `package p

import "strings"

func parts(s string) []string {
	a := strings.Split(s, "^")
	return strings.SplitN(a[0], "a$", 2)
}
`

// trimAnchoredPositions is position-aware: TrimPrefix flags a leading
// `^` (line 6) but not a trailing `$` (line 7 — a prefix's match position
// is the string start, so an end anchor there is not an anchor mistake),
// TrimSuffix the mirror image (lines 8 and 9).
const trimAnchoredPositions = `package p

import "strings"

func trim(s string) string {
	s = strings.TrimPrefix(s, "^~")
	s = strings.TrimPrefix(s, "~$")
	s = strings.TrimSuffix(s, "~$")
	s = strings.TrimSuffix(s, "^~")
	return s
}
`

// replaceBothAnchors yields two violations for one literal — leading and
// trailing are independent checks.
const replaceBothAnchors = `package p

import "strings"

func rewrite(s string) string {
	return strings.Replace(s, "^a$", "", 1)
}
`

// replaceCleanPatterns spares plain patterns, non-literal arguments and
// other receivers — the false-positive guards for shape 3.
const replaceCleanPatterns = `package p

import "strings"

var pat, sep = "^abc", "a$"

func rewrite(s string, other stringer) string {
	s = strings.Replace(s, "abc", "x", 1)
	s = strings.ReplaceAll(s, pat, "")
	s = strings.Split(s, sep)
	s = strings.Split(s)
	s = strings.EqualFold(s, pat)
	return other.Split(s, "^")
}

type stringer interface{ Split(s, sep string) []string }
`

// hazardStrings runs the gate over one fixture and returns its
// violations as `line:message` strings in file order.
func hazardStrings(t *testing.T, src string) []string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root+"/hazard.go", src)
	violations, checked := CheckStringHazards([]string{root + "/hazard.go"}, root)
	if checked != 1 {
		t.Fatalf("checked = %d, want 1", checked)
	}
	var got []string
	for _, v := range violations {
		if v.Path != "hazard.go" {
			t.Errorf("path = %q, want hazard.go", v.Path)
		}
		got = append(got, fmt.Sprintf("%d:%s", v.Line, v.Message))
	}
	return got
}

// matchViolations asserts got carries exactly one entry per want
// substring, in order.
func matchViolations(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("violations =\n%v\nwant %d entries", got, len(want))
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("violation %d = %q, want it to contain %q", i, got[i], w)
		}
	}
}

func TestStringHazardsRawRegexpAnchors(t *testing.T) {
	matchViolations(t, hazardStrings(t, rawRegexpTrailingEscapedDollar),
		[]string{`6:raw regexp pattern ends with '\$' — in a raw string that is a literal '$' character, not the end anchor`})
	matchViolations(t, hazardStrings(t, rawRegexpLeadingEscapedCaret),
		[]string{`6:raw regexp pattern starts with '\^' — in a raw string that is a literal '^' character, not the start anchor`})
	matchViolations(t, hazardStrings(t, rawRegexpPosixVariant),
		[]string{`6:raw regexp pattern starts with '\^'`})
	for _, src := range []string{rawRegexpPlainAnchors, regexpInterpretedSource, regexpNonLiteralSource, regexpAliasedReceiver} {
		if got := hazardStrings(t, src); len(got) != 0 {
			t.Errorf("violations = %v, want none", got)
		}
	}
}

func TestStringHazardsLiteralPatterns(t *testing.T) {
	matchViolations(t, hazardStrings(t, replaceAnchoredPatterns), []string{
		`6:literal pattern starts with '^' — string patterns match literally, so this can only match a literal '^' character; a regexp was probably intended`,
		`7:literal pattern ends with '$' — string patterns match literally, so this can only match a literal '$' character; a regexp was probably intended`,
	})
	matchViolations(t, hazardStrings(t, splitAnchoredSeparators), []string{
		`6:literal pattern starts with '^'`,
		`7:literal pattern ends with '$'`,
	})
	matchViolations(t, hazardStrings(t, trimAnchoredPositions), []string{
		`6:literal pattern starts with '^'`,
		`8:literal pattern ends with '$'`,
	})
	matchViolations(t, hazardStrings(t, replaceBothAnchors), []string{
		`6:literal pattern starts with '^'`,
		`6:literal pattern ends with '$'`,
	})
	if got := hazardStrings(t, replaceCleanPatterns); len(got) != 0 {
		t.Errorf("violations = %v, want none", got)
	}
}

func TestRun_StringHazardsWarningDefaultExitsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/hazard.go", replaceAnchoredPatterns)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"string-hazards"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 at the default warning severity (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"hazard.go:6: literal pattern starts with '^'",
		"hazard.go:7: literal pattern ends with '$'",
		"2 string hazard(s) in 1 files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_StringHazardsSeverityErrorExitsTwo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/hazard.go", replaceAnchoredPatterns)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"string-hazards", "--severity", "error"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with --severity error", code)
	}
}

func TestRun_StringHazardsSeverityUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"string-hazards", "--severity", "fatal"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for an unknown severity (stderr=%s)", code, errOut.String())
	}
}

func TestRun_StringHazardsIgnorable(t *testing.T) {
	root := t.TempDir()
	mark := func(marker, call string) string {
		return marker + "\n\t" + call
	}
	writeFile(t, root+"/hazard.go", strings.NewReplacer(
		"\ts = strings.Replace(s, \"^~\", \"\", 1)",
		mark("// ignore:string-hazards", `s = strings.Replace(s, "^~", "", 1)`),
		"\treturn strings.ReplaceAll(s, \"abc$\", \"\")",
		mark("// ignore:string-hazards", `return strings.ReplaceAll(s, "abc$", "")`),
	).Replace(replaceAnchoredPatterns))
	var out, errOut bytes.Buffer
	if code := runWithRoot([]string{"string-hazards", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 when every finding carries the granular ignore marker (stderr=%s)", code, errOut.String())
	}
	writeFile(t, root+"/hazard.go", strings.NewReplacer(
		"\ts = strings.Replace(s, \"^~\", \"\", 1)",
		mark("// crap:ignore", `s = strings.Replace(s, "^~", "", 1)`),
		"\treturn strings.ReplaceAll(s, \"abc$\", \"\")",
		mark("// crap:ignore", `return strings.ReplaceAll(s, "abc$", "")`),
	).Replace(replaceAnchoredPatterns))
	out.Reset()
	if code := runWithRoot([]string{"string-hazards", "--severity", "error"}, root, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 under the blanket ignore marker", code)
	}
	if want := "1 files free of string hazards"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}
