package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const verbatimBisectLog = `git bisect start
# bad: [1111111111111111111111111111111111111111] bad commit subject
# good: [2222222222222222222222222222222222222222] good commit subject
git bisect good 2222222222222222222222222222222222222222
# first bad commit: [abc1234] the broken change
`

// TestBisectCheckFound_LogForm pins yaah-mqt: git bisect log renders the
// result as "# first bad commit: [sha]", which the old regex could never match.
func TestBisectCheckFound_LogForm(t *testing.T) {
	ws := &fakeExecWorkspace{respond: func(req ExecRequest) (ExecResult, error) {
		return ExecResult{Stdout: verbatimBisectLog}, nil
	}}
	result := &bisectResult{Status: "in_progress"}

	(&BisectTool{WS: ws}).checkFound(context.Background(), result)

	if result.BadCommit != "abc1234" {
		t.Errorf("BadCommit = %q, want abc1234", result.BadCommit)
	}
	if result.Status != "found" {
		t.Errorf("Status = %q, want found", result.Status)
	}
}

// TestBisectAutoRunAbortIsNotInProgress pins yaah-6r3: git bisect run aborts
// the bisect on 126/127, so reporting "in_progress" invites more marks against
// a dead bisect.
func TestBisectAutoRunAbortIsNotInProgress(t *testing.T) {
	ws := &fakeExecWorkspace{respond: func(req ExecRequest) (ExecResult, error) {
		joined := strings.Join(req.Args, " ")
		switch {
		case joined == "bisect start", joined == "bisect bad bad-ref", joined == "bisect good good-ref":
			return ExecResult{Stdout: "Bisecting: 3 revisions left to test after this\n"}, nil
		case strings.HasPrefix(joined, "bisect run "):
			return ExecResult{
				ExitCode: 126,
				Stdout:   "fatal: unable to run the test command\n",
			}, errors.New("exit status 126")
		default:
			t.Fatalf("unexpected git command: %v", req.Args)
			return ExecResult{}, nil
		}
	}}

	args, _ := json.Marshal(map[string]any{
		"action": "start", "good_ref": "good-ref", "bad_ref": "bad-ref", "test_cmd": "false",
	})
	raw, err := (&BisectTool{WS: ws}).Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	var got bisectResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if got.Status == "in_progress" {
		t.Error("aborted auto-run still reports in_progress")
	}
	if got.Status != "error" {
		t.Errorf("Status = %q, want error", got.Status)
	}
	if !strings.Contains(got.Stderr, "aborted") || !strings.Contains(got.Stderr, "unable to run the test command") {
		t.Errorf("Stderr = %q, want the abort reason", got.Stderr)
	}
}

func TestBisectMarkFailureIsNotInProgress(t *testing.T) {
	ws := &fakeExecWorkspace{respond: func(req ExecRequest) (ExecResult, error) {
		return ExecResult{
			ExitCode: 1,
			Stdout:   "fatal: no bisect in progress\n",
		}, errors.New("exit status 1")
	}}

	raw, err := (&BisectTool{WS: ws}).Execute(context.Background(), `{"action":"good"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var got bisectResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if got.Status != "error" {
		t.Errorf("Status = %q, want error", got.Status)
	}
	if !strings.Contains(got.Stderr, "no bisect in progress") {
		t.Errorf("Stderr = %q, want the git reason", got.Stderr)
	}
}
