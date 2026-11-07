package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// withRoleRegistry installs a registry loaded from the given role files
// and restores the previous default on cleanup.
func withRoleRegistry(t *testing.T, files map[string][]byte) {
	t.Helper()
	prev := subagent.DefaultRegistry()
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(files); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(prev) })
}

func TestParseContractVerdict(t *testing.T) {
	t.Run("bullet without kind", func(t *testing.T) {
		out := "## Result\n\n```\nValidation Report\n- **verdict**: PASS\n- **detail**: all good\n```"
		v, ok := parseContractVerdict(out)
		if !ok || !strings.EqualFold(v, "PASS") {
			t.Errorf("verdict = %q ok=%v, want PASS", v, ok)
		}
	})
	t.Run("bullet with kind", func(t *testing.T) {
		out := "```\nGate\n- **verdict** (evidence): FAIL\n- **detail**: 3 tests broken\n```"
		v, ok := parseContractVerdict(out)
		if !ok || !strings.EqualFold(v, "FAIL") {
			t.Errorf("verdict = %q ok=%v, want FAIL", v, ok)
		}
	})
	t.Run("no block", func(t *testing.T) {
		if _, ok := parseContractVerdict("tests ran, things look fine"); ok {
			t.Error("expected no verdict")
		}
	})
}

func TestGateVerdictFailed_Structured(t *testing.T) {
	withRoleRegistry(t, map[string][]byte{
		"validator.md": []byte("---\nname: validator\ndescription: v\ncontract:\n  heading: Validation Report\n  fields:\n    - name: verdict\n    - name: detail\n---\nbody"),
	})

	t.Run("structured FAIL wins over PASS mentions", func(t *testing.T) {
		out := "## Validation Report\n\n```\nValidation Report\n- **verdict**: FAIL\n- **detail**: PASS was printed by the test suite earlier\n```"
		if !gateVerdictFailed("validator", out) {
			t.Error("expected FAIL from structured verdict")
		}
	})
	t.Run("structured PASS", func(t *testing.T) {
		out := "```\nValidation Report\n- **verdict**: PASS\n- **detail**: none\n```"
		if gateVerdictFailed("validator", out) {
			t.Error("expected PASS from structured verdict")
		}
	})
	t.Run("no structured block falls back to heuristic", func(t *testing.T) {
		if !gateVerdictFailed("validator", "ran tests\nFAIL TestX\n") {
			t.Error("expected heuristic FAIL")
		}
	})
}

func TestGateVerdictFailed_HeuristicWithoutContract(t *testing.T) {
	// No registry installed (or role unknown): heuristic decides.
	if gateVerdictFailed("no-such-role", "all good, tests PASS") {
		t.Error("expected heuristic PASS")
	}
	if !gateVerdictFailed("no-such-role", "FAIL TestX\n") {
		t.Error("expected heuristic FAIL")
	}
	if gateVerdictFailed("no-such-role", "FAIL earlier, but PASS at the end") {
		t.Error("last-occurrence heuristic should pass when PASS follows FAIL")
	}
}

// A tool that dispatches sub-agent work of its own must take a sub-agent
// sub-agent work is exactly a delegate call: it takes the concurrency
// slot and the task-specific branches. A composite tool that dispatched sub-agents
// of its own once existed (oversee) and is gone, so the flag is one concept again
// — this test pins that a non-spawn tool neither takes a slot nor is shaped like
// a spawn.
func TestNewDispatch_OnlySpawnSubAgentTakesASubAgentSlot(t *testing.T) {
	l := &Loop{}
	call := func(name string) types.ToolCall {
		return types.ToolCall{ID: name, Type: "function", Function: types.ToolCallFn{Name: name, Arguments: "{}"}}
	}

	for _, tc := range []struct {
		name string
		task bool
	}{
		{"delegate", true},
		{"read", false},
		{"proposal", false},
	} {
		d := l.newDispatch(0, call(tc.name))
		if d.isDelegate != tc.task {
			t.Errorf("%s: isDelegate = %v, want %v", tc.name, d.isDelegate, tc.task)
		}
	}
}

func TestDispatch_PlanModeRefusesUnadvertisedToolAtExecution(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	tool := &countingTool{name: "write"}
	reg.Register(tool)
	l := NewLoop(nil, reg, WithToolsLevel(ReadOnlyTools))
	l.Hooks = &HookEmitter{}
	d := l.newDispatch(0, types.ToolCall{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "write", Arguments: `{}`}})
	results := make(chan toolExecResult, 1)
	d.run(context.Background(), results)
	result := <-results
	if tool.calls != 0 {
		t.Fatalf("plan mode executed write %d times", tool.calls)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "not available at the current tool level") {
		t.Fatalf("result err = %v, want tool-level refusal", result.err)
	}
}

// dangerousGitStub classifies exactly like the real git tool — the same
// safe/mutating tables — but records instead of running a command, so the test
// can assert the dispatch refusal without risking a real `git commit`.
type dangerousGitStub struct{ ran int }

func (d *dangerousGitStub) Name() string              { return "git" }
func (d *dangerousGitStub) Description() string       { return "git stub" }
func (d *dangerousGitStub) Schema() json.RawMessage   { return json.RawMessage(`{"type":"object"}`) }
func (d *dangerousGitStub) IsDangerous(a string) bool { return (&tools.GitTool{}).IsDangerous(a) }
func (d *dangerousGitStub) Execute(context.Context, string) (string, error) {
	d.ran++
	return "ran", nil
}

// TestDispatch_PlanModeDeniesDangerousCallWithoutApprovalMiddleware pins the
// review finding: the plan-mode denial used to live only in the approval
// middleware, so `middleware.disabled: [approval]` let `git commit` through.
// Dispatch is the always-on gate and must refuse on its own.
func TestDispatch_PlanModeDeniesDangerousCallWithoutApprovalMiddleware(t *testing.T) {
	const commitArgs = `{"action":"commit","message":"x"}`
	reg := tools.NewEmptyRegistry()
	gitStub := &dangerousGitStub{}
	reg.Register(gitStub)

	l := NewLoop(nil, reg, WithToolsLevel(ReadOnlyTools))
	l.Config.PipelineDisabled = []string{"approval"}
	l.Hooks = &HookEmitter{}

	// The middleware really is absent from the configured pipeline, so this
	// test proves the dispatch layer, not the approval layer.
	for _, name := range pipeline.ResolvedPipelineNames(l.Config.PipelineNames, l.Config.PipelineDisabled) {
		if name == "approval" {
			t.Fatal("approval middleware is still configured; the test proves nothing")
		}
	}

	d := l.newDispatch(0, types.ToolCall{ID: "c1", Type: "function", Function: types.ToolCallFn{Name: "git", Arguments: commitArgs}})
	results := make(chan toolExecResult, 1)
	d.run(context.Background(), results)
	result := <-results
	if gitStub.ran != 0 {
		t.Fatalf("plan mode executed the dangerous git call %d time(s)", gitStub.ran)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "withheld in plan mode") {
		t.Fatalf("result err = %v, want a plan-mode withholding", result.err)
	}
	if !strings.Contains(result.content, "withheld in plan mode") {
		t.Errorf("content = %q, want the withholding surfaced to the model", result.content)
	}

	// Build mode is untouched: dispatch admits the call and the approval
	// flow decides.
	full := NewLoop(nil, reg, WithToolsLevel(FullTools))
	if ok, err := full.admitTool("git", commitArgs); !ok || err != nil {
		t.Errorf("build mode refused a dangerous call at dispatch: ok=%v err=%v", ok, err)
	}
}

type countingTool struct {
	name  string
	calls int
}

func (c *countingTool) Name() string                                    { return c.name }
func (c *countingTool) Description() string                             { return "counting test tool" }
func (c *countingTool) Schema() json.RawMessage                         { return json.RawMessage(`{"type":"object"}`) }
func (c *countingTool) Execute(context.Context, string) (string, error) { c.calls++; return "ran", nil }

// The speculation guidance promises that `max_concurrency` bounds a fan-out of
// parallel delegate calls. That bound is the sub-agent semaphore: each
// dispatch takes a slot and holds it until the dispatch ends, so N concurrent
// calls cannot exceed the cap.
func TestDispatch_SubAgentSemaphoreBoundsFanOut(t *testing.T) {
	l := &Loop{subAgentSem: make(chan struct{}, 1)}
	spawn := func() types.ToolCall {
		return types.ToolCall{Type: "function", Function: types.ToolCallFn{Name: "delegate"}}
	}

	first := l.newDispatch(0, spawn())
	if err := first.acquire(context.Background()); err != nil {
		t.Fatalf("first dispatch must take the only slot: %v", err)
	}
	defer first.release()

	second := l.newDispatch(1, spawn())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := second.acquire(ctx); err == nil {
		second.release()
		t.Fatal("a second dispatch took a slot while the cap was 1 — the fan-out bound does not hold")
	}

	// The slot frees when the first dispatch ends, not before.
	first.release()
	if err := second.acquire(context.Background()); err != nil {
		t.Fatalf("the slot must be reusable once the first dispatch ends: %v", err)
	}
	second.release()
}

// --- Stuck-child watchdog: arming window and kill classification ---

// watchdogDelegateCall builds a foreground delegate call for the tester role.
func watchdogDelegateCall() types.ToolCall {
	return types.ToolCall{ID: "c1", Type: "function", Function: types.ToolCallFn{
		Name:      "delegate",
		Arguments: `{"description":"x","prompt":"y","role":"tester"}`,
	}}
}

// The stuck-child deadline must not cover provisioning: a dispatch world that
// is still being created cannot heartbeat, so a slow world is not a stuck
// child (yaah-95ri.2).
func TestDispatch_WatchdogArmsAtWorldReadyNotAtSetup(t *testing.T) {
	l := &Loop{Config: LoopConfig{StuckChildTimeout: 250 * time.Millisecond}}
	d := l.newDispatch(0, watchdogDelegateCall())

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx = d.setupSubAgent(runCtx)

	// Provisioning: several multiples of the deadline pass with neither a
	// readiness signal nor a heartbeat.
	select {
	case <-runCtx.Done():
		t.Fatalf("watchdog killed the dispatch during provisioning: %v", runCtx.Err())
	case <-time.After(750 * time.Millisecond):
	}

	// The world exists; from here heartbeats are expected and do arrive.
	tools.WriteSubAgentWorld(runCtx, tools.SubAgentWorld{Resolved: "worktree"})
	tools.NotifySubAgentReady(runCtx)
	for range 4 {
		select {
		case <-runCtx.Done():
			t.Fatalf("watchdog killed a child that was heartbeating: %v", runCtx.Err())
		case <-time.After(30 * time.Millisecond):
			tools.SendHeartbeat(runCtx)
		}
	}
	if err := runCtx.Err(); err != nil {
		t.Fatalf("a healthy heartbeating dispatch was cancelled: %v", err)
	}
}

// A child that never beats after its world is ready never started: the kill
// must say so instead of claiming the child is stuck (yaah-95ri.3).
func TestDispatch_ZeroHeartbeatKillIsDispatchStartup(t *testing.T) {
	l := &Loop{Config: LoopConfig{StuckChildTimeout: 150 * time.Millisecond}}
	d := l.newDispatch(0, watchdogDelegateCall())

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx = d.setupSubAgent(runCtx)
	tools.WriteSubAgentWorld(runCtx, tools.SubAgentWorld{Resolved: "worktree"})
	tools.NotifySubAgentReady(runCtx)

	select {
	case <-runCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog never fired after the dispatch world became ready")
	}

	verdict := d.killVerdict()
	if !errors.Is(verdict, tools.ErrDispatchStartup) {
		t.Fatalf("verdict = %v, want ErrDispatchStartup", verdict)
	}
	if errors.Is(verdict, tools.ErrStuckChild) {
		t.Fatalf("a dispatch that never started must not be reported as stuck: %v", verdict)
	}
	if !strings.Contains(verdict.Error(), "worktree") {
		t.Errorf("verdict should name the dispatch world that never got going: %v", verdict)
	}
}

// A child that beats and then goes silent mid-run is stuck (yaah-95ri.3).
func TestDispatch_MidRunHeartbeatStopIsStuckChild(t *testing.T) {
	l := &Loop{Config: LoopConfig{StuckChildTimeout: 250 * time.Millisecond}}
	d := l.newDispatch(0, watchdogDelegateCall())

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx = d.setupSubAgent(runCtx)
	tools.WriteSubAgentWorld(runCtx, tools.SubAgentWorld{Resolved: "worktree"})
	tools.NotifySubAgentReady(runCtx)

	for range 3 {
		tools.SendHeartbeat(runCtx)
		select {
		case <-runCtx.Done():
			t.Fatal("watchdog fired while heartbeats were flowing")
		case <-time.After(30 * time.Millisecond):
		}
	}
	select {
	case <-runCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog never fired after the child went silent")
	}

	verdict := d.killVerdict()
	if !errors.Is(verdict, tools.ErrStuckChild) {
		t.Fatalf("verdict = %v, want ErrStuckChild", verdict)
	}
	if errors.Is(verdict, tools.ErrDispatchStartup) {
		t.Fatalf("a mid-run stop must not be reported as a dispatch-startup failure: %v", verdict)
	}
}

// A heartbeat buffered at the moment the deadline elapses must not be read as
// zero heartbeats: the watchdog's select can pick the timer over the pending
// beat, and the verdict has to count it (mid-run stall) rather than claim the
// dispatch never started.
func TestDispatch_BufferedHeartbeatAtDeadlineIsNotStartup(t *testing.T) {
	l := &Loop{Config: LoopConfig{StuckChildTimeout: 250 * time.Millisecond}}
	d := l.newDispatch(0, watchdogDelegateCall())

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCtx = d.setupSubAgent(runCtx)
	tools.WriteSubAgentWorld(runCtx, tools.SubAgentWorld{Resolved: "worktree"})
	tools.NotifySubAgentReady(runCtx)

	// Beat once (recorded), then queue one more beat into the buffered
	// heartbeat channel right around the deadline so it is pending when
	// the timer fires.
	tools.SendHeartbeat(runCtx)
	time.Sleep(250 * time.Millisecond)
	tools.SendHeartbeat(runCtx)

	select {
	case <-runCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog never fired")
	}

	verdict := d.killVerdict()
	if !errors.Is(verdict, tools.ErrStuckChild) {
		t.Fatalf("verdict = %v, want ErrStuckChild (heartbeats were flowing)", verdict)
	}
	if errors.Is(verdict, tools.ErrDispatchStartup) {
		t.Fatalf("an in-flight heartbeat was read as zero heartbeats: %v", verdict)
	}
}

// --- Quality-gate slot accounting (yaah-eyh) ---

// gateDelegateStub stands in for the delegate tool the gate path executes: it
// records the dispatch and returns a PASS verdict.
type gateDelegateStub struct{ calls int }

func (g *gateDelegateStub) Name() string            { return "delegate" }
func (g *gateDelegateStub) Description() string     { return "gate delegate stub" }
func (g *gateDelegateStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (g *gateDelegateStub) Execute(context.Context, string) (string, error) {
	g.calls++
	return "PASS", nil
}

// gateLoop builds a loop whose delegate tool is the stub and whose sub-agent
// cap is one: the geometry where a blocking gate acquire deadlocks, because
// the parent dispatch already holds the only slot.
func gateLoop(t *testing.T) (*Loop, *gateDelegateStub) {
	t.Helper()
	reg := tools.NewEmptyRegistry()
	stub := &gateDelegateStub{}
	reg.Register(stub)
	l := NewLoop(nil, reg, WithSubAgentConcurrency(1, 0, nil))
	l.subAgentSem = make(chan struct{}, 1)
	return l, stub
}

// captureSlogWarnings redirects the default logger for the duration of the
// test and returns a function yielding everything logged at warn or above.
func captureSlogWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string { return buf.String() }
}

// A gate dispatches while the parent sub-agent dispatch still holds the only
// slot. A blocking acquire would deadlock there, so the gate takes the
// unslotted branch — but it must still complete, and the overrun must be
// counted rather than silently swallowed (yaah-eyh).
func TestGateDispatch_SingleSlotDoesNotDeadlock(t *testing.T) {
	l, stub := gateLoop(t)
	l.subAgentSem <- struct{}{} // the parent dispatch holds the only slot
	defer func() { <-l.subAgentSem }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := l.runGateDispatch(ctx, "validator", "validate this")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gate dispatch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate dispatch blocked while the parent held the only slot — the non-blocking acquire is gone")
	}
	if stub.calls != 1 {
		t.Fatalf("validator dispatched %d time(s), want 1", stub.calls)
	}
	if got := l.UnslottedGateDispatches(); got != 1 {
		t.Errorf("unslotted gate dispatches = %d, want 1", got)
	}
	// The parent's slot is untouched: the gate never took one.
	if len(l.subAgentSem) != 1 {
		t.Errorf("sub-agent slots in use = %d, want 1 (the gate must not take the parent's slot)", len(l.subAgentSem))
	}
}

// The unslotted run is surfaced, not silent: the warning names the validator,
// the cap, and the running total, and the count is available to any run
// summary. A gate that does get a slot reports nothing (yaah-eyh).
func TestGateDispatch_UnslottedDispatchIsReported(t *testing.T) {
	t.Run("unslotted dispatch warns and counts", func(t *testing.T) {
		l, stub := gateLoop(t)
		l.subAgentSem <- struct{}{} // full: the gate must go unslotted
		logs := captureSlogWarnings(t)

		if _, err := l.runGateDispatch(context.Background(), "validator", "validate this"); err != nil {
			t.Fatalf("gate dispatch: %v", err)
		}
		if stub.calls != 1 {
			t.Fatalf("validator dispatched %d time(s), want 1", stub.calls)
		}
		if got := l.UnslottedGateDispatches(); got != 1 {
			t.Fatalf("unslotted gate dispatches = %d, want 1", got)
		}
		out := logs()
		for _, want := range []string{
			"without a sub-agent slot",
			"validator=validator",
			"max_concurrency=1",
			"unslotted_total=1",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("warning log missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("slotted dispatch is silent and releases its slot", func(t *testing.T) {
		l, stub := gateLoop(t) // a slot is free: the gate takes it
		logs := captureSlogWarnings(t)

		if _, err := l.runGateDispatch(context.Background(), "validator", "validate this"); err != nil {
			t.Fatalf("gate dispatch: %v", err)
		}
		if stub.calls != 1 {
			t.Fatalf("validator dispatched %d time(s), want 1", stub.calls)
		}
		if got := l.UnslottedGateDispatches(); got != 0 {
			t.Errorf("unslotted gate dispatches = %d, want 0 when a slot was free", got)
		}
		if out := logs(); strings.Contains(out, "without a sub-agent slot") {
			t.Errorf("a slotted gate must not warn; got:\n%s", out)
		}
		if len(l.subAgentSem) != 0 {
			t.Errorf("sub-agent slots still held = %d, want 0", len(l.subAgentSem))
		}
	})
}
