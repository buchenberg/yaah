package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/buchenberg/yaah/internal/prompts"
)

// ReplaceTool performs regex find-and-replace across multiple files.
// It walks a directory tree, filters by include glob, and applies the
// replacement to every matching file.
type ReplaceTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*ReplaceTool)(nil)
	_ WorkspaceSetter     = (*ReplaceTool)(nil)
)

func (t *ReplaceTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *ReplaceTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *ReplaceTool) Name() string { return "replace" }
func (t *ReplaceTool) Description() string {
	return prompts.ToolDescription("replace")
}

func (t *ReplaceTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "The regex pattern to search for"},
			"replacement": {"type": "string", "description": "The replacement text (supports $1, $2, etc. for capture groups)"},
			"path": {"type": "string", "description": "Directory to search in (default: current directory)"},
			"include": {"type": "string", "description": "File glob pattern to filter (e.g. \"*.go\", \"*.{ts,tsx}\")"},
			"dry_run": {"type": "boolean", "description": "If true, show what would change without writing files"}
		},
		"required": ["pattern", "replacement"]
	}`)
}

func (t *ReplaceTool) IsDangerous(argsJSON string) bool { return true }

func (t *ReplaceTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		Path        string `json:"path"`
		Include     string `json:"include"`
		DryRun      bool   `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("replace: invalid arguments: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("replace: pattern is required")
	}
	if params.Path == "" {
		params.Path = "."
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.Path)
	if err != nil {
		return "", err
	}
	params.Path = resolved

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("replace: invalid regex: %w", err)
	}

	var includeRe *regexp.Regexp
	if params.Include != "" {
		pat := globToRegex(params.Include)
		includeRe, err = regexp.Compile(pat)
		if err != nil {
			return "", fmt.Errorf("replace: invalid include pattern: %w", err)
		}
	}

	var report multiResult
	totalFiles := 0
	totalMatches := 0
	totalChanged := 0

	walkErr := walkWorkspace(ctx, ws, params.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if commonIgnoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if binaryExtensions[filepath.Ext(p)] {
			return nil
		}
		if includeRe != nil && !includeRe.MatchString(filepath.Base(p)) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, readErr := ws.ReadFile(ctx, p)
		if readErr != nil {
			report.failf("%s: read: %v", p, readErr)
			return nil
		}

		content := string(data)
		newContent := re.ReplaceAllString(content, params.Replacement)
		if newContent == content {
			return nil
		}

		matchCount := countMatches(re, content)

		if params.DryRun {
			report.donef("%s: %d match(es)", p, matchCount)
			totalFiles++
			totalMatches += matchCount
			return nil
		}

		mode := os.FileMode(0o644)
		if fi, err := d.Info(); err == nil {
			mode = fi.Mode()
		} else if st, err := ws.Stat(ctx, p); err == nil {
			mode = st.Mode()
		}
		if err := ws.WriteFile(ctx, p, []byte(newContent), mode); err != nil {
			report.failf("%s: write: %v", p, err)
			return nil
		}

		report.donef("%s: %d match(es)", p, matchCount)
		totalFiles++
		totalMatches += matchCount
		totalChanged++
		return nil
	})

	if walkErr != nil && walkErr != filepath.SkipDir && ctx.Err() == nil {
		return "", fmt.Errorf("replace: %w", walkErr)
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("replace: %w", ctx.Err())
	}

	if params.DryRun {
		headline := fmt.Sprintf("DRY RUN — %d file(s) with %d match(es):", totalFiles, totalMatches)
		return report.Report(headline)
	}
	headline := fmt.Sprintf("Replaced %d match(es) across %d file(s):", totalMatches, totalChanged)
	return report.Report(headline)
}

func countMatches(re *regexp.Regexp, content string) int {
	return len(re.FindAllStringIndex(content, -1))
}
