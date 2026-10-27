package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// DiffTool runs git-diff or unified diff and returns structured results.
type DiffTool struct {
	WS Workspace
}

var _ WorkspaceSetter = (*DiffTool)(nil)

func (t *DiffTool) SetWorkspace(ws Workspace) { t.WS = ws }

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

// diffRawMaxBytes caps the raw diff embedded in the JSON result. The cap is
// applied through the shared truncation helper in result.go.
const diffRawMaxBytes = 20480

func (t *DiffTool) Execute(ctx context.Context, args string) (string, error) {
	var params diffParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("diff: invalid arguments: %w", err)
	}

	mode := params.Mode
	if mode == "" {
		mode = "git"
	}
	if mode != "git" && mode != "unified" {
		return "", fmt.Errorf("diff: invalid mode %q; valid values: git, unified", params.Mode)
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

	req := ExecRequest{SeparateStreams: true}
	if useGit {
		req.Command, req.Args = "git", cmdArgs
	} else {
		req.Command, req.Args = cmdArgs[0], cmdArgs[1:]
	}

	res, err := workspaceOf(t.WS, nil).Exec(ctx, req)
	result := &diffResult{RawDiff: res.Stdout}
	if res.ExitCode < 0 {
		// The command never ran (not found, not executable).
		return "", fmt.Errorf("diff: command failed: %w", err)
	}
	// diff exits 1 to mean "differences found", which is a normal result.
	if res.ExitCode > 0 {
		result.ExitCode = res.ExitCode
		result.Stderr = res.Stderr
	}

	// Parse the diff to count stats
	result.Files, result.Insertions, result.Deletions = parseDiffStats(res.Stdout)
	result.FilesChanged = len(result.Files)

	// Truncate the raw diff through the shared policy (single owner in
	// result.go), preserving the JSON shape.
	result.RawDiff = truncateAt(result.RawDiff, diffRawMaxBytes)

	outJSON, _ := json.MarshalIndent(result, "", "  ")
	return string(outJSON), nil
}

// parseDiffStats scans unified diff output and counts per-file insertions and
// deletions. Hunk bodies are consumed using their declared line counts, so a
// content line that begins with a space ("+ " / "- ") is counted as the real
// change it is rather than mistaken for a file header.
func parseDiffStats(diff string) ([]diffFileStat, int, int) {
	var files []diffFileStat
	var totalIns, totalDel int
	var currentFile *diffFileStat

	inHunk := false
	oldLeft, newLeft := 0, 0

	for _, line := range strings.Split(diff, "\n") {
		if inHunk {
			if line == "" {
				inHunk = false
				continue
			}
			kind := line[0]
			switch kind {
			case ' ':
				oldLeft--
				newLeft--
			case '-':
				oldLeft--
			case '+':
				newLeft--
			case '\\':
				// "\ No newline at end of file" consumes no counts.
			default:
				// Not a valid hunk line: treat the hunk as finished and let
				// the header cases below inspect this line.
				inHunk = false
			}
			if inHunk {
				switch kind {
				case '+':
					if currentFile != nil {
						currentFile.Insertions++
						totalIns++
					}
				case '-':
					if currentFile != nil {
						currentFile.Deletions++
						totalDel++
					}
				}
				if oldLeft <= 0 && newLeft <= 0 {
					inHunk = false
				}
				continue
			}
		}

		switch {
		case strings.HasPrefix(line, "@@"):
			oldLeft, newLeft = parseHunkCounts(line)
			inHunk = oldLeft > 0 || newLeft > 0
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
			if currentFile != nil {
				path := strings.TrimPrefix(line, "--- ")
				path = strings.TrimPrefix(path, "a/")
				if path == "/dev/null" {
					currentFile.IsNew = true
				} else if currentFile.Path == "" || currentFile.IsNew {
					currentFile.Path = path
				}
			}
		case strings.HasPrefix(line, "+++ "):
			if currentFile != nil {
				path := strings.TrimPrefix(line, "+++ ")
				path = strings.TrimPrefix(path, "b/")
				if path == "/dev/null" {
					currentFile.IsDeleted = true
				} else if currentFile.Path == "" || currentFile.IsNew {
					currentFile.Path = path
				}
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
