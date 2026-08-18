package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxLooseFiles is the default maximum number of loose source files allowed
// directly in one directory (ported from crap4dart's folder_structure gate).
const maxLooseFiles = 0

// FolderStructureViolation is one directory with loose-file sprawl.
type FolderStructureViolation struct {
	Dir     string
	Message string
}

// RunFolderStructureCommand implements `crap4go folder-structure [dirs...]`:
// it flags directories containing more than maxLooseFiles non-test .go
// files directly (non-recursive) — a flat-file sprawl that should be
// organized into feature packages. Without positional dirs, the module
// root's direct children are checked (Go adaptation of crap4dart 0.9's
// folder_structure gate).
func RunFolderStructureCommand(args []string, root string, stdout, stderr io.Writer) int {
	dirs, err := selectGateDirs(args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var violations []FolderStructureViolation
	for _, dir := range dirs {
		if loose := countLooseFiles(dir); loose > maxLooseFiles {
			violations = append(violations, FolderStructureViolation{
				Dir: relPath(root, dir),
				Message: fmt.Sprintf("%d loose .go files directly in %s — group them into feature packages (max %d)",
					loose, relPath(root, dir), maxLooseFiles),
			})
		}
	}
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s: %s\n", v.Dir, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d directory(ies) with loose-file sprawl\n", len(violations))
		return 2
	}
	fmt.Fprintf(stdout, "%d directories organized into packages\n", len(dirs))
	return 0
}

// selectGateDirs returns the existing directories to check: the positional
// args resolved against root, defaulting to the module root itself.
func selectGateDirs(args []string, root string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	var dirs []string
	for _, arg := range args {
		path := arg
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, arg)
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", arg)
		}
		dirs = append(dirs, path)
	}
	return dirs, nil
}

// countLooseFiles counts the non-test .go files directly inside dir
// (non-recursive): files in subdirectories are the organized form.
func countLooseFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			count++
		}
	}
	return count
}
