package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// recordedLoopPreambles are the assistant preambles from the 2026-10-26
// incident loop (session sess-1793068719220085309, PR #597 review rounds) —
// the exact shape this detector exists for: paraphrased re-announcements,
// each followed by read-only tool calls.
var recordedLoopPreambles = []string{
	"The review bot (kilo-code-bot) found **4 issues: 1 warning + 3 suggestions**. Let me verify each against the actual code before judging:",
	"The review has **1 warning + 3 suggestions**. Checking each against the code — all four look real. Let me confirm the exact code paths and the test helpers before fixing:",
	"All four findings are real. Verifying the remaining code paths and the test setup before fixing:",
	"All four findings verified as real. Reading the two report-lane tests and the construction sites before fixing:",
	"All four findings are real and actionable. Let me read the test helpers to plan the fixes precisely:",
	"All four findings verified as real. Now implementing the fixes. First, checking the envelope field naming for consistency:",
	"All four findings are legitimate. Fixing them now. First, checking the wiring construction and the delegate background test to complete the picture:",
	"All four findings are legitimate — let me verify the envelope's worktree field naming before fixing:",
	"All four findings verified as real code paths. Implementing the fixes now. Grabbing the exact current text of the edit regions:",
	"All four findings are legitimate. Implementing fixes now — first the exact classifyRunnerResult return statements (finding 1 hinges on them):",
	"All four findings confirmed real. Now implementing the fixes. Checking the bodyErr declaration first (fix 1 needs to reassign it):",
	"Now I have the full picture. Two quick reads to nail down the helper and the code between the body run and the report block:",
}

// loopTurn feeds one incident turn through the middleware: model preamble,
// then a batch of read-only tool results (the incident's shape).
func loopTurn(t *testing.T, m *IntentLoopMiddleware, preamble string) (*Step, error) {
	t.Helper()
	step := &Step{Messages: []types.Message{types.SystemMsg("sys")}}
	msg := &types.Message{Role: "assistant", Content: preamble}
	step, err := m.PostModel(context.Background(), msg, step)
	if err != nil {
		t.Fatalf("PostModel: %v", err)
	}
	results := []ToolResult{
		{Name: "bash", Args: `{"command":"gh pr view 597 --json comments"}`, Result: "comment batch"},
		{Name: "bash", Args: `{"command":"sed -n '1,40p' internal/tools/retained.go"}`, Result: "file body"},
	}
	step, err = m.PostTool(context.Background(), results, step)
	return step, err
}

// TestIntentLoop_SteersThenHaltsTheRecordedLoop replays the real incident
// preambles: by the steer threshold the model is told to act, and by the
// halt threshold the run ends instead of burning another hour.
func TestIntentLoop_SteersThenHaltsTheRecordedLoop(t *testing.T) {
	m := NewIntentLoopMiddleware(4, 8)

	sawSteer := false
	halted := false
	for i, preamble := range recordedLoopPreambles {
		step, err := loopTurn(t, m, preamble)
		if err != nil {
			if !errors.Is(err, IntentLoopError{}) {
				t.Fatalf("turn %d: unexpected error %v", i, err)
			}
			halted = true
			if i+1 < 8 {
				t.Fatalf("halted at turn %d, want >= 8", i+1)
			}
			break
		}
		// PrepareStep is where steering lands.
		prep, prepErr := m.PrepareStep(context.Background(), step)
		if prepErr != nil {
			if !errors.Is(prepErr, IntentLoopError{}) {
				t.Fatalf("turn %d: unexpected prepare error %v", i, prepErr)
			}
			halted = true
			if i+1 < 8 {
				t.Fatalf("halted at turn %d, want >= 8", i+1)
			}
			break
		}
		for _, msg := range prep.Messages {
			if strings.Contains(msg.Content, "[STEER]") && strings.Contains(msg.Content, "re-announced the same plan") {
				sawSteer = true
			}
		}
	}
	if !sawSteer {
		t.Error("the recorded loop never produced a steer message")
	}
	if !halted {
		t.Error("the recorded loop ran past the halt threshold without halting")
	}
}

// TestIntentLoop_MutationResets: a successful edit is progress — the streak
// (and the anchor) reset, so a long but productive session never trips.
func TestIntentLoop_MutationResets(t *testing.T) {
	m := NewIntentLoopMiddleware(4, 8)

	for i := 0; i < 3; i++ {
		if _, err := loopTurn(t, m, recordedLoopPreambles[2]); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	// A successful edit.
	step := &Step{}
	if _, err := m.PostTool(context.Background(), []ToolResult{
		{Name: "edit", Args: `{"filePath":"x.go"}`, Result: "Replaced 1 occurrence(s)"},
	}, step); err != nil {
		t.Fatalf("PostTool: %v", err)
	}
	if m.streak != 0 {
		t.Errorf("streak = %d after a successful mutation, want 0", m.streak)
	}
}

// TestIntentLoop_UnrelatedPreamblesDoNotChain: healthy turns with different
// plans must never accumulate a streak.
func TestIntentLoop_UnrelatedPreamblesDoNotChain(t *testing.T) {
	m := NewIntentLoopMiddleware(4, 8)

	preambles := []string{
		"Let me read the failed test output to see which assertion fired.",
		"Now I need to update the configuration docs table for the renamed key.",
		"Next I will run the full test suite before committing anything further.",
		"The config loader needs a validation walk for the new reasoning parameter.",
		"Inspecting the auth middleware to understand how the login fallback resolves.",
		"Running gofmt and staticcheck across the changed packages now.",
	}
	for i, p := range preambles {
		step, err := loopTurn(t, m, p)
		if err != nil {
			t.Fatalf("turn %d halted unexpectedly: %v", i, err)
		}
		prep, err := m.PrepareStep(context.Background(), step)
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		for _, msg := range prep.Messages {
			if strings.Contains(msg.Content, "[STEER]") {
				t.Fatalf("turn %d steered on unrelated preambles: %q", i, msg.Content)
			}
		}
	}
}

// TestIntentLoop_ShortPreamblesAreIgnored: "Let me read the tests" carries no
// plan; similarity on it is noise.
func TestIntentLoop_ShortPreamblesAreIgnored(t *testing.T) {
	m := NewIntentLoopMiddleware(4, 8)
	for i := 0; i < 10; i++ {
		if _, err := loopTurn(t, m, "Let me read the tests."); err != nil {
			t.Fatalf("short preamble halted a run: %v", err)
		}
	}
	if m.streak != 0 {
		t.Errorf("streak = %d from short preambles, want 0 (ignored)", m.streak)
	}
}

// TestIntentLoop_ProgressClassification: the reset must follow what actually
// advances the plan — a proposal SELECT resets, a proposal LIST does not; a
// real go_mod add resets, a dry_run does not.
func TestIntentLoop_ProgressClassification(t *testing.T) {
	tests := []struct {
		name     string
		result   ToolResult
		progress bool
	}{
		{"edit", ToolResult{Name: "edit", Args: `{"filePath":"x.go"}`, Result: "ok"}, true},
		{"replace dry-run", ToolResult{Name: "replace", Args: `{"dry_run":true}`, Result: "Would replace"}, false},
		{"proposal list", ToolResult{Name: "proposal", Args: `{"action":"list"}`, Result: "{}"}, false},
		{"proposal select", ToolResult{Name: "proposal", Args: `{"action":"select"}`, Result: "settled"}, true},
		{"json_query read", ToolResult{Name: "json_query", Args: `{"action":"query"}`, Result: "[]"}, false},
		{"json_query write", ToolResult{Name: "json_query", Args: `{"action":"write"}`, Result: "ok"}, true},
		{"go_mod list", ToolResult{Name: "go_mod", Args: `{"action":"list"}`, Result: "[]"}, false},
		{"go_mod add dry-run", ToolResult{Name: "go_mod", Args: `{"action":"add","dry_run":true}`, Result: "would run"}, false},
		{"go_mod add", ToolResult{Name: "go_mod", Args: `{"action":"add"}`, Result: "ok"}, true},
		{"git status", ToolResult{Name: "git", Args: `{"action":"status"}`, Result: "clean"}, false},
		{"git commit", ToolResult{Name: "git", Args: `{"action":"commit"}`, Result: "ok"}, true},
		{"plan list", ToolResult{Name: "plan", Args: `{"action":"list"}`, Result: "[]"}, false},
		{"plan create", ToolResult{Name: "plan", Args: `{"action":"create"}`, Result: "ok"}, true},
		{"memory add", ToolResult{Name: "memory_add", Args: `{}`, Result: "ok"}, false},
		{"delegate proposal", ToolResult{Name: "delegate", Args: `{"role":"developer"}`, Result: `{"status":"proposal","proposal_id":"p1"}`}, true},
		{"delegate truncated proposal", ToolResult{Name: "delegate", Args: `{"role":"developer"}`, Result: `{"status":"proposal","proposal_id":"p1","diff":"` + strings.Repeat("x", 30000) + `"}`}, true},
		{"delegate report", ToolResult{Name: "delegate", Args: `{"role":"reviewer"}`, Result: `{"status":"report"}`}, true},
		{"delegate plain report", ToolResult{Name: "delegate", Args: `{"role":"reviewer"}`, Result: "## Report\n\nEverything passed."}, true},
		{"delegate error", ToolResult{Name: "delegate", Args: `{}`, Result: `{"status":"error"}`}, false},
		{"delegate timed out", ToolResult{Name: "delegate", Args: `{}`, Result: `{"error":"timed out","role":"developer","timeout":"600s"}`}, false},
		{"delegate background", ToolResult{Name: "delegate", Args: `{}`, Result: `{"job_id":"j1"}`}, false},
		{"failed edit", ToolResult{Name: "edit", Args: `{}`, Error: errSentinel}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := madeProgress(tt.result); got != tt.progress {
				t.Errorf("madeProgress(%s) = %v, want %v", tt.name, got, tt.progress)
			}
		})
	}
}

var errSentinel = errors.New("tool failed")

// TestNewIntentLoopMiddlewareClampsHalt: a halt at or below the steer
// threshold would end the run before the model ever saw the steer.
func TestNewIntentLoopMiddlewareClampsHalt(t *testing.T) {
	m := NewIntentLoopMiddleware(6, 6)
	if m.haltAfter != 7 {
		t.Errorf("haltAfter = %d, want 7 (steer+1)", m.haltAfter)
	}
}
