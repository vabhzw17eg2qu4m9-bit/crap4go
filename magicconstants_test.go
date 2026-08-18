package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// hexOutsideConst uses a hex color in a function body — flagged.
const hexOutsideConst = `package p

func draw() int {
	return 0xFF00AA
}
`

// hexInsideConst declares hex colors in const initializers, plain and via a
// call — both exempt from the hex check.
const hexInsideConst = `package p

const brandHex = 0xFF00AA

const paletteHex = mix(0xFF00AA, 0x00AAFF)

func mix(a, b uint32) uint32 { return a ^ b }
`

// repeatedString uses one string value three times — every occurrence is
// flagged as a repeat.
const repeatedString = `package p

func path() string {
	join("user/profile")
	join("user/profile")
	join("user/profile")
	return ""
}

func join(s string) string { return s }
`

// repeatedNumber uses one int lexeme three times — flagged like strings.
const repeatedNumber = `package p

func sizes() []int {
	return []int{1024, 1024, 1024}
}
`

// shortLiterals repeats literals below minLiteralLength — ignored.
const shortLiterals = `package p

func tiny() int {
	x := 7
	y := 7
	z := 7
	use("ab", "ab", "ab", 1.5, 1.5, 1.5)
	return x + y + z
}

func use(a, b string, c, d float64) int { return 0 }
`

// cleanFile has no hex colors and no value repeated three times.
const cleanFile = `package p

const limit = 100

func calc(n int) int {
	return n * limit / 7
}
`

// mapKeyAndIndexStrings repeats one string as a map-literal key and as an
// index operand — protocol identifiers, not magic constants (0.7.2/0.8.3).
const mapKeyAndIndexStrings = `package p

func cfg() int {
	m := map[string]int{"user/profile": 1}
	v, ok := m["user/profile"]
	if !ok {
		return 0
	}
	return v
}
`

// switchCaseLabels repeats one string three times, always as a case label —
// case labels match protocol values, not constants.
const switchCaseLabels = `package p

func kind(s string) string {
	switch s {
	case "alpha":
		return "one"
	}
	switch s {
	case "alpha":
		return "two"
	}
	switch s {
	case "alpha":
		return "three"
	}
	return ""
}
`

// constLinesExemptFromDuplicates repeats a string twice in code plus once
// on a const line: const occurrences no longer count towards the minimum
// (0.8.4), so the value stays below the threshold.
const constLinesExemptFromDuplicates = `package p

const prefix = "user/profile"

func p1() string { return "user/profile" }

func p2() string { return "user/profile" }
`

// constSubtreeHex puts hex colors inside a multi-line const initializer
// with a nested call: the full initializer subtree is const-exempt (0.8.6).
const constSubtreeHex = `package p

const nested = blend(
	0xFF00AA,
	shadow(0x00AAFF),
)

func blend(a, b uint32) uint32 { return a ^ b }

func shadow(c uint32) uint32 { return c }
`

// repeatedWithConstExtra repeats a string three times in code plus once on
// a const line: the count reflects only the non-const occurrences.
const repeatedWithConstExtra = `package p

const prefix = "user/profile"

func p1() string { return "user/profile" }

func p2() string { return "user/profile" }

func p3() string { return "user/profile" }
`

func TestCheckMagicConstants(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // expected "line:message" entries, exact set
	}{
		{
			name: "hex outside const flagged",
			src:  hexOutsideConst,
			want: []string{"4:hex color outside a constant declaration"},
		},
		{
			name: "const initializer and call args exempt",
			src:  hexInsideConst,
			want: nil,
		},
		{
			name: "repeated string flagged per occurrence",
			src:  repeatedString,
			want: []string{
				"4:literal user/profile repeats 3 times — extract a named constant",
				"5:literal user/profile repeats 3 times — extract a named constant",
				"6:literal user/profile repeats 3 times — extract a named constant",
			},
		},
		{
			name: "repeated number flagged per occurrence",
			src:  repeatedNumber,
			want: []string{
				"4:literal 1024 repeats 3 times — extract a named constant",
				"4:literal 1024 repeats 3 times — extract a named constant",
				"4:literal 1024 repeats 3 times — extract a named constant",
			},
		},
		{
			name: "short strings and numbers ignored",
			src:  shortLiterals,
			want: nil,
		},
		{
			name: "clean file has no violations",
			src:  cleanFile,
			want: nil,
		},
		{
			name: "map keys and index operands skipped",
			src:  mapKeyAndIndexStrings,
			want: nil,
		},
		{
			name: "switch case labels skipped",
			src:  switchCaseLabels,
			want: nil,
		},
		{
			name: "const-line occurrences do not count towards repeats",
			src:  constLinesExemptFromDuplicates,
			want: nil,
		},
		{
			name: "hex in nested const-initializer subtree exempt",
			src:  constSubtreeHex,
			want: nil,
		},
		{
			name: "repeats counted after const filtering",
			src:  repeatedWithConstExtra,
			want: []string{
				"5:literal user/profile repeats 3 times — extract a named constant",
				"7:literal user/profile repeats 3 times — extract a named constant",
				"9:literal user/profile repeats 3 times — extract a named constant",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root+"/magic.go", tt.src)
			violations, checked := CheckMagicConstants([]string{root + "/magic.go"}, root)
			if checked != 1 {
				t.Fatalf("checked = %d, want 1", checked)
			}
			var got []string
			for _, v := range violations {
				if v.Path != "magic.go" {
					t.Errorf("path = %q, want magic.go", v.Path)
				}
				got = append(got, fmt.Sprintf("%d:%s", v.Line, v.Message))
			}
			want := tt.want
			if want == nil {
				want = []string{}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("violations =\n%v\nwant\n%v", got, want)
			}
		})
	}
}

func TestRun_MagicConstantsViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/magic.go", hexOutsideConst)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"magic-constants"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"magic.go:4: hex color outside a constant declaration",
		"1 magic constant(s) in 1 files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_MagicConstantsClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/magic.go", cleanFile)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"magic-constants"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 files free of magic constants"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_MagicConstantsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"magic-constants", "/nonexistent/path"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%s)", code, errOut.String())
	}
}
