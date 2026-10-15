package main

import (
	"bytes"
	"go/ast"
	"strings"
	"testing"
)

// deeplyNestedFunc nests 6 levels deep: body(1) + if(2) + for(3) + if(4) +
// for(5) + if(6).
const deeplyNestedFunc = `package p

func Deep() {
	if x {
		for i := 0; i < 3; i++ {
			if x {
				for {
					if x {
						return
					}
				}
			}
		}
	}
}
`

// moderatelyNestedFunc nests 5 levels: body(1) + if(2) + for(3) + if(4) +
// if(5) — within the limit.
const moderatelyNestedFunc = `package p

func Fine() {
	if x {
		for i := 0; i < 3; i++ {
			if x {
				if x {
					return
				}
			}
		}
	}
}
`

func TestStmtNesting(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"flat body", "package p\nfunc F() { return }\n", 1},
		{"one if", "package p\nfunc F() { if x { } }\n", 2},
		{"else-if chain is flat", "package p\nfunc F() {\nif x {\n} else if y {\n} else {\n}\n}\n", 2},
		{"long else-if chain stays flat", "package p\nfunc F() {\nif x {\n} else if y {\n} else if z {\n} else if w {\n}\n}\n", 2},
		{"if inside plain else still nests", "package p\nfunc F() {\nif x {\n} else {\nif y {\n}\n}\n}\n", 3},
		{"switch case body", "package p\nfunc F() {\nswitch x {\ncase 1:\nif y {\n}\n}\n}\n", 3},
		{"plain block", "package p\nfunc F() { { { } } }\n", 3},
	}
	for _, tt := range tests {
		root := t.TempDir()
		path := root + "/src.go"
		writeFile(t, path, tt.src)
		file, _, err := parseGoFile(path)
		if err != nil {
			t.Fatalf("%s: parse: %v", tt.name, err)
		}
		fd, ok := file.Decls[0].(*ast.FuncDecl)
		if !ok {
			t.Fatalf("%s: first decl is not a FuncDecl", tt.name)
		}
		if got := stmtNesting(fd.Body); got != tt.want {
			t.Errorf("%s: nesting = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestRun_NestingViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/deep.go", deeplyNestedFunc)
	writeFile(t, root+"/fine.go", moderatelyNestedFunc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"nesting"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"deep.go:3: Deep nesting=6 > max 5", "1/2 functions nested deeper than 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Fine") {
		t.Errorf("function within limit flagged:\n%s", got)
	}
}

func TestRun_NestingClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/fine.go", moderatelyNestedFunc)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"nesting"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 functions within nesting 5"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_NestingNoFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"nesting"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

// elseIfChain is a 5-branch else-if chain: flat since 7bf7a15, so the whole
// chain nests at 1 (+ body = 2) and passes the default limit.
const elseIfChain = `package p

func Classify(x int) string {
	if x < 0 {
		return "neg"
	} else if x == 0 {
		return "zero"
	} else if x < 10 {
		return "small"
	} else if x < 100 {
		return "medium"
	}
	return "large"
}
`

// nestedElses buries an if in each plain else block, six levels deep: the
// chain-is-flat exemption does not apply, nesting = 7 > max 5.
const nestedElses = `package p

func Classify(x int) string {
	if x < 0 {
		return "neg"
	} else {
		if x == 0 {
			return "zero"
		} else {
			if x < 10 {
				return "small"
			} else {
				if x < 100 {
					return "medium"
				} else {
					if x < 1000 {
						return "large"
					} else {
						if x < 10000 {
							return "huge"
						}
					}
				}
			}
		}
	}
}
`

func TestRun_NestingElseIfChainFlat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/chain.go", elseIfChain)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"nesting"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (chain must stay flat; stderr=%s)", code, errOut.String())
	}
	if want := "1 functions within nesting 5"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_NestingIfInPlainElseStillNests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/nested.go", nestedElses)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"nesting"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	if want := "nested.go:3: Classify nesting=7 > max 5"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}
