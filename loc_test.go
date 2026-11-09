package main

import (
	"bytes"
	"strings"
	"testing"
)

// shortFile is a two-line file within any sane loc limit.
const shortFile = "package p\n\nfunc F() {}\n"

// makeLines builds a Go file with exactly n lines (a trailing newline after
// the last line), padding with single-line funcs.
func makeLines(n int) string {
	var b strings.Builder
	b.WriteString("package p\n")
	for range n - 1 {
		b.WriteString("func F() {}\n")
	}
	return b.String()
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"no trailing newline", "a\nb", 2},
		{"trailing newline", "a\nb\n", 2},
		{"blank lines count", "\n\n", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := t.TempDir() + "/f.txt"
			writeFile(t, path, tt.content)
			got, err := countLines(path)
			if err != nil {
				t.Fatalf("countLines: %v", err)
			}
			if got != tt.want {
				t.Fatalf("countLines = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRun_LocViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/big.go", makeLines(801))
	writeFile(t, root+"/fine.go", shortFile)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"loc"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"big.go: 801 lines > max 800", "1/2 files over their limit"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fine.go") {
		t.Errorf("file within limit flagged:\n%s", got)
	}
}

func TestRun_LocBoundaryPasses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/exact.go", makeLines(800))
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"loc"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if want := "1 files within 800 lines"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_LocMaxLinesFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/f.go", shortFile)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"loc", "--max-lines", "1"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if want := "f.go: 3 lines > max 1"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_LocExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/big.go", makeLines(801))
	writeFile(t, root+"/fine.go", shortFile)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"loc", "--exclude", "big.go"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (output=%s stderr=%s)", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "big.go") {
		t.Errorf("excluded file flagged:\n%s", out.String())
	}
	if want := "1 files within 800 lines"; !strings.Contains(out.String(), want) {
		t.Errorf("excluded file not dropped from checked count:\n%s", out.String())
	}
}

func TestRun_LocUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"loc", "--max-lines", "bogus"}, t.TempDir(), &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
