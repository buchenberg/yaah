package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// replaceMaxResultLen caps the per-file result message length.
const replaceMaxResultLen = 4096

// ReplaceTool performs regex find-and-replace across multiple files.
// It walks a directory tree, filters by include glob, and applies the
// replacement to every matching file.
type ReplaceTool struct{ PV *PathValidator }

var _ PathValidatorSetter = (*ReplaceTool)(nil)

func (t *ReplaceTool) SetPathValidator(pv *PathValidator) { t.PV = pv }

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
	resolved, err := resolvePathWithPV(t.PV, params.Path)
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

	type fileResult struct {
		Path    string
		Count   int
		Changed bool
		Err     error
	}

	var results []fileResult
	totalFiles := 0
	totalMatches := 0
	totalChanged := 0

	walkErr := filepath.WalkDir(params.Path, func(p string, d fs.DirEntry, err error) error {
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

		data, readErr := os.ReadFile(p)
		if readErr != nil {
			results = append(results, fileResult{Path: p, Err: readErr})
			return nil
		}

		content := string(data)
		newContent := re.ReplaceAllString(content, params.Replacement)
		if newContent == content {
			return nil
		}

		matchCount := countMatches(re, content)

		if params.DryRun {
			results = append(results, fileResult{Path: p, Count: matchCount})
			totalFiles++
			totalMatches += matchCount
			return nil
		}

		mode := os.FileMode(0o644)
		if fi, err := d.Info(); err == nil {
			mode = fi.Mode()
		}
		if err := os.WriteFile(p, []byte(newContent), mode); err != nil {
			results = append(results, fileResult{Path: p, Count: matchCount, Err: err})
			return nil
		}

		results = append(results, fileResult{Path: p, Count: matchCount, Changed: true})
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

	var sb strings.Builder
	if params.DryRun {
		sb.WriteString(fmt.Sprintf("DRY RUN — %d file(s) with %d match(es):\n", totalFiles, totalMatches))
	} else {
		sb.WriteString(fmt.Sprintf("Replaced %d match(es) across %d file(s):\n", totalMatches, totalChanged))
	}

	for _, r := range results {
		if r.Err != nil {
			sb.WriteString(fmt.Sprintf("  %s: %v\n", r.Path, r.Err))
		} else {
			sb.WriteString(fmt.Sprintf("  %s: %d match(es)\n", r.Path, r.Count))
		}
	}

	msg := sb.String()
	if len(msg) > replaceMaxResultLen {
		msg = msg[:replaceMaxResultLen] + "\n...[truncated]..."
	}
	return strings.TrimRight(msg, "\n"), nil
}

func countMatches(re *regexp.Regexp, content string) int {
	return len(re.FindAllStringIndex(content, -1))
}
