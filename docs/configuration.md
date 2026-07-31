# Configuration

Everything lives in `~/.yaah/config.yaml` (or `$YAAH_HOME/config.yaml`).
Environment variables referenced as `${VAR_NAME}` are substituted at load
time. Missing sections fall back to sensible defaults. On first run, a
scaffold is written automatically.

This file used to be smaller. I'm getting complicated. Sorry about that.

## Full example

```yaml
# ── Providers ──────────────────────────────────────────────────
providers:
  deepseek:
    name: DeepSeek
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
  anthropic:
    api: anthropic                     # native Anthropic Messages API
    name: Anthropic
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama
    timeout: 0                       # 0 = no timeout (slow local models)
  openrouter:
    name: OpenRouter
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    models:                          # limit available models
      - meta-llama/llama-4-maverick

# ── Agents ─────────────────────────────────────────────────────
agents:
  default:                           # the main agent (me!)
    provider: deepseek
    model: deepseek-v4-pro
    small_model: deepseek-v4-flash    # cheaper model for compaction
    max_iterations: 50                # hard loop cap
    max_turns: 5                      # soft cap that strips tools at N (0 = off)
    context_window: 128000
    approval: ask                     # ask | allow | deny
    max_inline_tools_per_turn: 0      # 0 = unlimited
    estimate_factor: 1.3              # token estimate multiplier (0 = default 1.3)

    # Compaction tuning — fractions of context_window that trigger summarisation.
    compaction_threshold: 0.5
    raw_compaction_threshold: 0.5     # triggers on raw prompt tokens (ignores cache)
    compact_max_messages: 50          # force compaction above N messages (0 = off)

    # Session directives — injected into all agent prompts.
    directives:
      - "always run tests after implementation"

    # Loop detection — halt when the same tool+args+result hash repeats.
    loop_detect_count: 5              # identical calls to trigger halt
    loop_detect_window: 10            # sliding window size

    # Provider resilience — retry transient errors with backoff.
    max_retries: 2                    # 0 = no retries
    retry_backoff_secs: 1             # base backoff (exponential)

    # Concurrency and caching.
    max_tool_concurrency: 8           # 0 = unlimited
    prompt_caching: false             # Anthropic cache-control breakpoints
    reasoning_protect_turns: 2        # preserve reasoning in N recent turns

  subagent:                           # my team
    provider: deepseek                # override provider (default: inherit)
    model: deepseek-v4-flash          # override model (default: inherit)
    max_concurrency: 3                # simultaneous sub-agents per turn
    default_timeout: 120              # seconds
    default_max_turns: 0              # 0 = unlimited
    output_limit: 51200               # bytes cap on sub-agent reports
    json_mode: false                  # force structured output
    roles:                            # per-role overrides (defaults live in the role files)
      analyst:
        timeout: 240
        max_iterations: 30
      developer:
        timeout: 300
        max_iterations: 40
      reviewer:
        timeout: 240
        max_iterations: 25
      tester:
        timeout: 300
        max_iterations: 30

  fallback:                           # optional — try on primary failure
    provider: ollama
    model: llama3.2

  quality_gates:                      # auto-validate sub-agent output
    developer: [tester]               # after developer completes, dispatch tester

  middleware:
    enabled:                          # explicit pipeline order
      - steer
      - followup
      - compaction
      - approval
      - loop_detection
    # disabled:                       # remove from default pipeline
    #   - approval

# ── Observability ──────────────────────────────────────────────
observability:
  otel:
    enabled: false
    endpoint: localhost:4317          # OTLP gRPC endpoint
    service_name: yaah
    traces: true                      # emit trace spans
    metrics: false                    # emit OTLP metrics
    verbose: false                    # record full conversations (debug)

# ── Hooks ──────────────────────────────────────────────────────
hooks:
  dir: ~/.yaah/hooks                  # JSONL event log (off by default)

# ── Editor ─────────────────────────────────────────────────────
editor: code --wait                   # overrides $EDITOR and $VISUAL
```

## Provider reference

At least one provider is required. Each needs a `base_url` and an `api_key`.

| Field | Default | Description |
|---|---|---|
| `api` | `openai` | API protocol: `openai` (default) or `anthropic` (native Messages API) |
| `base_url` | (required) | API endpoint (OpenAI-compatible or Anthropic Messages) |
| `api_key` | — | Supports `${ENV_VAR}` substitution |
| `name` | map key | Display name shown in CLI/TUI |
| `models` | — | Limit available models (empty = all from `/models`; n/a for Anthropic) |
| `timeout` | 120 | HTTP request timeout in seconds (0 = no timeout) |

## Agent reference

**`agents.default`** — the main agent loop:

| Field | Default | Description |
|---|---|---|
| `provider` | first alphabetically | Provider from `providers` |
| `model` | — | Model for the main agent |
| `small_model` | — | Cheaper model for context compaction |
| `max_iterations` | 50 | Safety cap on loop turns |
| `max_turns` | 0 (off) | Soft cap; tools are stripped at this iteration, forcing a final answer |
| `context_window` | — | Token budget (0 = disabled) |
| `approval` | `ask` | `allow`, `ask`, or `deny` |
| `max_inline_tools_per_turn` | 0 (unlimited) | Cap inline tool calls per turn |
| `estimate_factor` | 1.3 | Token estimate multiplier for preflight compaction |
| `compaction_threshold` | 0.5 | Fraction of context_window that triggers LLM summarisation (effective tokens after cache subtraction) |
| `raw_compaction_threshold` | 0.5 | Same as above but ignores cached tokens; guards latency |
| `compact_max_messages` | 0 (off) | Force compaction when message count exceeds N, regardless of token estimates |
| `directives` | — | Session-level policy statements injected into all agent prompts |
| `loop_detect_count` | 5 | Identical tool calls (hash) within the window that trigger a hard halt |
| `loop_detect_window` | 10 | Sliding window size (in turns) for loop detection |
| `max_retries` | 0 (off) | Retry count on transient provider errors |
| `retry_backoff_secs` | 1 | Base backoff seconds (exponential growth) |
| `max_tool_concurrency` | 0 (unlimited) | Cap concurrent tool goroutines per turn |
| `prompt_caching` | `false` | Inject Anthropic `cache_control` breakpoints (requires `api: anthropic`) |
| `reasoning_protect_turns` | 2 | Preserve reasoning on N recent turns in provider requests |

Sub-agents inherit all of the above (except `max_turns`, which they override via
`default_max_turns` below) — making `compaction_threshold`, `loop_detect_count`,
and `max_retries` consistent across the whole team.

**`agents.subagent`** — team configuration:

| Field | Default | Description |
|---|---|---|
| `provider` | `default.provider` | Provider for sub-agents |
| `model` | `default.model` | Model for sub-agents |
| `max_concurrency` | 3 | Max simultaneous `spawn_subagent` calls |
| `default_timeout` | — | Default seconds per sub-agent (0 = none) |
| `default_max_turns` | 0 (unlimited) | Default soft turn cap |
| `output_limit` | 51200 | Byte cap on sub-agent reports |
| `json_mode` | false | Force structured JSON output |
| `roles.<name>.timeout` | — | Per-role timeout override |
| `roles.<name>.max_iterations` | — | Per-role iteration cap |
| `roles.<name>.max_turns` | — | Per-role turn cap |
| `roles.<name>.provider` | — | Per-role provider override |
| `roles.<name>.model` | — | Per-role model override |
| `roles.<name>.context_window` | — | Per-role context window (halved from parent if unset) |
| `roles.<name>.max_concurrency` | — | Per-role concurrency cap |

**`agents.fallback`** — fallback on transient errors (429, 503):

| Field | Default | Description |
|---|---|---|
| `provider` | — | Fallback provider name |
| `model` | — | Fallback model name |

**`agents.middleware`** — control the pipeline. Set `enabled` for an
explicit order. Set `disabled` to remove from the default pipeline
(`steer → followup → compaction → soft_prune → approval → tool_concurrency → loop_detection → staleness`).

| Middleware | Default | Purpose |
|---|---|---|
| `steer` | on | High-priority mid-turn steering input |
| `followup` | on | Queued between-turn messages, coalesced |
| `compaction` | on | LLM-powered context summarization |
| `soft_prune` | on | Elide stale tool-output content (no LLM) |
| `approval` | on | Gate dangerous tools |
| `loop_detection` | on | Halt stuck loops |
| `staleness` | on | Annotate sub-agent results when context shifted |
| `permission` | off | Path-pattern allow/deny rules |
| `tool_concurrency` | off | Cap concurrent tool goroutines |
| `sub_agent` | off | Enforce sub-agent depth limits |
| `prompt_caching` | off | Anthropic cache-control breakpoints |

## Observability reference

```yaml
observability:
  otel:
    enabled: false
    endpoint: localhost:4317     # OTLP gRPC endpoint
    service_name: yaah
    traces: true                 # emit trace spans (default: true)
    metrics: false               # emit OTLP metrics (default: false)
    verbose: false               # record full conversations + summaries
```

## Hooks reference

```yaml
hooks:
  dir: ~/.yaah/hooks             # JSONL event log (off by default)
```

Events: `session.start`, `session.end`, `turn.start`, `tool.start`,
`tool.end`, `conflict.detect` — with timestamps, model, tool results, and
durations.

## Editor reference

```yaml
editor: code --wait              # overrides $EDITOR and $VISUAL
```

Resolution order: `editor` field → `$EDITOR` → `$VISUAL` → `vi`.
