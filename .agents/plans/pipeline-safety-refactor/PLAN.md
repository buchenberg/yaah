# Pipeline Safety Refactor Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.
> *(Phases 1–4 modify overlapping files in `internal/agent` and `internal/agent/pipeline`; implement those sequentially. Phase 5 is a deliberate deferral — do not implement.)*

**Goal:** Eliminate duplication between the agent loop and the middleware pipeline by making approval, inline-tool limiting, and conflict detection real middleware, and by removing dead/duplicated pipeline wiring.

**Architecture:** The pipeline (`internal/agent/pipeline`) is the single interception layer for tool-call filtering and post-tool enrichment. Today approval gating lives inline in `Loop.executeAndCollect`, `ApprovalMiddleware` is an empty stub, two `ToolConcurrencyMiddleware` instances exist per loop, and builder maps duplicate entries. This plan moves loop-inline safety logic into middleware behind injected function hooks (the pattern already used by `SteerDrain DrainFunc`), adds a `Step.SynthesizedResults` channel for middleware-generated tool results, and deduplicates builders.

**Tech Stack:** Go 1.25+, stdlib only, `testing` package, table-driven tests per repo convention.

---

## Background: current state (read this first)

| Concern | Where it lives today | Problem |
|---|---|---|
| Path permission rules | `PermissionMiddleware.PostModel` (`internal/agent/pipeline/permission.go:30-45`) | Correct — reference implementation |
| Approval (deny/ask) | Inline in `Loop.executeAndCollect` (`internal/agent/agent_tools.go:41-56`) | Should be middleware; stub is dead code |
| `ApprovalMiddleware` | `internal/agent/pipeline/approval.go` | All three hooks are no-op returns; `mode` field never read |
| Tool concurrency | TWO instances: pipeline builds one whose `Acquire`/`Release` are never called by the pipeline; the loop separately creates `l.toolConcurrency` (`internal/agent/lifecycle_init.go:145-147`, `internal/agent/subagent_loop.go:130-132`) used at `agent_tools.go:82-92` | Dead instance + duplicated config |
| Builder maps | `"permission"` and `"tool_concurrency"` closures literally duplicated between `builtinBuilders` and `subAgentBuilders` (`internal/agent/pipeline/config.go:98-99` vs `108-112`) | DRY violation |
| MaxInlineToolsPerTurn drop | Inline in `executeToolPhase` (`internal/agent/turn.go:129-151`) | Middleware-shaped call-batch filter |
| Conflict detection | Inline in `executeToolPhase` (`internal/agent/turn.go:167-194`) | Textbook PostTool |
| MaxToolTurns strip + wrap-up notice | `buildTurnRequest` (`internal/agent/turn.go:44-62`) | **Defer** — mutates `types.ChatRequest`, which middleware cannot see |

### Key constraint discovered during analysis

`Loop.runMiddleware` flow (loop.go:140-285): after `RunPostModel` returns, `executeToolPhase` executes `(*step).Messages = *messages` (turn.go:165), which **clobbers anything a PostModel middleware appended to `step.Messages`**. Therefore middleware that removes tool calls before dispatch cannot append synthesized results to `step.Messages`. The fix is a dedicated `Step.SynthesizedResults` field merged by `executeToolPhase` (Task 3.1). PostTool middleware CAN safely append to `step.Messages` because nothing overwrites it afterward (`messages = step.Messages` at loop.go:281).

### Behavioral notes to preserve exactly

- Denial error strings: `"error: tool %q requires approval but approval mode is 'deny'"` and `"error: tool %q was denied by user"` (agent_tools.go:42,50).
- Denied calls emit both `events.ToolStart` and `events.ToolEnd` hook events, with `ToolError` and `ToolResult` set on the end event (agent_tools.go:43-44,51-52).
- Approval is evaluated **per call, in order**, only for tools where `classifyDanger` is true (i.e., implements `tools.DangerClassifier`). Non-dangerous tools are never gated.
- Sub-agent loops run `ApprovalMode: "allow"` (`internal/agent/subagent_loop.go:104`) and exclude `approval` from their pipeline — no behavior change there.
- `PermissionMiddleware` must observe calls BEFORE approval strips them (permission rules take precedence). In PostModel chains this means `approval` must come later than any rule-filtering middleware.
- After this refactor, denied calls no longer appear in `pipeline.ToolResult` slices passed to PostTool/shepherd-trace. They remain visible as persisted synthetic tool-result messages. This is an accepted, documented behavior change.

---

## Phase 1 — Deduplicate builder maps (low risk, do first)

### Task 1.1: Extract shared builder functions

**Objective:** Replace duplicated map-literal closures with named package-level builder functions.

**Files:**
- Modify: `internal/agent/pipeline/config.go:73-128`
- Test: `internal/agent/pipeline/config_test.go`

**Step 1: Write failing test**

Add to `config_test.go`:

```go
func TestBuilders_SharedEntriesProduceSameTypes(t *testing.T) {
	cfg := PipelineConfig{
		PermissionRules:    []PermissionRule{{Tool: "bash", Mode: "deny"}},
		MaxToolConcurrency: 3,
	}
	for _, name := range []string{"permission", "tool_concurrency"} {
		builtin, okB := builtinBuilders[name]
		sub, okS := subAgentBuilders[name]
		if !okB || !okS {
			t.Fatalf("builder %q missing from one of the maps", name)
		}
		if reflect.TypeOf(builtin(cfg)) != reflect.TypeOf(sub(cfg)) {
			t.Errorf("builder %q produces different types across maps", name)
		}
	}
}
```

(Add `"reflect"` to imports.)

**Step 2: Run test to verify failure**

Run: `go test ./internal/agent/pipeline/ -run TestBuilders_SharedEntriesProduceSameTypes -v`
Expected: PASS already? No — it passes today (both produce the same types). This test is a regression guard, not a red test. Skip the red step here; write the test, confirm PASS, then refactor.

**Step 3: Refactor config.go**

Above `builtinBuilders`, add:

```go
// buildPermission is shared by the orchestrator and sub-agent pipelines.
func buildPermission(cfg PipelineConfig) Middleware {
	return &PermissionMiddleware{rules: cfg.PermissionRules}
}

// buildToolConcurrency is shared by the orchestrator and sub-agent
// pipelines. See Task 2.x: when cfg.ToolConc carries a pre-built
// instance from the Loop, both pipelines share it.
func buildToolConcurrency(cfg PipelineConfig) Middleware {
	if cfg.ToolConc != nil {
		return cfg.ToolConc
	}
	return NewToolConcurrencyMiddleware(cfg.MaxToolConcurrency)
}
```

Replace both map entries:

```go
"permission":       buildPermission,
"tool_concurrency": buildToolConcurrency,
```

in **both** `builtinBuilders` and `subAgentBuilders` (delete the duplicated closures at lines 98-99 and 108-112).

**Step 4: Run tests**

Run: `go test ./internal/agent/pipeline/ -v`
Expected: all PASS.

**Step 5: Commit**

```bash
git add internal/agent/pipeline/config.go internal/agent/pipeline/config_test.go
git commit -m "refactor(pipeline): extract shared permission/tool_concurrency builders"
```

---

## Phase 2 — Single tool-concurrency instance

### Task 2.1: Add `ToolConc` to PipelineConfig

**Objective:** Let the Loop pass its existing `l.toolConcurrency` semaphore into the pipeline so both share ONE instance.

**Files:**
- Modify: `internal/agent/pipeline/config.go` (`PipelineConfig` struct, after line 38)

**Step 1: Write failing test**

Add to `config_test.go`:

```go
func TestPipeline_ToolConcurrencySharesInstance(t *testing.T) {
	shared := NewToolConcurrencyMiddleware(2)
	p := NewFromConfig(PipelineConfig{ToolConc: shared, MaxToolConcurrency: 2})
	mw := p.Find("tool_concurrency")
	if mw == nil {
		t.Fatal("pipeline missing tool_concurrency")
	}
	got, ok := mw.(*ToolConcurrencyMiddleware)
	if !ok {
		t.Fatalf("tool_concurrency is %T, want *ToolConcurrencyMiddleware", mw)
	}
	if got != shared {
		t.Error("pipeline built a second ToolConcurrencyMiddleware instead of sharing the Loop's instance")
	}
}
```

Run: `go test ./internal/agent/pipeline/ -run TestPipeline_ToolConcurrencySharesInstance -v`
Expected: FAIL — `PipelineConfig` has no field `ToolConc`.

**Step 2: Implement**

In `PipelineConfig`, next to `MaxToolConcurrency int` (config.go:38), add:

```go
	// ToolConc, when non-nil, is the Loop-owned semaphore instance.
	// Both orchestrator and sub-agent pipelines reuse it so there is
	// exactly one live ToolConcurrencyMiddleware per loop (the pipeline's
	// own Acquire/Release are never called by the pipeline; the Loop's
	// executeAndCollect drives the semaphore directly).
	ToolConc *ToolConcurrencyMiddleware
```

(`buildToolConcurrency` from Task 1.1 already honours it.)

Run: `go test ./internal/agent/pipeline/ -run TestPipeline_ToolConcurrencySharesInstance -v`
Expected: PASS.

**Step 3: Wire the Loop to pass its instance**

Modify `internal/agent/loop.go` `toPipelineConfig()` (line 70 area):

```go
		MaxToolConcurrency:     l.Config.MaxToolConcurrency,
		ToolConc:               l.toolConcurrency,
```

Ordering check: `buildPipeline()` is called in `runMiddleware` (loop.go:137) AFTER `l.applyDefaults()` (loop.go:133), which creates `l.toolConcurrency` (lifecycle_init.go:145-147). Sub-agent loops create it in `NewSubAgentLoop` (subagent_loop.go:130-132). Both paths are safe.

**Step 4: Add a Loop-level guard test**

Add to `internal/agent/buildpipeline_test.go`:

```go
func TestBuildPipeline_SharesToolConcurrencyInstance(t *testing.T) {
	l := &Loop{
		CtxMgr: &ContextManager{},
		Config: LoopConfig{MaxToolConcurrency: 4},
	}
	l.applyDefaults()
	if l.toolConcurrency == nil {
		t.Fatal("applyDefaults did not create toolConcurrency")
	}
	p := l.buildPipeline()
	mw := p.Find("tool_concurrency")
	if mw != nil && mw != pipeline.Middleware(l.toolConcurrency) {
		t.Error("pipeline tool_concurrency is not the Loop's semaphore instance")
	}
}
```

Note: if `p.Find("tool_concurrency")` returns nil for default configs in tests (depends on `resolvedPipelineNames` defaults — `tool_concurrency` IS in `defaultPipelineNames`, config.go:150), the nil-guard keeps the test honest either way.

**Step 5: Full package verification**

Run: `go test ./internal/agent/... ./internal/agent/pipeline/...`
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/agent/pipeline/config.go internal/agent/loop.go internal/agent/buildpipeline_test.go internal/agent/pipeline/config_test.go
git commit -m "refactor(agent): share one ToolConcurrencyMiddleware between Loop and pipeline"
```

---

## Phase 3 — Real ApprovalMiddleware

This is the largest phase. It moves deny/ask gating from `executeAndCollect` into `ApprovalMiddleware.PostModel`, using injected function hooks (same decoupling pattern as `SteerDrain`).

### Task 3.1: Add `Step.SynthesizedResults`

**Objective:** Give middleware a way to inject tool-result messages for calls they strip, which `executeToolPhase` merges into the conversation.

**Files:**
- Modify: `internal/agent/pipeline/middleware.go:11-19`
- Modify: `internal/agent/turn.go:127-165`
- Test: `internal/agent/pipeline/middleware_test.go` (create if absent)

**Step 1: Write failing test**

Create/extend `internal/agent/pipeline/middleware_test.go`:

```go
func TestStep_SynthesizedResultsField(t *testing.T) {
	step := &Step{}
	step.SynthesizedResults = append(step.SynthesizedResults,
		types.ToolResultMsg("call-1", "bash", "error: denied"))
	if len(step.SynthesizedResults) != 1 {
		t.Fatal("SynthesizedResults not retained")
	}
}
```

(This is a compile-level contract test; the real behaviour is exercised via the Loop integration test in Task 3.5.)

**Step 2: Implement**

In `middleware.go` extend `Step`:

```go
type Step struct {
	Messages      []types.Message
	Tools         []types.ToolDef
	Iteration     int
	MaxToolTurns  int
	MaxLoopCycles int
	Model         string
	SystemPrompt  string

	// SynthesizedResults holds tool-result messages generated by
	// middleware for tool calls removed before dispatch (approval
	// denials, inline-limit drops). Every tool_call_id on an assistant
	// message needs a result; executeToolPhase appends these ahead of
	// executed-call results so provider invariants hold.
	SynthesizedResults []types.Message
}
```

In `turn.go` `executeToolPhase`, immediately before `toolResults := l.executeAndCollect(...)` (line 164):

```go
	for _, m := range (*step).SynthesizedResults {
		*messages = append(*messages, m)
		l.Persister.Persist(m)
	}
	(*step).SynthesizedResults = nil
```

**Step 3: Verify**

Run: `go test ./internal/agent/... ./internal/agent/pipeline/...`
Expected: PASS (no behaviour change yet — nothing populates the field).

**Step 4: Commit**

```bash
git add internal/agent/pipeline/middleware.go internal/agent/turn.go
git commit -m "feat(pipeline): add Step.SynthesizedResults for pre-dispatch tool-result synthesis"
```

### Task 3.2: Implement ApprovalMiddleware.PostModel

**Objective:** Filter dangerous tool calls per `ApprovalMode`, synthesize denial results, emit hook events — all through injected functions.

**Files:**
- Rewrite: `internal/agent/pipeline/approval.go`
- Modify: `internal/agent/pipeline/config.go` (builder + PipelineConfig fields)
- Test: `internal/agent/pipeline/approval_test.go`

**Step 1: Write failing tests**

Create `internal/agent/pipeline/approval_test.go`:

```go
package pipeline

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

func TestApprovalMiddleware_DenyModeStripsDangerousCalls(t *testing.T) {
	var deniedArgs string
	m := &ApprovalMiddleware{
		mode: "deny",
		classify: func(name, args string) bool { return name == "bash" },
		approve:  func(name, args string) bool { t.Fatal("approve must not run in deny mode"); return false },
		emitDeny: func(name, args, errMsg string) { deniedArgs = args },
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"rm -rf /"}`}},
		{ID: "2", Function: types.FunctionCall{Name: "read", Arguments: `{"filePath":"/etc/hosts"}`}},
	}}
	step := &Step{}
	out, err := m.PostModel(context.Background(), msg, step)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read" {
		t.Errorf("dangerous call not stripped: %+v", msg.ToolCalls)
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
	}
	wantErr := `error: tool "bash" requires approval but approval mode is 'deny'`
	if !contains(step.SynthesizedResults[0].Content, wantErr) {
		t.Errorf("synthesized result = %q, want substring %q", step.SynthesizedResults[0].Content, wantErr)
	}
	if deniedArgs != `{"command":"rm -rf /"}` {
		t.Errorf("emitDeny not called with original args: %q", deniedArgs)
	}
	_ = out
}

func TestApprovalMiddleware_AskModeHonoursApprove(t *testing.T) {
	approved := false
	m := &ApprovalMiddleware{
		mode:     "ask",
		classify: func(name, args string) bool { return true },
		approve:  func(name, args string) bool { approved = true; return approved },
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.FunctionCall{Name: "write", Arguments: "{}"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if !approved {
		t.Error("approve callback not invoked in ask mode")
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("approved call was stripped")
	}
	if len(step.SynthesizedResults) != 0 {
		t.Error("approved call produced a synthesized denial result")
	}
}

func TestApprovalMiddleware_AskModeUserDenial(t *testing.T) {
	m := &ApprovalMiddleware{
		mode:     "ask",
		classify: func(name, args string) bool { return true },
		approve:  func(name, args string) bool { return false },
		emitDeny: func(name, args, errMsg string) {},
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.FunctionCall{Name: "bash", Arguments: "{}"}},
	}}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Error("denied call not stripped")
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatal("denied call produced no synthesized result")
	}
	if !contains(step.SynthesizedResults[0].Content, `was denied by user`) {
		t.Errorf("wrong denial message: %q", step.SynthesizedResults[0].Content)
	}
}

func TestApprovalMiddleware_AllowModeIsNoop(t *testing.T) {
	called := false
	m := &ApprovalMiddleware{
		mode:     "allow",
		classify: func(name, args string) bool { called = true; return true },
	}
	msg := &types.Message{ToolCalls: []types.ToolCall{{ID: "1"}}}
	if _, err := m.PostModel(context.Background(), msg, &Step{}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("classify invoked despite allow mode")
	}
	if len(msg.ToolCalls) != 1 {
		t.Error("allow mode stripped a call")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
```

(Use `strings.Contains` instead of the local helper if preferred — simpler.)

Run: `go test ./internal/agent/pipeline/ -run TestApprovalMiddleware -v`
Expected: FAIL — fields/methods don't exist.

**Step 2: Implement approval.go**

Full replacement of `internal/agent/pipeline/approval.go`:

```go
package pipeline

import (
	"context"

	"github.com/buchenberg/yaah/internal/types"
)

// ApprovalMiddleware enforces the tool approval policy (mode "deny" /
// "ask") before dispatch. Classification, user prompting, and hook
// emission are injected as functions by the composition site (Loop.
// toPipelineConfig), keeping pipeline→agent dependency direction clean.
//
// Dangerous calls are stripped from msg.ToolCalls and replaced by
// synthesized error tool-results on step.SynthesizedResults, preserving
// the OpenAI invariant that every tool_call_id receives a result.
type ApprovalMiddleware struct {
	mode string
	// classify reports whether the named call requires gating.
	classify func(name, args string) bool
	// approve asks the user (or ApproveFn); only consulted in ask mode.
	approve func(name, args string) bool
	// emitDeny fires the ToolStart/ToolEnd hook pair for a denied call.
	emitDeny func(name, args, errMsg string)
}

func (m *ApprovalMiddleware) Name() string { return "approval" }

func (m *ApprovalMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *ApprovalMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.mode != "deny" && m.mode != "ask" {
		return step, nil
	}
	if len(msg.ToolCalls) == 0 {
		return step, nil
	}

	filtered := make([]types.ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		name, args := tc.Function.Name, tc.Function.Arguments
		if !m.classify(name, args) {
			filtered = append(filtered, tc)
			continue
		}
		var errMsg string
		switch m.mode {
		case "deny":
			errMsg = "error: tool " + quote(name) + " requires approval but approval mode is 'deny'"
		case "ask":
			if m.approve(name, args) {
				filtered = append(filtered, tc)
				continue
			}
			errMsg = "error: tool " + quote(name) + " was denied by user"
		}
		m.emitDeny(name, args, errMsg)
		step.SynthesizedResults = append(step.SynthesizedResults,
			types.ToolResultMsg(tc.ID, name, errMsg))
	}
	msg.ToolCalls = filtered
	return step, nil
}

func (m *ApprovalMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
```

Use `fmt.Sprintf` with the exact original format strings rather than the `quote` helper:

```go
errMsg = fmt.Sprintf("error: tool %q requires approval but approval mode is 'deny'", name)
...
errMsg = fmt.Sprintf("error: tool %q was denied by user", name)
```

**Step 3: Extend PipelineConfig and builder**

In `config.go`, add to `PipelineConfig`:

```go
	// Approval callbacks injected by the composition site. classify and
	// approve gate dangerous tool calls in deny/ask modes; emitDeny fires
	// hook events for stripped calls. When classify is nil the approval
	// middleware is inert regardless of mode.
	ApprovalClassify func(name, args string) bool
	ApprovalApprove  func(name, args string) bool
	ApprovalEmitDeny func(name, args, errMsg string)
```

Update the builder:

```go
	"approval": func(cfg PipelineConfig) Middleware {
		return &ApprovalMiddleware{
			mode:     cfg.ApprovalMode,
			classify: cfg.ApprovalClassify,
			approve:  cfg.ApprovalApprove,
			emitDeny: cfg.ApprovalEmitDeny,
		}
	},
```

Guard in `PostModel`: also early-return when `m.classify == nil`:

```go
	if m.mode != "deny" && m.mode != "ask" || m.classify == nil {
		return step, nil
	}
```

**Step 4: Run pipeline tests**

Run: `go test ./internal/agent/pipeline/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/agent/pipeline/approval.go internal/agent/pipeline/approval_test.go internal/agent/pipeline/config.go
git commit -m "feat(pipeline): implement ApprovalMiddleware with injected classify/approve/emit hooks"
```

### Task 3.3: Wire the Loop's callbacks into PipelineConfig

**Objective:** Composition site supplies real implementations; sub-agents stay unaffected.

**Files:**
- Modify: `internal/agent/loop.go` `toPipelineConfig()` (around line 66)
- Test: `internal/agent/buildpipeline_test.go`

**Step 1: Write failing test**

Append to `buildpipeline_test.go`:

```go
func TestToPipelineConfig_ApprovalHooksWired(t *testing.T) {
	l := &Loop{CtxMgr: &ContextManager{}, Config: LoopConfig{ApprovalMode: "ask"}}
	l.Hooks = events.NewHookEmitter("", "")
	cfg := l.toPipelineConfig()
	if cfg.ApprovalClassify == nil || cfg.ApprovalApprove == nil || cfg.ApprovalEmitDeny == nil {
		t.Fatal("approval callbacks not wired into PipelineConfig")
	}
	if !cfg.ApprovalClassify("bash", "{}") {
		t.Error("classify should flag bash (implements DangerClassifier)")
	}
}
```

(Import `"github.com/buchenberg/yaah/internal/agent/events"`.)

Run: `go test ./internal/agent/ -run TestToPipelineConfig_ApprovalHooksWired -v`
Expected: FAIL — fields don't exist on PipelineConfig usage site yet (they do exist after Task 3.2; this fails because toPipelineConfig doesn't set them).

**Step 2: Implement**

In `toPipelineConfig()`:

```go
		ApprovalMode:           l.Config.ApprovalMode,
		ApprovalClassify:       l.classifyDanger,
		ApprovalApprove:        func(name, args string) bool { return l.approveTool(name, abbreviateArgs(args, 120)) },
		ApprovalEmitDeny: func(name, args, errMsg string) {
			l.Hooks.Emit(HookEvent{Event: events.ToolStart, ToolName: name, ToolArgs: args})
			l.Hooks.Emit(HookEvent{Event: events.ToolEnd, ToolName: name, ToolArgs: args, ToolError: errMsg, ToolResult: errMsg})
		},
```

Nil-safety: if `l.Hooks` may be nil at config time, wrap: `if l.Hooks != nil { l.Hooks.Emit(...) }` — `applyDefaults` guarantees non-nil before `buildPipeline`, but defensive check costs nothing.

**Step 3: Run tests**

Run: `go test ./internal/agent/ -run 'TestToPipelineConfig|TestBuildPipeline' -v`
Expected: PASS.

**Step 4: Commit**

```bash
git add internal/agent/loop.go internal/agent/buildpipeline_test.go
git commit -m "refactor(agent): wire approval callbacks from Loop into pipeline config"
```

### Task 3.4: Delete inline approval gating from executeAndCollect

**Objective:** Remove the duplicated logic; the middleware now owns it.

**Files:**
- Modify: `internal/agent/agent_tools.go:41-56`
- Modify: `internal/agent/agent_tools.go:19-30` (remove `ToolDeniedError`)
- Test: `internal/agent/agent_safety_test.go` + new integration test

**Step 1: Write the integration test FIRST (red)**

Add to `internal/agent/agent_tools_test.go` (create if absent) — verify end-to-end that a denied call yields a persisted synthetic result and the remaining call executes. Model the test on existing loop tests in `agent_test.go` (fake Provider returning scripted messages):

```go
func TestExecuteAndCollect_ApprovalMovedToMiddleware(t *testing.T) {
	t.Run("deny mode synthesizes result without executing", func(t *testing.T) {
		l := &Loop{
			CtxMgr:   &ContextManager{},
			Hooks:    events.NewHookEmitter("", ""),
			Persister: NewSessionPersister(nil, nil, ""),
			Config: LoopConfig{ApprovalMode: "deny"},
		}
		l.applyDefaults()
		pipe := l.buildPipeline()

		msg := types.AssistantMsg("", []types.ToolCall{
			{ID: "1", Function: types.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
			{ID: "2", Function: types.FunctionCall{Name: "read", Arguments: `{"filePath":"/tmp/x"}`}},
		})
		step := &pipeline.Step{}
		if _, err := pipe.RunPostModel(context.Background(), &msg, step); err != nil {
			t.Fatal(err)
		}
		if len(msg.ToolCalls) != 1 {
			t.Fatalf("expected bash stripped, got %d calls", len(msg.ToolCalls))
		}
		if len(step.SynthesizedResults) != 1 {
			t.Fatalf("expected 1 synthesized result, got %d", len(step.SynthesizedResults))
		}
	})
}
```

Adjust constructor signatures to match actual fakes in `agent_test.go` — read that file first and reuse its helpers.

Run: `go test ./internal/agent/ -run TestExecuteAndCollect_ApprovalMovedToMiddleware -v`
Expected: PASS already after Task 3.3 (middleware active, inline gating redundant but harmless — it would deny again but the call list is already filtered). This test locks the contract before deletion.

**Step 2: Delete the inline block**

Remove `internal/agent/agent_tools.go:41-56` (both `if l.Config.ApprovalMode == ...` blocks).

Remove `ToolDeniedError` type (lines 19-30) — grep confirms no consumers outside this file:
`rg -n "ToolDeniedError" --type go` → expect only agent_tools.go hits after deletion.

Also remove the now-unneeded `"fmt"` import ONLY if unused elsewhere in the file (it is used elsewhere — quality gates — keep it).

**Step 3: Update stale comments**

- `internal/agent/subagent_loop.go:68` comment mentions "skip ... approval dialogs" — still accurate (sub-agent pipeline excludes approval).
- `internal/agent/types.go:99` `ApproveFn` doc comment — still accurate (delegated via `ApprovalApprove` closure).

**Step 4: Full test suite + vet + staticcheck**

```bash
go build . && go test ./... && go vet ./... && gofmt -l .
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

Expected: all clean.

**Step 5: Commit**

```bash
git add internal/agent/agent_tools.go internal/agent/
git commit -m "refactor(agent): move approval gating from executeAndCollect into ApprovalMiddleware"
```

### Task 3.5: Manual smoke test (dev-loop skill)

Load `.agents/skills/yaah-testing/SKILL.md`. Then:

1. `go build -trimpath -ldflags '-s -w' -o yaah .`
2. Run a REPL session with `YAAH_APPROVAL=ask`-equivalent config (check `docs/configuration.md` for the actual approval-mode key) and issue a prompt that triggers `bash`.
3. Confirm the approval prompt appears once per dangerous call, denial produces an `error: tool "bash" was denied by user` tool message in the transcript, and traces (`yaah doctor` / SigNoz) show no orphaned tool spans for denied calls.

---

## Phase 4 — InlineLimitMiddleware (MaxInlineToolsPerTurn)

### Task 4.1: Extract truncation into middleware using SynthesizedResults

**Objective:** Move turn.go:129-151 into an `"inline_limit"` PostModel middleware reusing `Step.SynthesizedResults`.

**Files:**
- Create: `internal/agent/pipeline/inlinelimit.go`
- Modify: `internal/agent/pipeline/config.go` (builder + `MaxInlineToolsPerTurn` field + add to `defaultPipelineNames` after `"approval"`)
- Modify: `internal/agent/loop.go` `toPipelineConfig` (pass `l.Config.MaxInlineToolsPerTurn`)
- Modify: `internal/agent/turn.go:127-151` (delete inline block)
- Tests: `internal/agent/pipeline/inlinelimit_test.go`, update `internal/agent/buildpipeline_test.go` expected-names lists

**Step 1: Failing tests**

```go
func TestInlineLimitMiddleware_TruncatesAndSynthesizes(t *testing.T) {
	m := NewInlineLimitMiddleware(1)
	msg := &types.Message{ToolCalls: []types.ToolCall{
		{ID: "1", Function: types.FunctionCall{Name: "read"}},
		{ID: "2", Function: types.FunctionCall{Name: "read"}},
	}}
	step := &Step{Messages: []types.Message{}}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("calls = %d, want 1", len(msg.ToolCalls))
	}
	if len(step.SynthesizedResults) != 1 {
		t.Fatalf("synthesized = %d, want 1", len(step.SynthesizedResults))
	}
	if !strings.Contains(fmt.Sprint(step.SynthesizedResults[0].Content), "[dropped: this call exceeded the inline tool limit") {
		t.Errorf("unexpected drop message: %q", step.SynthesizedResults[0].Content)
	}
}

func TestInlineLimitMiddleware_ZeroIsUnlimited(t *testing.T) {
	m := NewInlineLimitMiddleware(0)
	msg := &types.Message{ToolCalls: make([]types.ToolCall, 5)}
	step := &Step{}
	if _, err := m.PostModel(context.Background(), msg, step); err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 5 || len(step.SynthesizedResults) != 0 {
		t.Error("zero limit should not truncate")
	}
}
```

**Step 2: Implement `inlinelimit.go`**

```go
package pipeline

import (
	"context"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

// InlineLimitMiddleware caps tool calls dispatched per turn. Dropped
// calls receive synthesized results so every tool_call_id is answered.
type InlineLimitMiddleware struct{ max int }

func NewInlineLimitMiddleware(max int) *InlineLimitMiddleware { return &InlineLimitMiddleware{max: max} }

func (m *InlineLimitMiddleware) Name() string { return "inline_limit" }

func (m *InlineLimitMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	return step, nil
}

func (m *InlineLimitMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if m.max <= 0 || len(msg.ToolCalls) <= m.max {
		return step, nil
	}
	dropped := msg.ToolCalls[m.max:]
	msg.ToolCalls = msg.ToolCalls[:m.max]
	for _, tc := range dropped {
		step.SynthesizedResults = append(step.SynthesizedResults, types.ToolResultMsg(
			tc.ID, tc.Function.Name,
			fmt.Sprintf(
				"[dropped: this call exceeded the inline tool limit (%d per turn) and was not executed. "+
					"Break large batches into smaller turns or use the delegate tool for batch work.]",
				m.max,
			),
		))
	}
	return step, nil
}

func (m *InlineLimitMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	return step, nil
}
```

Builder entry `"inline_limit"` + `MaxInlineToolsPerTurn int` in `PipelineConfig` + name added to `defaultPipelineNames` **after** `"approval"` (so approval strips denials first, then the limit truncates the survivors — matching current semantics where approval ran inside dispatch before truncation could matter less; note ordering nuance in the map comment).

**Step 3: Delete the inline block** at turn.go:129-151 (keep the OTel `dispatch.inline` event at 153-162).

**Step 4:** Update both name-list assertions in `buildpipeline_test.go` (lines 28, 43) and `TestPipeline_*` lists in `config_test.go:39,220` to include `"inline_limit"`. Run full suite:

```bash
go test ./internal/agent/... ./internal/agent/pipeline/... && go vet ./... && gofmt -l .
```

**Step 5: Commit**

```bash
git add internal/agent/pipeline/inlinelimit.go internal/agent/pipeline/inlinelimit_test.go \
        internal/agent/pipeline/config.go internal/agent/turn.go internal/agent/loop.go \
        internal/agent/buildpipeline_test.go internal/agent/pipeline/config_test.go
git commit -m "refactor(pipeline): extract MaxInlineToolsPerTurn truncation into inline_limit middleware"
```

---

## Phase 5 — ConflictDetectionMiddleware (PostTool)

### Task 5.1: Extract conflict-detection block

**Objective:** Move turn.go:167-194 into a PostTool middleware. Safe because PostTool appends to `step.Messages` AFTER the clobber point (verified constraint above).

**Files:**
- Create: `internal/agent/pipeline/conflictdetect.go`
- Modify: `internal/agent/pipeline/config.go` (+`ConflictTracker *tools.ConflictTracker` field, `"conflict_detect"` builder, appended to `defaultPipelineNames` after `"staleness"`)
- Modify: `internal/agent/loop.go` `toPipelineConfig` (`ConflictTracker: l.ConflictTracker`)
- Modify: `internal/agent/turn.go:167-194` (delete block)
- Tests: `internal/agent/pipeline/conflictdetect_test.go`, update name-list tests

**Design detail:** The deleted block touches four things: hook emission (`l.Hooks.Emit` with `ConflictCheck`/`ConflictDetect`), OTel span attributes, message append + persist, and `l.State.Messages` sync. Inject as callbacks on the middleware:

```go
type ConflictDetectMiddleware struct {
	tracker interface{ DetectAndReset() string }
	onCheck func(turn int)
	onFound func(turn int, report string, fileCount int) // emits hooks, span attrs
	persist func(msg types.Message)                      // Persister.Persist
	state   *[]types.Message                             // points at l.State.Messages
}
```

`PostTool` body mirrors the extracted logic verbatim (same `strings.Count(report, "File: ")` heuristic, same `types.UserMsg(report)`).

**Steps:** TDD cycle as in prior tasks — write `conflictdetect_test.go` covering (a) nil tracker no-op, (b) empty report no-op, (c) report path emits onFound + appends UserMsg; then implement, delete the inline block, wire config, update name-list tests, run full gates, commit:

```bash
git commit -m "refactor(pipeline): extract conflict detection into conflict_detect middleware"
```

**Caveat:** `onCheck` currently fires even when tracker exists but report empty — preserve that (hook emitted unconditionally at turn.go:168 before `DetectAndReset`).

---

## Phase 6 (DEFERRED) — TurnBudget/wrap-up extraction

**Decision: do NOT implement now.** `buildTurnRequest`'s max-turns stripping and wrap-up injection (turn.go:44-62) mutate `types.ChatRequest`, which the `Middleware` interface cannot see (it only sees `Step`, messages, and tool defs). Extraction would require extending the interface or threading request-mutator hooks through `Step` — interface churn with low payoff while the logic sits in one well-tested place. Revisit only if a third request-shaping concern appears. File a bead (`bd create`) titled "consider RequestMutator middleware hook for turn-budget logic" referencing this section.

---

## Final phase — Cleanup and docs

### Task 7.1: Docs and memory updates

**Files:**
- Modify: `docs/features.md` — middleware table: mark `approval` as enforcing (not stub), add `inline_limit` and `conflict_detect` rows, note the single-instance `tool_concurrency`.
- Modify: `AGENTS.md` engine-view section if it names middleware (grep first: `rg -n "approval" docs/ AGENTS.md`).
- Save project memory: decision record "approval_gating_moved_to_middleware :: Approval deny/ask gating moved from Loop.executeAndCollect into ApprovalMiddleware.PostModel; denied calls surface as Step.SynthesizedResults synthesized tool messages; ToolDeniedError deleted; denied calls no longer appear in PostTool ToolResult slices."

### Task 7.2: Full verification gate

```bash
gofmt -w . && go build . && go test ./... && go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o dist/yaah-darwin-arm64 .
```

Then dev-loop smoke test per Task 3.5, plus one batched-tool-call scenario exercising `inline_limit` (>N parallel tool calls in one assistant turn).

---

## Verification checklist

- [ ] `builtinBuilders` and `subAgentBuilders` share `buildPermission`/`buildToolConcurrency`
- [ ] Exactly one `ToolConcurrencyMiddleware` allocation per loop (test asserts pointer identity)
- [ ] `ApprovalMiddleware` denies in deny-mode, prompts in ask-mode, no-ops otherwise
- [ ] Denial strings byte-identical to originals
- [ ] Every stripped `tool_call_id` gets a persisted synthesized result
- [ ] `ToolDeniedError` gone; `rg ToolDeniedError` returns nothing
- [ ] Sub-agent pipeline unchanged: `tool_concurrency`, `shepherd_trace`, conditional `permission` — no `approval`
- [ ] All gates green: `go test ./... && go vet ./... && staticcheck && gofmt -l .` empty
