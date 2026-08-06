package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// GoRefactorTool provides AST-level Go code transformations using
// golang.org/x/tools. Actions operate on the Go package level (not
// text-level) so they handle cross-package references correctly.
type GoRefactorTool struct{ PV *PathValidator }

var _ PathValidatorSetter = (*GoRefactorTool)(nil)

func (t *GoRefactorTool) SetPathValidator(pv *PathValidator) { t.PV = pv }

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

	switch params.Action {
	case "format":
		return t.doFormat(params.File)
	case "info":
		return t.doInfo(params.Dir)
	default:
		return "", fmt.Errorf("go_refactor: unknown action %q (use format or info)", params.Action)
	}
}

func (t *GoRefactorTool) doFormat(file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("go_refactor format: file is required")
	}
	resolved, err := resolvePathWithPV(t.PV, file)
	if err != nil {
		return "", err
	}
	file = resolved
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("go_refactor format: %w", err)
	}

	formatted, err := imports.Process(file, data, nil)
	if err != nil {
		return "", fmt.Errorf("go_refactor format: %w", err)
	}

	if string(formatted) == string(data) {
		return fmt.Sprintf("%s: already formatted", filepath.Base(file)), nil
	}

	if err := os.WriteFile(file, formatted, 0o644); err != nil {
		return "", fmt.Errorf("go_refactor format: write: %w", err)
	}

	importsAdded := countDiff(string(data), string(formatted), "import")
	return fmt.Sprintf("%s: formatted (+%d import changes)", filepath.Base(file), importsAdded), nil
}

func (t *GoRefactorTool) doInfo(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	resolved, err := resolvePathWithPV(t.PV, dir)
	if err != nil {
		return "", err
	}
	dir = resolved

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return "", fmt.Errorf("go_refactor info: load: %w", err)
	}

	var out strings.Builder
	out.WriteString(fmt.Sprintf("Package: %s (%d packages loaded)\n\n", dir, len(pkgs)))

	for _, pkg := range pkgs {
		out.WriteString(fmt.Sprintf("=== %s ===\n", pkg.PkgPath))
		out.WriteString(fmt.Sprintf("Name: %s | Go files: %d | Imports: %d\n",
			pkg.Name, len(pkg.GoFiles), len(pkg.Imports)))

		if pkg.Name == "" {
			continue
		}

		out.WriteString("\n## Functions\n")
		for _, f := range pkg.Syntax {
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					pos := pkg.Fset.Position(fd.Pos())
					recv := ""
					if fd.Recv != nil && len(fd.Recv.List) > 0 {
						recv = " (method)"
					}
					out.WriteString(fmt.Sprintf("  %s%s (line %d)\n", fd.Name.Name, recv, pos.Line))
				}
			}
		}

		if pkg.TypesInfo == nil {
			out.WriteString("\n  (type info unavailable — package has errors)\n")
			continue
		}
		for name, obj := range pkg.TypesInfo.Defs {
			if obj == nil || !obj.Pos().IsValid() {
				continue
			}
			out.WriteString(fmt.Sprintf("  %s: %s\n", name, obj.Type().String()))
		}
	}

	if out.Len() == 0 {
		return fmt.Sprintf("go_refactor info: no symbols found in %s", dir), nil
	}
	return out.String(), nil
}

func countDiff(original, modified, keyword string) int {
	origCount := strings.Count(original, keyword)
	modCount := strings.Count(modified, keyword)
	return modCount - origCount
}
