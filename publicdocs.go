package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
)

// PublicDocsViolation is one exported declaration without a doc comment.
type PublicDocsViolation struct {
	Path    string
	Line    int
	Message string
}

// ignorePath/ignoreLine expose the location to the shared ignore filter.
func (v PublicDocsViolation) ignorePath() string { return v.Path }
func (v PublicDocsViolation) ignoreLine() int    { return v.Line }

// RunPublicDocsCommand implements `crap4go public-docs [--exclude GLOB]...
// [paths...]`: it flags exported package-level declarations — functions,
// methods on exported receivers, types, vars, consts — without a godoc
// comment; New* constructors and methods on unexported receivers are exempt
// (upstream constructor/private-container rules, Go host analog). Prints one
// line per violation plus a summary and returns exit code 2 iff there are
// violations.
func RunPublicDocsCommand(args []string, root string, stdout, stderr io.Writer) int {
	exclude, paths, code := parsePublicDocsFlags(args, stderr)
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
	violations, checked := CheckPublicDocs(files, root, exclude)
	violations = filterIgnored("public-docs", root, violations)
	for _, v := range violations {
		fmt.Fprintf(stdout, violationLocationFormat, v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d public declarations missing docs\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d public declarations documented\n", checked)
	return 0
}

// parsePublicDocsFlags parses the --exclude flag plus the positional paths;
// the returned code is 1 on a usage error (already reported).
func parsePublicDocsFlags(args []string, stderr io.Writer) ([]string, []string, int) {
	fs := flag.NewFlagSet("crap4go public-docs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	excl := &stringSlice{}
	fs.Var(excl, excludeFlagName, excludeFlagUsage)
	if err := fs.Parse(args); err != nil {
		return nil, nil, 1
	}
	return excl.slice(), fs.Args(), 0
}

// CheckPublicDocs evaluates every non-excluded file's exported declarations
// and returns the missing-doc violations plus the number of declarations
// checked.
func CheckPublicDocs(files []string, root string, exclude []string) ([]PublicDocsViolation, int) {
	var violations []PublicDocsViolation
	checked := 0
	for _, f := range files {
		if matchesAnyGlob(relPath(root, f), exclude) {
			continue
		}
		checked += publicDocsFile(f, root, &violations)
	}
	return violations, checked
}

// publicDocsFile flags one file's undocumented exported declarations,
// appending violations to out, and returns the declarations checked.
func publicDocsFile(f, root string, out *[]PublicDocsViolation) int {
	file, fset, err := parseGoFile(f)
	if err != nil {
		return 0
	}
	rel := relPath(root, f)
	missing := func(pos token.Pos, kind, name string) {
		*out = append(*out, PublicDocsViolation{
			Path:    rel,
			Line:    fset.Position(pos).Line,
			Message: fmt.Sprintf("missing godoc on %s %q", kind, name),
		})
	}
	checked := 0
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			checked += funcDocs(d, fset, missing)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				checked += publicDocsSpec(d, spec, missing)
			}
		}
	}
	return checked
}

// funcDocs checks one function declaration: exported, receiver-exported
// (methods on unexported types are not public API), and not a New*
// constructor (upstream exempts constructors). Returns whether it was
// checked.
func funcDocs(fd *ast.FuncDecl, fset *token.FileSet, missing func(token.Pos, string, string)) int {
	if !fd.Name.IsExported() || (fd.Recv != nil && !receiverExported(fd)) || isConstructor(fd) {
		return 0
	}
	if fd.Doc == nil {
		missing(fd.Pos(), funcKind(fd), funcDisplayName(fd))
	}
	return 1
}

// publicDocsSpec checks one spec of a type/var/const declaration group; a
// spec is documented by the group's own doc comment or one directly above
// the spec. Returns the number of exported names checked.
func publicDocsSpec(d *ast.GenDecl, spec ast.Spec, missing func(token.Pos, string, string)) int {
	report := func(pos token.Pos, name string) {
		if !documented(d.Doc, specDoc(spec)) {
			missing(pos, d.Tok.String(), name)
		}
	}
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if !s.Name.IsExported() {
			return 0
		}
		report(s.Pos(), s.Name.Name)
		return 1
	case *ast.ValueSpec:
		checked := 0
		for _, name := range s.Names {
			if !name.IsExported() {
				continue
			}
			checked++
			report(name.Pos(), name.Name)
		}
		return checked
	}
	return 0
}

// specDoc returns the doc comment directly above a type/var/const spec, or
// nil for other spec kinds.
func specDoc(spec ast.Spec) *ast.CommentGroup {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return s.Doc
	case *ast.ValueSpec:
		return s.Doc
	}
	return nil
}

// documented reports whether a declaration group or its spec carries a doc
// comment.
func documented(group, spec *ast.CommentGroup) bool {
	return group != nil || spec != nil
}

// funcKind is the godoc kind label for a function declaration: "method" for
// receiver-bearing declarations, "func" otherwise.
func funcKind(fd *ast.FuncDecl) string {
	if fd.Recv != nil {
		return "method"
	}
	return "func"
}
