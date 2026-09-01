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
  copilot:
    api: copilot                     # GitHub Copilot (OpenAI-compatible)
    name: GitHub Copilot
    api_key: ${GITHUB_TOKEN}
    models:
      - gpt-4o

# ── Agents ─────────────────────────────────────────────────────
agents:
  default:                           # the main agent (me!)
    provider: deepseek
    model: deepseek-v4-pro
    small_model: deepseek-v4-flash    # cheaper model for compaction
    max_iterations: 50                # hard loop cap
    max_turns: 5                      # soft cap that strips tools at N (0 = off)
    context_window: 1048576
    approval: ask                     # ask | allow | deny
    workspace_ask: false              # with --workspace: prompt before denying out-of-bounds access
    max_inline_tools_per_turn: 0      # 0 = unlimited
    estimate_factor: 1.3              # token estimate multiplier (0 = default 1.3)

    # Compaction tuning — fractions of context_window that trigger summarisation.
    compaction_threshold: 0.5
    raw_compaction_threshold: 0.5     # triggers on raw prompt tokens (ignores cache)
    compact_max_messages: 50          # force compaction above N messages (0 = off)

    # Session directives — injected into all agent prompts.
    directives:
      - "always run tests after implementation"

    # Execution tracing via Shepherd — records every tool call to a durable,
    # inspectable trace store. Active when shepherd_trace is in the pipeline.
    shepherd_trace_dir: ~/.yaah/traces   # default, optional

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
    default_min_turns: 0              # global turn floor; 0 = none
    output_limit: 51200               # bytes cap on sub-agent reports
    json_mode: false                  # force structured output
    roles:                            # per-role overrides (defaults live in the role files)
      analyst:
        timeout: 240
        max_iterations: 30
      developer:
        timeout: 300
        max_iterations: 40
        min_turns: 6                  # per-call overrides cannot go below
      reviewer:
        timeout: 240
        max_iterations: 25
        max_turns: 12
        min_turns: 8
        min_iterations: 10            # iteration floor
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
    endpoint: localhost:4318          # OTLP HTTP endpoint (SigNoz: localhost:4318)
    service_name: yaah
    traces: true                      # emit trace spans
    metrics: false                    # emit OTLP metrics
    verbose: false                    # record full conversations (debug)

# ── Hooks ──────────────────────────────────────────────────────
hooks:
  dir: ~/.yaah/hooks                  # JSONL event log (off by default)

# ── Editor ─────────────────────────────────────────────────────
  editor: code --wait                   # overrides $EDITOR and $VISUAL

# ── Embeddings ───────────────────────────────────────────────────
embedding:
  provider: lmstudio                    # provider from the providers map
  model: text-embedding-nomic-embed-text-v1.5  # embedding model name
```

## Provider reference

At least one provider is required. Each needs a `base_url` and an `api_key`.

| Field | Default | Description |
|---|---|---|
| `api` | `openai` | API protocol: `openai` (default), `anthropic` (native Messages API), or `copilot` (GitHub Copilot, OpenAI-compatible) |
| `base_url` | (required) | API endpoint (OpenAI-compatible or Anthropic Messages) |
| `api_key` | — | Supports `${ENV_VAR}` substitution |
| `name` | map key | Display name shown in CLI/TUI |
| `models` | — | Limit available models. Each entry is a plain name or `{name: …, thinking: true/false}` to override thinking detection (empty = all from `/models`; n/a for Anthropic) |
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
| `context_window` | 1048576 | Token budget; caps the model's resolved window (0 = disabled) |
| `approval` | `ask` | `allow`, `ask`, or `deny` |
| `workspace_ask` | `false` | With `--workspace`: prompt before denying out-of-bounds file access instead of hard-rejecting |
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
| `wrap_up_turns` | 1 | Inject a wrap-up notice N turns before the soft cap (negative = off) |
| `tool_result_max_lines` | 500 | Truncate tool results to N lines |
| `tool_result_max_bytes` | 20480 | Truncate tool results to N bytes |
| `prune_protect_tokens` | 2000 | Recent tool-output tokens shielded from soft-prune |
| `prune_min_reclaim` | 400 | Minimum tokens required to commit a soft-prune |
| `prune_min_turns` | 1 | Recent turns always kept by soft-prune |

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
| `stuck_child_timeout` | 60 | Seconds without a heartbeat before a sub-agent is force-cancelled (0 = off) |
| `default_max_turns` | 0 (unlimited) | Default soft turn cap |
| `default_min_turns` | 0 (none) | Global turn floor — no per-call override or role budget may drop below it |
| `output_limit` | 51200 | Byte cap on sub-agent reports |
| `json_mode` | false | Force structured JSON output |
| `roles.<name>.timeout` | — | Per-role timeout override |
| `roles.<name>.max_iterations` | — | Per-role iteration cap |
| `roles.<name>.min_iterations` | 0 (none) | Per-role iteration floor; beats per-call overrides and the role file's floor |
| `roles.<name>.max_turns` | — | Per-role turn cap |
| `roles.<name>.min_turns` | 0 (none) | Per-role turn floor; beats per-call overrides and the role file's floor |
| `roles.<name>.provider` | — | Per-role provider override |
| `roles.<name>.model` | — | Per-role model override |
| `roles.<name>.context_window` | — | Per-role context window (halved from parent if unset) |
| `roles.<name>.max_concurrency` | — | Per-role concurrency cap |
| `roles.<name>.output_limit` | — | Per-role report byte cap |
| `roles.<name>.json_mode` | — | Per-role structured-output toggle |
| `roles.<name>.directives` | — | Per-role directives injected into that role's prompt |
| `roles.<name>.stuck_child_timeout` | — | Per-role stuck-child timeout |

**Sub-agent budget resolution** — each dispatch resolves two dimensions
(iterations = hard loop cap; turns = soft cap that strips tools and forces a
final answer). Resolution is a pure function in `internal/agent/budget`:

| Precedence | Iterations | Turns |
|---|---|---|
| 1 | per-call `max_iterations` (clamped down to the role file's max) | per-call `max_turns` |
| 2 | `roles.<name>.max_iterations` (bypasses the role-file ceiling) | `roles.<name>.max_turns` |
| 3 | role file `max_iterations` | role file `max_turns` |
| 4 | builtin fallback 25 | `default_max_turns`, else derived `iterations - 1` |

Floors apply after the pick: `min_iterations` (config, then role file) and
`min_turns` (config, then role file, then `default_min_turns`) raise either
dimension, including against per-call overrides. A floored turn budget that
reaches the iteration cap *grows* iterations (headroom) instead of being cut —
floors never shrink the other dimension. The schema maximum (50) bounds the
final budget. Unfloored turns keep the historical clamp, so
`max_iterations: 1` alone can still force a deliberate cheap probe.

Validation rejects negative floors, `min > max` within a role config, and
floors unsatisfiable under the schema ceiling; role files clamp with a warning
instead of failing startup. Effective budgets and their sources appear on
sub-agent spans (`subagent.budget.*`), in `list_subagents`, and in
`MaxIterationsError`.

**`agents.fallback`** — fallback on transient errors (429, 503):

| Field | Default | Description |
|---|---|---|
| `provider` | — | Fallback provider name |
| `model` | — | Fallback model name |

**`agents.middleware`** — control the pipeline. `enabled` is
**additive**: listed middleware are added to the default pipeline
(duplicates are collapsed). `disabled` removes middleware from the
union. The default pipeline is
`steer → followup → compaction → soft_prune → approval → inline_limit → tool_concurrency → loop_detection → conflict_detect`.

| Middleware | Default | Purpose |
|---|---|---|
| `steer` | on | High-priority mid-turn steering input |
| `followup` | on | Queued between-turn messages, coalesced |
| `compaction` | on | LLM-powered context summarization |
| `soft_prune` | on | Elide stale tool-output content (no LLM) |
| `approval` | on | Gate dangerous tools |
| `inline_limit` | on | Cap tool calls dispatched per turn (0 = unlimited) |
| `loop_detection` | on | Halt stuck loops |
| `conflict_detect` | on | Flag files touched by multiple sub-agents |
| `permission` | off | Path-pattern allow/deny rules (auto-added when parent rules exist for sub-agents) |
| `tool_concurrency` | on | Cap concurrent tool goroutines |
| `prompt_caching` | off | Anthropic cache-control breakpoints — include via `agents.default.prompt_caching: true`; naming it in `enabled` also works |

## Observability reference

```yaml
observability:
  otel:
    enabled: false
    endpoint: localhost:4317     # OTLP HTTP endpoint (OpenObserve: localhost:5080 — see docs/otel-setup.md)
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

## Embeddings reference

Semantic memory search uses vector embeddings via any OpenAI-compatible
`/v1/embeddings` endpoint (LM Studio, Ollama, llama.cpp, or cloud providers).

| Field | Default | Description |
|---|---|---|
| `provider` | — | Provider name from the `providers` map. Its `base_url` is used for the embeddings endpoint. When empty, semantic search is disabled. |
| `model` | — | Embedding model name sent to `/v1/embeddings` (e.g. `text-embedding-nomic-embed-text-v1.5` for LM Studio, `nomic-embed-text:latest` for Ollama, `local` for llama-server) |

Each `memory_add` embeds synchronously so entries are searchable immediately.
Messages are embedded asynchronously during the agent loop so persist never
blocks. A startup reconciler backfills embeddings for any entries created
before the embedding provider was configured.

Memory search tries cosine-similarity vector search first and falls back to
FTS5 keyword search. Results are scored and sorted.

## Editor reference

```yaml
editor: code --wait              # overrides $EDITOR and $VISUAL
```

Resolution order: `editor` field → `$EDITOR` → `$VISUAL` → `vi`.
