package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
// (add, commit, push, pull) are flagged as dangerous via DangerClassifier,
// and `branch` is classified per argument because the same action both
// lists refs and rewrites them.
type GitTool struct {
	WS Workspace
}

var _ WorkspaceSetter = (*GitTool)(nil)

func (t *GitTool) SetWorkspace(ws Workspace) { t.WS = ws }

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

// IsDangerous reports whether the call needs approval. The table's dangerous
// bit covers the actions that mutate wholesale (add, commit, push, pull);
// `branch` is refined per argument by branchCallDangerous, because listing
// and deleting refs share one action name — `branch -D` used to pass as
// read-only and delete a branch with no merged-commit protection (bead
// yaah-5m5).
func (t *GitTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Action string   `json:"action"`
		Flags  []string `json:"flags"`
		Paths  []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return false
	}
	info, ok := allowedGitActions[params.Action]
	if !ok {
		return false
	}
	if info.dangerous {
		return true
	}
	if params.Action == "branch" {
		return branchCallDangerous(params.Flags, params.Paths)
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

	// No host PATH pre-flight: this process's PATH says nothing about the
	// workspace where git will run (bead yaah-8nz). A git that cannot start
	// there is classified from the Exec result below (gitNotStarted), naming
	// the workspace as the place that failed instead of interrogating the host.

	timeout := 30 * time.Second
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// branch keeps its own option tables: the shared whitelist is keyed by
	// letter for diff/log, where -D/-m/-C mean something harmless, so a
	// mutating branch option must not be waved through by it.
	safeFlags, err := validateActionFlags(params.Action, params.Flags, info.dangerous)
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
		if len(params.Paths) > 0 {
			// Scoping a commit to paths: everything after '--' is a
			// pathspec, so a path can never be re-read as an option and a
			// path-limited commit cannot silently commit the whole index
			// (bead yaah-jd4).
			cmdArgs = append(cmdArgs, "--")
			cmdArgs = append(cmdArgs, params.Paths...)
		}
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

	// The injected workspace carries the working directory (a worktree for an
	// isolated sub-agent); there is no PathValidator for git, which spawns a
	// process rather than reading files directly.
	res, err := workspaceOf(t.WS, nil).Exec(ctx, ExecRequest{Command: "git", Args: cmdArgs})
	if ctx.Err() == context.DeadlineExceeded {
		return "", ToolTimeoutError{Tool: "git", Timeout: timeout.String()}
	}
	output := truncateOutput([]byte(res.Stdout))
	if err != nil {
		if !executableExit(res.ExitCode) {
			return "", gitNotStarted(res, err)
		}
		return "", fmt.Errorf("%s\n%s: %w", string(output), info.description, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// gitNotStarted builds the message for a git command that never ran where it was
// asked to run: a host exec failure reports -1, a sandbox launch failure reports
// -1 with the reason on stderr, and a shell-wrapped miss reports 126/127
// (executableExit). Asking the workspace, not this process's PATH, is the whole
// point (bead yaah-8nz); naming the cause keeps a jail failure from being
// mistaken for a missing binary.
func gitNotStarted(res ExecResult, err error) error {
	detail := strings.TrimSpace(res.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(res.Stdout)
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", res.ExitCode)
	}
	return fmt.Errorf("git: git did not start in this workspace: %s — is git installed and on PATH where this tool runs?", detail)
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
	// NOTE: --output / --output=<file> are deliberately NOT whitelisted:
	// they make read-only actions like `git diff` write to an arbitrary
	// filesystem path, bypassing path validation and approval gating.
	"--break-rewrites": true,
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
// NOTE: no "--output=" prefix — see the safeGitFlags comment.
var safeGitFlagPrefixes = []string{
	"--format=", "--pretty=", "--date=", "--encoding=", "--diff-filter=",
	"--find-renames=", "--find-copies=", "--stat-count=", "--stat-width=",
	"--stat-name-width=", "--stat-graph-width=", "--inter-hunk-context=",
	"--break-rewrites=", "--detect-renames=",
	"--max-count=", "--skip=", "--since=", "--until=", "--after=", "--before=",
	"--author=", "--committer=", "--grep=", "--remotes=", "--branches=",
	"--tags=", "--sort=", "--contains=", "--no-contains=", "--merged=",
	"--no-merged=", "--points-at=", "--unified=", "--word-diff=",
	"--color-words=", "--color=", "--diff-algorithm=", "--anchored=",
	"--ignore-submodules=", "--submodule=", "--relative=", "--src-prefix=",
	"--dst-prefix=", "--pickaxe-regex=", "-U", "-L", "-O", "-n",
}

// Branch flags have their own tables because git reuses single letters across
// subcommands: -D, -m and -C are harmless diff/log options in safeGitFlags and
// mutations here. A `branch` call is read-only only when every option merely
// lists or inspects refs.
var branchReadOnlyFlags = map[string]bool{
	"--all": true, "-a": true,
	"--remotes": true, "-r": true,
	// -l is refused rather than guessed at: it is --list for `git branch`
	// since git 2.16 (builtin/branch.c moved the -l short form there), but
	// through git 2.15 it is --create-reflog's short form, where
	// `git branch -l <name>` created a branch. --list itself is safe on
	// every git: older ones reject it outright and none mutate through it.
	// Use --list.
	"--list":         true,
	"--show-current": true,
	"--contains":     true, "--no-contains": true,
	"--merged": true, "--no-merged": true,
	"--points-at": true,
	"--sort":      true, "--format": true,
	"--column": true, "--no-column": true,
	"--verbose": true, "-v": true, "-vv": true,
	"--quiet": true, "-q": true,
	"--ignore-case": true,
	"--abbrev":      true, "--no-abbrev": true,
	"--omit-empty": true, "--color": true, "--no-color": true,
}

// branchReadOnlyValueFlags are the read-only options that consume the next
// token when written without '=' (--contains <ref>), so classifyBranchFlag can
// tell an option's value from a bare branch name. Callers must apply that skip
// only to a token that is not flag-like (no leading '-'): git's value
// consumption is not uniform across options (see --abbrev below), so a dash
// token after a value option is classified on its own. The worst case is a
// false refusal of an exotic value like `--sort -refname` (write the attached
// `--sort=-refname` form), never a mutation passing as read-only.
var branchReadOnlyValueFlags = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true,
	"--no-merged": true, "--points-at": true, "--sort": true,
	"--format": true,
	// --abbrev is deliberately absent: it takes an OPTIONAL argument, so
	// git does not consume a separate next token. Treating one as its
	// value let `git branch --abbrev -D <name>` pass as read-only while
	// git parsed -D as a delete and removed the branch. Only the
	// `--abbrev=<n>` form is read-only (branchReadOnlyFlagPrefixes).
}

var branchReadOnlyFlagPrefixes = []string{
	"--contains=", "--no-contains=", "--merged=", "--no-merged=",
	"--points-at=", "--sort=", "--format=", "--abbrev=",
}

// branchMutatingFlags are the git-branch options that write refs. -D deletes a
// branch even when its commits are unmerged (unlike -d), so a branch call
// carrying any of them must reach the approval gate.
var branchMutatingFlags = map[string]bool{
	"-d": true, "-D": true, "--delete": true,
	"-m": true, "-M": true, "--move": true,
	"-c": true, "-C": true, "--copy": true,
	"-f": true, "--force": true, "--force-with-lease": true,
	"-u": true, "--set-upstream-to": true, "--unset-upstream": true,
	"-t": true, "--track": true, "--no-track": true,
	"--edit-description":   true,
	"--create-reflog":      true,
	"--no-create-reflog":   true,
	"--recurse-submodules": true,
}

// branchMutatingValueFlags are the mutating options that consume the next token
// when written without '=' (--set-upstream-to origin/main).
var branchMutatingValueFlags = map[string]bool{
	"--set-upstream-to": true, "-u": true, "--track": true,
	"--recurse-submodules": true, "--force-with-lease": true,
}

var branchMutatingFlagPrefixes = []string{
	"--delete=", "--move=", "--copy=", "--force=", "--force-with-lease=",
	"--set-upstream-to=", "--unset-upstream=", "--track=",
	"--create-reflog=", "--recurse-submodules=",
}

// branchFlagKind separates a branch option that only inspects refs from one
// that writes them.
type branchFlagKind int

const (
	branchFlagUnknown branchFlagKind = iota
	branchFlagReadOnly
	branchFlagMutating
)

// classifyBranchFlag classifies one `git branch` option token and reports
// whether the option consumes the next token as its value.
func classifyBranchFlag(flag string) (branchFlagKind, bool) {
	if branchReadOnlyFlags[flag] {
		return branchFlagReadOnly, branchReadOnlyValueFlags[flag]
	}
	if branchMutatingFlags[flag] {
		return branchFlagMutating, branchMutatingValueFlags[flag]
	}
	for _, prefix := range branchReadOnlyFlagPrefixes {
		if strings.HasPrefix(flag, prefix) {
			return branchFlagReadOnly, false
		}
	}
	for _, prefix := range branchMutatingFlagPrefixes {
		if strings.HasPrefix(flag, prefix) {
			return branchFlagMutating, false
		}
	}
	return branchFlagUnknown, false
}

// branchCallDangerous reports whether a `branch` call mutates refs. Every
// option that is not a known read-only listing option is a mutation signal,
// and an unrecognized option is assumed mutating — a whitelist hole is worse
// than an extra approval prompt. Positional arguments are branch names, so they
// are read-only only alongside a listing option.
func branchCallDangerous(flags, paths []string) bool {
	listing := false
	expectValue := false
	for _, flag := range flags {
		// A value is taken only when the next token is not flag-like (see
		// branchReadOnlyValueFlags): a dash token after a value option is
		// classified on its own, so `--merged -D <name>` cannot ride
		// through as a value on a git whose parser disagrees with ours.
		if expectValue && !strings.HasPrefix(flag, "-") {
			expectValue = false
			continue
		}
		expectValue = false
		kind, takesValue := classifyBranchFlag(flag)
		if kind != branchFlagReadOnly {
			return true
		}
		listing = true
		expectValue = takesValue
	}
	if len(paths) == 0 {
		return false
	}
	return !listing
}

// validateActionFlags routes branch to its own tables and every other action
// to the shared whitelist.
func validateActionFlags(action string, flags []string, dangerous bool) ([]string, error) {
	if action != "branch" {
		return validateFlags(flags, dangerous)
	}
	if len(flags) == 0 {
		return nil, nil
	}
	safe := make([]string, 0, len(flags))
	expectValue := false
	for _, flag := range flags {
		// Same value rule as branchCallDangerous: only a non-flag-like
		// token is swallowed as an option value.
		if expectValue && !strings.HasPrefix(flag, "-") {
			expectValue = false
			safe = append(safe, flag)
			continue
		}
		expectValue = false
		kind, takesValue := classifyBranchFlag(flag)
		if kind == branchFlagUnknown {
			return nil, fmt.Errorf("git: flag %q is not a known branch option — read-only listing flags are whitelisted and mutating options are approval-gated, so an unrecognized option is refused", flag)
		}
		expectValue = takesValue
		safe = append(safe, flag)
	}
	return safe, nil
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
