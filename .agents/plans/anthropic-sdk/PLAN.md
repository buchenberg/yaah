# Plan: Adopt anthropic-sdk-go for the native Anthropic provider path

## Goal

Replace the hand-rolled Anthropic Messages wire/SSE layer with the official
`github.com/anthropics/anthropic-sdk-go`, deleting ~600 lines of transport
code while preserving every externally observable behavior of the
`api: anthropic` provider path: identical wire payloads, identical
`StreamChunk` contract, identical error classification, zero transport
retries (yaah's classified retry ladder in `internal/agent/llm/client.go`
stays the single retry owner).

## Non-goals

- No changes to the OpenAI-compat client (`providers.go`, `wire.go`,
  `stream.go`). It serves 6+ compat providers with extension fields
  (`reasoning_content`, copilot mode) the official OpenAI SDK cannot
  round-trip.
- No changes to `internal/agent/errorclassify` policy or `internal/agent/llm`
  stream assembly.
- No SDK-managed retries (see decisions).

## Spike findings (verified against anthropic-sdk-go v1.81.0, cloned in .scratch/)

| Requirement | SDK capability | Status |
|---|---|---|
| Custom base URL (DeepSeek/ZAI run the Anthropic protocol) | `option.WithBaseURL` (option/requestoption.go:144) | OK |
| Prompt caching | `TextBlockParam.CacheControl` (`CacheControlEphemeralParam`, TTL support) on system blocks and content blocks | OK |
| Thinking blocks | `ThinkingConfigParamUnion`, `ThinkingDelta` stream events, `NewThinkingBlock` | OK, with wire delta (below) |
| Streaming | `Messages.NewStreaming` → `ssestream.Stream[MessageStreamEventUnion]` (message.go:88) | OK |
| Usage incl. cache tokens | `Usage.CacheCreationInputTokens` / `CacheReadInputTokens` | OK |
| Typed errors | exported `anthropic.Error` with `StatusCode` + `Type()` ("rate_limit_error", …) (internal/apierror/apierror.go:17, aliases.go:15) | OK |
| Mid-stream SSE `error` events | `richErrorDecoder.newAPIError` builds API errors from SSE error payloads (packages/ssestream/ssestream.go:86-98) | OK |
| Param helpers | `NewUserMessage`, `NewAssistantMessage`, `NewTextBlock`, `NewThinkingBlock`, `NewToolUseBlock`, `NewToolResultBlock(id, content, isError)` | OK |
| Go version | SDK requires go 1.24; yaah is on 1.26.3 | OK |

## Decisions

1. **`option.WithMaxRetries(0)`** — yaah's classified retry ladder
   (`llm/client.go` MaxRetries/backoff config) stays the only retry
   mechanism. No double-retry compounding, behavior identical to today's
   hand-rolled client.
2. **`providers.APIError` stays the error currency.** `*anthropic.Error` is
   converted at the client boundary (`StatusCode` → `StatusCode`,
   `Type()` → `Code`, message extracted from the error). `errorclassify`
   consumes the same `statusCoder` interface as today — zero policy changes.
   `anthropicErrorStatus` (stream-error type → HTTP status) is kept and used
   when the SDK surfaces mid-stream errors.
3. **Session affinity headers** via per-request `option.WithHeader`
   (`x-session-id` / `x-session-affinity`, SHA-256 of session ID from
   context — same values as `setSessionHeaders` today).
4. **HTTP timeout semantics** via `option.WithHTTPClient(&http.Client{Timeout: to})`
   — identical to the current `c.client.Timeout` (same "whole request"
   semantics, including 0 = no timeout for slow local models).
5. **Known wire delta — thinking block signature.** The SDK's
   `ThinkingBlockParam.Signature` is `api:"required"` (message.go:12888):
   replayed thinking blocks will serialize `"signature":""` where today's
   hand-rolled path omits the field. yaah never stores real signatures
   (`types.Message.ReasoningContent` is text only), so today's shape also
   cannot satisfy real Anthropic signature validation; the delta only
   matters for compat endpoints (DeepSeek/ZAI), where an empty string in an
   otherwise-ignored field is expected to be tolerated. Accept the delta;
   verify against ZAI in live testing.
6. **Constructor signature unchanged** — `NewAnthropicClient(baseURL, apiKey
   string, timeoutSeconds int)` keeps its shape, so
   `cmd/yaah/provider_resolve.go:100,123` is untouched.
7. **`ListModels`** keeps returning "not supported" — the Messages API has no
   model listing; SDK adds no capability here.

## File-by-file changes

### `go.mod` / `go.sum`
Add `github.com/anthropics/anthropic-sdk-go` (pin the tagged release go get
resolves — expect v1.81.x).

### `internal/providers/anthropic.go` (rewrite, ~309 → ~260 lines)
- `AnthropicClient` wraps `anthropic.Client` plus base options
  (`WithBaseURL`, `WithHTTPClient` w/ timeout, `WithMaxRetries(0)`).
- `Send`: `lowerAnthropicRequest(req) → anthropic.MessageNewParams`
  → `client.Messages.New(ctx, params, perReqOpts...)` → `raiseAnthropicResponse`.
- **Lowering maps 1:1 from today's logic** (`anthropic.go:132-262`):
  - system messages → `[]anthropic.TextBlockParam` with `CacheControl`
    passthrough (today's `sysBlocks`)
  - user/assistant/tool message walk, pending-tool-result flush,
    consecutive-user merge — preserved exactly, built with
    `NewUserMessage`/`NewAssistantMessage`/`NewToolResultBlock`
  - assistant reasoning → `NewThinkingBlock("", reasoning)` (decision 5)
  - tool_use → `NewToolUseBlock` with raw JSON input
  - `MaxTokens` default 4096 when unset
  - temperature passthrough
- `raiseAnthropicResponse` maps `anthropic.Message` → `types.ChatResponse`:
  text/thinking/tool_use blocks, `json.Valid` guard on tool input (today's
  behavior), stop-reason map (`end_turn`/`stop_sequence`→stop,
  `tool_use`→tool_calls, `max_tokens`→length), usage incl.
  `CacheReadInputTokens` → `PromptTokensDetails.CachedTokens`.
- Errors: `*anthropic.Error` → `*APIError` (decision 2).

### `internal/providers/anthropic_stream.go` (rewrite, ~376 → ~200 lines)
- `SendStream`: `client.Messages.NewStreaming(ctx, params, perReqOpts...)`
  → goroutine iterating `stream.Recv()` → translate each
  `MessageStreamEventUnion` to the existing `StreamChunk` shapes:
  - `MessageStartEvent` → ID/Model chunk + usage (as today)
  - `ContentBlockStartEvent` (text/thinking/tool_use) → role /
    tool-call-open chunks
  - `ContentBlockDeltaEvent` (`TextDelta`/`ThinkingDelta`/`InputJSONDelta`)
    → content / reasoning / tool-args delta chunks
  - `MessageDeltaEvent` → finish-reason chunk (+usage), same
    stop-reason mapping as today
  - `MessageStopEvent` → stop-reason fallback when no `message_delta`
    carried one (today's proxy-quirk handling at
    `anthropic_stream.go:289-305`)
  - `stream.Recv()` error → convert (mid-stream SSE errors arrive here as
    `*anthropic.Error`; use `anthropicErrorStatus` + `Type()` → `APIError`)
- The SSE scanner, event JSON structs (`anthropicSSEEvent` et al.), and
  `processAnthropicSSEEvent` are deleted — the SDK owns decoding.

### `internal/providers/anthropic_test.go` (adapt, keep test intent)
- Lowering tests: keep bodies/data, assert on `json.Marshal(params)` wire
  JSON so expectations stay byte-comparable (expect the signature delta in
  the reasoning test).
- SSE tests: unmarshal existing event fixtures into
  `MessageStreamEventUnion` and call the new translate function — fixtures
  unchanged.
- End-to-end httptest tests (`MidStreamErrorSurfaces`,
  `ProxyOmitsMessageDelta`, interface test) keep running against an
  httptest server via `WithBaseURL`.
- `anthropicErrorStatus` table test unchanged.

### `AGENTS.md`
Update the convention line: "No model SDK" → scoped exception: the native
Anthropic path uses `anthropics/anthropic-sdk-go`; the OpenAI-compat path
stays direct-HTTP (rationale: compat-provider extension fields).

## Verification gates

1. `go build .`
2. `go test ./internal/providers/ ./internal/agent/...`
3. `go vet ./...`, `gofmt -l .` empty, staticcheck clean
4. Live check: run a one-shot yaah prompt against the configured ZAI
   provider (`api: anthropic`) and confirm a fresh trace in SigNoz, plus
   watch for stream replay/error regressions in logs.

## Rollback

Revert the four files + go.mod/go.sum. No data or config migrations involved.
