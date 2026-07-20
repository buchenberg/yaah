package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

// GoOutlineTool parses Go source files using go/ast and returns structural
// outlines or extracts specific symbols by name. Read-only — never dangerous.
type GoOutlineTool struct{}

func (t *GoOutlineTool) Name() string        { return "go_outline" }
func (t *GoOutlineTool) Description() string { return "Parses a Go file with go/ast: outline its structure or extract a named symbol's source." }

func (t *GoOutlineTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["outline", "extract"],
				"description": "Action: 'outline' lists all declarations with line ranges; 'extract' returns the source of a named symbol"
			},
			"file": {
				"type": "string",
				"description": "Path to the Go source file"
			},
			"name": {
				"type": "string",
				"description": "Symbol name to extract. For methods use the form '(*Type).Method' or 'Type.Method'."
			}
		},
		"required": ["action", "file"]
	}`)
}

func (t *GoOutlineTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action string `json:"action"`
		File   string `json:"file"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("go_outline: invalid arguments: %w", err)
	}
	if params.Action == "" {
		return "", fmt.Errorf("go_outline: action is required (outline or extract)")
	}
	if params.File == "" {
		return "", fmt.Errorf("go_outline: file is required")
	}
	params.File = expandHomeDir(params.File)

	if params.Action == "extract" && params.Name == "" {
		return "", fmt.Errorf("go_outline: name is required for extract action")
	}

	src, err := os.ReadFile(params.File)
	if err != nil {
		return "", fmt.Errorf("go_outline: %w", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, params.File, src, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("go_outline: parse error: %w", err)
	}

	switch params.Action {
	case "outline":
		return buildOutline(fset, f, params.File), nil
	case "extract":
		return extractSymbol(fset, f, src, params.Name, params.File)
	default:
		return "", fmt.Errorf("go_outline: unsupported action %q — use outline or extract", params.Action)
	}
}

// --- outline ---

type outlineEntry struct {
	kind  string
	name  string
	start int
	end   int
}

func buildOutline(fset *token.FileSet, f *ast.File, filename string) string {
	var entries []outlineEntry

	// Package.
	pkgEnd := fset.Position(f.Name.End()).Line
	entries = append(entries, outlineEntry{"package", f.Name.Name, 1, pkgEnd})

	// Imports.
	if len(f.Imports) > 0 {
		first := fset.Position(f.Imports[0].Pos()).Line
		last := fset.Position(f.Imports[len(f.Imports)-1].End()).Line
		entries = append(entries, outlineEntry{"import", importList(f.Imports), first, last})
	}

	// Top-level declarations in source order.
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			addGenDecl(&entries, fset, d)
		case *ast.FuncDecl:
			addFuncDecl(&entries, fset, d)
		}
	}

	// Sort by start line.
	sort.Slice(entries, func(i, j int) bool { return entries[i].start < entries[j].start })

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "File: %s\n", filename)
	for _, e := range entries {
		if e.start == e.end {
			fmt.Fprintf(&buf, "  %-4d       %s %s\n", e.start, e.kind, e.name)
		} else {
			fmt.Fprintf(&buf, "  %-4d-%-4d  %s %s\n", e.start, e.end, e.kind, e.name)
		}
	}
	return strings.TrimRight(buf.String(), "\n")
}

func importList(imports []*ast.ImportSpec) string {
	paths := make([]string, 0, len(imports))
	for _, imp := range imports {
		p := strings.Trim(imp.Path.Value, `"`)
		paths = append(paths, p)
	}
	return strings.Join(paths, ", ")
}

func addGenDecl(entries *[]outlineEntry, fset *token.FileSet, d *ast.GenDecl) {
	start := fset.Position(d.Pos()).Line
	end := fset.Position(d.End()).Line

	switch d.Tok {
	case token.CONST:
		if len(d.Specs) == 1 {
			vs := d.Specs[0].(*ast.ValueSpec)
			name := identNames(vs.Names)
			*entries = append(*entries, outlineEntry{"const", name, start, end})
		} else {
			*entries = append(*entries, outlineEntry{"const", fmt.Sprintf("(%d specs)", len(d.Specs)), start, end})
		}
	case token.VAR:
		if len(d.Specs) == 1 {
			vs := d.Specs[0].(*ast.ValueSpec)
			name := identNames(vs.Names)
			*entries = append(*entries, outlineEntry{"var", name, start, end})
		} else {
			*entries = append(*entries, outlineEntry{"var", fmt.Sprintf("(%d specs)", len(d.Specs)), start, end})
		}
	case token.TYPE:
		for _, spec := range d.Specs {
			ts := spec.(*ast.TypeSpec)
			n := ts.Name.Name
			switch ts.Type.(type) {
			case *ast.StructType:
				n = "struct " + n
			case *ast.InterfaceType:
				n = "interface " + n
			}
			s := fset.Position(ts.Pos()).Line
			e := fset.Position(ts.End()).Line
			*entries = append(*entries, outlineEntry{"type", n, s, e})
		}
	}
}

func addFuncDecl(entries *[]outlineEntry, fset *token.FileSet, d *ast.FuncDecl) {
	start := fset.Position(d.Pos()).Line
	end := fset.Position(d.End()).Line

	name := d.Name.Name
	if d.Recv != nil && len(d.Recv.List) > 0 {
		recv := receiverName(d.Recv.List[0])
		name = fmt.Sprintf("(%s).%s", recv, name)
	}
	*entries = append(*entries, outlineEntry{"func", name, start, end})
}

func receiverName(field *ast.Field) string {
	switch t := field.Type.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return "*" + ident.Name
		}
		return fmtExpr(t)
	case *ast.Ident:
		return t.Name
	default:
		return fmtExpr(field.Type)
	}
}

func identNames(idents []*ast.Ident) string {
	names := make([]string, len(idents))
	for i, id := range idents {
		names[i] = id.Name
	}
	return strings.Join(names, ", ")
}

// fmtExpr returns a best-effort string representation of an ast.Expr.
func fmtExpr(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + fmtExpr(t.X)
	case *ast.SelectorExpr:
		return fmtExpr(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + fmtExpr(t.Elt)
	case *ast.MapType:
		return "map[" + fmtExpr(t.Key) + "]" + fmtExpr(t.Value)
	default:
		return fmt.Sprintf("%T", e)
	}
}

// --- extract ---

func extractSymbol(fset *token.FileSet, f *ast.File, src []byte, name, filename string) (string, error) {
	lines := strings.Split(string(src), "\n")

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					ts := spec.(*ast.TypeSpec)
					if ts.Name.Name == name {
						return formatExtracted(lines, fset, ts, filename, "type", name), nil
					}
				}
			}
		case *ast.FuncDecl:
			n := funcDisplayName(d)
			if n == name {
				return formatExtracted(lines, fset, d, filename, "func", n), nil
			}
		}
	}

	return "", fmt.Errorf("go_outline: symbol %q not found in %s", name, filename)
}

func funcDisplayName(d *ast.FuncDecl) string {
	if d.Recv != nil && len(d.Recv.List) > 0 {
		return fmt.Sprintf("(%s).%s", receiverName(d.Recv.List[0]), d.Name.Name)
	}
	return d.Name.Name
}

func formatExtracted(lines []string, fset *token.FileSet, node ast.Node, filename, kind, name string) string {
	start := fset.Position(node.Pos()).Line
	end := fset.Position(node.End()).Line

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "// %s: %s %s (lines %d-%d)\n", filename, kind, name, start, end)
	for i := start; i <= end; i++ {
		if i-1 < len(lines) {
			fmt.Fprintf(&buf, "%d| %s\n", i, lines[i-1])
		}
	}
	return strings.TrimRight(buf.String(), "\n")
}
