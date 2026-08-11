package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// FindSourceFiles walks root recursively and returns the sorted list of .go
// files excluding *_test.go and anything under vendor/.
func FindSourceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// ChangedFiles returns the git-tracked or untracked .go source files (excluding
// *_test.go) reported by "git -C root status --porcelain". Added, modified,
// untracked, and rename-target entries are kept; deleted entries are dropped by
// the .go suffix check.
func ChangedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		path := parseStatusLine(line)
		if path == "" {
			continue
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(root, path))
	}
	sort.Strings(files)
	return files, nil
}

// parseStatusLine extracts the relevant path from one porcelain line. Format
// is "XY <path>" or "XY <old> -> <new>" for renames; XY is two status chars
// followed by a space. Returns "" for blank or short lines.
func parseStatusLine(line string) string {
	if len(line) < 4 {
		return ""
	}
	pathPart := strings.TrimSpace(line[3:])
	if pathPart == "" {
		return ""
	}
	if idx := strings.Index(pathPart, " -> "); idx >= 0 {
		pathPart = pathPart[idx+4:]
	}
	return pathPart
}

// ExpandPaths resolves explicit CLI path args: each file is kept verbatim, each
// directory is walked for Go source via FindSourceFiles. Results are deduped
// and sorted. Non-flag args are resolved against root when relative.
func ExpandPaths(args []string, root string) ([]string, error) {
	seen := make(map[string]bool)
	var files []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
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
		if info.IsDir() {
			walked, err := FindSourceFiles(path)
			if err != nil {
				return nil, err
			}
			for _, f := range walked {
				add(f)
			}
		} else {
			add(path)
		}
	}
	sort.Strings(files)
	return files, nil
}
