package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// --- TaskTool timeout / cancellation ---

// sleepingRunner blocks until ctx is done or the given duration elapses,
// returning a partial string alongside the ctx error on cancellation.
func sleepingRunner(d time.Duration) tools.TaskRunner {
	return func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		select {
		case <-time.After(d):
			return "completed", nil
		case <-ctx.Done():
			return "partial output", ctx.Err()
		}
	}
}

func TestTaskToolTimeoutReturnsStructuredResult(t *testing.T) {
	tool := &tools.TaskTool{
		Runner: sleepingRunner(5 * time.Second),
		ResolveTimeout: func(tools.SubAgentParams) time.Duration {
			return 50 * time.Millisecond
		},
	}
	result, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"tester"}`)
	if err != nil {
		t.Fatalf("expected structured result on timeout, got error: %v", err)
	}
	var parsed struct {
		Error   string `json:"error"`
		Partial string `json:"partial"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v (raw: %s)", err, result)
	}
	if parsed.Error != "timed out" {
		t.Errorf("error = %q, want \"timed out\"", parsed.Error)
	}
	if parsed.Partial != "partial output" {
		t.Errorf("partial = %q, want \"partial output\"", parsed.Partial)
	}
}

func TestTaskToolPerCallTimeoutOverridesDefault(t *testing.T) {
	// ResolveTimeout would give 10s; the per-call 1s must win.
	tool := &tools.TaskTool{
		Runner: sleepingRunner(5 * time.Second),
		ResolveTimeout: func(tools.SubAgentParams) time.Duration {
			return 10 * time.Second
		},
	}
	start := time.Now()
	result, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"tester","timeout_seconds":1}`)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Error   string `json:"error"`
		Timeout string `json:"timeout"`
	}
	json.Unmarshal([]byte(result), &parsed)
	if parsed.Error != "timed out" {
		t.Errorf("error = %q, want \"timed out\"", parsed.Error)
	}
	if parsed.Timeout != "1s" {
		t.Errorf("timeout = %q, want \"1s\"", parsed.Timeout)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("per-call override did not win; elapsed=%v (expected ~1s)", elapsed)
	}
}

func TestTaskToolTimeoutClampedToMax(t *testing.T) {
	// A wildly large per-call timeout is capped at 600s.
	tool := &tools.TaskTool{Runner: func(ctx context.Context, prompt string, _ tools.SubAgentParams) (string, error) {
		return "ok", nil
	}}
	// Use a runner that records the ctx deadline via a blocking runner
	// is overkill; instead verify clamping by inspecting the structured
	// result when the deadline fires. Simpler: assert the runner sees a
	// deadline ~600s out by having it return the deadline duration.
	seen := make(chan time.Duration, 1)
	tool.Runner = func(ctx context.Context, prompt string, _ tools.SubAgentParams) (string, error) {
		if dl, ok := ctx.Deadline(); ok {
			seen <- time.Until(dl)
		}
		return "ok", nil
	}
	tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"tester","timeout_seconds":999999999}`)
	select {
	case d := <-seen:
		if d > 605*time.Second || d < 595*time.Second {
			t.Errorf("deadline not clamped to ~600s: got %v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not observe a deadline")
	}
}

func TestTaskToolRoleAwareDefaultTimeout(t *testing.T) {
	// ResolveTimeout receives params.Role, so role-aware defaults apply.
	var seenRole string
	tool := &tools.TaskTool{
		Runner: func(ctx context.Context, prompt string, _ tools.SubAgentParams) (string, error) {
			return "ok", nil
		},
		ResolveTimeout: func(p tools.SubAgentParams) time.Duration {
			seenRole = p.Role
			return 0
		},
	}
	tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"reviewer"}`)
	if seenRole != "reviewer" {
		t.Errorf("ResolveTimeout saw role %q, want reviewer", seenRole)
	}
}

func TestTaskToolCancellationReturnsStructuredResult(t *testing.T) {
	runner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		<-ctx.Done()
		return "partial", ctx.Err()
	}
	tool := &tools.TaskTool{Runner: runner}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	result, err := tool.Execute(ctx, `{"description":"x","prompt":"y","role":"tester"}`)
	if err != nil {
		t.Fatalf("expected structured result on cancel, got error: %v", err)
	}
	var parsed struct {
		Error   string `json:"error"`
		Partial string `json:"partial"`
	}
	json.Unmarshal([]byte(result), &parsed)
	if parsed.Error != "cancelled" {
		t.Errorf("error = %q, want \"cancelled\"", parsed.Error)
	}
	if parsed.Partial != "partial" {
		t.Errorf("partial = %q, want \"partial\"", parsed.Partial)
	}
}

func TestTaskToolPassesParamsToRunner(t *testing.T) {
	var captured tools.SubAgentParams
	runner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		captured = params
		return "ok", nil
	}
	tool := &tools.TaskTool{Runner: runner}
	_, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"reviewer","timeout_seconds":42,"max_iterations":7}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Role != "reviewer" {
		t.Errorf("Role = %q, want reviewer", captured.Role)
	}
	if captured.TimeoutSeconds != 42 {
		t.Errorf("TimeoutSeconds = %d, want 42", captured.TimeoutSeconds)
	}
	if captured.MaxLoopCycles != 7 {
		t.Errorf("MaxLoopCycles = %d, want 7", captured.MaxLoopCycles)
	}
}

func TestTaskToolRejectsEmptyPrompt(t *testing.T) {
	tool := &tools.TaskTool{Runner: sleepingRunner(time.Second)}
	_, err := tool.Execute(context.Background(), `{"description":"x"}`)
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestTaskToolNoRunnerConfigured(t *testing.T) {
	tool := &tools.TaskTool{}
	_, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y","role":"tester"}`)
	if err == nil {
		t.Fatal("expected error when Runner is nil")
	}
}

// --- Sub-agent concurrency cap (via agent Loop) ---

// runTrackingRunner returns a TaskRunner that records the high-water
// mark of concurrent executions and blocks for the given duration.
func runTrackingRunner(d time.Duration, concurrent, maxSeen *atomic.Int32) tools.TaskRunner {
	return func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		cur := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if cur <= old || maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		defer concurrent.Add(-1)
		select {
		case <-time.After(d):
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func TestLoop_subAgentConcurrencyCap(t *testing.T) {
	const maxConc = 2
	const numCalls = 4

	var concurrent, maxSeen atomic.Int32

	runner := runTrackingRunner(80*time.Millisecond, &concurrent, &maxSeen)

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{
					Role:      "assistant",
					ToolCalls: taskCallsN(numCalls),
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "all done"},
				FinishReason: "stop",
			}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: runner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxLoopCycles:          5,
		MaxSubAgentConcurrency: maxConc}, Provider: fp,
		Registry: reg,
	}

	if _, err := loop.Run(context.Background(), "fan out"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if got := maxSeen.Load(); got > maxConc {
		t.Errorf("observed %d concurrent sub-agents, cap was %d", got, maxConc)
	}
	if got := maxSeen.Load(); got < maxConc {
		t.Errorf("expected the cap (%d) to be reached, but only saw %d concurrent", maxConc, got)
	}
}

func TestLoop_subAgentParallelWithoutCap(t *testing.T) {
	// With MaxSubAgentConcurrency == 0, all sub-agent calls run at once.
	var concurrent, maxSeen atomic.Int32
	runner := runTrackingRunner(60*time.Millisecond, &concurrent, &maxSeen)

	const n = 3
	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", ToolCalls: taskCallsN(n)}, FinishReason: "tool_calls"}}},
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}},
		},
	}
	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: runner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test", MaxLoopCycles: 5}, Provider: fp, Registry: reg}
	start := time.Now()
	if _, err := loop.Run(context.Background(), "fan out"); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	elapsed := time.Since(start)

	if got := maxSeen.Load(); got != n {
		t.Errorf("expected all %d to run concurrently, saw %d", n, got)
	}
	// Parallel: ~60ms, not ~180ms.
	if elapsed > 150*time.Millisecond {
		t.Errorf("ran sequentially? elapsed=%v", elapsed)
	}
}

// --- Interrupt propagation (parent cancel → child cancelled) ---

func TestLoop_subAgentInterruptPropagation(t *testing.T) {
	started := make(chan struct{})
	once := sync.Once{}

	runner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return "interrupted", ctx.Err()
	}

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{
					Role:      "assistant",
					ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"x","prompt":"y","role":"tester"}`}}},
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: runner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test", MaxLoopCycles: 5}, Provider: fp, Registry: reg}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	resp, err := loop.Run(ctx, "do a thing")
	// After cancellation the loop returns; the response may be empty or
	// an error depending on timing. What matters is that the loop
	// terminates promptly and the parent context is honoured.
	_ = resp
	_ = err
	// If we reach here without hanging, interrupt propagation works.
}

// --- helpers ---

// taskCallsN builds n task tool calls under the tester role.
func taskCallsN(n int) []types.ToolCall {
	calls := make([]types.ToolCall, n)
	for i := range calls {
		calls[i] = types.ToolCall{
			ID:       "c" + string(rune('1'+i)),
			Type:     "function",
			Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"x","prompt":"y","role":"tester"}`},
		}
	}
	return calls
}

// --- Stuck-child heartbeat detection ---

func TestLoop_subAgentStuckChildTimeout(t *testing.T) {
	// A hung sub-agent that blocks forever without sending heartbeats
	// should be killed by the watchdog after StuckChildTimeout.
	hangingRunner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		<-ctx.Done()
		return "partial", ctx.Err()
	}

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:       "c1",
						Type:     "function",
						Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"hang","prompt":"do nothing","role":"tester"}`},
					}},
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "handled"},
				FinishReason: "stop",
			}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: hangingRunner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxLoopCycles:     5,
		StuckChildTimeout: 100 * time.Millisecond}, Provider: fp,
		Registry: reg,
	}

	start := time.Now()
	resp, err := loop.Run(context.Background(), "hang")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("watchdog did not fire within 2s: elapsed=%v", elapsed)
	}

	if !strings.Contains(resp, "sub-agent stuck") && !strings.Contains(resp, "handled") {
		t.Errorf("expected stuck-child handling, got: %q", resp)
	}
}

func TestLoop_subAgentHeartbeatKeepsAlive(t *testing.T) {
	// A sub-agent that emits heartbeats periodically should NOT be
	// killed by the watchdog, even if it runs longer than the timeout.
	beatingRunner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; i < 5; i++ {
			select {
			case <-ticker.C:
				tools.SendHeartbeat(ctx)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "completed normally", nil
	}

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:       "c1",
						Type:     "function",
						Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"beat","prompt":"do work","role":"tester"}`},
					}},
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "done"},
				FinishReason: "stop",
			}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: beatingRunner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxLoopCycles:     5,
		StuckChildTimeout: 50 * time.Millisecond}, Provider: fp,
		Registry: reg,
	}

	resp, err := loop.Run(context.Background(), "beat")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !strings.Contains(resp, "done") {
		t.Errorf("expected normal completion, got: %q", resp)
	}
}

func TestLoop_subAgentStuckChildDisabled(t *testing.T) {
	// StuckChildTimeout == 0 should disable the watchdog entirely.
	// A blocking sub-agent is only bounded by normal context cancellation.
	hangingRunner := func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		<-ctx.Done()
		return "partial", ctx.Err()
	}

	fp := &fakeProvider{
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{
				Message: types.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:       "c1",
						Type:     "function",
						Function: types.ToolCallFn{Name: "spawn_subagent", Arguments: `{"description":"hang","prompt":"do nothing","role":"tester"}`},
					}},
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{
				Message:      types.Message{Role: "assistant", Content: "handled"},
				FinishReason: "stop",
			}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: hangingRunner})

	loop := &Loop{Config: LoopConfig{SystemPrompt: "test",
		MaxLoopCycles:     5,
		StuckChildTimeout: 0}, Provider: fp,
		Registry: reg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := loop.Run(ctx, "hang")
	if err == nil {
		t.Fatal("expected context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected context error, got: %v", err)
	}
}
