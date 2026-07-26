---
goal: Extract reusable session infrastructure so any UI can be built without duplicating setup
version: 1.0
date_created: 2026-07-26
owner: yaah
status: Implemented
tags: architecture, refactor, session, tui, modularity
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

The `agentSession` struct in `cmd/yaah/agent_frame.go` already owns all shared infrastructure (config, provider, MCP, memory, tools, messages). The REPL and one-shot modes use it. The TUI in `cmd/yaah/tui.go` duplicates ~230 lines of the same setup inline and runs its own agent loop variant (`runAgentForTUI`) that is structurally identical to `agentSession.runPrompt()`.

This plan extracts the remaining gaps — control messages, View injection, model switching, question/approval handlers — so that any UI (bubbletea TUI, tview TUI, HTTP API, etc.) can reuse the session without duplicating setup or agent-loop wiring.

## 1. Requirements & Constraints

- **REQ-001**: A new UI implementation must call `newAgentSession()` + `sess.close()` and nothing else for infrastructure setup. No config loading, no MCP startup, no tool registration, no memory open.
- **REQ-002**: `agentSession.runPrompt()` must accept an optional `agent.View` — if nil, it falls back to `terminalView` (existing REPL behavior).
- **REQ-003**: A UI must be able to receive asynchronous control-plane messages (questions, approvals, model lists, todos, status updates, errors, context info) without a coupling to `tui.ControlMsg`.
- **REQ-004**: A UI must be able to switch provider/model between turns without rebuilding the session.
- **REQ-005**: A UI must be able to wire a `tools.QuestionTool.Handler` and an approval callback without reaching into `tui` package types.
- **REQ-006**: Zero behavior change for existing REPL and one-shot consumers. All changes must be backward compatible.
- **REQ-007**: The TUI (`cmd/yaah/tui.go` + `internal/tui/`) continues to work identically after the extraction. The TUI may use the new hooks or keep its own wiring — but it must not regress.
- **CON-001**: `agentSession` stays in the `yaah` package (cmd). It is not extracted to `internal/` to avoid circular imports (it imports from too many packages).
- **CON-002**: The new control message types must be in a package that both `cmd/yaah` and `internal/tui` can import without creating cycles. `internal/types` is the natural home since `types.Message` is already there.
- **CON-003**: The `Event` sealed interface pattern (unexported marker method) is followed for consistency.
- **PAT-001**: Use the same functional-options pattern as `agent.NewLoop()` for session configuration (e.g., `sess.WithView(v)`, `sess.WithControlCh(ch)`).

## 2. Implementation Steps

### Implementation Phase 1: Extract shared control message types

- GOAL-001: Move control-plane message types out of `internal/tui/` into `internal/types/` so they can be consumed by `agentSession` without importing `tui`.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | In `internal/types/`, create a new file `control.go` defining the `CtrlMsg` sealed interface and all concrete types. The interface uses an unexported `ctrlMarker()` method to seal it: | | |
| | `CtrlMsg` — sealed interface | | |
| | `CtrlStatus{Text string}` — status/notification string | | |
| | `CtrlError{Err error}` — error display | | |
| | `CtrlQuestion{Header, Question string; Options []CtrlOption; Multiple bool; AnswerCh chan<- string}` — interactive question | | |
| | `CtrlApproval{Name, Args string; ApproveCh chan<- bool}` — tool approval prompt | | |
| | `CtrlModelList{Models []string; ProviderNames map[string]string}` — available model list | | |
| | `CtrlTodos{Items []todo.Item}` — todo list update | | |
| | `CtrlContextInfo{Tokens, Window int}` — context usage info | | |
| | `CtrlDone{}` — sentinel marking session close; sent once from `sess.close()`. The goroutine forwarding from `controlCh` to `prog.Send` must detect this type and return (not forward it to bubbletea) to avoid a leaked goroutine after the TUI exits. | | |
| TASK-002 | Add `CtrlOption` struct to `internal/types/control.go`: `{Label, Description string}` | | |
| TASK-003 | Update `internal/tui/` to define aliases or replace uses of `tui.ControlMsg`, `tui.QuestionModal`, `tui.QuestionOption` with the new `types.CtrlMsg` types. The TUI's `Model.handleControlMsg()` type-switches on the concrete types instead of inspecting `tui.ControlMsg` struct fields. | | |
| TASK-004 | Update `internal/tui/` — `Model.HandleEvent` no longer needs `tui.ControlMsg` import for types other than its own rendering state. Remove `tui.ServerInfo` if possible (use `mcp.ServerInfo` directly). | | |
| TASK-005 | Run `go build ./internal/...` and `go build ./cmd/yaah/...` — verify no import cycles | | |

### Implementation Phase 2: Extend agentSession with optional TUI hooks

- GOAL-002: Add optional View, control channel, model switch, question/approval handlers to `agentSession` so the TUI can use it without duplicating setup.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-006 | Add the following fields to `agentSession` in `cmd/yaah/agent_frame.go`: | | |
| | `view agent.View` — nil means use `terminalView` (existing behavior) | | |
| | `ctrlCh chan<- types.CtrlMsg` — nil means suppress (print to stderr for errors, ignore for others) | | |
| | `approveFn func(name, args string) bool` — nil means auto-approve (existing behavior from `agent.WithApprovalMode`) | | |
| TASK-007 | Add setter methods on `agentSession`: | | |
| | `func (s *agentSession) SetView(v agent.View)` — sets `s.view` | | |
| | `func (s *agentSession) SetCtrlCh(ch chan<- types.CtrlMsg)` — sets `s.ctrlCh` | | |
| | `func (s *agentSession) SetApproveFn(fn func(name, args string) bool)` — sets `s.approveFn` | | |
| | `func (s *agentSession) SetModel(providerName, modelName string)` — re-resolves the provider from config and sets `s.provider`, `s.providerName`, `s.modelName` | | |
| | `func (s *agentSession) SetSystemPrompt(prompt string)` — sets `s.systemPrompt` for dynamic updates | | |
| TASK-007a | Add `mu sync.RWMutex` to `agentSession`. All setter methods acquire a write lock before mutating fields. `runPrompt()` takes a read-lock snapshot of `s.provider`, `s.modelName`, `s.view`, `s.ctrlCh`, and `s.approveFn` at entry (captures into locals before releasing) so that a concurrent `SetModel()` call from the bubbletea goroutine cannot race with an in-flight prompt. | | |
| TASK-008 | Refactor `agentSession.runPrompt()` to accept an optional `agent.View` parameter instead of always creating `terminalView`. If the argument is nil and `s.view` is set, use `s.view`. If both are nil, use `terminalView`. Signature change: | | |
| | Before: `func (s *agentSession) runPrompt(prompt string) (string, bool, error)` | | |
| | After: `func (s *agentSession) runPrompt(prompt string, view ...agent.View) (string, bool, error)` | | |
| TASK-009 | In `agentSession.runPrompt()`, after the agent loop completes, if `s.ctrlCh` is set and there was an error, send `CtrlError`. Always send `CtrlContextInfo` with the final estimated tokens and context window. | | |
| TASK-010 | Update `agentSession.compactContext()` to accept the option of sending status to `s.ctrlCh` instead of printing to stderr. If `s.ctrlCh` is set, send `CtrlStatus`. If not, print to stderr (existing behavior). | | |
| TASK-011 | Wire the question tool handler inside `agentSession` (or expose it so callers can set it). Add `s.SetQuestionHandler(fn func(tools.QuestionEntry) string)` — if set, use it; otherwise, existing behavior (prompt via stderr). | | |
| TASK-012 | Update the session's `OnWrite` todo callback (registered once in `newAgentSession()`) to check `s.ctrlCh` at **call time**: the closure must capture `s` by pointer (not capture the value of `s.ctrlCh` at registration time), so that a later `SetCtrlCh()` call is visible to subsequent `OnWrite` invocations. When `s.ctrlCh` is set, send `CtrlTodos`; otherwise print to stderr (existing behavior). | | |
| TASK-013 | Run `go build ./cmd/yaah/...` and `go test ./cmd/yaah/...` — REPL and one-shot tests must pass unchanged | | |

### Implementation Phase 3: Consolidate runAgentForTUI into agentSession

- GOAL-003: Eliminate the ~90-line `runAgentForTUI()` function and `tuiEventForwarder` by making `agentSession.runPrompt()` serve both the REPL and TUI paths.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-014 | Inline `tuiEventForwarder` into the `OnSubmit` closure in `cmd/yaah/tui.go` (lines 584-592). The forwarding concept is not removed — a View that calls `prog.Send(evt)` is still required — but the named file-level struct is replaced with an anonymous struct or a local type scoped inside `runTUI()`, eliminating the exported declaration. | | |
| TASK-015 | Remove `runAgentForTUI()` function from `cmd/yaah/tui.go` (lines 594-687). Replace its call in `OnSubmit` with: | | |
| | `sess.SetView(...)` — create a forwarding View that calls `prog.Send(evt)` | | |
| | `sess.SetCtrlCh(controlCh)` — wire the TUI's control channel | | |
| | `sess.SetApproveFn(...)` — wire the approval callback | | |
| | `sess.SetModel(...)` — ensure current model is set | | |
| | `sess.runPrompt(input)` — reuse the shared method | | |
| | Update `OnFollowUp` callback to send to `sess.followupCh` instead of the local `followupCh` | | |
| | Update `OnSteer` callback to send to `sess.steerCh` instead of the local `steerCh` | | |
| | Remove the local `steerCh`/`followupCh` variables and their `defer close(...)` calls — the session now owns these channels and `sess.close()` closes them | | |
| TASK-016 | After `sess.runPrompt()` returns, extract `sess.Messages` and `sess.MsgIdx` from the session (it already owns them). Remove the shared mutable pointer pattern (`*messages`). | | |
| TASK-017 | Run `go build ./cmd/yaah/...` — verify TUI compiles | | |
| TASK-018 | Smoke test the TUI: `go build -o yaah.exe . && ./yaah.exe tui` | | |

### Implementation Phase 4: Clean up in tui.go

- GOAL-004: The TUI command in `tui.go` drops from ~687 lines to ~250 lines of pure UI-framework code. Remove all the duplicated setup.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-019 | In `runTUI()`, remove lines 140-560 (approximately) — config load, OTel, roles, prompts, memory, MCP, tool reg, steer/followup channels, conflict tracker, subagent — everything that duplicates `newAgentSession()`. Replace with: | | |
| | `sess, err := newAgentSession()` | | |
| | `defer sess.close()` | | |
| | Use `sess.cfg`, `sess.modelName`, `sess.providerName`, `sess.toolReg`, `sess.systemPrompt` etc. directly | | |
| TASK-019a | Before removing the TUI's `systemPrompt` construction, verify `newAgentSession()` includes the memory-guidelines block (`"\n\n## Memory Guidelines\n..."`) that `runTUI()` currently appends after `prompts.Build()`. If absent, add it to `newAgentSession()` first so that TUI memory-tool behavior is not silently degraded. | | |
| TASK-020 | Remove `sessionModel` struct (lines 49-68) — model switching is handled by `sess.SetModel()` now | | |
| TASK-021 | Remove `providerFor()` (lines 107-115) — the session already has `sess.provider` | | |
| TASK-022 | Remove `fetchAllModels()` (lines 70-105) — move it to a helper on `agentSession` or keep as a standalone but use `sess.cfg` | | |
| TASK-023 | Update `OnSubmit` callback to use `sess.SetView(forwarder)`, `sess.SetCtrlCh(controlCh)`, `sess.runPrompt()` | | |
| TASK-024 | Update `OnCompact` callback to call `sess.compactContext()` (which now reports via `sess.ctrlCh`) | | |
| TASK-025 | Update `OnModel` callback to call `sess.SetModel(pName, mName)` | | |
| TASK-026 | Remove the deferred panic recovery that does `prog.Kill()` — it's now handled by `sess.close()` cleanup. Keep only bubbletea-specific cleanup. | | |
| TASK-027 | Run `go vet ./...` — must be clean | | |
| TASK-028 | Run `go test ./...` — all tests pass | | |
| TASK-029 | Manual smoke test: `go build -o yaah.exe . && ./yaah.exe tui` | | |

## 3. Alternatives

- **Extract a `Session` interface to `internal/session/`**: Would give a cleaner abstraction boundary but requires moving many types (config, agent, tools, mcp, memory) into the interface or facing import cycles. The `agentSession` is already in `cmd/yaah` for a reason — it imports everything. Keeping it there is pragmatic.
- **Build `tuiSession` as a separate struct**: Would avoid touching `agentSession` at all but means the TUI still duplicates setup code. The whole point is eliminating that duplication.
- **Push everything into `internal/`**: Not feasible without restructuring the entire package dependency graph. The `cmd/yaah` package is the natural integration point where all `internal/` packages meet.

## 4. Dependencies

- **DEP-001**: `internal/types` — gets new `control.go` file with `CtrlMsg` interface and concrete types. No new dependencies.
- **DEP-002**: `internal/tools.QuestionTool` — the `Handler` field type must be compatible with the new question flow. No changes needed; `QuestionEntry` and callback already exist.
- **DEP-003**: `internal/tui` — must replace its `ControlMsg` struct with the new `types.CtrlMsg` types. This is a pure refactor; no behavior change.

## 5. Files

| File | Size | Change |
|------|------|--------|
| `internal/types/control.go` | new (~80 lines) | Define `CtrlMsg` sealed interface + 9 concrete types |
| `internal/tui/tui.go` | 1809 lines | Replace `ControlMsg` struct with type switches on `types.CtrlMsg`; remove `QuestionModal`, `QuestionOption`, `ServerInfo` if now redundant |
| `internal/tui/tui_test.go` | 1525 lines | Update test `ControlMsg` constructions to use new types |
| `cmd/yaah/agent_frame.go` | 551 lines | Add `view`, `ctrlCh`, `approveFn`, `mu sync.RWMutex`, `mcpInfos` fields to `agentSession`; add setter methods (TASK-007) + mutex (TASK-007a); update `runPrompt()` signature; update `compactContext()`; wire question handler |
| `cmd/yaah/tui.go` | 687 lines | Remove `runAgentForTUI`, `tuiEventForwarder`, `sessionModel`, `providerFor`; switch to `newAgentSession()`; shrink to ~250 lines |
| `cmd/yaah/root_cmd.go` | 85 lines | Update calls to `sess.runPrompt()` if signature changed |
| `cmd/yaah/repl_loop.go` | unknown | Update calls to `sess.runPrompt()` if signature changed |

## 6. Testing

- **TEST-001**: `TestAgentSessionRunPrompt` — run prompt with `terminalView` (nil view arg), verify output to stderr, verify messages are populated on session
- **TEST-002**: `TestAgentSessionRunPromptWithView` — run prompt with custom `recordingView`, verify events are delivered to the view
- **TEST-003**: `TestAgentSessionControlCh` — set `sess.ctrlCh`, run prompt, verify `CtrlContextInfo` and `CtrlDone` are sent (or verify no panic when unset)
- **TEST-004**: `TestAgentSessionSetModel` — call `sess.SetModel("ollama", "llama3")`, verify `sess.providerName` and `sess.modelName` updated, verify next `runPrompt` uses the new provider
- **TEST-005**: `TestAgentSessionSetViewNil` — verify nil view falls back to `terminalView` (matching existing REPL behavior)
- **TEST-006**: `TestTuiControlMsgMigration` — in `internal/tui/`, verify all control message types work with the new `types.CtrlMsg` interface via type switch
- **TEST-007**: `go vet ./...` — must be clean after each phase
- **TEST-008**: `go test ./...` — all tests pass after each phase

## 7. Risks & Assumptions

- **RISK-001**: The TUI's `controlCh` currently carries messages that combine multiple concerns in one struct (e.g., `{Err, ContextTokens, ContextWindow}` shipped together). Splitting into separate `CtrlMsg` types means the TUI's `handleControlMsg()` must handle multiple messages that were previously one. This is safe but requires a careful reading of the delivery goroutine to ensure atomicity isn't lost.
- **RISK-002**: `newAgentSession()` currently does not set up the TUI's specific tools differently (e.g., the TUI registers `TodoWriteTool.OnWrite` to push to `controlCh`, while the REPL version prints to stderr). The extracted session uses callbacks to handle this — the TUI sets its own `OnWrite` via the session hooks. Must verify the TUI's todo updates still work.
- **RISK-003**: The TUI's compact logic (lines 408-477) is more sophisticated than `agentSession.compactContext()` — it uses the small model, formats structured summaries, and handles fallback trimming. Rather than merging them, the plan keeps them separate but routes `compactContext()` status through the control channel. A future follow-up can unify them.
- **RISK-004**: `SetModel()` race with `runPrompt()` — `OnModel` (bubbletea goroutine) can call `SetModel()` while a prior `runPrompt()` goroutine reads `s.provider`/`s.modelName`. Without the mutex added in TASK-007a, `go test -race` will flag this. Mitigation: apply TASK-007a before Phase 3.
- **RISK-005**: Memory-guidelines system prompt loss — `runTUI()` appends `"\n\n## Memory Guidelines\n..."` to `systemPrompt` after `prompts.Build()`. `newAgentSession()` does not. Phase 4 silently drops these guidelines unless TASK-019a runs first. Mitigation: apply TASK-019a before removing the TUI's `systemPrompt` construction.
- **ASSUMPTION-001**: The TUI's `ServerInfo` conversion (lines 288-299) can stay in `runTUI()`. However, `newAgentSession()` currently discards MCP infos (`_, mcpTools, _, _`). To prevent Phase 4 from silently dropping the MCP status footer, add a `mcpInfos []mcp.ServerInfo` field to `agentSession` and populate it in `newAgentSession()`; `runTUI()` then reads `sess.mcpInfos` instead of calling `StartMCPClientsWithStderr` a second time.
- **ASSUMPTION-002**: `modelName` in `agentSession` is updated by `SetModel()` for the *next* turn. The current turn's model is captured when `runPrompt()` creates the agent loop. This matches existing TUI behavior.

## 8. Related Specifications / Further Reading

- `cmd/yaah/agent_frame.go` — existing `agentSession` struct and `runPrompt()`
- `cmd/yaah/tui.go` — the 687-line `runTUI()` and `runAgentForTUI()` being replaced
- `internal/tui/tui.go` — `ControlMsg` struct (lines 814-826), `handleControlMsg()` (line 828+)
- `internal/types/types.go` — existing shared types, where `control.go` will live
- `internal/tools/tool_question.go` — `QuestionTool` and `QuestionEntry` types
- `.agents/plans/feature-event-api-fidelity-1.md` — companion plan for enriching DoneEvent with finish_reason/usage (prerequisite for clean View contract)
