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

// SedTool performs regex search and replacement across files.
type SedTool struct{ PV *PathValidator }

var _ PathValidatorSetter = (*SedTool)(nil)

func (t *SedTool) SetPathValidator(pv *PathValidator) { t.PV = pv }

func (t *SedTool) Name() string { return "sed" }
func (t *SedTool) Description() string {
	return prompts.ToolDescription("sed")
}
func (t *SedTool) IsDangerous(argsJSON string) bool { return true }

func (t *SedTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Regex pattern to find (Go regex syntax)"},
			"replacement": {"type": "string", "description": "Replacement text. Use $1, $2, etc. for capture groups."},
			"path": {"type": "string", "description": "File or directory to search. Directories are walked recursively."},
			"include": {"type": "string", "description": "Glob pattern to filter files (e.g. '*.go'). Defaults to '*.go' for directories."},
			"dryRun": {"type": "boolean", "description": "If true, report matches without modifying files."}
		},
		"required": ["pattern", "replacement", "path"]
	}`)
}

func (t *SedTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		Path        string `json:"path"`
		Include     string `json:"include"`
		DryRun      bool   `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("sed: invalid arguments: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("sed: pattern is required")
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("sed: invalid regex: %w", err)
	}

	if params.Path == "" {
		return "", fmt.Errorf("sed: path is required")
	}
	resolved, err := resolvePathWithPV(t.PV, params.Path)
	if err != nil {
		return "", err
	}
	params.Path = resolved

	files, err := collectFiles(params.Path, params.Include)
	if err != nil {
		return "", fmt.Errorf("sed: %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("sed: no files matched")
	}

	var results []string
	totalHits := 0

	for _, fp := range files {
		data, readErr := os.ReadFile(fp)
		if readErr != nil {
			results = append(results, fmt.Sprintf("%s: %v", fp, readErr))
			continue
		}

		content := string(data)
		matches := re.FindAllString(content, -1)
		hits := len(matches)
		if hits == 0 {
			continue
		}
		totalHits += hits

		if params.DryRun {
			results = append(results, fmt.Sprintf("%s: %d match(es) [DRY RUN]", fp, hits))
			continue
		}

		replaced := re.ReplaceAllString(content, params.Replacement)
		if err := os.WriteFile(fp, []byte(replaced), 0o644); err != nil {
			results = append(results, fmt.Sprintf("%s: write error: %v", fp, err))
			continue
		}
		delta := len(replaced) - len(content)
		results = append(results, fmt.Sprintf("%s: %d replacement(s) (%+d bytes)", fp, hits, delta))
	}

	if totalHits == 0 {
		return fmt.Sprintf("sed: no matches for %q in %d file(s)", params.Pattern, len(files)), nil
	}

	if params.DryRun {
		return fmt.Sprintf("sed (dry run): %d match(es) across %d file(s)\n%s",
			totalHits, len(files), strings.Join(results, "\n")), nil
	}
	return fmt.Sprintf("sed: %d replacement(s) across %d file(s)\n%s",
		totalHits, len(files), strings.Join(results, "\n")), nil
}

func collectFiles(path, include string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	if include == "" {
		include = "*.go"
	}

	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if matched, _ := filepath.Match(include, filepath.Base(p)); matched {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}
