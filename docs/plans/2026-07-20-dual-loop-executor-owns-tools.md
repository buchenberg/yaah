# Dual-Loop: Executor Owns Tools — Implementation Plan

> **Goal:** Make the dual-loop architecturally sound by giving each decision exactly one owner: the **executor owns tool selection**, the **planner emits an intent-level directive** via a single `delegate(task)` tool. Eliminate the duplicated tool-selection responsibility, the unconditional same-model overhead, the full-system-prompt bloat, and the "user is asking me to…" intent loss.

**Architecture:** When a dedicated executor is configured, the planner's tool set is reduced to `{delegate}` — it *cannot* call file/bash tools, only hand a directive to the executor. The executor receives the directive **plus the original user intent**, runs on its own (optionally cheaper) model with a **purpose-built minimal system prompt**, chains tools as needed, and returns a terse summary. The summary is injected back as the honest `tool` result of the planner's `delegate` call. When no executor is configured, yaah runs the existing single-loop path with the full tool set (unchanged).

**Root cause addressed:** today the outer loop emits concrete tool calls AND the inner loop re-derives the same calls (`agent.go:560-562` + `innerLoop` first iteration at `agent.go:776-796`). One decision, two owners.

---

## Contract changes (encode in tests first)

| Aspect | Old (buggy) | New (sound) |
|---|---|---|
| Planner tool set | all tools | `{delegate}` when executor configured; all tools otherwise |
| Planner output | concrete tool calls | `delegate(task)` directive |
| Executor context | `SystemPrompt` + `"Execute the following tool calls: …"` | minimal executor prompt + directive + **original user intent** |
| Tool-selection owner | both loops | executor only |
| Dual-loop trigger | always (unless `DisableInnerLoop`) | only when `ExecutorProvider` configured |
| Result injection | `tool` msg (hack — planner had called real tools) | `tool` msg (honest — planner called `delegate`) |
| `DisableInnerLoop` flag | test-only, 27 usages | **removed** — gate is provider-presence; single-loop path (`executeAndCollect`) retained |

---

## Task 1: Executor system prompt + delegate ToolDef + activation gate

**Objective:** Add the building blocks (no behavior change yet): the executor's minimal system prompt, the `delegate` tool definition, and the `dualLoopActive()` gate.

**Files:** `internal/agent/agent.go` (add near `maxInnerSummaryLen`, `~line 85` and near `buildToolDefs`, `~line 1768`); `internal/agent/agent_test.go` (new tests).

**Step 1 — write failing tests** in `agent_test.go`:

```go
func TestDualLoopInactiveByDefault(t *testing.T) {
	loop := &Loop{Provider: &fakeProvider{}, Registry: tools.NewRegistry()}
	if loop.dualLoopActive() {
		t.Fatalf("dual-loop must be inactive when ExecutorProvider is nil")
	}
}

func TestDualLoopActiveWhenExecutorConfigured(t *testing.T) {
	loop := &Loop{Provider: &fakeProvider{}, ExecutorProvider: &fakeProvider{},
		Registry: tools.NewRegistry()}
	if !loop.dualLoopActive() {
		t.Fatalf("dual-loop must be active when ExecutorProvider is set")
	}
}

func TestPlannerToolSet_DelegateOnlyWhenActive(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "bash", result: "ok"})
	reg.Register(&fakeTool{name: "read", result: "ok"})

	// Inactive → planner sees the full tool set.
	inactive := &Loop{Registry: reg}
	got := inactive.buildPlannerToolDefs()
	if names := toolDefNames(got); !sameSet(names, []string{"bash", "read"}) {
		t.Fatalf("inactive planner tools = %v, want [bash read]", names)
	}

	// Active → planner sees only delegate.
	active := &Loop{Registry: reg, ExecutorProvider: &fakeProvider{}}
	got = active.buildPlannerToolDefs()
	if names := toolDefNames(got); len(names) != 1 || names[0] != "delegate" {
		t.Fatalf("active planner tools = %v, want [delegate]", names)
	}
}
```

(Add helpers `toolDefNames`, `sameSet` if not present.)

**Step 2 — run, verify failure:** `go test ./internal/agent/ -run 'DualLoop|PlannerToolSet'` → FAIL (symbols undefined).

**Step 3 — implement** in `agent.go`:

```go
// executorSystemPrompt is the purpose-built prompt for the inner executor.
// It is deliberately NOT the planner's identity prompt: the executor does
// tactical tool selection, not user-facing reasoning, so it gets only what
// its responsibility requires.
const executorSystemPrompt = `You are a tool executor. You receive a task directive and the user's original request. Select and run the built-in tools needed to accomplish the directive; you may chain tools based on their results. When finished, respond with a terse structured summary: one line per tool executed naming the tool and its outcome (e.g. "write(path): wrote 138B", "bash(cmd): exit 0"). Do not write conversational prose, confirmations, or next-step plans.`

// delegateToolName is the single tool the planner may call when the
// dual-loop is active. It hands an intent-level directive to the executor.
const delegateToolName = "delegate"

// dualLoopActive reports whether the executor-owns-tools dual-loop should
// run. It is gated purely on an explicitly-configured executor provider so
// that the default (no executor) — and all subagents, which never set an
// ExecutorProvider — stay on the single-loop path. A subagent is already
// an isolated executor; nesting another executor inside it is redundant.
//
// This gate replaces the old DisableInnerLoop flag, which was set only by
// tests and never by production code (subagent_runner.go builds subagent
// Loops with neither ExecutorProvider nor DisableInnerLoop). Under this
// gate, tests that want single-loop simply omit ExecutorProvider.
func (l *Loop) dualLoopActive() bool {
	return l.ExecutorProvider != nil
}

// delegateToolDef returns the planner's sole tool when dual-loop is active.
// Its description tells the model to use it for ANY tool work, which is how
// the "one decision, one owner" boundary is enforced by schema rather than
// convention.
func delegateToolDef() types.ToolDef {
	return types.ToolDef{
		Type: "function",
		Function: types.ToolFn{
			Name:        delegateToolName,
			Description: "Delegate a tool-execution task to the executor. Use this for ANY task that requires running tools (read, write, bash, grep, etc.). Provide a concise intent-level directive describing what to accomplish, not which specific tools to call — the executor selects the tools.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task": {"type": "string", "description": "Intent-level directive: what to accomplish."}
				},
				"required": ["task"]
			}`),
		},
	}
}

// buildPlannerToolDefs returns the tool set exposed to the planner. When
// the dual-loop is active the planner gets only {delegate} (it cannot pick
// file/bash tools — the executor owns tool selection). Otherwise it gets
// the full set (single-loop path).
func (l *Loop) buildPlannerToolDefs() []types.ToolDef {
	if l.dualLoopActive() {
		return []types.ToolDef{delegateToolDef()}
	}
	return l.buildToolDefs()
}

// buildExecutorToolDefs returns the full tool set the executor may use.
func (l *Loop) buildExecutorToolDefs() []types.ToolDef { return l.buildToolDefs() }
```

**Step 4 — run, verify pass:** `go test ./internal/agent/ -run 'DualLoop|PlannerToolSet'` → PASS.

**Step 5 — commit:** `feat(agent): add executor prompt, delegate tool, dual-loop activation gate`

---

## Task 2: Rewrite the executor loop — directive + intent + minimal prompt

**Objective:** Replace `innerLoop(ctx, taskPrompt, tools)` with `runExecutor(ctx, directive, originalIntent)`. The executor no longer takes pre-formed tool calls (it owns selection) and no longer reuses the planner's system prompt.

**Files:** `internal/agent/agent.go` (`innerLoop` at `~line 732`); `internal/agent/agent_test.go`.

**Step 1 — write failing test** asserting the executor sees original intent and the executor prompt (not the planner's):

```go
func TestExecutorReceivesOriginalIntent(t *testing.T) {
	var seen []types.Message
	inner := &recordingProvider{record: &seen,
		responses: []*types.ChatResponse{
			{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "read(f): 10B"}, FinishReason: "stop"}}},
		}}
	outer := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant",
			ToolCalls: []types.ToolCall{{ID: "d1", Type: "function",
				Function: types.ToolCallFn{Name: delegateToolName,
					Arguments: `{"task":"read the file and report its size"}`}}},
			FinishReason: "tool_calls"}}},
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "Done."}, FinishReason: "stop"}}},
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "read", result: "10B"})
	loop := &Loop{Provider: outer, ExecutorProvider: inner, Registry: reg,
		SystemPrompt: "PLANNER-IDENTITY", MaxIterations: 5, MaxInnerIterations: 5}

	if _, err := loop.Run(context.Background(), "tell me about f"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// First executor message is its OWN system prompt, not the planner identity.
	if len(seen) == 0 || seen[0].Role != "system" {
		t.Fatalf("executor saw no system message")
	}
	if seen[0].Content == "PLANNER-IDENTITY" {
		t.Fatalf("executor reused planner identity prompt — must use executorSystemPrompt")
	}
	// The user-message payload to the executor must contain the ORIGINAL intent.
	var payload string
	for _, m := range seen {
		if m.Role == "user" { payload = m.Content; break }
	}
	if !strings.Contains(payload, "tell me about f") {
		t.Fatalf("executor payload missing original intent: %q", payload)
	}
}
```

(`recordingProvider` — add a small fake that captures requests; reuse `fakeProvider` pattern.)

**Step 2 — run, verify failure:** `go test ./internal/agent/ -run ExecutorReceivesOriginalIntent` → FAIL.

**Step 3 — implement:** replace `innerLoop` with:

```go
// runExecutor runs the tool-execution loop that owns tool selection for a
// single delegated directive. It uses the dedicated executor provider/model
// and a purpose-built system prompt (NOT the planner identity), and it
// receives the original user intent so its reasoning reflects the real
// request rather than a reframed subtask. Returns the executor's final
// summary and whether it exhausted its iteration budget.
func (l *Loop) runExecutor(ctx context.Context, directive, originalIntent string) (string, bool, error) {
	var span trace.Span
	if l.OtelEnabled {
		ctx, span = observability.StartInnerLoop(ctx, directive)
		defer span.End()
	}

	provider := l.ExecutorProvider
	model := l.ExecutorModel
	if model == "" { model = l.Model }
	if span != nil {
		span.SetAttributes(
			attribute.String("inner.model", model),
			attribute.Bool("inner.dedicated_provider", provider != nil && provider != l.Provider),
		)
		if l.Model != "" { span.SetAttributes(attribute.String("outer.model", l.Model)) }
	}

	// Directive + original intent. This is the fix for the "user is asking
	// me to…" mischaracterization: the executor finally sees the real request.
	payload := directive
	if originalIntent != "" {
		payload += "\n\n## Original user request\n" + originalIntent
	}
	messages := []types.Message{
		types.SystemMsg(executorSystemPrompt),
		types.UserMsg(payload),
	}
	if l.OtelVerbose && span != nil {
		observability.RecordInnerTask(span, payload)
		observability.RecordConversation(span, messages)
	}
	tools := l.buildExecutorToolDefs()

	for iter := 0; iter < l.MaxInnerIterations; iter++ {
		req := types.ChatRequest{Model: model, Messages: messages, Tools: tools}
		msg, err := l.getExecutorMessage(ctx, provider, req)
		if err != nil {
			if span != nil { observability.FinishInnerLoop(span, iter+1, false, err) }
			return "", false, fmt.Errorf("executor: %w", err)
		}
		messages = append(messages, msg)
		if l.OtelVerbose && span != nil {
			observability.RecordAssistantResponse(span, msg, "")
		}
		if len(msg.ToolCalls) == 0 {
			if span != nil { observability.FinishInnerLoop(span, iter+1, false, nil) }
			return msg.Content, false, nil
		}
		for _, tc := range msg.ToolCalls {
			abbr := abbreviateArgs(tc.Function.Arguments, 60)
			if l.OnTool != nil { l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbr}) }
			result := l.executeOneTool(ctx, tc)
			if l.OnTool != nil {
				errMsg := ""
				if result.err != nil { errMsg = result.err.Error() }
				l.OnTool(ToolInfo{Name: tc.Function.Name, Args: abbr, Duration: result.dur, Result: result.content, Error: errMsg})
			}
			messages = append(messages, types.Message{Role: "tool", Content: result.content, ToolCallID: tc.ID, Name: tc.Function.Name})
			if result.err != nil {
				if span != nil { observability.FinishInnerLoop(span, iter+1, false, result.err) }
				return "", false, fmt.Errorf("executor tool %q: %w", tc.Function.Name, result.err)
			}
		}
	}
	if span != nil { observability.FinishInnerLoop(span, l.MaxInnerIterations, true, nil) }
	return "", true, fmt.Errorf("executor exhausted after %d iterations", l.MaxInnerIterations)
}
```

Add `getExecutorMessage` (mirrors `getAssistantMessage` but non-streaming, or streaming if `ExecutorProvider` is a `StreamProvider` — reuse `runStream`):

```go
func (l *Loop) getExecutorMessage(ctx context.Context, p Provider, req types.ChatRequest) (types.Message, error) {
	if sp, ok := p.(StreamProvider); ok {
		return l.runStream(ctx, sp, req), nil
	}
	resp, err := p.Send(ctx, req)
	if err != nil { return types.Message{}, err }
	if len(resp.Choices) == 0 { return types.Message{}, fmt.Errorf("executor: no choices") }
	return resp.Choices[0].Message, nil
}
```

**Step 4 — run, verify pass:** `go test ./internal/agent/ -run ExecutorReceivesOriginalIntent` → PASS.

**Step 5 — commit:** `feat(agent): executor owns tool selection with intent + minimal prompt`

---

## Task 3: Route planner `delegate` calls to the executor in Run()

**Objective:** In `Run()`, expose `buildPlannerToolDefs()` to the model, and when the planner calls `delegate`, parse the directive, capture original intent (most recent user message), run the executor, and inject the summary as the honest `tool` result for that call. Replace the old `formatToolCallsForExecutor` + unconditional `innerLoop` block.

**Files:** `internal/agent/agent.go` (`Run`, `~line 475-618`); `internal/agent/agent_test.go`.

**Step 1 — write failing test** (end-to-end delegate contract):

```go
func TestDualLoop_DelegateRoutesToExecutor(t *testing.T) {
	// Planner: turn 0 delegates; turn 1 final answer.
	outer := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant",
			ToolCalls: []types.ToolCall{{ID: "d1", Type: "function",
				Function: types.ToolCallFn{Name: "delegate",
					Arguments: `{"task":"list go files in internal/agent"}`}}},
			FinishReason: "tool_calls"}}},
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "Found 3 files."}, FinishReason: "stop"}}},
	}}
	inner := &fakeProvider{responses: []*types.ChatResponse{
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant",
			ToolCalls: []types.ToolCall{{ID: "i1", Type: "function",
				Function: types.ToolCallFn{Name: "glob", Arguments: `{"pattern":"internal/agent/*.go"}`}}}},
			FinishReason: "tool_calls"}}},
		{Choices: []types.Choice{{Message: types.Message{Role: "assistant", Content: "glob: 3 matches"}, FinishReason: "stop"}}},
	}}
	reg := tools.NewRegistry()
	reg.Register(&fakeTool{name: "glob", result: "a.go\nb.go\nc.go"})
	loop := &Loop{Provider: outer, ExecutorProvider: inner, Registry: reg,
		MaxIterations: 5, MaxInnerIterations: 5}

	resp, err := loop.Run(context.Background(), "how many go files in internal/agent")
	if err != nil { t.Fatalf("Run: %v", err) }
	if resp != "Found 3 files." { t.Fatalf("resp = %q", resp) }

	// The delegate call must have a tool-result message carrying the summary.
	var found bool
	for i := range loop.Messages {
		m := &loop.Messages[i]
		if m.Role == "tool" && m.ToolCallID == "d1" && strings.Contains(m.Content, "glob") {
			found = true
		}
	}
	if !found { t.Fatalf("delegate tool result not injected for call d1") }
}
```

**Step 2 — run, verify failure.**

**Step 3 — implement.** In `Run()`:

- At request build (`~line 490`): `Tools: l.buildPlannerToolDefs()` instead of `step.Tools`. (Keep `step.Tools` for middleware that may still reference it; set both.)
- Replace the dual-loop block (`agent.go:560-618`) with:

```go
if l.dualLoopActive() && hasDelegateCall(msg.ToolCalls) {
	originalIntent := l.lastUserMessage(messages)
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name != delegateToolName {
			continue // planner may only delegate; ignore anything else defensively
		}
		directive := parseDelegateTask(tc.Function.Arguments)
		summary, exhausted, innerErr := l.runExecutor(turnCtx, directive, originalIntent)
		switch {
		case innerErr != nil:
			summary = fmt.Sprintf("executor error: %v", innerErr)
		case exhausted:
			if summary == "" { summary = "executor iteration budget exhausted" } else { summary += "\n(executor iteration budget exhausted)" }
		}
		if len(summary) > maxInnerSummaryLen { summary = truncateRunes(summary, maxInnerSummaryLen) }
		tr := types.Message{Role: "tool", Content: summary, ToolCallID: tc.ID, Name: delegateToolName}
		messages = append(messages, tr)
		l.persistMessage(tr)
		if l.OtelVerbose && turnSpan != nil { observability.RecordInnerSummary(turnSpan, summary, len(messages)) }
	}
	if turnSpan != nil { turnSpan.End() }
	continue
}

// single-loop path (unchanged): direct parallel execution
toolResults := l.executeAndCollect(turnCtx, msg.ToolCalls, &messages)
// … existing code …
```

Add helpers:

```go
func hasDelegateCall(calls []types.ToolCall) bool {
	for _, c := range calls { if c.Function.Name == delegateToolName { return true } }
	return false
}
func parseDelegateTask(args string) string {
	var v struct{ Task string `json:"task"` }
	if err := json.Unmarshal([]byte(args), &v); err != nil || v.Task == "" {
		return strings.TrimSpace(args) // fall back to raw args
	}
	return v.Task
}
func (l *Loop) lastUserMessage(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" { return msgs[i].Content }
	}
	return ""
}
```

**Step 4 — run, verify pass.**

**Step 5 — commit:** `feat(agent): route planner delegate calls to executor with original intent`

---

## Task 4: Update the two legacy dual-loop tests to the new contract

**Objective:** The old tests encode the buggy "planner emits tools, executor re-runs them" contract. Move them to the delegate contract; delete the now-dead `formatToolCallsForExecutor`.

**Files:** `internal/agent/agent_test.go` (`TestFormatToolCallsForExecutor_noInstructionsRefeed` `~1568`, `TestDualLoop_summaryInjectedAsToolMessage` `~1604`); `internal/agent/agent.go` (delete `formatToolCallsForExecutor` `~691`).

**Steps:**
1. Delete `TestFormatToolCallsForExecutor_noInstructionsRefeed` (function it tests is being removed).
2. Rewrite `TestDualLoop_summaryInjectedAsToolMessage`: planner emits a `delegate` call (not `bash`); executor runs `bash` internally; assert the delegate call_id's tool message carries the executor summary and there is no standalone assistant summary. Use `delegateToolName` constant.
3. Delete `formatToolCallsForExecutor` from `agent.go` (now unreferenced).
4. Run: `go test ./internal/agent/` → PASS (full package).
5. Commit: `refactor(agent): move dual-loop tests to delegate contract; drop formatToolCallsForExecutor`

---

## Task 5: Remove the `DisableInnerLoop` flag and its 27 test usages

**Objective:** The flag is now redundant — `dualLoopActive()` is provider-presence-based, so tests that want single-loop simply omit `ExecutorProvider`. The flag was never set by production code (confirmed: `subagent_runner.go:178` builds subagent Loops with neither field). Removing it is the cleanup and eliminates a footgun (a stale flag that doesn't actually control the gate anymore).

**Files:** `internal/agent/agent.go` (field `~163`, gate `~561`); `internal/agent/agent_test.go` (27 `DisableInnerLoop: true` usages); `internal/agent/subagent_test.go` (3 usages).

**Steps:**
1. Delete the `DisableInnerLoop bool` field + its doc comment from the `Loop` struct.
2. Delete the `if !l.DisableInnerLoop {` wrapper in `Run()` — the dual-loop block is now guarded solely by `if l.dualLoopActive() && hasDelegateCall(msg.ToolCalls)`. (Already handled in Task 3, but verify no lingering reference.)
3. `grep -rn DisableInnerLoop` must return **zero** matches after edits.
4. In test files, delete every `DisableInnerLoop: true,` line (mechanical; the `&Loop{…}` literals stay valid struct syntax). For the test at `agent_test.go:1671` whose comment reads `// DisableInnerLoop defaults to false → dual-loop path runs`, replace the comment with `// ExecutorProvider set above → dual-loop path runs` and ensure that test sets `ExecutorProvider` (it already does at `~1666`).
5. Run: `go build ./... && go test ./...` → PASS. Every formerly-`DisableInnerLoop: true` test now exercises single-loop via the absent `ExecutorProvider`, unchanged behaviour.
6. Commit: `refactor(agent): remove redundant DisableInnerLoop flag; gate on executor presence`

---

## Task 6: Verify default config stays single-loop; clean wiring

**Objective:** Confirm `resolveExecutor` returns `(nil, "")` when unconfigured so the default path is single-loop (the waste fix), and that no executor is silently injected.

**Files:** `cmd/yaah/agent_frame.go` (`resolveExecutor`), `cmd/yaah/tui.go`.

**Steps:**
1. Read `resolveExecutor`; assert it returns nil provider when `agents.executor.provider` empty. If it ever defaulted to the main provider, change it to return nil.
2. Add a doctor check (optional, lightweight): `yaah doctor` prints `dual-loop: disabled` / `dual-loop: enabled (model=<…>)`.
3. `go build ./... && go vet ./... && go test ./...`.
4. Commit (if changed): `chore(agent): dual-loop is opt-in via executor config`

---

## Task 7: Lint + full verification

**Steps:**
1. `gofmt -w .` (must produce no diff).
2. `go vet ./...`.
3. `go run honnef.co/go/tools/cmd/staticcheck@latest ./...`.
4. `go test ./...`.
5. Manual trace check (per yaah-testing skill): run a delegated prompt with `docker compose up -d jaeger` and OTel verbose on; confirm in Jaeger that (a) `inner.loop`'s first `llm.stream` no longer duplicates the outer decision, (b) the executor system message is the executor prompt not the planner identity, (c) total `llm.stream` count drops for equivalent work.
6. Commit if any cleanup: `chore(agent): gofmt + staticcheck clean`

---

## Out of scope (deliberate YAGNI)

- Parallel multi-delegate dispatch (sequential in v1).
- Planner-side meta-tools (`question`, `todo`) — start with `{delegate}`-only for a clean boundary; relax later if needed.
- An explicit `agents.executor.enabled` config flag — the `ExecutorProvider != nil` gate is sufficient and self-documenting.
