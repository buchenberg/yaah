package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

const findMaxResults = 1000

// FindTool searches a workspace tree by name, size, modification age, and
// entry type, returning path + metadata so a caller does not have to ls and
// filter manually.
type FindTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*FindTool)(nil)
	_ WorkspaceSetter     = (*FindTool)(nil)
)

func (t *FindTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *FindTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *FindTool) Name() string        { return "find" }
func (t *FindTool) Description() string { return prompts.ToolDescription("find") }

func (t *FindTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory to search (default current directory)."},
			"name": {"type": "string", "description": "Optional glob matched against each entry's base name (e.g. \"*.go\", \"test_*\")."},
			"type": {
				"type": "string",
				"enum": ["file", "dir", "all"],
				"description": "Entry type filter (default file).",
				"default": "file"
			},
			"min_size": {"type": "integer", "minimum": 0, "description": "Minimum size in bytes (inclusive)."},
			"max_size": {"type": "integer", "minimum": 0, "description": "Maximum size in bytes (inclusive)."},
			"newer_than": {"type": "string", "description": "Only entries modified within this duration (e.g. \"24h\", \"30m\")."},
			"older_than": {"type": "string", "description": "Only entries not modified within this duration (e.g. \"7d\" is not supported; use hours)."}
		},
		"required": []
	}`)
}

func (t *FindTool) IsDangerous(string) bool { return false }

func (t *FindTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Path      string `json:"path"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		MinSize   int64  `json:"min_size"`
		MaxSize   int64  `json:"max_size"`
		NewerThan string `json:"newer_than"`
		OlderThan string `json:"older_than"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("find: invalid arguments: %w", err)
	}
	if params.Path == "" {
		params.Path = "."
	}
	switch params.Type {
	case "", "file":
		params.Type = "file"
	case "dir", "all":
	default:
		return "", fmt.Errorf("find: type must be file, dir, or all")
	}
	if params.MaxSize > 0 && params.MinSize > params.MaxSize {
		return "", fmt.Errorf("find: min_size %d is greater than max_size %d", params.MinSize, params.MaxSize)
	}
	var newer, older time.Duration
	var err error
	if params.NewerThan != "" {
		if newer, err = time.ParseDuration(params.NewerThan); err != nil || newer <= 0 {
			return "", fmt.Errorf("find: newer_than %q is not a positive duration", params.NewerThan)
		}
	}
	if params.OlderThan != "" {
		if older, err = time.ParseDuration(params.OlderThan); err != nil || older <= 0 {
			return "", fmt.Errorf("find: older_than %q is not a positive duration", params.OlderThan)
		}
	}
	var nameRE *regexp.Regexp
	if params.Name != "" {
		nameRE, err = patternToRegex(filepath.ToSlash(params.Name))
		if err != nil {
			return "", fmt.Errorf("find: invalid name pattern: %w", err)
		}
	}

	ws := workspaceOf(t.WS, t.PV)
	root, err := ws.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("find: %w", err)
	}
	now := time.Now()
	var results []string
	walkErr := walkWorkspace(ctx, ws, root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d == nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if p == root && info.IsDir() {
			return nil
		}
		switch params.Type {
		case "file":
			if info.IsDir() {
				return nil
			}
		case "dir":
			if !info.IsDir() {
				return nil
			}
		}
		if nameRE != nil && !nameRE.MatchString(filepath.ToSlash(d.Name())) {
			return nil
		}
		if params.MinSize > 0 && info.Size() < params.MinSize {
			return nil
		}
		if params.MaxSize > 0 && info.Size() > params.MaxSize {
			return nil
		}
		age := now.Sub(info.ModTime())
		if newer > 0 && age > newer {
			return nil
		}
		if older > 0 && age < older {
			return nil
		}
		results = append(results, fmt.Sprintf("%s\t%d\t%s", p, info.Size(), info.ModTime().UTC().Format(time.RFC3339)))
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipDir && walkErr != filepath.SkipAll {
		return "", fmt.Errorf("find: %w", walkErr)
	}
	if len(results) == 0 {
		return "No matches.", nil
	}
	sort.Strings(results)
	total := len(results)
	if total > findMaxResults {
		results = results[:findMaxResults]
	}
	out := strings.Join(results, "\n")
	if total > findMaxResults {
		// Prefix the marker so truncateResult's head-preserving cap cannot
		// remove it. Results are sorted before the cutoff, so the retained
		// set is the lexicographically first page rather than traversal order.
		out = fmt.Sprintf("...[truncated: showing %d of %d matches]...\n%s", findMaxResults, total, out)
	}
	return truncateResult(out), nil
}
