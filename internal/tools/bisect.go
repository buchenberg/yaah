package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// BisectTool runs guided `git bisect` to find which commit introduced a bug.
type BisectTool struct{}

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
				"description": "Shell command that exits 0 for good, non-zero for bad. If provided with 'start', auto-bisect runs. Can also be used with 'good'/'bad' to run one step."
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
	bisectCurrentRe   = regexp.MustCompile(`remaining:\s*(\d+)`)
	bisectFoundRe     = regexp.MustCompile(`([0-9a-f]{7,}) is the first bad commit`)
)

func (t *BisectTool) Execute(ctx context.Context, args string) (string, error) {
	var params bisectParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("bisect: invalid arguments: %w", err)
	}

	result := &bisectResult{Action: params.Action, Status: "in_progress"}

	switch params.Action {
	case "start":
		t.doStart(ctx, params, result)
	case "good":
		t.doMark(ctx, "good", params.Ref, result)
	case "bad":
		t.doMark(ctx, "bad", params.Ref, result)
	case "skip":
		t.doMark(ctx, "skip", params.Ref, result)
	case "log":
		t.doLog(ctx, result)
	case "reset":
		t.doReset(ctx, result)
	default:
		return "", fmt.Errorf("bisect: unknown action %q", params.Action)
	}

	// After any action, check if we've found the commit
	if result.BadCommit == "" {
		t.checkFound(ctx, result)
	}

	outBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(outBytes), nil
}

func (t *BisectTool) doStart(ctx context.Context, params bisectParams, result *bisectResult) {
	if params.GoodRef == "" || params.BadRef == "" {
		result.Status = "error"
		result.Stderr = "both good_ref and bad_ref are required for 'start'"
		return
	}

	// git bisect start
	if out, err := runGitCmd(ctx, "bisect", "start"); err != nil {
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect start failed: %s\n%s", err, out)
		return
	}

	// git bisect bad <bad_ref>
	if out, err := runGitCmd(ctx, "bisect", "bad", params.BadRef); err != nil {
		runGitCmd(ctx, "bisect", "reset")
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect bad failed: %s\n%s", err, out)
		return
	}

	// git bisect good <good_ref>
	out, err := runGitCmd(ctx, "bisect", "good", params.GoodRef)
	if err != nil {
		runGitCmd(ctx, "bisect", "reset")
		result.Status = "error"
		result.Stderr = fmt.Sprintf("bisect good failed: %s\n%s", err, out)
		return
	}
	result.Log += out

	// Parse output for remaining
	t.parseRemaining(out, result)

	// Auto-bisect if test_cmd provided
	if params.TestCmd != "" {
		runOut, runErr := runBisectTest(ctx, params.TestCmd)
		result.Log += runOut
		if runErr != nil {
			result.Stderr = runErr.Error() + "\n" + runOut
		}
		// Parse for the found commit
		if m := bisectFoundRe.FindStringSubmatch(runOut); len(m) >= 2 {
			result.BadCommit = m[1]
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
	out, err := runGitCmd(ctx, cmdArgs...)
	result.Log = out
	if err != nil {
		result.Stderr = err.Error()
	}
	t.parseRemaining(out, result)

	// If test_cmd was stored elsewhere... We can't do auto-run without re-reading state.
	// For manual mode, just return the new state.
	if strings.Contains(out, "first bad commit") {
		if m := bisectFoundRe.FindStringSubmatch(out); len(m) >= 2 {
			result.BadCommit = m[1]
			result.Status = "found"
		}
	}
}

func (t *BisectTool) doLog(ctx context.Context, result *bisectResult) {
	out, err := runGitCmd(ctx, "bisect", "log")
	result.Log = out
	if err != nil {
		result.Stderr = err.Error()
	}
	// Count steps
	result.StepsCompleted = strings.Count(out, "# bad: ")
}

func (t *BisectTool) doReset(ctx context.Context, result *bisectResult) {
	out, err := runGitCmd(ctx, "bisect", "reset")
	result.Log = out
	result.Status = "reset"
	if err != nil {
		result.Stderr = err.Error()
	}
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
	// Check bisect log for "first bad commit"
	out, err := runGitCmd(ctx, "bisect", "log")
	if err != nil {
		return
	}
	if m := bisectFoundRe.FindStringSubmatch(out); len(m) >= 2 {
		result.BadCommit = m[1]
		result.Status = "found"
	}
}

func runGitCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runBisectTest runs a test command via git bisect run, using the
// platform-appropriate shell wrapper.
func runBisectTest(ctx context.Context, testCmd string) (string, error) {
	var shellArgs []string
	if runtime.GOOS == "windows" {
		shellArgs = []string{"cmd", "/c", testCmd}
	} else {
		shellArgs = []string{"sh", "-c", testCmd}
	}
	return runGitCmd(ctx, append([]string{"bisect", "run"}, shellArgs...)...)
}
