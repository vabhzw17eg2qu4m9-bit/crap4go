package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// TestParseStatusLine covers all branches of the porcelain-line parser:
// blank/short lines, normal paths, leading-space trimming, and renames.
func TestParseStatusLine(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"??":                "",
		"abc":               "", // too short
		"?? foo.go":         "foo.go",
		"M  bar.go":         "bar.go",
		"A  dir/baz.go":     "dir/baz.go",
		"R  old.go -> n.go": "n.go",
		"C  a.go -> b.go":   "b.go",
		" D gone.go":        "gone.go",
	}
	for in, want := range cases {
		if got := parseStatusLine(in); got != want {
			t.Errorf("parseStatusLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// gitRepo initializes a throwaway git repo under dir and returns a cleanup.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

// TestChangedFiles exercises the real git subprocess: untracked .go files are
// kept, _test.go and non-.go files are dropped, and paths are joined to root.
func TestChangedFiles(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)

	mustWrite := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("tracked.go", "package x\n")
	mustWrite("ignored_test.go", "package x\n")
	mustWrite("notes.txt", "hi\n")
	mustWrite("untracked.go", "package x\n")

	// Commit tracked.go, then modify it so it shows as Modified, and stage
	// a rename so the rename branch of parseStatusLine is exercised end-to-end.
	mustWrite("renamed.go", "package x\n")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "tracked.go", "renamed.go")
	run("commit", "-qm", "seed")
	// Rename renamed.go -> renamed2.go (staged rename) and modify tracked.go.
	run("mv", "renamed.go", "renamed2.go")
	if err := os.WriteFile(filepath.Join(root, "tracked.go"), []byte("package x // mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ChangedFiles(root)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	// Expect the non-test .go files git knows about: tracked.go (M),
	// renamed2.go (R target), untracked.go (??). ignored_test.go and
	// notes.txt must be absent.
	want := []string{
		filepath.Join(root, "renamed2.go"),
		filepath.Join(root, "tracked.go"),
		filepath.Join(root, "untracked.go"),
	}
	sort.Strings(got)
	if !pathsEqual(got, want) {
		t.Errorf("ChangedFiles =\n  %v\nwant\n  %v", got, want)
	}
}

// TestFindSourceFiles covers the directory walker: vendor/ skipped, _test.go
// skipped, nested .go included, sorted output.
func TestFindSourceFiles(t *testing.T) {
	root := t.TempDir()
	must := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("a.go", "package x\n")
	must("a_test.go", "package x\n")
	must("vendor/v.go", "package v\n")
	must("pkg/b.go", "package pkg\n")
	must("pkg/b_test.go", "package pkg\n")

	got, err := FindSourceFiles(root)
	if err != nil {
		t.Fatalf("FindSourceFiles: %v", err)
	}
	want := []string{
		filepath.Join(root, "a.go"),
		filepath.Join(root, "pkg/b.go"),
	}
	if !pathsEqual(got, want) {
		t.Errorf("FindSourceFiles =\n  %v\nwant\n  %v", got, want)
	}
}

// TestExpandPaths covers file-kept, dir-walked, dedup, and sort behavior.
func TestExpandPaths(t *testing.T) {
	root := t.TempDir()
	must := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("one.go", "package x\n")
	must("pkg/two.go", "package pkg\n")
	must("pkg/two_test.go", "package pkg\n")

	// Explicit file, a directory, and a duplicate that must be deduped.
	got, err := ExpandPaths([]string{
		filepath.Join(root, "one.go"),
		filepath.Join(root, "pkg"),
		filepath.Join(root, "one.go"), // duplicate -> deduped
	}, root)
	if err != nil {
		t.Fatalf("ExpandPaths: %v", err)
	}
	want := []string{
		filepath.Join(root, "one.go"),
		filepath.Join(root, "pkg/two.go"),
	}
	if !pathsEqual(got, want) {
		t.Errorf("ExpandPaths =\n  %v\nwant\n  %v", got, want)
	}
}

func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
