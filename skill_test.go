package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_Skill(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"skill"}, t.TempDir(), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	got := out.String()
	for _, want := range []string{
		"# crap4go Profiling Skill",
		"## When to Use",
		"## Reading the Report",
		"## How It Works",
		"go test",
		"TOTAL(ms)",
		"Install as an agent skill",
		"crap4go skill > .agents/skills/crap4go-profiling/SKILL.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill output missing %q", want)
		}
	}
	if lines := strings.Count(got, "\n"); lines > 80 {
		t.Errorf("skill output is %d lines, want under ~80", lines)
	}
}

func TestRun_SkillAsPathIsNotDispatched(t *testing.T) {
	// "skill" must only dispatch as the FIRST argument; as a later
	// positional arg it takes the analyze path (here: nonexistent path →
	// error exit 1, not skill output).
	var out, errOut bytes.Buffer
	code := runWithRoot([]string{"somepath", "skill"}, t.TempDir(), &out, &errOut)
	if code != 1 || strings.Contains(out.String(), "Profiling Skill") {
		t.Errorf("exit = %d, want 1 with no skill output (out=%q)", code, out.String())
	}
}
