package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// DiffTool runs git-diff or unified diff and returns structured results.
type DiffTool struct{}

func NewDiffTool() *DiffTool { return &DiffTool{} }

func (t *DiffTool) Name() string        { return "diff" }
func (t *DiffTool) Description() string { return prompts.ToolDescription("diff") }

func (t *DiffTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"a": {
				"type": "string",
				"description": "First file path or git ref (commit, branch, tag)"
			},
			"b": {
				"type": "string",
				"description": "Second file path or git ref. Empty means the working tree."
			},
			"context_lines": {
				"type": "integer",
				"description": "Lines of context around each change (default 3)"
			},
			"mode": {
				"type": "string",
				"enum": ["git", "unified"],
				"description": "Diff mode: 'git' uses git diff (default), 'unified' uses the system diff command"
			},
			"path": {
				"type": "string",
				"description": "Restrict diff to a specific file or directory path"
			}
		},
		"required": ["a"]
	}`)
}

type diffParams struct {
	A            string `json:"a"`
	B            string `json:"b"`
	ContextLines int    `json:"context_lines"`
	Mode         string `json:"mode"`
	Path         string `json:"path"`
}

type diffFileStat struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	IsNew      bool   `json:"is_new"`
	IsDeleted  bool   `json:"is_deleted"`
}

type diffResult struct {
	FilesChanged int            `json:"files_changed"`
	Insertions   int            `json:"insertions"`
	Deletions    int            `json:"deletions"`
	Files        []diffFileStat `json:"files"`
	RawDiff      string         `json:"raw_diff"`
	Stderr       string         `json:"stderr"`
	ExitCode     int            `json:"exit_code"`
}

func (t *DiffTool) Execute(ctx context.Context, args string) (string, error) {
	var params diffParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("diff: invalid arguments: %w", err)
	}

	mode := params.Mode
	if mode == "" {
		mode = "git"
	}
	contextLines := params.ContextLines
	if contextLines <= 0 {
		contextLines = 3
	}

	var cmdArgs []string
	useGit := true

	if mode == "unified" {
		useGit = false
		cmdArgs = []string{"diff", "-U" + strconv.Itoa(contextLines), "--", params.A, params.B}
	} else {
		// git mode: refs come before --, path comes after
		cmdArgs = []string{"diff", "--no-color", "-U" + strconv.Itoa(contextLines)}
		if params.B != "" {
			cmdArgs = append(cmdArgs, params.A, params.B)
		} else {
			cmdArgs = append(cmdArgs, params.A)
		}
		if params.Path != "" {
			cmdArgs = append(cmdArgs, "--", params.Path)
		}
	}

	var cmd *exec.Cmd
	if useGit {
		cmd = exec.CommandContext(ctx, "git", cmdArgs...)
	} else {
		cmd = exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	}

	outBytes, err := cmd.Output()
	result := &diffResult{RawDiff: string(outBytes)}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Stderr = string(exitErr.Stderr)
		} else {
			return "", fmt.Errorf("diff: command failed: %w", err)
		}
	}

	// Parse the diff to count stats
	result.Files, result.Insertions, result.Deletions = parseDiffStats(string(outBytes))
	result.FilesChanged = len(result.Files)

	// Truncate raw diff at 20KB
	if len(result.RawDiff) > 20480 {
		result.RawDiff = result.RawDiff[:20480] + "\n... (truncated)"
	}

	outJSON, _ := json.MarshalIndent(result, "", "  ")
	return string(outJSON), nil
}

// parseDiffStats scans unified diff output and counts per-file insertions/deletions.
func parseDiffStats(diff string) ([]diffFileStat, int, int) {
	var files []diffFileStat
	var totalIns, totalDel int
	var currentFile *diffFileStat

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			if currentFile != nil {
				files = append(files, *currentFile)
			}
			currentFile = &diffFileStat{}
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				// "diff --git a/path b/path"
				currentFile.Path = strings.TrimPrefix(parts[3], "b/")
			}
		case strings.HasPrefix(line, "--- "):
			if currentFile != nil && currentFile.Path == "" {
				path := strings.TrimPrefix(line, "--- ")
				path = strings.TrimPrefix(path, "a/")
				currentFile.Path = path
				if path == "/dev/null" {
					currentFile.IsNew = true
				}
			}
		case strings.HasPrefix(line, "+++ "):
			if currentFile != nil {
				path := strings.TrimPrefix(line, "+++ ")
				path = strings.TrimPrefix(path, "b/")
				if path == "/dev/null" {
					currentFile.IsDeleted = true
				} else if currentFile.Path == "" {
					currentFile.Path = path
				}
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "+ "):
			if currentFile != nil {
				currentFile.Insertions++
				totalIns++
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "- "):
			if currentFile != nil {
				currentFile.Deletions++
				totalDel++
			}
		}
	}

	if currentFile != nil {
		files = append(files, *currentFile)
	}

	if files == nil {
		files = []diffFileStat{}
	}
	return files, totalIns, totalDel
}
