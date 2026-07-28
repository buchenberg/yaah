# Plan: Native Anthropic Messages API Provider

## Goal

Add a native Anthropic Messages API client so yaah can use Claude models
directly (with prompt caching, extended thinking, and native tool use)
instead of routing through an OpenAI-compatible proxy.

## Motivation

- DeepSeek's OpenAI-compatible API has no client-side prompt caching;
  Anthropic's native API supports `cache_control` breakpoints that yaah's
  `PromptCachingMiddleware` already injects.
- The existing `types.Message.CacheControl` field is serialized but ignored
  by OpenAI-compatible endpoints. A native client makes it functional.
- Anthropic's streaming protocol carries `cache_read_input_tokens` in usage,
  enabling accurate cache-hit observability in traces.

## Architecture

The adapter lives entirely in `internal/providers/`. It implements the same
`Provider` + `StreamProvider` interfaces (`Send`, `SendStream`) that
`OpenAIClient` does, translating between yaah's internal OpenAI-shaped types
and Anthropic's wire format at the boundary. **Zero changes to the agent
loop, pipeline, tools, TUI, or LLM client.**

```
┌─────────────────────────────────────────────────────────┐
│  agent loop / llm.Client / pipeline / TUI               │
│  (unchanged — speaks types.ChatRequest / StreamChunk)   │
└────────────────────────┬────────────────────────────────┘
                         │ Provider interface
┌────────────────────────▼────────────────────────────────┐
│  internal/providers/                                     │
│  ┌──────────────────┐   ┌────────────────────────────┐  │
│  │ OpenAIClient     │   │ AnthropicClient (NEW)      │  │
│  │ /chat/completions│   │ /v1/messages               │  │
│  └──────────────────┘   └────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

## Config

Add an `api` field to the `Provider` config struct:

```yaml
providers:
  anthropic:
    api: anthropic          # NEW — "openai" (default) or "anthropic"
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}
    name: Anthropic
```

When `api: anthropic`, `makeProvider()` returns `AnthropicClient` instead of
`OpenAIClient`. Omitting `api` or setting `api: openai` preserves current
behavior for all existing configs.

## Files

| File | Action | Lines (est.) |
|------|--------|-------------|
| `internal/providers/anthropic.go` | NEW — client, wire types, translation | ~350 |
| `internal/providers/anthropic_stream.go` | NEW — SSE event parser | ~150 |
| `internal/providers/anthropic_test.go` | NEW — unit tests | ~250 |
| `internal/config/load.go` | EDIT — add `API string` field to Provider | +2 |
| `cmd/yaah/provider_resolve.go` | EDIT — branch on `api` field in makeProvider | +8 |
| `internal/config/create.go` | EDIT — add `api` to generated config template | +1 |

Total: ~760 new/changed lines.

## Step 1: Wire types (`anthropic.go`)

Anthropic request/response shapes, private to the providers package:

```go
type anthropicRequest struct {
    Model       string             `json:"model"`
    MaxTokens   int                `json:"max_tokens"`
    System      []anthropicBlock   `json:"system,omitempty"`
    Messages    []anthropicMessage `json:"messages"`
    Tools       []anthropicTool    `json:"tools,omitempty"`
    Stream      bool               `json:"stream"`
    Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
    Role    string           `json:"role"` // "user" | "assistant"
    Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
    Type         string            `json:"type"` // "text", "tool_use", "tool_result", "thinking"
    Text         string            `json:"text,omitempty"`
    ID           string            `json:"id,omitempty"`
    Name         string            `json:"name,omitempty"`
    Input        json.RawMessage   `json:"input,omitempty"`
    ToolUseID    string            `json:"tool_use_id,omitempty"`
    Content      string            `json:"content,omitempty"`
    Thinking     string            `json:"thinking,omitempty"`
    CacheControl *types.CacheControl `json:"cache_control,omitempty"`
}

type anthropicResponse struct {
    ID           string           `json:"id"`
    Model        string           `json:"model"`
    Role         string           `json:"role"`
    Content      []anthropicBlock `json:"content"`
    StopReason   string           `json:"stop_reason"`
    Usage        anthropicUsage   `json:"usage"`
}

type anthropicUsage struct {
    InputTokens              int `json:"input_tokens"`
    OutputTokens             int `json:"output_tokens"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
```

## Step 2: Translation functions (`anthropic.go`)

### Request: `types.ChatRequest` → `anthropicRequest`

1. Extract `role: "system"` messages → top-level `system` blocks (with
   `cache_control` passthrough).
2. Walk remaining messages, merging consecutive same-role messages:
   - `role: "user"` → `anthropicMessage{Role: "user"}` with text blocks.
   - `role: "assistant"` → text block + `tool_use` blocks from `ToolCalls`.
   - `role: "tool"` → fold into the preceding `user` message as
     `tool_result` blocks (Anthropic requires tool results in user turns).
3. Convert `types.ToolDef` → `anthropicTool` (name, description,
   input_schema from Parameters).
4. Pass through `CacheControl` on blocks where set.

### Response: `anthropicResponse` → `types.ChatResponse`

1. Concatenate `text` blocks → `Message.Content`.
2. Concatenate `thinking` blocks → `Message.ReasoningContent`.
3. Convert `tool_use` blocks → `Message.ToolCalls`.
4. Map `stop_reason`: `"end_turn"` → `"stop"`, `"tool_use"` → `"tool_calls"`.
5. Map usage: `input_tokens` → `PromptTokens`, `output_tokens` →
   `CompletionTokens`, `cache_read_input_tokens` →
   `PromptTokensDetails.CachedTokens`.

## Step 3: Non-streaming Send (`anthropic.go`)

```go
func (c *AnthropicClient) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
```

- POST to `{baseURL}/v1/messages`
- Headers: `x-api-key`, `anthropic-version: 2023-06-01`, `content-type: application/json`
- Translate request, send, translate response.
- Error mapping: 429 → rate limit, 401 → auth, 529 → overloaded.

## Step 4: Streaming (`anthropic_stream.go`)

```go
func (c *AnthropicClient) SendStream(ctx context.Context, req types.ChatRequest) (<-chan providers.StreamChunk, <-chan error)
```

Anthropic SSE events → `StreamChunk` translation:

| Anthropic event | Maps to |
|---|---|
| `message_start` | Initial chunk with model, ID |
| `content_block_start` (type=text) | Delta with role="assistant" |
| `content_block_start` (type=tool_use) | Delta with ToolCall (ID + name) |
| `content_block_start` (type=thinking) | Delta with ReasoningContent |
| `content_block_delta` (text_delta) | Delta.Content |
| `content_block_delta` (input_json_delta) | Delta.ToolCalls[0].Function.Arguments |
| `content_block_delta` (thinking_delta) | Delta.ReasoningContent |
| `content_block_stop` | (no-op, or finalize tool call) |
| `message_delta` | FinishReason + Usage |
| `message_stop` | Close stream |

The parser reads `event:` and `data:` lines (Anthropic uses named events,
unlike OpenAI's bare `data:` lines). Each `data:` payload is JSON with a
`type` field matching the event name.

Key detail: tool call arguments arrive as incremental JSON fragments via
`input_json_delta`. Accumulate them per content-block index and emit the
full arguments string on `content_block_stop`.

## Step 5: Config + wiring

### `internal/config/load.go`

```go
type Provider struct {
    API            string   `yaml:"api,omitempty"` // "openai" (default) or "anthropic"
    BaseURL        string   `yaml:"base_url"`
    // ... existing fields
}
```

### `cmd/yaah/provider_resolve.go`

```go
func makeProvider(p config.Provider) (agent.Provider, bool) {
    r := config.Resolve(p)
    if !isRealKey(r.APIKey) && r.BaseURL == "" {
        return nil, false
    }
    switch r.API {
    case "anthropic":
        return providers.NewAnthropicClient(r.BaseURL, r.APIKey, r.TimeoutSeconds), true
    default:
        return providers.NewOpenAIClient(r.BaseURL, r.APIKey, r.TimeoutSeconds), true
    }
}
```

## Step 6: Prompt caching integration

No code changes needed — `PromptCachingMiddleware` already injects
`CacheControl` on messages. The translation layer passes it through to
Anthropic content blocks. The response parser maps
`cache_read_input_tokens` → `PromptTokensDetails.CachedTokens`, which
flows into:
- `addUsage()` → `LastCachedPromptTokens`
- OTel span attribute `llm.cached_prompt_tokens`
- Compaction's effective-token calculation (cache subtraction)

## Step 7: Tests

- **Translation round-trip**: OpenAI request → Anthropic wire → back.
- **Stream parser**: feed recorded Anthropic SSE payloads, assert
  StreamChunk sequence matches expected deltas.
- **Tool call accumulation**: multi-fragment `input_json_delta` → single
  complete arguments string.
- **Error mapping**: 429/401/529 responses → classified errors.
- **Cache control passthrough**: messages with CacheControl → wire blocks
  with `cache_control` field.
- **Config**: `api: anthropic` in YAML → AnthropicClient created.

## Out of scope

- Anthropic-specific features not in the OpenAI shape (PDF input, images,
  citations, batches API).
- Extended thinking budget configuration (`thinking.budget_tokens`) — can
  be added later as a provider-level config field.
- Model listing (`/v1/models` equivalent) — Anthropic has no list endpoint;
  models are configured explicitly.

## Verification

1. `go build ./...` — compiles clean.
2. `go test ./internal/providers/... -v` — all translation + stream tests pass.
3. `go vet ./...` && `staticcheck` — clean.
4. Manual: configure `api: anthropic` with a real key, run `yaah "hello"`,
   confirm streaming works and `cached_tokens` appears in trace on turn 2+.
5. Manual: enable `prompt_caching: true`, confirm `cache_control` blocks
   appear in the request (verbose trace) and `cache_read_input_tokens > 0`
   on subsequent turns.
