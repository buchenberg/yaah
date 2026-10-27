package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// BisectTool runs guided `git bisect` to find which commit introduced a bug.
type BisectTool struct {
	WS Workspace
}

var _ WorkspaceSetter = (*BisectTool)(nil)

func (t *BisectTool) SetWorkspace(ws Workspace) { t.WS = ws }

func NewBisectTool() *BisectTool { return &BisectTool{} }

func (t *BisectTool) Name() string        { return "bisect" }
func (t *BisectTool) Description() string { return prompts.ToolDescription("bisect") }

func (t *BisectTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["start", "good", "bad", "skip", "log", "reset"],
				"description": "Bisect action: start a new bisect, mark a commit as good/bad/skip, view log, or reset"
			},
			"good_ref": {
				"type": "string",
				"description": "Known-good commit ref (required for 'start')"
			},
			"bad_ref": {
				"type": "string",
				"description": "Known-bad commit ref (required for 'start')"
			},
			"test_cmd": {
				"type": "string",
				"description": "Shell command that exits 0 for good, non-zero for bad. If provided with 'start', auto-bisect runs. Only used with 'start'."
			},
			"ref": {
				"type": "string",
				"description": "Specific ref to mark (used with 'good', 'bad', 'skip' actions)"
			}
		},
		"required": ["action"]
	}`)
}

type bisectParams struct {
	Action  string `json:"action"`
	GoodRef string `json:"good_ref"`
	BadRef  string `json:"bad_ref"`
	TestCmd string `json:"test_cmd"`
	Ref     string `json:"ref"`
}

type bisectResult struct {
	Action           string `json:"action"`
	Status           string `json:"status"`
	RemainingCommits int    `json:"remaining_commits,omitempty"`
	CurrentCommit    string `json:"current_commit,omitempty"`
	BadCommit        string `json:"bad_commit,omitempty"`
	StepsCompleted   int    `json:"steps_completed"`
	Log              string `json:"log"`
	Stderr           string `json:"stderr"`
}

var (
	bisectRemainingRe = regexp.MustCompile(`Bisecting: (\d+) revision`)
	bisectFoundRe     = regexp.MustCompile(`([0-9a-f]{7,}) is the first bad commit`)
	// git bisect log renders the result as "# first bad commit: [abc1234]
	// subject", not the "<sha> is the first bad commit" line printed by
	// git bisect run. Both forms are real output and must be parsed.
	bisectLogFoundRe = regexp.MustCompile(`# first bad commit: \[([0-9a-f]{7,})\]`)
)

// bisectFoundCommit extracts the first-bad-commit hash from either git bisect
// output form, or "" when the bisect has not concluded.
func bisectFoundCommit(output string) string {
	for _, re := range []*regexp.Regexp{bisectFoundRe, bisectLogFoundRe} {
		if m := re.FindStringSubmatch(output); len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

func (t *BisectTool) Execute(ctx context.Context, args string) (string, error) {
	var params bisectParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bisect: invalid arguments: %w", err)
	}

	result := &bisectResult{Action: params.Action, Status: "in_progress"}

	switch params.Action {
	case "start":
		t.doStart(ctx, params, result)
	case "good", "bad", "skip":
		// The schema only advertises test_cmd for 'start'; refusing it
		// here beats silently dropping it and reporting a plain mark.
		if params.TestCmd != "" {
			result.Status = "error"
			result.Stderr = "bisect: test_cmd is only used with the 'start' action; run the test yourself and mark the commit with good/bad"
		} else {
			t.doMark(ctx, params.Action, params.Ref, result)
		}
	case "log":
		t.doLog(ctx, result)
	case "reset":
		t.doReset(ctx, result)
	default:
		return "", fmt.Errorf("bisect: unknown action %q", params.Action)
	}

	// After any action, check if we've found the commit. Skip after an abort
	// or reset: the bisect is no longer live.
	if result.BadCommit == "" && result.Status != "error" && result.Status != "reset" {
		t.checkFound(ctx, result)
	}

	outBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("bisect: encode result: %w", err)
	}
	if result.Status == "error" {
		// A nil error means the operation succeeded, full stop (tools.go).
		// An aborted bisect is a failure even though the JSON describes it.
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = "bisect action failed"
		}
		return string(outBytes), fmt.Errorf("bisect: %s: %s", params.Action, detail)
	}
	return string(outBytes), nil
}

func (t *BisectTool) doStart(ctx context.Context, params bisectParams, result *bisectResult) {
	if params.GoodRef == "" || params.BadRef == "" {
		result.Status = "error"
		result.Stderr = "both good_ref and bad_ref are required for 'start'"
		return
	}

	// git bisect start
	if out, err := t.runGitCmd(ctx, "bisect", "start"); err != nil {
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect start failed: %s\n%s", err, out)
		return
	}

	// git bisect bad <bad_ref>
	if out, err := t.runGitCmd(ctx, "bisect", "bad", params.BadRef); err != nil {
		t.runGitCmd(ctx, "bisect", "reset")
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect bad failed: %s\n%s", err, out)
		return
	}

	// git bisect good <good_ref>
	out, err := t.runGitCmd(ctx, "bisect", "good", params.GoodRef)
	if err != nil {
		t.runGitCmd(ctx, "bisect", "reset")
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect good failed: %s\n%s", err, out)
		return
	}
	result.Log += out

	// Parse output for remaining
	t.parseRemaining(out, result)

	// Auto-bisect if test_cmd provided
	if params.TestCmd != "" {
		runOut, runErr := t.runBisectTest(ctx, params.TestCmd)
		result.Log += runOut
		if runErr != nil {
			// git bisect run aborts the whole bisect on exit 126/127 (and
			// other fatal wrappers). Leaving status "in_progress" invites
			// the model to keep marking a dead bisect.
			result.Status = "error"
			result.Stderr = fmt.Sprintf("bisect run aborted: %v\n%s", runErr, runOut)
			return
		}
		if commit := bisectFoundCommit(runOut); commit != "" {
			result.BadCommit = commit
			result.Status = "found"
		}
		t.checkFound(ctx, result)
	}
}

func (t *BisectTool) doMark(ctx context.Context, mark string, ref string, result *bisectResult) {
	cmdArgs := []string{"bisect", mark}
	if ref != "" {
		cmdArgs = append(cmdArgs, ref)
	}
	out, err := t.runGitCmd(ctx, cmdArgs...)
	result.Log = out
	if err != nil {
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect %s failed: %v\n%s", mark, err, out)
		return
	}
	t.parseRemaining(out, result)

	// If test_cmd was stored elsewhere... We can't do auto-run without re-reading state.
	// For manual mode, just return the new state.
	if commit := bisectFoundCommit(out); commit != "" {
		result.BadCommit = commit
		result.Status = "found"
	}
}

func (t *BisectTool) doLog(ctx context.Context, result *bisectResult) {
	out, err := t.runGitCmd(ctx, "bisect", "log")
	result.Log = out
	if err != nil {
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect log failed: %v\n%s", err, out)
		return
	}
	// Count steps
	result.StepsCompleted = strings.Count(out, "# bad: ")
}

func (t *BisectTool) doReset(ctx context.Context, result *bisectResult) {
	out, err := t.runGitCmd(ctx, "bisect", "reset")
	result.Log = out
	if err != nil {
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect reset failed: %v\n%s", err, out)
		return
	}
	result.Status = "reset"
}

func (t *BisectTool) parseRemaining(output string, result *bisectResult) {
	if m := bisectRemainingRe.FindStringSubmatch(output); len(m) >= 2 {
		result.RemainingCommits, _ = strconv.Atoi(m[1])
	}
	// Extract current commit
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "HEAD is now at") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				result.CurrentCommit = parts[4]
			}
		}
	}
}

func (t *BisectTool) checkFound(ctx context.Context, result *bisectResult) {
	if result.Status == "found" {
		return
	}
	// Check bisect log for the concluded bisect. git bisect log uses the
	// "# first bad commit: [sha]" form; bisect run output uses the other.
	out, err := t.runGitCmd(ctx, "bisect", "log")
	if err != nil {
		return
	}
	if commit := bisectFoundCommit(out); commit != "" {
		result.BadCommit = commit
		result.Status = "found"
	}
}

func (t *BisectTool) runGitCmd(ctx context.Context, args ...string) (string, error) {
	res, err := workspaceOf(t.WS, nil).Exec(ctx, ExecRequest{Command: "git", Args: args})
	return res.Stdout, err
}

// runBisectTest runs a test command via git bisect run, wrapped in a shell.
// git bisect run branches on exact exit codes (125 = skip, 126/127 = abort),
// so the wrapper must propagate the command's exit status: cmd /c and sh -c
// do, pwsh -Command collapses it to 0/1. A local workspace therefore keeps
// the GOOS-selected wrapper, while a sandbox is POSIX by construction and
// uses its own sh.
func (t *BisectTool) runBisectTest(ctx context.Context, testCmd string) (string, error) {
	ws := workspaceOf(t.WS, nil)
	if ws.Local() {
		if runtime.GOOS == "windows" {
			return t.runGitCmd(ctx, "bisect", "run", "cmd", "/c", testCmd)
		}
		return t.runGitCmd(ctx, "bisect", "run", "sh", "-c", testCmd)
	}
	shell, flag := ws.Shell()
	return t.runGitCmd(ctx, "bisect", "run", shell, flag, testCmd)
}
