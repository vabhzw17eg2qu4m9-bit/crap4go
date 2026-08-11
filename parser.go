package main

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// ExtractMethods parses Go source and returns one MethodDescriptor per
// function or method declaration that has a body. Interface method declarations
// (no body) are skipped. Names follow the form "(Foo)Bar" for methods or "Bar"
// for plain functions. Source is parsed with ParseFile in mode 0 (no type
// checking); if filePath is non-empty it is recorded in the file set for line
// positions, otherwise src alone is parsed.
func ExtractMethods(filePath string, src []byte) ([]MethodDescriptor, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, src, 0)
	if err != nil {
		return nil, err
	}
	var methods []MethodDescriptor
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		if recv := receiverTypeName(fd); recv != "" {
			name = "(" + recv + ")" + name
		}
		methods = append(methods, MethodDescriptor{
			Name:       name,
			StartLine:  fset.Position(fd.Pos()).Line,
			EndLine:    fset.Position(fd.End()).Line,
			Complexity: ComplexityOf(fd.Body),
		})
	}
	return methods, nil
}

// receiverTypeName returns the bare type name of a method's receiver (e.g.
// "Foo" for both "func (f Foo) M()" and "func (f *Foo) M()"), or "" when the
// declaration is a plain function or has no receiver.
func receiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
