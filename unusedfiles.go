package main

import (
	"fmt"
	"go/ast"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// UnusedFilesViolation is one non-main package never imported by any other
// analyzed package.
type UnusedFilesViolation struct {
	Path    string
	Message string
}

// RunUnusedFilesCommand implements `crap4go unused-files [paths...]`. Go
// adaptation of crap4dart's UnusedFilesGate: it flags non-main packages in
// the module that are never imported by any other analyzed package (main
// packages are entry points and never flagged, like Dart files with a
// top-level main). Package selection is whole-module by nature, so an
// explicit path selection prints a skip message and exits 0 (ported from
// crap4dart 0.5.1).
func RunUnusedFilesCommand(args []string, root string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stdout, "unused-files: not meaningful for a partial selection")
		return 0
	}
	files, err := selectFiles(false, nil, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckUnusedFiles(files, root, modulePath(root))
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s: %s\n", v.Path, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d packages are never imported\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d packages are imported by analyzed code\n", checked)
	return 0
}

// CheckUnusedFiles returns the non-main packages (keyed by project-relative
// directory) never imported by any analyzed file, plus the number of package
// directories checked.
func CheckUnusedFiles(files []string, root, module string) ([]UnusedFilesViolation, int) {
	pkgNames := map[string]string{}
	imported := map[string]bool{}
	for _, f := range files {
		file, _, err := parseGoFile(f)
		if err != nil {
			continue
		}
		recordPackage(file, relPath(root, filepath.Dir(f)), module, pkgNames, imported)
	}
	return unusedPackageViolations(pkgNames, imported), nonMainCount(pkgNames)
}

// recordPackage records a file's package name and its imports of packages
// inside the module.
func recordPackage(file *ast.File, pkgDir, module string, pkgNames map[string]string, imported map[string]bool) {
	pkgNames[pkgDir] = file.Name.Name
	for _, spec := range file.Imports {
		if target := resolveImportDir(unquoted(spec), module); target != "" {
			imported[target] = true
		}
	}
}

// unusedPackageViolations flags every non-main package never imported.
func unusedPackageViolations(pkgNames map[string]string, imported map[string]bool) []UnusedFilesViolation {
	var violations []UnusedFilesViolation
	for _, pkgDir := range sortedDirs(pkgNames) {
		name := pkgNames[pkgDir]
		if name == "main" || imported[pkgDir] {
			continue
		}
		violations = append(violations, UnusedFilesViolation{
			Path:    pkgDir,
			Message: fmt.Sprintf("package %s is never imported by any analyzed package", name),
		})
	}
	return violations
}

// nonMainCount counts package directories whose package is not main.
func nonMainCount(pkgNames map[string]string) int {
	n := 0
	for _, name := range pkgNames {
		if name != "main" {
			n++
		}
	}
	return n
}

// sortedDirs returns the map's keys sorted (deterministic output).
func sortedDirs(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unquoted returns an import spec's path without quotes.
func unquoted(spec *ast.ImportSpec) string {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return ""
	}
	return path
}

// resolveImportDir maps an import path inside the module to its
// project-relative directory, or "" for external imports.
func resolveImportDir(path, module string) string {
	if module == "" {
		return ""
	}
	if path == module {
		return "."
	}
	if strings.HasPrefix(path, module+"/") {
		return strings.TrimPrefix(path, module+"/")
	}
	return ""
}

// modulePath returns the module path declared in root's go.mod, or "" when
// absent or unreadable.
func modulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
