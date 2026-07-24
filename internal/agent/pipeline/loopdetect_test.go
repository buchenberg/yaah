package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func grepResults(n int) []ToolResult {
	res := make([]ToolResult, n)
	for i := 0; i < n; i++ {
		res[i] = ToolResult{Name: "grep", Args: `{"pattern":"foo"}`, Result: "matched 3"}
	}
	return res
}

func readResults(n int) []ToolResult {
	res := make([]ToolResult, n)
	for i := 0; i < n; i++ {
		res[i] = ToolResult{Name: "read", Args: `{"file":"x.go"}`, Result: "content"}
	}
	return res
}

func TestLoopDetect_Name(t *testing.T) {
	m := &LoopDetectionMiddleware{}
	if m.Name() != "loop_detection" {
		t.Errorf("Name() = %q, want %q", m.Name(), "loop_detection")
	}
}

// --- category repetition (convergence nudge) ---

func TestLoopDetect_CategoryStrikeInjectsHint(t *testing.T) {
	m := &LoopDetectionMiddleware{
		categoryStrikeCap: 3,
		categoryMinCalls:  5,
	}
	step := &Step{Messages: nil}

	// Two turns of >=5 greps — no hint yet (cap is 3).
	_, _ = m.PostTool(context.Background(), grepResults(6), step)
	_, _ = m.PostTool(context.Background(), grepResults(7), step)
	if len(step.Messages) > 0 {
		t.Fatalf("no hint expected at strike 2, got %d messages", len(step.Messages))
	}

	// Third consecutive turn — hint must be injected.
	_, _ = m.PostTool(context.Background(), grepResults(9), step)
	if len(step.Messages) != 1 {
		t.Fatalf("expected 1 hint message, got %d", len(step.Messages))
	}
	msg := step.Messages[0].Content
	if !strings.Contains(msg, "[STEER]") {
		t.Errorf("hint missing STEER prefix: %q", msg)
	}
	if !strings.Contains(msg, "grep") {
		t.Errorf("hint should mention the dominant tool, got %q", msg)
	}
	if m.categoryStrikes["grep"] != 0 {
		t.Errorf("strike counter should reset after hint, got %d", m.categoryStrikes["grep"])
	}
}

func TestLoopDetect_CategoryBreakResets(t *testing.T) {
	m := &LoopDetectionMiddleware{
		categoryStrikeCap: 3,
		categoryMinCalls:  5,
	}
	step := &Step{Messages: nil}

	// Turn 1: dominant = grep.
	_, _ = m.PostTool(context.Background(), grepResults(6), step)
	// Turn 2: dominant = read (breaks the grep streak).
	_, _ = m.PostTool(context.Background(), readResults(6), step)
	if m.categoryStrikes["grep"] != 0 {
		t.Errorf("grep strikes should be cleared when dominant tool changes: %d", m.categoryStrikes["grep"])
	}
	if m.categoryStrikes["read"] != 1 {
		t.Errorf("read should start a new strike: %d", m.categoryStrikes["read"])
	}
}

func TestLoopDetect_TiedDominanceSkips(t *testing.T) {
	m := &LoopDetectionMiddleware{
		categoryStrikeCap: 3,
		categoryMinCalls:  5,
	}
	step := &Step{Messages: nil}

	// Turn 1: grep dominance.
	_, _ = m.PostTool(context.Background(), grepResults(7), step)
	if m.categoryStrikes["grep"] != 1 {
		t.Fatalf("grep should be at strike 1, got %d", m.categoryStrikes["grep"])
	}
	// Turn 2: grep AND read both called 5 times — tie, no clear dominant.
	_, _ = m.PostTool(context.Background(), append(grepResults(5), readResults(5)...), step)
	if m.categoryStrikes["grep"] != 0 {
		t.Errorf("tied dominance should clear strikes for the old dominant, got grep=%d", m.categoryStrikes["grep"])
	}
}

func TestLoopDetect_BelowMinCallsSkips(t *testing.T) {
	m := &LoopDetectionMiddleware{
		categoryStrikeCap: 3,
		categoryMinCalls:  5,
	}
	step := &Step{Messages: nil}

	// Only 3 greps — under the 5-call threshold → no dominance.
	_, _ = m.PostTool(context.Background(), grepResults(3), step)
	if len(m.categoryStrikes) > 0 {
		t.Errorf("below-threshold turn should reset strikes: %v", m.categoryStrikes)
	}
}

// --- iteration steering ---

func TestLoopDetect_SteerAtBoundary(t *testing.T) {
	m := &LoopDetectionMiddleware{
		steerThreshold: 0.8,
	}
	step := &Step{
		Iteration:    8,
		MaxIterations: 10,
		Messages:      nil,
	}
	_, _ = m.PrepareStep(context.Background(), step)
	if len(step.Messages) != 1 {
		t.Fatalf("expected 1 steer message at 80%%, got %d", len(step.Messages))
	}
	msg := step.Messages[0].Content
	if !strings.Contains(msg, "[STEER]") {
		t.Errorf("steer missing STEER prefix: %q", msg)
	}
	if !strings.Contains(msg, "iteration 8 of 10") {
		t.Errorf("steer should mention iteration numbers, got %q", msg)
	}
	if !strings.Contains(msg, "2 remain") {
		t.Errorf("steer should mention remaining iterations, got %q", msg)
	}
}

func TestLoopDetect_NoSteerBelowBoundary(t *testing.T) {
	m := &LoopDetectionMiddleware{
		steerThreshold: 0.8,
	}
	step := &Step{
		Iteration:    7,
		MaxIterations: 10,
		Messages:      nil,
	}
	_, _ = m.PrepareStep(context.Background(), step)
	if len(step.Messages) > 0 {
		t.Errorf("no steer expected at iteration %d of %d, got %d messages", step.Iteration, step.MaxIterations, len(step.Messages))
	}
}

func TestLoopDetect_SteerOnlyOnce(t *testing.T) {
	m := &LoopDetectionMiddleware{
		steerThreshold: 0.8,
	}
	step := &Step{Iteration: 8, MaxIterations: 10, Messages: nil}
	_, _ = m.PrepareStep(context.Background(), step)
	if len(step.Messages) != 1 {
		t.Fatalf("first call should inject, got %d", len(step.Messages))
	}
	// Same boundary, different iteration — must not re-inject.
	step2 := &Step{Iteration: 9, MaxIterations: 10, Messages: nil}
	_, _ = m.PrepareStep(context.Background(), step2)
	if len(step2.Messages) > 0 {
		t.Errorf("deduplication failed: same boundary should not re-inject, got %d messages", len(step2.Messages))
	}
}

func TestLoopDetect_NoSteerWhenUnlimited(t *testing.T) {
	m := &LoopDetectionMiddleware{
		steerThreshold: 0.8,
	}
	step := &Step{Iteration: 100, MaxIterations: 0, Messages: nil}
	_, _ = m.PrepareStep(context.Background(), step)
	if len(step.Messages) > 0 {
		t.Errorf("unlimited iterations should never trigger steer, got %d", len(step.Messages))
	}
}

// --- exact-hash loop detection (regression guard) ---

func TestLoopDetect_ExactHashHalt(t *testing.T) {
	m := &LoopDetectionMiddleware{
		window: 5,
		count:  3,
	}
	step := &Step{Messages: nil}

	// Same tool+args+result three times → must halt.
	same := ToolResult{Name: "bash", Args: `echo 1`, Result: "1\n"}
	_, _ = m.PostTool(context.Background(), []ToolResult{same}, step)
	_, _ = m.PostTool(context.Background(), []ToolResult{same}, step)
	_, _ = m.PostTool(context.Background(), []ToolResult{same}, step)

	// The third call should have returned (step, nil) — no error.
	// A fourth identical call should error.
	_, err := m.PostTool(context.Background(), []ToolResult{same}, step)
	if err == nil {
		t.Fatal("exact-hash loop should halt on 4th identical result")
	}
	if !strings.Contains(err.Error(), "loop detected") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoopDetect_DifferentArgsNotHalted(t *testing.T) {
	m := &LoopDetectionMiddleware{
		window: 10,
		count:  3,
	}
	step := &Step{Messages: nil}

	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "grep", Args: `a`, Result: "1"}}, step)
	_, _ = m.PostTool(context.Background(), []ToolResult{{Name: "grep", Args: `b`, Result: "1"}}, step)
	_, err := m.PostTool(context.Background(), []ToolResult{{Name: "grep", Args: `c`, Result: "1"}}, step)
	if err != nil {
		t.Errorf("different args should not trigger exact-hash halt: %v", err)
	}
}

func TestLoopDetect_CategoryDoesNotHalt(t *testing.T) {
	// Category repetition should nudge (inject hint), NOT halt with an error.
	// Use varied args so exact-hash doesn't fire, but the same tool name
	// dominates consecutive turns — exactly the real feedback-loop pattern.
	m := &LoopDetectionMiddleware{
		categoryStrikeCap: 2,
		categoryMinCalls:  5,
		window:            4,
		count:             10,
	}
	step := &Step{Messages: nil}

	// Three turns of heavy grepping, each with different patterns → no hash collision.
	for turn := 0; turn < 3; turn++ {
		res := make([]ToolResult, 6)
		for i := 0; i < 6; i++ {
			res[i] = ToolResult{Name: "grep", Args: fmt.Sprintf(`{"pattern":"p%d-%d"}`, turn, i), Result: fmt.Sprintf("match-%d", turn)}
		}
		_, err := m.PostTool(context.Background(), res, step)
		if err != nil {
			t.Fatalf("category repetition must NOT halt (only inject hint): %v", err)
		}
	}
	if len(step.Messages) == 0 {
		t.Error("category repetition should have injected at least one hint")
	}
}

func TestLoopDetect_DefaultsApplied(t *testing.T) {
	// No fields set — defaults must be applied silently.
	m := &LoopDetectionMiddleware{window: 5, count: 3}
	step := &Step{Messages: nil, MaxIterations: 10, Iteration: 8}
	_, _ = m.PrepareStep(context.Background(), step)
	// Should use defaultSteerThreshold (0.8 → boundary at 8).
	if len(step.Messages) != 1 {
		t.Fatalf("defaults not applied: expected 1 steer at boundary, got %d", len(step.Messages))
	}
}
