package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// BulkFilesTool applies one copy/move/delete action to every workspace file
// matching a glob, with a dry-run preview. It exists so relocating or clearing
// a set of files is one call instead of N, especially inside a sub-agent
// sandbox where each turn is expensive.
type BulkFilesTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*BulkFilesTool)(nil)
	_ WorkspaceSetter     = (*BulkFilesTool)(nil)
)

func (t *BulkFilesTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *BulkFilesTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *BulkFilesTool) Name() string        { return "bulk_files" }
func (t *BulkFilesTool) Description() string { return prompts.ToolDescription("bulk_files") }

func (t *BulkFilesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["copy", "move", "delete"],
				"description": "Operation to apply to every match."
			},
			"pattern": {
				"type": "string",
				"description": "Glob matched against paths relative to path (e.g. \"*.go\", \"logs/*.jsonl\")."
			},
			"path": {
				"type": "string",
				"description": "Directory to search (default current directory)."
			},
			"to": {
				"type": "string",
				"description": "Destination root for copy/move. The matched path relative to path is preserved under it."
			},
			"force": {
				"type": "boolean",
				"description": "Replace an existing destination file. Default false: it is refused and reported as a failure.",
				"default": false
			},
			"mkdir_p": {
				"type": "boolean",
				"description": "Create missing destination parent directories. Default false.",
				"default": false
			},
			"dry_run": {
				"type": "boolean",
				"description": "List the planned operations without changing anything.",
				"default": false
			}
		},
		"required": ["action", "pattern"]
	}`)
}

func (t *BulkFilesTool) IsDangerous(args string) bool {
	var params struct {
		DryRun bool `json:"dry_run"`
	}
	// A dry run changes nothing, so it does not need approval. A malformed
	// call stays on the conservative side.
	return json.Unmarshal([]byte(args), &params) != nil || !params.DryRun
}

func (t *BulkFilesTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action  string `json:"action"`
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		To      string `json:"to"`
		Force   bool   `json:"force"`
		MkdirP  bool   `json:"mkdir_p"`
		DryRun  bool   `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bulk_files: invalid arguments: %w", err)
	}
	switch params.Action {
	case "copy", "move":
		if params.To == "" {
			return "", fmt.Errorf("bulk_files: to is required for %s", params.Action)
		}
	case "delete":
		if params.To != "" {
			return "", fmt.Errorf("bulk_files: to is not valid for delete")
		}
	default:
		return "", fmt.Errorf("bulk_files: action must be copy, move, or delete")
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("bulk_files: pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}
	ws := workspaceOf(t.WS, t.PV)
	root, err := ws.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("bulk_files: %w", err)
	}
	rootInfo, err := ws.Stat(ctx, root)
	if err != nil {
		return "", fmt.Errorf("bulk_files: %s: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("bulk_files: path %s is not a directory", root)
	}

	var toRoot string
	if params.Action != "delete" {
		toRoot, err = ws.ResolvePath(params.To)
		if err != nil {
			return "", fmt.Errorf("bulk_files: %w", err)
		}
		if pathWithin(ws, root, toRoot) {
			return "", fmt.Errorf("bulk_files: destination %s is inside the search root %s", toRoot, root)
		}
	}

	re, err := patternToRegex(filepath.ToSlash(params.Pattern))
	if err != nil {
		return "", fmt.Errorf("bulk_files: invalid pattern: %w", err)
	}
	hasSep := strings.ContainsAny(params.Pattern, "/\\")
	var matches []string
	walkErr := walkWorkspace(ctx, ws, root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d == nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		target := filepath.ToSlash(rel)
		if !hasSep {
			target = d.Name()
		}
		if re.MatchString(target) {
			matches = append(matches, p)
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipDir && walkErr != filepath.SkipAll {
		return "", fmt.Errorf("bulk_files: %w", walkErr)
	}
	if len(matches) == 0 {
		return "No matches.", nil
	}
	sort.Strings(matches)

	var res multiResult
	for _, src := range matches {
		rel, err := filepath.Rel(root, src)
		if err != nil {
			res.failf("%s: %v", src, err)
			continue
		}
		var dst string
		if params.Action != "delete" {
			dst = ws.Join(toRoot, rel)
		}
		if params.DryRun {
			switch params.Action {
			case "delete":
				res.donef("delete %s", src)
			default:
				res.donef("%s %s -> %s", params.Action, src, dst)
			}
			continue
		}

		switch params.Action {
		case "delete":
			if err := ws.Remove(ctx, src); err != nil {
				res.failf("delete %s: %v", src, err)
			} else {
				res.donef("deleted %s", src)
			}
		case "copy", "move":
			if params.MkdirP {
				if err := ws.MkdirAll(ctx, wsPathDir(ws, dst), 0o755); err != nil {
					res.failf("%s %s: create parent: %v", params.Action, src, err)
					continue
				}
			}
			if dstInfo, derr := ws.Lstat(ctx, dst); derr == nil && !params.Force {
				res.failf("%s %s: destination %s already exists; pass force=true", params.Action, src, dst)
				continue
			} else if derr != nil && !errors.Is(derr, fs.ErrNotExist) {
				res.failf("%s %s: %v", params.Action, src, derr)
				continue
			} else if dstInfo != nil && dstInfo.IsDir() {
				res.failf("%s %s: destination %s is a directory", params.Action, src, dst)
				continue
			}
			var opErr error
			if params.Action == "copy" {
				opErr = ws.Copy(ctx, src, dst)
			} else {
				opErr = ws.Move(ctx, src, dst)
			}
			if opErr != nil {
				res.failf("%s %s -> %s: %v", params.Action, src, dst, opErr)
			} else {
				res.donef("%s %s -> %s", params.Action, src, dst)
			}
		}
	}
	if params.DryRun {
		return fmt.Sprintf("bulk_files %s dry-run (%d match(es)):%s", params.Action, len(matches), reportList(&res)), nil
	}
	return res.Report(fmt.Sprintf("bulk_files %s (%d match(es)):", params.Action, len(matches)))
}

// reportList renders the success steps for a dry run; the multiResult's
// failure list is empty in that path.
func reportList(r *multiResult) string {
	var b strings.Builder
	for _, d := range r.done {
		b.WriteString("\n  ")
		b.WriteString(d)
	}
	return truncateResult(b.String())
}
