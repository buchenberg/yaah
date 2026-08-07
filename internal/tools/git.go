package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

var allowedGitActions = map[string]struct {
	description string
	dangerous   bool
}{
	"status":      {"Working tree status", false},
	"diff":        {"Show unstaged changes", false},
	"diff_cached": {"Show staged/index changes", false},
	"log":         {"Show recent commit history", false},
	"show":        {"Show details of a commit", false},
	"branch":      {"List, create, or delete branches. Branch names go in 'paths'.", false},
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
	return prompts.ToolDescription("git")
}

func (t *GitTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["status", "diff", "diff_cached", "log", "show", "branch", "add", "commit", "push", "pull", "fetch"],
				"description": "The git action to perform. For 'branch', pass branch names via the 'paths' parameter."
			},
			"flags": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Safe git flags for read-only commands, e.g. [\"--oneline\", \"-5\", \"--stat\"]. MUST start with '-' or '--'. Do NOT put branch names, file paths, or revision specs here — use 'paths' for those."
			},
			"paths": {
				"type": "array",
				"items": {"type": "string"},
				"description": "File paths, branch names (for 'branch' action), or revision refs (for show/log/diff). Do NOT put dash-prefixed flags here — use 'flags' for those."
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
		Flags   []string `json:"flags"`
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

	safeFlags, err := validateFlags(params.Flags, info.dangerous)
	if err != nil {
		return "", err
	}

	var cmdArgs []string
	switch params.Action {
	case "status":
		cmdArgs = []string{"status", "--porcelain"}
		cmdArgs = append(cmdArgs, safeFlags...)
		cmdArgs = append(cmdArgs, params.Paths...)
	case "diff":
		cmdArgs = []string{"diff"}
		cmdArgs = append(cmdArgs, safeFlags...)
		cmdArgs = append(cmdArgs, params.Paths...)
	case "diff_cached":
		cmdArgs = []string{"diff", "--cached"}
		cmdArgs = append(cmdArgs, safeFlags...)
		cmdArgs = append(cmdArgs, params.Paths...)
	case "log":
		cmdArgs = []string{"log"}
		if len(safeFlags) == 0 {
			cmdArgs = append(cmdArgs, "--oneline", "-20")
		} else {
			cmdArgs = append(cmdArgs, safeFlags...)
		}
		cmdArgs = append(cmdArgs, params.Paths...)
	case "show":
		cmdArgs = []string{"show"}
		cmdArgs = append(cmdArgs, safeFlags...)
		cmdArgs = append(cmdArgs, params.Paths...)
	case "branch":
		cmdArgs = []string{"branch"}
		cmdArgs = append(cmdArgs, safeFlags...)
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
		cmdArgs = append(cmdArgs, safeFlags...)
		cmdArgs = append(cmdArgs, params.Paths...)
	default:
		return "", fmt.Errorf("git: action %q is recognized but not implemented — this is a bug, please report it", params.Action)
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ToolTimeoutError{Tool: "git", Timeout: timeout.String()}
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

// safeGitFlags is a whitelist of read-only git flags that are safe to pass
// directly. Flags not in this list are rejected to prevent injection of
// mutating options (e.g. --force, --hard, --exec).
var safeGitFlags = map[string]bool{
	"--oneline": true, "--stat": true, "--graph": true, "--all": true,
	"--decorate": true, "--no-decorate": true, "--abbrev-commit": true,
	"--no-abbrev-commit": true, "--relative-date": true, "--date-order": true,
	"--topo-order": true, "--reverse": true, "--no-merges": true, "--merges": true,
	"--first-parent": true, "--no-walk": true, "--follow": true,
	"--name-only": true, "--name-status": true, "--shortstat": true,
	"--numstat": true, "--summary": true, "--patch": true, "--unified": true,
	"--word-diff": true, "--color-words": true, "--no-color": true, "--color": true,
	"--cached": true, "--staged": true, "--check": true, "--full-index": true,
	"--binary": true, "--compact-summary": true, "--dst-prefix": true,
	"--src-prefix": true, "--no-prefix": true, "--left-right": true,
	"--cherry-pick": true, "--cherry-mark": true, "--diff-filter": true,
	"--find-renames": true, "--find-copies": true, "--irreversible-delete": true,
	"--parents": true, "--children": true, "--left-only": true, "--right-only": true,
	"--cherry": true, "--reflog": true, "--walk-reflogs": true, "--boundary": true,
	"--simplify-by-decoration": true, "--full-history": true, "--dense": true,
	"--sparse": true, "--simplify-merges": true, "--ancestry-path": true,
	"--date": true, "--format": true, "--pretty": true, "--encoding": true,
	"--notes": true, "--no-notes": true, "--show-notes": true,
	"--show-signature": true, "--expand-tabs": true, "--no-expand-tabs": true,
	"--indent-heuristic": true, "--no-indent-heuristic": true,
	"--ignore-space-change": true, "--ignore-all-space": true,
	"--ignore-blank-lines": true, "--function-context": true,
	"--max-count": true, "--skip": true, "--since": true, "--until": true,
	"--after": true, "--before": true, "--author": true, "--committer": true,
	"--grep": true, "--all-match": true, "--invert-grep": true,
	"--regexp-ignore-case": true, "--basic-regexp": true,
	"--extended-regexp": true, "--fixed-strings": true,
	"--remotes": true, "--branches": true, "--tags": true,
	"--list": true, "--sort": true, "--contains": true, "--no-contains": true,
	"--merged": true, "--no-merged": true, "--points-at": true,
	"--verbose": true, "--quiet": true, "--porcelain": true,
	"--long": true, "--short": true, "--medium": true, "--full": true,
	"--fuller": true, "--raw": true, "--patch-with-stat": true,
	"--patch-with-raw": true, "--minimal": true, "--patience": true,
	"--histogram": true, "--anchored": true, "--diff-algorithm": true,
	"--stat-count": true, "--stat-width": true, "--stat-name-width": true,
	"--stat-graph-width": true, "--inter-hunk-context": true,
	"--output": true, "--output-indicator-new": true, "--output-indicator-old": true,
	"--output-indicator-context": true, "--break-rewrites": true,
	"--detect-renames": true, "--no-renames": true, "--pickaxe-all": true,
	"--pickaxe-regex": true, "--relative": true, "--no-relative": true,
	"--text": true, "--ignore-submodules": true, "--submodule": true,
	"--ita-invisible-in-index": true, "--ita-visible-in-index": true,
	"-p": true, "-u": true, "-w": true, "-b": true, "-R": true, "-B": true,
	"-C": true, "-D": true, "-M": true, "-W": true, "-a": true, "-c": true,
	"-d": true, "-g": true, "-n": true, "-r": true, "-t": true, "-v": true,
	"-q": true, "-m": true, "-i": true, "-E": true, "-F": true, "-G": true,
	"-S": true, "-L": true, "-O": true, "-P": true,
}

// safeGitFlagPrefixes allows flags with values (e.g. -5, --format=%h).
var safeGitFlagPrefixes = []string{
	"--format=", "--pretty=", "--date=", "--encoding=", "--diff-filter=",
	"--find-renames=", "--find-copies=", "--stat-count=", "--stat-width=",
	"--stat-name-width=", "--stat-graph-width=", "--inter-hunk-context=",
	"--output=", "--output-indicator-new=", "--output-indicator-old=",
	"--output-indicator-context=", "--break-rewrites=", "--detect-renames=",
	"--max-count=", "--skip=", "--since=", "--until=", "--after=", "--before=",
	"--author=", "--committer=", "--grep=", "--remotes=", "--branches=",
	"--tags=", "--sort=", "--contains=", "--no-contains=", "--merged=",
	"--no-merged=", "--points-at=", "--unified=", "--word-diff=",
	"--color-words=", "--color=", "--diff-algorithm=", "--anchored=",
	"--ignore-submodules=", "--submodule=", "--relative=", "--src-prefix=",
	"--dst-prefix=", "--pickaxe-regex=", "-U", "-L", "-O", "-n",
}

// validateFlags checks flags against the safe whitelist. Dangerous commands
// (add, commit, push, pull) reject all flags.
func validateFlags(flags []string, dangerous bool) ([]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	if dangerous {
		return nil, fmt.Errorf("git: flags are not allowed for dangerous actions (add, commit, push, pull)")
	}
	var safe []string
	for _, f := range flags {
		if safeGitFlags[f] {
			safe = append(safe, f)
			continue
		}
		matched := false
		for _, prefix := range safeGitFlagPrefixes {
			if strings.HasPrefix(f, prefix) {
				safe = append(safe, f)
				matched = true
				break
			}
		}
		if !matched {
			// Allow bare numeric args like "-5", "-20" (shorthand for --max-count)
			if len(f) > 1 && f[0] == '-' && isNumeric(f[1:]) {
				safe = append(safe, f)
				continue
			}
			return nil, fmt.Errorf("git: flag %q is not in the safe whitelist", f)
		}
	}
	return safe, nil
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

func allowedActionList() string {
	actions := make([]string, 0, len(allowedGitActions))
	for a := range allowedGitActions {
		actions = append(actions, a)
	}
	return strings.Join(actions, ", ")
}
