package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
)

// defaultAnonStructMaxFields caps the field count of an anonymous struct type
// in a signature before the message calls the shape oversized, ported from
// crap4dart 4d844a4's record gates (record_size's max_fields default 5).
const defaultAnonStructMaxFields = 5

// AnonStructViolation is one anonymous struct type found in a function
// signature.
type AnonStructViolation struct {
	Path    string
	Line    int
	Message string
}

// anonStructOptions holds the parsed `anon-struct-signatures` subcommand
// configuration.
type anonStructOptions struct {
	maxFields int
	paths     []string
}

// RunAnonStructSignaturesCommand implements `crap4go anon-struct-signatures
// [--max-fields N] [paths...]`: it flags anonymous struct types in function
// signatures — declarations and function literals, parameters and results,
// nested inside slice/map/chan/pointer types and generic type arguments
// included. Go adaptation of crap4dart 4d844a4's record gates (inline record
// types + unnamed record fields collapsed into one shape: every Go anonymous
// struct in a signature forces consumers to diff the shape field-by-field;
// Dart's .$N positional access has no Go counterpart). Unnamed multiple
// results are Go's core idiom and never flagged. Named types are the blessed
// form and are not scanned. Exit code 2 iff there are violations.
func RunAnonStructSignaturesCommand(args []string, root string, stdout, stderr io.Writer) int {
	opts, code, ok := parseAnonStructFlags(args, stderr)
	if !ok {
		return code
	}
	files, err := selectFiles(false, opts.paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckAnonStructSignatures(files, root, opts.maxFields)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d anonymous struct types in %d signatures\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d signatures checked, no anonymous struct types in signatures\n", checked)
	return 0
}

// parseAnonStructFlags parses the anon-struct-signatures flags plus positional
// paths. ok=false means a usage error (already reported to stderr) or help
// printed; the code is the exit code to return.
func parseAnonStructFlags(args []string, stderr io.Writer) (anonStructOptions, int, bool) {
	fs := flag.NewFlagSet("crap4go anon-struct-signatures", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := anonStructOptions{maxFields: defaultAnonStructMaxFields}
	fs.IntVar(&opts.maxFields, "max-fields", defaultAnonStructMaxFields, "field cap over which an anonymous struct is called oversized")
	if err := fs.Parse(args); err != nil {
		return anonStructOptions{}, 1, false
	}
	if opts.maxFields < 1 {
		fmt.Fprintln(stderr, "--max-fields must be positive")
		return anonStructOptions{}, 1, false
	}
	opts.paths = fs.Args()
	return opts, 0, true
}

// CheckAnonStructSignatures scans every function signature in the file set
// and returns the anonymous-struct violations plus the number of signatures
// checked.
func CheckAnonStructSignatures(files []string, root string, maxFields int) ([]AnonStructViolation, int) {
	var violations []AnonStructViolation
	signatures := 0
	for _, f := range files {
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		signatures += scanAnonStructSignatures(file, fset, relPath(root, f), maxFields, &violations)
	}
	return violations, signatures
}

// scanAnonStructSignatures walks one parsed file's signature nodes —
// FuncDecl, FuncLit and function-typed TypeSpec — recording anonymous-struct
// violations and returning the number of signatures checked.
func scanAnonStructSignatures(file *ast.File, fset *token.FileSet, path string, maxFields int, violations *[]AnonStructViolation) int {
	signatures := 0
	ast.Inspect(file, func(n ast.Node) bool {
		var typ *ast.FuncType
		switch node := n.(type) {
		case *ast.FuncDecl:
			typ = node.Type
		case *ast.FuncLit:
			typ = node.Type
		case *ast.TypeSpec:
			ft, ok := node.Type.(*ast.FuncType)
			if !ok {
				return true
			}
			// A named function type blesses nothing (upstream: a
			// function-type typedef hides the anonymous shape behind a
			// name without extracting it).
			typ = ft
		default:
			return true
		}
		signatures++
		*violations = append(*violations, anonStructInFuncType(typ, fset, path, maxFields)...)
		return true
	})
	return signatures
}

// anonStructInFuncType reports the outermost anonymous struct type reachable
// from the signature's parameter and result types, nested inside composite
// types (slice, map, chan, pointer, generic type arguments) included; an
// inner anonymous struct inside a flagged shape is fixed by the same
// extraction and not reported separately.
func anonStructInFuncType(typ *ast.FuncType, fset *token.FileSet, path string, maxFields int) []AnonStructViolation {
	var violations []AnonStructViolation
	for _, list := range []*ast.FieldList{typ.Params, typ.Results} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			ast.Inspect(field.Type, func(n ast.Node) bool {
				st, ok := n.(*ast.StructType)
				if !ok {
					return true
				}
				violations = append(violations, anonStructViolation(st, fset, path, maxFields))
				return false
			})
		}
	}
	return violations
}

// anonStructViolation builds the finding for one anonymous struct type: the
// message names the field cap when NumFields exceeds maxFields (upstream's
// record_size concern, folded into the same shape finding).
func anonStructViolation(st *ast.StructType, fset *token.FileSet, path string, maxFields int) AnonStructViolation {
	message := "anonymous struct type in signature: declare it as a named type and reference it by name"
	if fields := st.Fields.NumFields(); fields > maxFields {
		message = fmt.Sprintf("anonymous struct type with %d fields (max %d) in signature: declare it as a named type and reference it by name", fields, maxFields)
	}
	return AnonStructViolation{
		Path:    path,
		Line:    fset.Position(st.Pos()).Line,
		Message: message,
	}
}
