---
goal: Add finish_reason, usage, and response model fields to the agent event system
version: 1.0
date_created: 2026-07-26
owner: yaah
status: Implemented
tags: feature, event-system, openai-fidelity, agent-view
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Add three missing OpenAI API fields to the `agent.Event` system so consumers (TUI, REPL, sub-agent runners) can distinguish `stop` from `length` truncation, see actual token usage, and identify the response model. Currently these fields are parsed internally but never reach event consumers.

## 1. Requirements & Constraints

- **REQ-001**: `DoneEvent` must carry the `finish_reason` from the last LLM call (`"stop"`, `"length"`, `"tool_calls"`, `"content_filter"`, or `""`)
- **REQ-002**: `DoneEvent` must carry the cumulative token usage (`prompt_tokens`, `completion_tokens`, `total_tokens`, `reasoning_tokens`, `cached_prompt_tokens`)
- **REQ-003**: `DoneEvent` must carry the response model string (e.g. `"gpt-4o-2024-11-20"`), if available from the last provider response
- **REQ-004**: Zero new dependencies. All data already exists in the codebase as parsed struct fields — it only needs to be threaded through to events
- **REQ-005**: Backward compatible. Existing consumers that don't read the new fields continue to compile and work unchanged (Go ignores unread struct fields)
- **CON-001**: The `Event` interface is sealed via an unexported `eventMarker()` — no changes to the interface itself, only to concrete event types
- **CON-002**: Finish_reason is per-turn, not per-agent-loop. Only the *last* turn's finish_reason goes into `DoneEvent`; earlier turns are collapsed
- **GUD-001**: Follow the existing field-zero-value conventions: empty string for absent finish_reason, zero-value `Usage` for absent usage
- **PAT-001**: Same pointer-receiver + `eventMarker()` pattern as all other events

## 2. Implementation Steps

### Implementation Phase 1: Add fields to DoneEvent and thread finish_reason

- GOAL-001: Add `FinishReason`, `Usage`, and `ResponseModel` to `DoneEvent` and wire them through from the LLM call result

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | Add `FinishReason string`, `Usage types.Usage`, `ResponseModel string` fields to `DoneEvent` in `internal/agent/events.go` | | |
| TASK-002 | Add `FinishReason string` field to `types.Message` in `internal/types/types.go` | | |
| TASK-003 | In `internal/agent/llm/stream.go`, set `msg.FinishReason = finishReason` inside `checkTruncatedStream()` before the success return (line ~195) | | |
| TASK-004 | In `internal/agent/llm/client.go`, after the non-streaming success path extracts `msg` at line 69, set `msg.FinishReason = resp.Choices[0].FinishReason` | | |
| TASK-005 | In `internal/agent/agent.go`, add `lastFinishReason string` and `lastResponseModel string` fields to the `Loop` struct | | |
| TASK-006 | In `internal/agent/agent.go`, after line 561 (`l.addUsage(usage)`), capture `msg.FinishReason` and the response model. The response model is not on `types.Message` — thread it from `llm.Call()` return or add it to `types.Message`. For simplicity, add `ResponseModel string` to `types.Message` and set it in client.go alongside FinishReason | | |
| TASK-007 | In `internal/agent/agent.go`, update the deferred `DoneEvent` construction (~lines 409-419) to set `done.FinishReason = l.lastFinishReason`, `done.ResponseModel = l.lastResponseModel`, and `done.Usage = l.TotalTokens`. Note: `l.TotalTokens` only accumulates flat counts — `addUsage()` stores reasoning and cached tokens separately in `l.TotalReasoningTokens` and `l.TotalCachedPromptTokens`. After the copy, populate the nested structs: if `l.TotalReasoningTokens > 0` set `done.Usage.CompletionTokensDetails = &types.CompletionTokensDetails{ReasoningTokens: l.TotalReasoningTokens}`; if `l.TotalCachedPromptTokens > 0` set `done.Usage.PromptTokensDetails = &types.PromptTokensDetails{CachedTokens: l.TotalCachedPromptTokens}` | | |
| TASK-008 | Add `DoneEvent` test to `internal/agent/events_test.go` verifying the new fields are set via `eventMarker()` and round-trip correctly | | |

### Implementation Phase 2: Wire response model through streaming

- GOAL-002: Capture the `model` field from streaming SSE chunks (OpenAI includes it on every chunk; non-OpenAI providers may omit it)

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-009 | Add `Model string` to `StreamChunk` in `internal/providers/stream.go` (optional — some providers omit it, but it costs nothing to parse) | | |
| TASK-010 | In `internal/agent/llm/stream.go`, capture `chunk.Model` from the first chunk (non-empty) into a local `responseModel` string inside `runStream()` | | |
| TASK-011 | In `internal/agent/llm/stream.go`, pass `responseModel` through `checkTruncatedStream()` and set `msg.ResponseModel = responseModel` on the returned message | | |
| TASK-012 | In `internal/agent/llm/client.go`, after the non-streaming success path (line 69), set `msg.ResponseModel = resp.Model` | | |

### Implementation Phase 3: Update consumer tests

- GOAL-003: All existing tests compile and pass with the new struct fields

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-013 | Update `internal/agent/events_test.go` — add `TestDoneEvent` with field assertions | | |
| TASK-014 | Update `internal/tui/tui_test.go` — all 7 `&agent.DoneEvent{}` literals compile as-is (zero-value new fields) and still pass. No changes needed unless a test reads fields that now have different zero semantics | | |
| TASK-015 | Run `go build ./...` — catch any zero-value-affected locations in test files | | |
| TASK-016 | Run `go test ./internal/agent/llm/...` — verify stream.go changes don't break streaming tests | | |
| TASK-017 | Run `go vet ./...` — must be clean | | |
| TASK-018 | Run full test suite `go test ./...` — all pass | | |

## 3. Alternatives

- **Put finish_reason on FlushEvent instead of DoneEvent**: More semantically accurate since finish_reason is per-turn, but requires adding the field to `FlushEvent` and publishing it on every turn. `DoneEvent` is simpler for REPL consumers that only care about the final state. Can be done as a follow-up if per-turn granularity is needed.
- **Put the full `types.Usage` struct on `FlushEvent` rather than cumulative on `DoneEvent`**: Would give per-turn usage granularity but adds complexity since FlushEvent is published in multiple code paths. The cumulative `DoneEvent.Usage` meets the stated goal of "consumers can display token counts."
- **Thread response model via a new `InitEvent` sent once at loop start**: Cleaner for constant-per-conversation metadata, but requires a new event type and consumer changes. Putting it on `DoneEvent` is simpler.
- **Add a `FinishReasonEvent` as a new event type**: Most semantically clear but requires adding a case to every `HandleEvent` type switch (3 consumers + N test views). Overkill for one field.

## 4. Dependencies

- **DEP-001**: `types.Usage` struct (already in `internal/types/types.go`) — used directly as the `DoneEvent.Usage` field type
- **DEP-002**: `types.Message` struct — gets the new `FinishReason` and `ResponseModel` fields that flow through the existing return chain

## 5. Files

| File | Change |
|------|--------|
| `internal/agent/events.go` | Add `FinishReason`, `Usage`, `ResponseModel` to `DoneEvent` |
| `internal/types/types.go` | Add `FinishReason`, `ResponseModel` to `Message` |
| `internal/agent/llm/stream.go` | Set `msg.FinishReason` in `checkTruncatedStream()`; capture `responseModel` from `StreamChunk` |
| `internal/agent/llm/client.go` | Set `msg.FinishReason` and `msg.ResponseModel` in non-streaming path |
| `internal/providers/stream.go` | Add `Model` to `StreamChunk` (optional, for response model capture) |
| `internal/agent/agent.go` | Add `lastFinishReason`/`lastResponseModel` to `Loop`; populate `DoneEvent` fields in deferred publisher |
| `internal/agent/events_test.go` | Add `TestDoneEvent` with new field assertions |
| `internal/tui/tui_test.go` | Verify existing `&agent.DoneEvent{}` literals still compile (zero-value compatible) |
| `internal/agent/llm/*_test.go` | Any streaming/accumulator tests that check message fields |

## 6. Testing

- **TEST-001**: `TestDoneEvent` — construct with all fields, verify `eventMarker()` compiles, verify field values round-trip
- **TEST-002**: `TestDoneEventZeroValues` — construct with zero values, verify `FinishReason==""`, `Usage==types.Usage{}`, `ResponseModel==""`
- **TEST-003**: Streaming path integration — mock `StreamChunk` with `FinishReason: strPtr("stop")`, verify final message has `FinishReason: "stop"` (covered by existing tests if they set FinishReason on mock responses)
- **TEST-004**: Non-streaming path — mock `ChatResponse` with `FinishReason: "tool_calls"`, verify final message has `FinishReason: "tool_calls"`
- **TEST-005**: Consumer compatibility — verify `agent.NoopView` and `recordingView` still compile with zero-value event literals
- **TEST-006**: `go vet ./...` — no new vet warnings
- **TEST-007**: `go test ./...` — all tests pass

## 7. Risks & Assumptions

- **RISK-001**: Some providers (ollama, llama.cpp) do not include `model` in SSE chunks. `ResponseModel` will be empty string for those. The streaming capture logic must use `chunk.Model` only when non-empty (first chunk wins).
- **RISK-002**: `types.Message` is used for both request messages (sent to the API) and response messages (received from the API). Adding `FinishReason` and `ResponseModel` only matters on response messages; request messages will have zero values. No existing code sets these fields on request messages, so this is safe.
- **RISK-003**: Adding fields to `DoneEvent` will trigger compile errors in any external code that constructs `DoneEvent` structs using positional (non-field) syntax. The entire codebase uses field-name initialization (`&DoneEvent{Response: "...", ...}`), so this is safe. New fields default to zero values.
- **ASSUMPTION-001**: The `model` field from SSE responses corresponds to the *provider's* model name, which may differ from the requested model (e.g., requested `gpt-4o` → response `gpt-4o-2024-11-20`). This is intentional and useful.
- **ASSUMPTION-002**: Cumulative usage in `DoneEvent.Usage` is sufficient. Per-turn granularity would require adding usage to `FlushEvent`, which is out of scope.

## 8. Related Specifications / Further Reading

- `.agents/plans/architecture-ui-session-extraction-1.md` — **implement this plan first**. The extraction plan is a consumer of `DoneEvent`. Once this plan lands, the extraction plan's TASK-009 (`CtrlContextInfo{Tokens, Window int}`) should be dropped — `DoneEvent.Usage.TotalTokens` and `DoneEvent.ContextWindow` already carry the same data through the broker/View path, making a separate control-channel message redundant.
- `internal/agent/events.go` — existing event types and patterns
- `internal/agent/llm/stream.go` — `runStream()`, `checkTruncatedStream()`, `assembleStreamed()`
- `internal/agent/llm/client.go` — `Call()` streaming and non-streaming paths
- `internal/agent/agent.go` — `runMiddleware()` deferred DoneEvent publisher (lines 408-420), `addUsage()` (lines 819-835)
- `internal/types/types.go` — `Message`, `Usage`, `ChatResponse` structs
- `internal/providers/stream.go` — `StreamChunk` SSE parsing
- `docs/architecture.md` — existing architecture documentation
