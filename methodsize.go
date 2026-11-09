package main

import (
	"flag"
	"fmt"
	"go/ast"
	"io"
	"strings"
)

// Method-size limits, ported from crap4dart's MethodSizeGateConfig defaults.
const (
	defaultMethodLines  = 60
	defaultMethodParams = 6
)

// MethodSizeViolation is one function over the line or parameter limit.
type MethodSizeViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v MethodSizeViolation) ignorePath() string { return v.Path }
func (v MethodSizeViolation) ignoreLine() int    { return v.Line }

// RunMethodSizeCommand implements `crap4go method-size [--max-lines N]
// [--max-params N] [paths...]`: it flags functions spanning more than
// --max-lines or taking more than --max-params parameters; New* functions
// (the Go constructor convention) are checked for parameters only, mirroring
// the upstream gate's constructor exemption. Prints one line per violation
// plus a summary and returns exit code 2 iff there are violations.
func RunMethodSizeCommand(args []string, root string, stdout, stderr io.Writer) int {
	maxLines, maxParams, paths, code := parseMethodSizeFlags(args, stderr)
	if code != 0 {
		return code
	}
	files, err := selectFiles(false, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, noGoFilesNotice)
		return 0
	}
	violations, checked := CheckMethodSize(files, root, maxLines, maxParams)
	violations = filterIgnored("method-size", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d violations in %d methods over their size limits\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d methods within their size limits\n", checked)
	return 0
}

// parseMethodSizeFlags parses the --max-lines/--max-params flags plus the
// positional paths; the returned code is 1 on a usage error (already
// reported).
func parseMethodSizeFlags(args []string, stderr io.Writer) (int, int, []string, int) {
	fs := flag.NewFlagSet("crap4go method-size", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxLines := fs.Int("max-lines", defaultMethodLines, "max lines per method")
	maxParams := fs.Int("max-params", defaultMethodParams, "max parameters per method")
	if err := fs.Parse(args); err != nil {
		return 0, 0, nil, 1
	}
	return *maxLines, *maxParams, fs.Args(), 0
}

// CheckMethodSize evaluates every declared function and returns the size
// violations plus the number of functions checked.
func CheckMethodSize(files []string, root string, maxLines, maxParams int) ([]MethodSizeViolation, int) {
	var violations []MethodSizeViolation
	checked := 0
	for _, f := range files {
		checked += methodSizeFile(f, root, maxLines, maxParams, &violations)
	}
	return violations, checked
}

// methodSizeFile flags one file's functions over the size limits, appending
// violations to out, and returns the functions checked. New* constructors
// are checked for parameters only.
func methodSizeFile(f, root string, maxLines, maxParams int, out *[]MethodSizeViolation) int {
	file, fset, err := parseGoFile(f)
	if err != nil {
		return 0
	}
	checked := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		checked++
		name := funcDisplayName(fd)
		startLine := fset.Position(fd.Pos()).Line
		if !isConstructor(fd) {
			lines := fset.Position(fd.End()).Line - startLine + 1
			if lines > maxLines {
				*out = append(*out, MethodSizeViolation{
					Path:    relPath(root, f),
					Line:    startLine,
					Message: fmt.Sprintf("%s has %d lines > max %d", name, lines, maxLines),
				})
			}
		}
		if count := fd.Type.Params.NumFields(); count > maxParams {
			*out = append(*out, MethodSizeViolation{
				Path:    relPath(root, f),
				Line:    startLine,
				Message: fmt.Sprintf("%s has %d params > max %d", name, count, maxParams),
			})
		}
	}
	return checked
}

// isConstructor reports whether fd follows the New* constructor convention:
// constructors are checked for parameters only, per the upstream method-size
// gate's constructor exemption.
func isConstructor(fd *ast.FuncDecl) bool {
	return fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "New")
}

// receiverExported reports whether a method's receiver type is exported;
// methods on unexported types are not public API (the upstream gate exempts
// members of private containers). Generic receivers do not resolve to a
// plain base name and are skipped.
func receiverExported(fd *ast.FuncDecl) bool {
	if fd.Recv == nil {
		return false
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	id, ok := t.(*ast.Ident)
	return ok && id.IsExported()
}
