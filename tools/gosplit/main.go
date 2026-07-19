// Binary gosplit splits Go source files at the AST level, moving methods
// matching a name pattern from source to target file.
//
// Usage:
//
//	go run ./tools/gosplit/ <source.go> <target.go> <pattern>
//
// Pattern is matched case-insensitively against function names.
// Run goimports on both files after the split to clean unused imports.
package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "Usage: gosplit <source.go> <target.go> <pattern>\n")
		os.Exit(1)
	}

	srcPath := os.Args[1]
	dstPath := os.Args[2]
	pattern := strings.ToLower(os.Args[3])

	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, srcPath, nil, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", srcPath, err)
		os.Exit(1)
	}

	var keep, move []ast.Decl
	for _, d := range src.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && shouldMove(fn, pattern) {
			move = append(move, d)
		} else {
			keep = append(keep, d)
		}
	}

	if len(move) == 0 {
		fmt.Println("no methods matched pattern")
		os.Exit(0)
	}

	// Deep-copy imports before the source file is modified.
	var savedImports []*ast.ImportSpec
	for _, imp := range src.Imports {
		savedImports = append(savedImports, &ast.ImportSpec{
			Name: imp.Name,
			Path: &ast.BasicLit{Kind: imp.Path.Kind, Value: imp.Path.Value},
		})
	}

	// Write source with remaining declarations.
	src.Decls = keep
	writeFile(srcPath, fset, src)
	fmt.Printf("kept %d decls in %s\n", len(keep), srcPath)

	// Build target file.
	var dst *ast.File
	if _, err := os.Stat(dstPath); err == nil {
		dst, err = parser.ParseFile(fset, dstPath, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", dstPath, err)
			os.Exit(1)
		}
	} else {
		dst = &ast.File{Name: src.Name}
		if len(savedImports) > 0 {
			dst.Decls = append(dst.Decls, &ast.GenDecl{
				Tok:   token.IMPORT,
				Specs: make([]ast.Spec, len(savedImports)),
			})
			for i, imp := range savedImports {
				dst.Decls[0].(*ast.GenDecl).Specs[i] = imp
			}
		}
	}

	dst.Decls = append(dst.Decls, move...)
	writeFile(dstPath, fset, dst)
	fmt.Printf("moved %d decls to %s\n", len(move), dstPath)
	fmt.Println("Run goimports on both files to clean unused imports.")
	for _, d := range move {
		if fn, ok := d.(*ast.FuncDecl); ok {
			fmt.Printf("  %s\n", fn.Name.Name)
		}
	}
}

func shouldMove(fn *ast.FuncDecl, pattern string) bool {
	return strings.Contains(strings.ToLower(fn.Name.Name), pattern)
}

func writeFile(path string, fset *token.FileSet, f *ast.File) {
	out, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", path, err)
		os.Exit(1)
	}
	defer out.Close()
	if err := format.Node(out, fset, f); err != nil {
		fmt.Fprintf(os.Stderr, "format %s: %v\n", path, err)
		os.Exit(1)
	}
}
