package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// --- Role profiles ---

func TestRoleProfileFor(t *testing.T) {
	t.Run("worker", func(t *testing.T) {
		p := RoleProfileFor(RoleWorker)
		if !contains(p.Tools, "bash") {
			t.Error("worker profile must include bash")
		}
		if !contains(p.Tools, "webfetch") {
			t.Error("worker profile must include webfetch")
		}
		if contains(p.Tools, "task") {
			t.Error("worker profile must NOT include task (workers cannot spawn)")
		}
		if p.IsSpawnCapable() {
			t.Error("worker should not be spawn-capable")
		}
		if p.MaxIterations != 25 {
			t.Errorf("worker MaxIterations = %d, want 25", p.MaxIterations)
		}
		if p.Timeout != 120*time.Second {
			t.Errorf("worker Timeout = %v, want 120s", p.Timeout)
		}
	})

	t.Run("reviewer is read-only", func(t *testing.T) {
		p := RoleProfileFor(RoleReviewer)
		for _, dangerous := range []string{"write", "edit", "delete", "bash", "powershell", "task"} {
			if contains(p.Tools, dangerous) {
				t.Errorf("reviewer profile must NOT include %q", dangerous)
			}
		}
		if !contains(p.Tools, "read") || !contains(p.Tools, "grep") {
			t.Error("reviewer profile must include read and grep")
		}
		if p.IsSpawnCapable() {
			t.Error("reviewer should not be spawn-capable")
		}
		if p.Timeout != 0 {
			t.Errorf("reviewer Timeout = %v, want 0 (unlimited)", p.Timeout)
		}
	})

	t.Run("planner can spawn", func(t *testing.T) {
		p := RoleProfileFor(RolePlanner)
		if !contains(p.Tools, "task") {
			t.Error("planner profile must include task")
		}
		if !p.IsSpawnCapable() {
			t.Error("planner should be spawn-capable")
		}
		if p.MaxIterations != 50 {
			t.Errorf("planner MaxIterations = %d, want 50", p.MaxIterations)
		}
		if p.Timeout != 300*time.Second {
			t.Errorf("planner Timeout = %v, want 300s", p.Timeout)
		}
		if p.MaxDepth != 3 {
			t.Errorf("planner MaxDepth = %d, want 3", p.MaxDepth)
		}
	})

	t.Run("default is zero-value (legacy)", func(t *testing.T) {
		p := RoleProfileFor(RoleDefault)
		if len(p.Tools) != 0 {
			t.Errorf("RoleDefault should have no tools, got %v", p.Tools)
		}
		if p.IsSpawnCapable() {
			t.Error("RoleDefault should not be spawn-capable")
		}
	})

	t.Run("unknown role falls back to default", func(t *testing.T) {
		p := RoleProfileFor(SubAgentRole("bogus"))
		if len(p.Tools) != 0 {
			t.Errorf("unknown role should fall back to zero-value profile")
		}
	})
}

func TestRoleGuidance(t *testing.T) {
	for _, role := range []SubAgentRole{RoleWorker, RoleReviewer, RolePlanner} {
		if g := RoleGuidance(role); g == "" {
			t.Errorf("RoleGuidance(%q) returned empty", role)
		}
	}
	if g := RoleGuidance(RoleDefault); g != "" {
		t.Errorf("RoleGuidance(RoleDefault) should be empty, got %q", g)
	}
}

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
	result, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y"}`)
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
	result, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y","timeout_seconds":1}`)
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
	tool.Execute(context.Background(), `{"description":"x","prompt":"y","timeout_seconds":999999999}`)
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

	result, err := tool.Execute(ctx, `{"description":"x","prompt":"y"}`)
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
	if captured.MaxIterations != 7 {
		t.Errorf("MaxIterations = %d, want 7", captured.MaxIterations)
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
	_, err := tool.Execute(context.Background(), `{"description":"x","prompt":"y"}`)
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

	loop := &Loop{
		Provider:               fp,
		Registry:               reg,
		SystemPrompt:           "test",
		MaxIterations:          5,
		MaxSubAgentConcurrency: maxConc,
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

	loop := &Loop{Provider: fp, Registry: reg, SystemPrompt: "test", MaxIterations: 5}
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
					ToolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"description":"x","prompt":"y"}`}}},
				},
				FinishReason: "tool_calls",
			}}},
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}},
		},
	}

	reg := tools.NewEmptyRegistry()
	reg.Register(&tools.TaskTool{Runner: runner})

	loop := &Loop{Provider: fp, Registry: reg, SystemPrompt: "test", MaxIterations: 5}

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

// --- SubAgentMiddleware per-role depth ---

func TestSubAgentMiddleware_roleDepthEnforcement(t *testing.T) {
	t.Run("global MaxDepth blocks beyond limit", func(t *testing.T) {
		m := &SubAgentMiddleware{MaxDepth: 2}
		msg := &types.Message{ToolCalls: taskCallsN(3)}
		step := &Step{Messages: []types.Message{}}
		_, err := m.PostModel(context.Background(), msg, step)
		if err != nil {
			t.Fatalf("PostModel error: %v", err)
		}
		if got := len(msg.ToolCalls); got != 2 {
			t.Errorf("expected 2 task calls retained, got %d", got)
		}
	})

	t.Run("per-role limit", func(t *testing.T) {
		m := &SubAgentMiddleware{
			MaxDepthByRole: map[SubAgentRole]int{RolePlanner: 1},
		}
		// Two planner task calls: first allowed, second blocked.
		calls := []types.ToolCall{
			{ID: "1", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"role":"planner","prompt":"a"}`}},
			{ID: "2", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"role":"planner","prompt":"b"}`}},
		}
		msg := &types.Message{ToolCalls: calls}
		step := &Step{Messages: []types.Message{}}
		_, err := m.PostModel(context.Background(), msg, step)
		if err != nil {
			t.Fatalf("PostModel error: %v", err)
		}
		if got := len(msg.ToolCalls); got != 1 {
			t.Errorf("expected 1 planner call retained, got %d", got)
		}
	})

	t.Run("non-task calls preserved", func(t *testing.T) {
		m := &SubAgentMiddleware{MaxDepth: 1}
		msg := &types.Message{ToolCalls: []types.ToolCall{
			{ID: "1", Type: "function", Function: types.ToolCallFn{Name: "read", Arguments: `{}`}},
			{ID: "2", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"prompt":"a"}`}},
			{ID: "3", Type: "function", Function: types.ToolCallFn{Name: "task", Arguments: `{"prompt":"b"}`}},
		}}
		step := &Step{Messages: []types.Message{}}
		m.PostModel(context.Background(), msg, step)
		if len(msg.ToolCalls) != 2 {
			t.Errorf("expected read + 1 task retained, got %d calls", len(msg.ToolCalls))
		}
	})

	t.Run("disabled when no limits set", func(t *testing.T) {
		m := &SubAgentMiddleware{}
		msg := &types.Message{ToolCalls: taskCallsN(5)}
		step := &Step{Messages: []types.Message{}}
		m.PostModel(context.Background(), msg, step)
		if len(msg.ToolCalls) != 5 {
			t.Errorf("with no limits, all calls should pass, got %d", len(msg.ToolCalls))
		}
	})
}

func TestSubAgentMiddleware_roleFromArgs(t *testing.T) {
	cases := []struct {
		args string
		want SubAgentRole
	}{
		{`{"role":"worker","prompt":"x"}`, RoleWorker},
		{`{"role":"planner"}`, RolePlanner},
		{`{"prompt":"x"}`, RoleDefault},
		{``, RoleDefault},
		{`{not valid json`, RoleDefault},
	}
	for _, c := range cases {
		if got := roleFromTaskArgs(c.args); got != c.want {
			t.Errorf("roleFromTaskArgs(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

// --- helpers ---

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// taskCallsN builds n task tool calls without a role.
func taskCallsN(n int) []types.ToolCall {
	calls := make([]types.ToolCall, n)
	for i := range calls {
		calls[i] = types.ToolCall{
			ID:       "c" + string(rune('1'+i)),
			Type:     "function",
			Function: types.ToolCallFn{Name: "task", Arguments: `{"description":"x","prompt":"y"}`},
		}
	}
	return calls
}
