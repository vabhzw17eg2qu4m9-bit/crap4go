package main

import (
	"testing"
)

const parserSrc = `package main

type Foo struct{}

func plain(x int) int { return x }

func (f Foo) Value() int { return 1 }

func (f *Foo) Ptr() int {
	if true {
		return 2
	}
	return 0
}

type I interface {
	M(x int) int
}
`

func TestExtractMethods_NamesAndRanges(t *testing.T) {
	methods, err := ExtractMethods("sample.go", []byte(parserSrc))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	wantNames := map[string]bool{"plain": false, "(Foo)Value": false, "(Foo)Ptr": false}
	for _, m := range methods {
		if _, ok := wantNames[m.Name]; !ok {
			t.Fatalf("unexpected method %q", m.Name)
		}
		wantNames[m.Name] = true
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("method %q not extracted", name)
		}
	}
}

func TestExtractMethods_SkipsInterfaceMethods(t *testing.T) {
	methods, _ := ExtractMethods("sample.go", []byte(parserSrc))
	for _, m := range methods {
		if m.Name == "(I)M" || m.Name == "M" {
			t.Fatalf("interface method M should be skipped, got %q", m.Name)
		}
	}
}

func TestExtractMethods_LineRangesAndComplexity(t *testing.T) {
	methods, _ := ExtractMethods("sample.go", []byte(parserSrc))
	find := func(name string) MethodDescriptor {
		for _, m := range methods {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("method %q not found", name)
		return MethodDescriptor{}
	}

	plain := find("plain") // line 5
	if plain.StartLine != 5 || plain.EndLine != 5 {
		t.Errorf("plain range = [%d,%d], want [5,5]", plain.StartLine, plain.EndLine)
	}
	if plain.Complexity != 1 {
		t.Errorf("plain complexity = %d, want 1", plain.Complexity)
	}

	ptr := find("(Foo)Ptr") // declared on line 9, ends on line 14
	if ptr.Complexity != 2 {
		t.Errorf("(Foo)Ptr complexity = %d, want 2 (base + if)", ptr.Complexity)
	}
	if ptr.StartLine != 9 || ptr.EndLine != 14 {
		t.Errorf("(Foo)Ptr range = [%d,%d], want [9,14]", ptr.StartLine, ptr.EndLine)
	}

	val := find("(Foo)Value")
	if val.StartLine != 7 || val.EndLine != 7 {
		t.Errorf("(Foo)Value range = [%d,%d], want [7,7]", val.StartLine, val.EndLine)
	}
}
