package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var allowedGitActions = map[string]struct {
	description string
	dangerous   bool
}{
	"status":      {"Working tree status", false},
	"diff":        {"Show unstaged changes", false},
	"diff_staged": {"Show staged changes", false},
	"log":         {"Show recent commit history", false},
	"show":        {"Show details of a commit", false},
	"branch":      {"List local branches", false},
	"add":         {"Stage file(s) for commit", true},
	"commit":      {"Create a new commit", true},
	"push":        {"Push commits to remote", true},
	"pull":        {"Pull commits from remote", true},
	"fetch":       {"Fetch from remote (read-only)", false},
}

// GitTool runs git commands directly (not through a shell).
// Only a whitelist of subcommands is allowed; mutating operations
// (add, commit) are flagged as dangerous via DangerClassifier.
type GitTool struct{}

func (t *GitTool) Name() string { return "git" }
func (t *GitTool) Description() string {
	return "Run a git command (status, diff, log, show, branch, add, commit, push, pull, fetch)."
}

func (t *GitTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["status", "diff", "diff_staged", "log", "show", "branch", "add", "commit", "push", "pull", "fetch"],
				"description": "The git action to perform"
			},
			"paths": {
				"type": "array",
				"items": {"type": "string"},
				"description": "File paths or arguments for the git command (optional)"
			},
			"message": {
				"type": "string",
				"description": "Commit message (required for commit action)"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default 30)"
			}
		},
		"required": ["action"]
	}`)
}

func (t *GitTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return false
	}
	if info, ok := allowedGitActions[params.Action]; ok {
		return info.dangerous
	}
	return false
}

func (t *GitTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Action  string   `json:"action"`
		Paths   []string `json:"paths"`
		Message string   `json:"message"`
		Timeout int      `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("git: invalid arguments: %w", err)
	}
	if params.Action == "" {
		return "", fmt.Errorf("git: action is required")
	}

	if err := validatePaths(params.Paths); err != nil {
		return "", err
	}

	info, ok := allowedGitActions[params.Action]
	if !ok {
		return "", fmt.Errorf("git: unsupported action %q — allowed: %s", params.Action, allowedActionList())
	}

	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git: git not found on PATH — is git installed?")
	}

	timeout := 30 * time.Second
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmdArgs []string
	switch params.Action {
	case "status":
		cmdArgs = []string{"status", "--porcelain"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "diff":
		cmdArgs = []string{"diff"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "diff_staged":
		cmdArgs = []string{"diff", "--cached"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "log":
		cmdArgs = []string{"log", "--oneline", "-20"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "show":
		cmdArgs = []string{"show"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "branch":
		cmdArgs = []string{"branch"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "add":
		if len(params.Paths) == 0 {
			return "", fmt.Errorf("git: add requires at least one file path")
		}
		cmdArgs = append([]string{"add"}, params.Paths...)
	case "commit":
		if params.Message == "" {
			return "", fmt.Errorf("git: commit requires a message")
		}
		cmdArgs = []string{"commit", "-m", params.Message}
	case "push":
		cmdArgs = []string{"push"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "pull":
		cmdArgs = []string{"pull"}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "fetch":
		cmdArgs = []string{"fetch"}
		cmdArgs = append(cmdArgs, params.Paths...)
	default:
		return "", fmt.Errorf("git: unsupported action %q", params.Action)
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git: timed out after %s", timeout)
	}
	output = truncateOutput(output)
	if err != nil {
		return "", fmt.Errorf("%s\n%s: %w", string(output), info.description, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// validatePaths rejects paths that start with "-" to prevent flag injection.
// Use "./-filename" to reference files whose names begin with a hyphen.
func validatePaths(paths []string) error {
	for _, p := range paths {
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("git: path %q starts with '-'; use './%s' instead to prevent flag injection", p, p)
		}
	}
	return nil
}

func allowedActionList() string {
	actions := make([]string, 0, len(allowedGitActions))
	for a := range allowedGitActions {
		actions = append(actions, a)
	}
	return strings.Join(actions, ", ")
}
