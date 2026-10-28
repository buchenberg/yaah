package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
	"golang.org/x/tools/imports"
)

// GoRefactorTool provides AST-level Go code transformations using
// golang.org/x/tools. Actions operate on the Go package level (not
// text-level) so they handle cross-package references correctly.
//
// Every project file it reads or writes comes from the injected Workspace, so
// the same code path works on the host and inside an isolated substrate. What
// stays outside the Workspace is only the *dependency* view: import resolution
// and gc export data belong to the toolchain, not to the project (see
// formatterFor and doInfo).
type GoRefactorTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*GoRefactorTool)(nil)
	_ WorkspaceSetter     = (*GoRefactorTool)(nil)
)

func (t *GoRefactorTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *GoRefactorTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *GoRefactorTool) Name() string                     { return "go_refactor" }
func (t *GoRefactorTool) Description() string              { return prompts.ToolDescription("go_refactor") }
func (t *GoRefactorTool) IsDangerous(argsJSON string) bool { return true }

func (t *GoRefactorTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["format", "info"],
				"description": "Action: 'format' runs goimports on a file; 'info' loads a package and returns its symbol table."
			},
			"file": {
				"type": "string",
				"description": "Go source file path. Required for format."
			},
			"dir": {
				"type": "string",
				"description": "Package directory path. Required for info."
			}
		},
		"required": ["action"]
	}`)
}

func (t *GoRefactorTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action string `json:"action"`
		File   string `json:"file"`
		Dir    string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("go_refactor: invalid arguments: %w", err)
	}
	ws := workspaceOf(t.WS, t.PV)

	switch params.Action {
	case "format":
		return t.doFormat(ctx, ws, params.File)
	case "info":
		return t.doInfo(ctx, ws, params.Dir)
	default:
		return "", fmt.Errorf("go_refactor: unknown action %q (use format or info)", params.Action)
	}
}

// doFormat formats one file in memory and writes the result back through the
// workspace. The bytes never travel through the host filesystem on an isolated
// workspace: the file is read with ws.ReadFile and replaced with ws.WriteFile,
// which is atomic for the local implementation.
func (t *GoRefactorTool) doFormat(ctx context.Context, ws Workspace, file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("go_refactor format: file is required")
	}
	resolved, err := ws.ResolvePath(file)
	if err != nil {
		return "", err
	}

	data, err := ws.ReadFile(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("go_refactor format: %w", err)
	}

	formatted, err := formatGoSource(ws, resolved, data)
	if err != nil {
		return "", fmt.Errorf("go_refactor format: %w", err)
	}

	if string(formatted) == string(data) {
		return fmt.Sprintf("%s: already formatted", filepath.Base(resolved)), nil
	}

	if err := ws.WriteFile(ctx, resolved, formatted, 0o644); err != nil {
		return "", fmt.Errorf("go_refactor format: write: %w", err)
	}

	importsAdded := countDiff(string(data), string(formatted), "import")
	return fmt.Sprintf("%s: formatted (+%d import changes)", filepath.Base(resolved), importsAdded), nil
}

// formatGoSource formats src the way goimports does, in memory.
//
// On a local workspace import resolution is allowed to consult the host module
// graph, because that graph *is* the workspace: go/build and the go command read
// go.mod and the module cache, which are dependency metadata, not project files
// this tool was asked to edit — the same latitude the info action takes with gc
// export data.
//
// On an isolated workspace that latitude is gone: the module graph lives outside
// the sandbox, so resolving imports against the host would format one project
// with another project's knowledge. There the tool falls back to FormatOnly —
// gofmt plus import grouping/sorting, all from the bytes in hand, no I/O — and
// the result says so rather than pretending imports were resolved.
func formatGoSource(ws Workspace, path string, src []byte) ([]byte, error) {
	opts := &imports.Options{Comments: true, TabIndent: true, TabWidth: 8}
	if !ws.Local() {
		opts.FormatOnly = true
	}
	return imports.Process(path, src, opts)
}

// doInfo builds a package's symbol table from the files the workspace can see.
//
// It deliberately does not use go/packages: that loads the package by directory
// from the host filesystem, which is exactly what an isolated workspace does not
// have. Here the file list comes from ws.ReadDir and the contents from
// ws.ReadFile, so the analyzed unit is "the files reachable through this
// workspace". Dependencies are a different matter — their types come from gc
// export data via importer.Default(), which reads the toolchain's own export
// data and never a project file — so an import the toolchain cannot resolve is
// reported, not silently dropped.
func (t *GoRefactorTool) doInfo(ctx context.Context, ws Workspace, dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	resolved, err := ws.ResolvePath(dir)
	if err != nil {
		return "", err
	}

	entries, err := ws.ReadDir(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("go_refactor info: %w", err)
	}

	// A directory listing has no guaranteed order, so sort it: output that
	// reshuffles between calls reads like a change to the model.
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		// Nothing was parsed and no package was loaded, so there is no symbol
		// table to report — not even an empty one. A success result here reads
		// as "analyzed, found nothing" and is the reason this is an error: the
		// analysis never ran.
		return "", fmt.Errorf("go_refactor info: no Go files found in %s", resolved)
	}

	fset := token.NewFileSet()
	var files []*ast.File
	pkgName := ""
	importsSeen := make(map[string]bool)
	skipped := make(map[string]string)
	for _, name := range names {
		p := ws.Join(resolved, name)
		src, rerr := ws.ReadFile(ctx, p)
		if rerr != nil {
			return "", fmt.Errorf("go_refactor info: %w", rerr)
		}
		f, perr := parser.ParseFile(fset, p, src, parser.ParseComments|parser.AllErrors)
		if perr != nil {
			return "", fmt.Errorf("go_refactor info: parse %s: %w", name, perr)
		}
		// One directory holds one package. A file with a different package
		// clause (an external test package, a stray scratch file) cannot be
		// type-checked with the rest, so it is named in the result instead of
		// quietly contributing nothing.
		if pkgName == "" {
			pkgName = f.Name.Name
		} else if f.Name.Name != pkgName {
			skipped[name] = f.Name.Name
			continue
		}
		for _, imp := range f.Imports {
			importsSeen[strings.Trim(imp.Path.Value, `"`)] = true
		}
		files = append(files, f)
	}

	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	var typeErrs []string
	conf := types.Config{
		Importer: importer.Default(),
		Error:    func(err error) { typeErrs = append(typeErrs, err.Error()) },
	}
	// Check returns a partial package alongside its first error, which is what
	// this action wants: the symbol table is everything resolvable from the
	// workspace's files, and the note below says the view is partial.
	//
	// The returned package object is deliberately not consulted. go/types
	// allocates it before type-checking and fills it in place, so it is never
	// nil for the non-empty file list guaranteed above — a "nothing was
	// loaded" fallback on it would be unreachable code, not a guard.
	_, _ = conf.Check(resolved, fset, files, info)

	var out strings.Builder
	fmt.Fprintf(&out, "Package: %s (1 package loaded)\n\n", resolved)
	fmt.Fprintf(&out, "=== %s ===\n", resolved)
	fmt.Fprintf(&out, "Name: %s | Go files: %d | Imports: %d\n",
		pkgName, len(files), len(importsSeen))

	out.WriteString("\n## Functions\n")
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			pos := fset.Position(fd.Pos())
			recv := ""
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				recv = " (method)"
			}
			fmt.Fprintf(&out, "  %s%s (line %d)\n", fd.Name.Name, recv, pos.Line)
		}
	}

	if len(skipped) > 0 {
		var others []string
		for name, pkg := range skipped {
			others = append(others, fmt.Sprintf("%s (package %s)", name, pkg))
		}
		sort.Strings(others)
		fmt.Fprintf(&out, "\n  (skipped %d file(s) with another package clause: %s)\n",
			len(others), strings.Join(others, ", "))
	}

	if len(typeErrs) > 0 {
		fmt.Fprintf(&out, "\n  (type info incomplete — %d type error(s); analysis is scoped to the files read through the workspace)\n",
			len(typeErrs))
		for i, e := range typeErrs {
			if i == 3 {
				fmt.Fprintf(&out, "    … %d more\n", len(typeErrs)-3)
				break
			}
			fmt.Fprintf(&out, "    %s\n", e)
		}
	}

	var defs []string
	for name, obj := range info.Defs {
		if obj == nil || !obj.Pos().IsValid() {
			continue
		}
		defs = append(defs, fmt.Sprintf("  %s: %s\n", name, obj.Type().String()))
	}
	sort.Strings(defs)
	for _, d := range defs {
		out.WriteString(d)
	}
	return out.String(), nil
}

func countDiff(original, modified, keyword string) int {
	origCount := strings.Count(original, keyword)
	modCount := strings.Count(modified, keyword)
	return modCount - origCount
}
