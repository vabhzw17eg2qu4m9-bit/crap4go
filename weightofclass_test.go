package main

import (
	"bytes"
	"go/ast"
	"strings"
	"testing"
)

const weightFixture = `package p

type DataOnly struct { // 3 exported fields, 0 methods -> weight 1.0
	A, B string
	C    int
}

type Balanced struct { // 1 exported field, 3 exported methods -> weight 0.25
	Name string
}

func (b Balanced) String() string  { return b.Name }
func (b Balanced) Other() string   { return b.Name }
func (b Balanced) Third() string   { return b.Name }

type unexported struct { // never checked
	X int
}

type NoExportedFields struct { // weight 0 -> never flagged
	name string
}

func (n NoExportedFields) Get() string { return n.name }

type NotAStruct int
`

func TestRun_WeightOfClassViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/types.go", weightFixture)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"weight-of-class"}, root, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "types.go:3: DataOnly exposes 3 public fields of 3 public members (weight=1.00)") {
		t.Errorf("output missing DataOnly violation:\n%s", got)
	}
	for _, notWanted := range []string{"Balanced", "unexported", "NoExportedFields", "NotAStruct"} {
		if strings.Contains(got, notWanted) {
			t.Errorf("unexpected violation for %s:\n%s", notWanted, got)
		}
	}
}

func TestRun_WeightOfClassClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/balanced.go", `package p

type Balanced struct {
	Name string
}

func (b Balanced) String() string { return b.Name }
func (b Balanced) Other() string  { return b.Name }
func (b Balanced) Third() string  { return b.Name }
`)
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"weight-of-class"}, root, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, errOut.String())
	}
	if want := "1 types within weight 0.33"; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestRun_WeightOfClassNoFiles(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"weight-of-class"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "No Go files to check."; !strings.Contains(out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, out.String())
	}
}

func TestExportedFieldCount(t *testing.T) {
	root := t.TempDir()
	path := root + "/fields.go"
	writeFile(t, path, `package p

type T struct {
	A        string
	b        string
	C, D     int
	io.Writer
	*bytes.Buffer
	unexported
}
`)
	file, _, err := parseGoFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ts := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
	if got := exportedFieldCount(ts.Type.(*ast.StructType)); got != 5 {
		t.Errorf("exportedFieldCount = %d, want 5 (A, C, D, Writer, Buffer)", got)
	}
}
