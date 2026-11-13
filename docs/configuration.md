# Configuration

yaah reads `~/.yaah/config.yaml` (or `$YAAH_HOME/config.yaml`). Missing
sections fall back to built-in defaults; a scaffold is written on first run.
`${VAR_NAME}` references are substituted in provider connection fields and
workspace containerd fields when the value is resolved, not for arbitrary
config strings.

## Full example

```yaml
# ── Providers ──────────────────────────────────────────────────
providers:
  deepseek:
    name: DeepSeek
    base_url: https://api.deepseek.com
    api_key: ${DEEPSEEK_API_KEY}
  anthropic:
    api: anthropic                     # native Anthropic Messages API
    name: Anthropic
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}
    max_tokens: 8192                   # optional output cap
    # reasoning_replay: auto           # auto (default) | omit | text — auto replays
                                       # unsigned thinking as a thinking block on
                                       # Anthropic-compatible endpoints and omits it on
                                       # api.anthropic.com (which validates signatures);
                                       # text can make reasoning models echo their
                                       # chain-of-thought into the visible answer
  zai:
    api: anthropic                     # Anthropic-compatible endpoint
    name: ZAI
    base_url: https://api.z.ai/api/anthropic
    api_key: ${ZAI_API_KEY}
    max_tokens: 16384                  # room for reasoning models
  ollama:
    name: Ollama
    base_url: http://localhost:11434/v1
    api_key: ollama
    timeout: 0                         # 0 = no timeout
  openrouter:
    name: OpenRouter
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    models:                            # limit the model list
      - meta-llama/llama-4-maverick

# ── Agents ─────────────────────────────────────────────────────
agents:
  default:
    provider: deepseek
    model: deepseek-v4-pro
    compaction_model: deepseek-v4-flash # context compaction model
    max_iterations: 50                 # hard loop cap
    max_turns: 0                       # 0 = no soft tool-turn cap
    context_window: 1048576            # 0 = use the model's known window
    approval: ask                      # ask | allow | deny
    mcp_approval: ask                  # ask | allow | deny for MCP tools
    mode: full                         # full | read-only — the session mode axis default
    workspace_ask: false               # prompt before denying out-of-workspace paths
    idle_wake_turns: true              # a watch may start a turn while idle
    max_inline_tools_per_turn: 0       # 0 = unlimited
    estimate_factor: 1.3               # token estimate multiplier (0 = 1.3)

    # Tool belt: the tools this session may call. Every tool the model
    # does not use still ships its schema on every request, so a lane
    # that needs only a few tools names them and the rest never reach
    # the wire. Naming tools is narrow-only: the list is intersected
    # with the registry, and a name the registry refuses (platform
    # rules, unknown tool) is skipped with a warning, not added.
    # Omit the key (or leave it empty) for the curated orchestrator
    # default — the general-purpose belt (see agents.default in the
    # reference below for what it withholds). Use ["*"] or ["all"] for
    # the full registry surface.
    # The belt covers the main session; a sub-agent's tools come from
    # its role file. A tool the belt excludes is never registered, and
    # the session is told which ones are missing. MCP-served tools are
    # admitted at registration: a configured mcp_servers entry is
    # itself the opt-in, so this list cannot silently drop a server's
    # tools (name them to narrow them back out on purpose). The
    # curated default folds the file cluster and the analysis/net
    # families behind the toolbelt loader (one stub line each,
    # mountable mid-session); an explicit list that names folded tools
    # keeps them always on, one that names toolbelt mounts them on
    # demand, and one that omits toolbelt keeps the session as
    # configured.
    tools: [read, write, edit, bash, grep, glob, ls, file_info]

    compaction_threshold: 0.5          # fraction of context_window
    raw_compaction_threshold: 0.5      # ignore cached tokens
    compact_max_messages: 0            # force compaction above N messages
    consolidate_turns_interval: 0      # 0 = off; nudge the consolidator every N turns
    consolidate_iters_interval: 0      # 0 = off; nudge every N tool results
    consolidate_deep_every: 1          # run the deep fork every Nth nudge

    directives:                        # orchestrator prompt policy
      - "always run tests after implementation"

    shepherd_trace_path: ""             # "" = ~/.yaah/trace.db; tracing is always on

    retained_repo_path: ""             # "" = cwd
    retained_worktree_root: ""         # "" = ./.yaah/shepherd-worktrees
    retained_worktree_bootstrap: ""    # e.g. "npm ci"
    turn_checkpoint_max: 0             # 0 = unlimited live checkpoints
    max_turn_restores: 3               # 0 = default 3

    loop_detect_count: 5
    loop_detect_window: 10
    intent_steer_after: 3              # same-plan turns before the model is steered
    intent_halt_after: 8               # same-plan turns before the run halts
    evidence_guard_nudge: 6            # 0 = default 6; negative = disabled
    evidence_guard_halt: 0             # 0 = never halt; e.g. 16
    max_retries: 2
    retry_backoff_secs: 1
    max_tool_concurrency: 8            # 0 = unlimited
    prompt_caching: false
    reasoning_protect_turns: 2

    supervisor:                        # active when tracing is enabled
      enabled: true
      halt_root: false
      rules:
        destructive_guard:
          enabled: true
        high_error_rate:
          threshold: 0.5
          window: 10
        stuck_detection:
          repeat_threshold: 3

  subagent:
    provider: ""                       # "" = inherit the orchestrator
    model: ""                          # "" = inherit the orchestrator
    max_concurrency: 3                 # 0 = unlimited
    default_timeout: 0                 # seconds; 0 = none
    default_max_turns: 0               # 0 = derive iterations - 1
    default_min_turns: 0               # global floor; 0 = none
    output_limit: 51200                # bytes; 0 = built-in 50 KiB cap
    json_mode: false
    mode: retained                     # retained | shared; per-role routing
                                       # comes from each role's `output` field
    world: auto                        # auto | worktree | container
                                       # (see shepherd-parity.md)
    # grants:                            # narrow the dispatch world's writes
    #   - path: src                      # world-relative, disjoint roots
    #     access: readwrite
    #   - path: docs
    #     access: readonly
    containment: auto                  # auto | off | paddock
    network: allow_all                 # allow_all | deny_all | broker
    network_hosts: []                  # broker allowlist; empty = admit nothing
    roles:
      analyst:
        timeout: 600
        max_iterations: 50             # schema hard cap (role file asks for 75)
      developer:
        timeout: 900
        max_iterations: 50             # schema hard cap (role file asks for 75)
        min_turns: 6
        turn_checkpoints: false
      reviewer:
        timeout: 480
        max_iterations: 25
        max_turns: 12
        min_turns: 8
      tester:
        timeout: 300
        max_iterations: 30

  fallback:
    provider: ollama
    model: llama3.2

  quality_gates:
    developer: [tester]

# ── MCP servers ────────────────────────────────────────────────
mcp_servers:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    env:
      LOG_LEVEL: info
  remote:
    url: http://127.0.0.1:3000/mcp
    transport: http

# ── Workspace ──────────────────────────────────────────────────
workspace:
  backend: local                       # local | containerd
  root: /workspace                     # containment root inside the sandbox
  containerd:
    address: /run/containerd/containerd.sock
    namespace: default
    image: docker.io/library/yaah-workspace:latest
    snapshotter: overlayfs
    fifo_dir: ""                       # default <yaah home>/containerd-fifo

# ── Observability ──────────────────────────────────────────────
observability:
  otel:
    enabled: false
    endpoint: localhost:4318           # OTLP/HTTP (SigNoz: 4318)
    service_name: yaah
    traces: true
    metrics: false
    verbose: false

# ── Hooks ──────────────────────────────────────────────────────
hooks:
  dir: ""                              # unset/empty disables JSONL hooks

# ── Editor ─────────────────────────────────────────────────────
editor: code --wait

# ── Embeddings ─────────────────────────────────────────────────
embedding:
  provider: ollama
  model: nomic-embed-text
```

## Provider reference

At least one provider is required. A provider needs a `base_url`. A real
`api_key` (or `auth: oauth` with a stored token from `yaah login`) is optional
for keyless local endpoints.

| Field | Default | Description |
|---|---|---|
| `api` | `openai` | API protocol: `openai` (default), `anthropic` (native or compatible Messages API), or `copilot` (GitHub Copilot, OpenAI-compatible) |
| `auth` | `api_key` | `api_key` or `oauth` |
| `base_url` | (required) | API endpoint |
| `api_key` | — | Supports `${ENV_VAR}` substitution |
| `name` | map key | Display name shown in the CLI and TUI |
| `models` | — | Limit available models. Each entry is a plain name or `{name: …, thinking: true/false}` |
| `timeout` | 0 (no timeout) | HTTP request timeout in seconds; negative values fall back to 120s |
| `max_tokens` | 4096 native, 16384 compatible; unset on `openai` | Output-token cap when a request has none. Native `api.anthropic.com` stays at 4096 for older Claude models; compatible endpoints default to 16384 because reasoning tokens count against the budget. When the model exhausts a known cap before emitting content, the client retries once with a doubled cap, clamped to a conservative known model output limit (and to 65536); providers that do not report a cap are not retried, and the native Anthropic safe floor is never raised |
| `headers` | — | Extra HTTP headers, sent by OpenAI-compatible providers only |
| `supports_cache_control` | `false` | Opts an OpenAI-compatible provider into Anthropic-style `cache_control` breakpoints. `cache_control` is an Anthropic wire concept, not an OpenAI one, so it must be opted into: Mistral and strict OpenAI-compatible endpoints answer `422 extra_forbidden` when the field leaks. Independent of `agents.default.prompt_caching`, which only decides whether breakpoints are stamped. Ignored for `api: anthropic` (always legal there); GitHub Copilot is opted in implicitly |
| `oauth_client_id` / `oauth_scope` / `oauth_domain` | — | OAuth device-flow settings used with `auth: oauth` |

Anthropic-shaped providers report `input_tokens` excluding cache reads. yaah
keeps that provider-shaped value for persisted token totals but displays a
cache-inclusive context size in the TUI and web UI.

OAuth providers are authenticated once with `yaah login <provider>`; the
stored token is used instead of `api_key`. See `yaah login --help` for the
provider-specific defaults.

### Thinking mode

Some models run in a *thinking* mode where the provider requires the model's
reasoning (`reasoning_content`) to be replayed on every subsequent assistant
message. Omitting it fails the request outright with a message like
`the thinking mode must be passed back`.

**Config is the source of truth.** Set it per model:

```yaml
providers:
  deepseek:                             # mapping keyed by provider name
    base_url: https://api.deepseek.com/v1
    models:
      - name: deepseek-flash
        thinking: true      # authoritative — wins over auto-detection
```

Resolution order, first match wins:

1. per-model `thinking: true|false` from config — **authoritative**
2. a built-in registry of known thinking models (`deepseek-r1*`, `deepseek-v4*`,
   `o1-*`, `o3-*`, `qwq-*`, …) — a default, not truth
3. auto-detection from any `reasoning_content` already present in history
4. otherwise off

There is no API that advertises this capability — OpenAI, Anthropic, and
DeepSeek `/models` endpoints return identifiers only — so config is the only
reliable way to declare a model you know to think. The registry exists to spare
the common cases a round trip, not to be authoritative.

If a provider reports thinking mode is required and config did not declare it,
`yaah` aborts with a message naming the model and the `thinking: true` knob
rather than retrying a request that cannot succeed.


## Agent reference

**`agents.default`** — the main agent loop:

| Field | Default | Description |
|---|---|---|
| `provider` | first usable provider | Resolution: explicit value, then a provider prefix in `model`, then the first provider alphabetically |
| `model` | `deepseek/deepseek-v4-pro` | Model for the main agent |
| `agent` | `build` | Persona the session's orchestrator runs as: a built-in (`build`, `plan`) or a role file with `mode: primary`/`all`. Unknown or delegate-only names warn and fall back to `build` at session start. A persona file's `max_iterations`/`max_turns` fill gaps only — an explicit value here wins. See [agents.md](./agents.md) |
| `compaction_model` | `deepseek/deepseek-v4-flash` | Model for context compaction. Unset or unresolvable values fall back to the main model with a warning — summarizing with the main model is slower and produces worse summaries |
| `max_iterations` | 50 | Safety cap on loop turns |
| `max_turns` | 0 | Soft cap; tools are stripped at this iteration and a final-report notice tells the model why. `0` = no soft cap (the derive rule applies only to `agents.subagent.default_max_turns`). In ACP, a turn ended by this cap reports `stopReason: max_turn_requests` |
| `context_window` | 1048576 | Caps the model's resolved window. `0` = use the model's known window, not "disabled" |
| `approval` | `ask` | `allow`, `ask`, or `deny` |
| `mcp_approval` | `ask` | `allow`, `ask`, or `deny` for MCP-served tools |
| `permissions` | — | Permission ruleset applied to every agent: ordered per-tool `allow`/`deny`/`ask` rules over a glob, evaluated last-match-wins. See [Permission rulesets](#permission-rulesets) |
| `mode` | `full` | The session mode axis default: `full` allows mutation, `read-only` withholds the mutating tools (the inspection set only). Independent of the persona — any agent can run in either mode — and switchable mid-session over ACP |
| `tools` | curated orchestrator default | The main session's tool belt. Explicit list is narrow-only (intersected with the registry; refused entries are warned and skipped). Empty/omitted = curated general-purpose belt; `["*"]` or `["all"]` = full registry surface. The curated default carries the working core plus the spine directly, and the `toolbelt` loader for the rest: the file cluster and two rarely-used families are folded to mountable stubs (see [the lazy toolbelt](#the-lazy-toolbelt-toolbelt) below). Withheld outright by the curated default, mountable as the `file_ops` family, and also carried by the developer role (so `delegate` reaches them too): `append`, `bulk_files`, `checksum`, `copy`, `copy_tree`, `find`, `mkdir`, `move`, `move_tree`, `rmdir`, `tail`. Folded by the curated default, mountable as the `analysis` family (`calculate`, `json_query`, `file_info`, `diff`, `bisect`) and the `net` family (`http`, `webfetch`): name them in this list to keep them always on. See `cmd/yaah/orchestrator_belt.go`. Gated feature lanes (`proposal` settlement, the `memory_*` cluster, the Go quintet `go_test`/`go_mod`/`go_outline`/`go_refactor`/`staticcheck`, `apply_out`, the wake pair `wait_for`/`watch`) are belt-admitted and register when their own gate opens — a session that opens the lane keeps its tools; `wait_for`/`watch` register only in interactive sessions (REPL, TUI, ACP, serve) and never reach a one-shot/headless floor. MCP-served tools are admitted at registration: a configured `mcp_servers` entry is itself the opt-in |
| `workspace_ask` | `false` | With `--workspace`: prompt before denying out-of-bounds access |
| `workspace_deny_patterns` | nil (built-in secret defaults) | Globs denied even inside the workspace. `nil` applies `.env`, `*.pem`, `*.key`, SSH keys; `[]` disables the built-ins |
| `idle_wake_turns` | `true` | Allow a watch that fires while an interactive session is idle to start a turn. `false` queues the digest as a follow-up instead; the session-shape gate controls whether `wait_for`/`watch` exist (since yaah-6mi9.2 they register in interactive sessions and never in a one-shot run, belt or no belt) |
| `max_inline_tools_per_turn` | 0 (unlimited) | Cap inline tool calls per turn |
| `estimate_factor` | 1.3 | Token estimate multiplier for preflight compaction |
| `compaction_threshold` | 0.5 | Fraction of `context_window` that triggers summarisation (effective tokens after cache subtraction) |
| `raw_compaction_threshold` | 0.5 | Same, but ignores cached tokens |
| `compact_max_messages` | 0 (off) | Force compaction above N messages |
| `consolidate_turns_interval` | 0 (off) | Every N turns, nudge the consolidator to write durable atoms. Requires the `consolidate` middleware |
| `consolidate_iters_interval` | 0 (off) | Same nudge, counted in tool results |
| `consolidate_deep_every` | 1 | Run the deep consolidator fork on every Nth nudge (0 or 1 = every nudge); intervening nudges take the light path |
| `directives` | — | Policy statements injected into the orchestrator prompt only |
| `shepherd_trace_path` | ~/.yaah/trace.db | Path to the Shepherd trace store **file**. Tracing is always on — this only relocates the store. Facts include the session context (system prompt, cwd, model/config fingerprint), assistant content plus reasoning signature, and tool output bodies capped at 64KiB with truncation markers/spill refs |
| `retained_repo_path` | cwd | Repository retained worktrees are cut from |
| `retained_worktree_root` | .yaah/shepherd-worktrees | Parent directory for retained worktrees |
| `retained_worktree_bootstrap` | — | Command run inside each retained worktree, e.g. `npm ci` |
| `turn_checkpoint_max` | 0 (unlimited) | Live turn checkpoints per run |
| `max_turn_restores` | 3 | Turn-level restores per run |
| `loop_detect_count` | 5 | Identical tool-call hashes within the window that trigger a halt |
| `loop_detect_window` | 10 | Sliding window size for loop detection |
| `intent_steer_after` | 3 | Consecutive same-plan turns (no workspace progress) before an in-conversation steer |
| `intent_halt_after` | 8 | Consecutive same-plan turns before the run halts; must be greater than `intent_steer_after` |
| `evidence_guard_nudge` | 6 | Stale calls before a “no new evidence” steer; negative disables |
| `evidence_guard_halt` | 0 (off) | Stale calls before halting; 0 = never |
| `max_retries` | 0 (off) | Retries on transient provider errors |
| `retry_backoff_secs` | 1 | Base backoff seconds (exponential) |
| `max_tool_concurrency` | 0 (unlimited) | Cap concurrent tool goroutines per turn |
| `prompt_caching` | `false` | Inject Anthropic `cache_control` breakpoints |
| `reasoning_protect_turns` | 2 | Preserve reasoning on N recent turns |
| `wrap_up_turns` | 1 | Inject a wrap-up notice N turns before the soft cap (negative = off) |
| `tool_result_max_lines` | 500 | Truncate tool results to N lines |
| `tool_result_max_bytes` | 20480 | Truncate tool results to N bytes |
| `prune_protect_tokens` | 2000 | Recent tool-output tokens shielded from soft-prune |
| `prune_min_reclaim` | 200 | Minimum tokens required to commit a soft-prune |
| `prune_min_turns` | 1 | Recent turns always kept by soft-prune |
| `supervisor` | enabled when tracing is on | Advisory supervision rules; see the supervision table below |

The 2026-10-29 live recapture and controlled threshold probe are recorded in
[evidence-guard.md](./evidence-guard.md): `nudge=6`, `halt=off`.

Sub-agent inheritance is selective. `max_retries`, `retry_backoff_secs`,
`max_tool_concurrency`, tool-result limits, prune settings, context window,
and turn-checkpoint limits are passed down. `loop_detect_*`, compaction
tuning, `prompt_caching`, `reasoning_protect_turns`,
`max_inline_tools_per_turn`, approval policy, and `directives` are not.
Sub-agents use their own curated pipeline and per-role directives.

**`agents.subagent`** — team configuration:

| Field | Default | Description |
|---|---|---|
| `provider` | inherits | Provider for sub-agents; the global override sits below the per-role config entry and the role file, and above the parent session |
| `model` | inherits | Model for sub-agents; same position in the chain |
| `max_concurrency` | 3 | Max simultaneous `delegate` calls; 0 = unlimited |
| `default_timeout` | 0 (none) | Total wall-clock seconds per sub-agent dispatch, including provider thinking time; 0 = none |
| `stuck_child_timeout` | 60 | Seconds without a heartbeat before a sub-agent is force-cancelled (0 = off) |
| `default_max_turns` | 0 (derive) | Default soft turn cap; 0 derives `iterations - 1` |
| `default_min_turns` | 0 (none) | Global turn floor — no per-call override or role budget may drop below it |
| `output_limit` | 51200 | Byte cap on sub-agent reports; 0 = built-in 50 KiB cap |
| `json_mode` | false | Force structured JSON output |
| `mode` | `retained` | Dispatch lane — `retained` (isolated worktree + proposal) or `shared` (direct to host) |
| `world` | `auto` | Dispatch world — `auto` (per-dispatch container for report and proposal dispatches in a host session when `workspace.containerd.image` is set on Linux, else the worktree, recording any degradation; in a container session, report and proposal lanes use a sibling container; a proposal settles into the session container), `worktree` (never container), `container` (fails closed if unavailable). A proposal seals a GitDelta manifest and destroys its container at seal; a report never seals. Orthogonal to `mode`. See [shepherd-parity.md](./shepherd-parity.md#dispatch-worlds) |
| `roles.<name>.world` | — | Per-role world override (`auto` / `worktree` / `container`); empty inherits `agents.subagent.world` |
| `grants` | `[]` | Per-dispatch write authority — a list of `{path, access}` roots relative to the dispatch world. `readwrite` roots are writable, `readonly` roots are declared but denied writes; roots must be disjoint. Empty = the whole world (legacy). Both the jail and a shaped container lower them; an all-read-only set masks the container workspace |
| `roles.<name>.grants` | — | Per-role grant set; empty inherits `agents.subagent.grants` |
| `containment` | `auto` | Syscall-jail placement — `auto` (jail if the host can, else advisory), `off`/`none`/`advisory`, `paddock`/`jail` (fails closed) |
| `network` | `allow_all` | Egress posture for a confined dispatch — `allow_all`, `deny_all`, or `broker` (allowlist via the session's broker) |
| `network_hosts` | `[]` | The broker's allowlist (exact host names); only meaningful with `network: broker`. Empty = the broker admits nothing; setting hosts with another mode is a validation error |
| `roles.<name>.timeout` | — | Per-role total wall-clock budget in seconds; overrides both the role file and `default_timeout` |
| `roles.<name>.max_iterations` | — | Per-role iteration cap |
| `roles.<name>.min_iterations` | 0 (none) | Per-role iteration floor; beats per-call overrides and the role file's floor |
| `roles.<name>.max_turns` | — | Per-role turn cap |
| `roles.<name>.min_turns` | 0 (none) | Per-role turn floor; beats per-call overrides and the role file's floor |
| `roles.<name>.provider` | — | Per-role provider override, config-authoritative: beats the role file and the global override, loses to a per-call `provider` on `delegate` |
| `roles.<name>.model` | — | Per-role model override, same precedence as `provider` |
| `roles.<name>.context_window` | — | Per-role context window (halved from parent if unset) |
| `roles.<name>.output_limit` | — | Per-role report byte cap; 0 = inherit the sub-agent default |
| `roles.<name>.json_mode` | — | Per-role structured-output toggle |
| `roles.<name>.directives` | — | Per-role directives injected into that role's prompt |
| `roles.<name>.stuck_child_timeout` | — | Per-role stuck-child timeout |
| `roles.<name>.turn_checkpoints` | false | Capture workspace + conversation before each turn and restore on failure (bounded by `max_turn_restores`) |

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
final budget. Unfloored turns are clamped to `iterations - 1`, so
`max_iterations: 1` alone can still force a deliberate cheap probe.

Validation rejects negative floors and `min > max` within a role config, and
rejects turn floors at or above the 50-turn schema ceiling. Iteration floors
above the ceiling are clamped during budget resolution rather than rejected.
Role files clamp with a warning instead of failing startup. Effective budgets and their sources appear on
sub-agent spans (`subagent.budget.*`), in `list_subagents`, and in
`MaxIterationsError`.

**`agents.fallback`** — rotate on auth, billing, or rate-limit/quota failures (401/402/403/429). 503/529 are retried with backoff instead:

| Field | Default | Description |
|---|---|---|
| `provider` | — | Fallback provider name |
| `model` | — | Fallback model name |

### The lazy toolbelt (`toolbelt`)

The curated default belt folds rarely-called tools out of the always-on
floor and advertises them as one-line stubs in the prompt
(`docs/lazy-toolbelt-design.md`): each folded schema would cost ~780
prompt tokens on every call that never uses it. The `toolbelt` tool is
the loader for that surface:

| Family | Members | Notes |
|---|---|---|
| `file_ops` | `append`, `bulk_files`, `checksum`, `copy`, `copy_tree`, `find`, `mkdir`, `move`, `move_tree`, `rmdir`, `tail` | The restore path yaah-6mi9.1 withheld; the developer role still carries them, so `delegate` reaches them too |
| `analysis` | `calculate`, `json_query`, `file_info`, `diff`, `bisect` | Folded from always-on by the curated default (yaah-6mi9.5); name them in `agents.default.tools` to keep them always on |
| `net` | `http`, `webfetch` | Same fold as `analysis` |

- `action: list` shows each family, its mount state, and full
  membership; `action: mount` registers a family for the rest of the
  session.
- Mount is sticky: there is no unmount, and remounting is a no-op.
- Mounts flow through the same admission as startup registration: a
  persona allowlist cannot be widened by a mount, read-only sessions
  withhold the mutating members (a family every member of which is
  withheld fails the mount citing `mode_floor`), and every mounted
  call still passes the per-call permission and approval gates. A
  mount adds schemas, never permission. The mount-time check consults
  the session's *live* mode and persona — a mid-session `session/set_mode`
  flip or role switch changes what a later mount admits — and the
  per-loop advertise-time narrowing remains the floor above it either
  way.
- A mount appends a `yaah.tool.mounted.v1` fact on the session's root
  scope, naming the family, the tools, and any per-member refusals.
- Belt admission of `toolbelt` is the opt-in: the curated default names
  it, an explicit `agents.default.tools` list that omits it keeps the
  session exactly as configured (no stubs, no mounts), and one that
  names it sanctions the widening.
- Sub-agents do not carry the loader: their surface is their role
  profile. A dispatch pattern that needs a family is a role-file
  change.

### Middleware

The middleware pipeline is **fixed**: every built-in middleware runs on
every turn. There is no enable/disable list. Each middleware is either a
safety net (compaction, the evidence/loop/intent guards, and the approval
pipeline, which is the only layer that can prompt), required plumbing (the
steer/followup channels and tracing), or inert until its own knob turns it
on. The orchestrator order is
`steer → followup → compaction → soft_prune → approval → inline_limit → tool_concurrency → shepherd_trace → loop_detection → intent_loop → evidence_guard → conflict_detect → consolidate`.

| Middleware | Purpose |
|---|---|
| `steer` | High-priority mid-turn steering input |
| `followup` | Queued between-turn messages, coalesced |
| `compaction` | LLM-powered context summarization |
| `soft_prune` | Elide stale tool-output content (no LLM); `prune_*` keys tune it |
| `approval` | Gate dangerous tools |
| `inline_limit` | Cap tool calls dispatched per turn (0 = unlimited) |
| `tool_concurrency` | Cap concurrent tool goroutines |
| `shepherd_trace` | Record the session transcript and tool facts |
| `loop_detection` | Halt identical tool+result loops |
| `intent_loop` | Steer (3 turns) and halt (8 turns) when the assistant re-announces the same plan without changing files — catches the paraphrased, read-only loops `loop_detection` cannot see |
| `evidence_guard` | Steer/halt when repeated calls or unchanged re-reads produce no new evidence |
| `conflict_detect` | Flag evidence-backed divergent writes and unknown shell mutations across sub-agents |
| `consolidate` | Cadence-driven memory nudge; inert until `consolidate_turns_interval` or `consolidate_iters_interval` is set |

The two genuinely optional behaviours are controlled by their own keys, not
by a pipeline list:

- `agents.default.prompt_caching: true` opts into Anthropic cache-control
  breakpoints.
- `agents.default.consolidate_turns_interval` / `consolidate_iters_interval`
  enable the memory nudge.

Sub-agent loops run a curated subset (`tool_concurrency`, `shepherd_trace`,
`evidence_guard`, plus `steer` when a supervision channel is wired and
`supervisor_guard` when supervision is active).

### Permission rulesets

**`agents.default.permissions`** (and the same key in a role file's
frontmatter) — ordered per-tool rules refining what the tool belt and the
mode axis already allow:

```yaml
agents:
  default:
    permissions:
      edit: { "*": deny, "*.md": allow }
      bash: { "git push*": ask, "git status*": allow }
      webfetch: deny
```

Each key is a tool name; its value is either a bare effect (every call of
that tool) or a map of glob → effect. Rules keep their source order and the
**last matching rule wins**, so put specific rules after catch-alls. Globs
match the resource the call acts on: the **command string** for `bash`/
`powershell`, the **target path or URL** for everything else (`filePath`,
`path`, `to`, or `url` — whichever the tool carries), tool-name-only when
the call has no target. `*` and `**` cross directory separators; regex
metacharacters are literal.

Resolution order, each stage able to narrow but none to widen the one
before it: the **mode axis** (in `read-only` mode a mutating tool stays
withheld no matter what any rule says) → the **tool belt** → the ruleset →
the **per-call danger classification** → the `approval` posture. The global
rules apply to every agent; when the session's persona also declares
`permissions:`, its rules are appended after the global ones, so the persona
wins wherever both name the same tool and resource.

Things the rules deliberately do **not** do:

- `allow` does **not** pre-approve a dangerous call. It only overrides an
  earlier rule of the same ruleset; a dangerous call still goes through the
  `approval` posture, and in `read-only` mode it stays withheld.
- `bash` deny rules are prefix globs on the **raw command string** —
  advisory, not a security boundary. `"git push*": deny` does not catch
  `git -C . push` or a quoted/variable-obfuscated command; use the mode
  axis or the tool belt when you need a hard boundary.
- `ask` routes through the approval prompt of the running surface (REPL,
  TUI, web, ACP). If the `approval` middleware is disabled, an `ask` rule
  degrades to allow; `deny` rules are enforced regardless. A **sub-agent
  dispatch is the exception**: it installs no approval middleware and nobody
  is attached to it, so an `ask` rule is refused there rather than allowed —
  the refusal names the rule and the missing prompt. A subagent-capable role
  file that declares `ask` warns at load, and `headless: deny` in its
  frontmatter states that the refusal is the intent (the only accepted
  value; primary-only roles are exempt, since a persona's session has a
  prompt).

An unknown effect (`allo` instead of `allow`) fails the config load or the
role file's parse — a typo never silently widens or narrows.

**Sub-agents (delegate inheritance).** A `deny` rule on a file-mutating
tool (`write`, `edit`, `append`, `delete`, `patch`, `move`/`move_tree`,
`copy`/`copy_tree`, `mkdir`, `rmdir`, `bulk_files`, `replace`,
`apply_out`) propagates as a ceiling to every `delegate` dispatch,
foreground and background: the sub-agent's own loop refuses the call even
though the dispatch runs without approval prompts. The dispatched role's
own `permissions:` apply too, but the inherited ceiling is appended after
them — a role file can narrow further, never widen what the parent denied.
That composition cascades: a role file's own mutating denies become part
of the ceiling any deeper dispatch from that role carries. Shell rules
(`bash`, `powershell`) deliberately do **not** propagate: a broad shell
deny inherited by a sub-agent that needs that command fails one level
removed from the rule that caused it (the Kilo #11523 lesson); restrict
shells on the parent, or scope the sub-agent's tool belt. `ask` rules do
not propagate either — a sub-agent has no one to ask — and an `ask` rule in
the dispatched role's *own* ruleset is refused at dispatch for the same
reason, naming the rule and the missing prompt; declare `headless: deny` in
the role file to record that the refusal is intended. Note for the
move-family tools that carry both a source and a destination: a resource
rule matches the **source** path, so `move: { "docs/*": deny }` blocks
moves *out of* `docs/`, not into it.

**Hot reload.** `agents.default.permissions` is re-read when `config.yaml`
changes: every run boundary (each prompt turn) checks the file's content
hash, and a changed file's rules apply from the next turn on — the same
contract role files have (new work picks up the change; an in-flight run
finishes with the rules it started with). Exactly this one stanza reloads;
everything else in the config (model routing, budgets, postures, mode)
stays frozen at session start, and mode and approval keep their own
mid-session switches. A config that fails to re-load — a broken file, a
stanza the validator rejects, or a file that disappears after having been
read — keeps the last-known rules and warns on stderr. Deleting the file
mid-session is not how a rule is revoked: it takes the running rules with
it, and they return when the file does. Only a config that has never
existed is the empty ruleset a fresh session would see.

**Persisted "always" approvals.** An `allow-always` answer on the ACP
approval bridge persists project-scoped (the canonical workspace root):
a later session in the same workspace consults the saved row instead of
re-asking. The consult is a rule like any other — a config `deny` or
`allow` naming the same tool and resource still wins, and the consult is
only read in the ask branch, so it can never override a `deny`. A config
`ask`, by contrast, is exactly what a saved row is there to lift.

Credential-shaped targets are carved out on both sides: a persisted
"always" never lifts an ask for a dotenv family file (`.env`,
`.env.local`, `prod.env`, …), direnv's `.envrc`, or an OpenSSH private key
(`id_rsa`, `id_ed25519`, …) on a file tool — the write family, and the
content readers (`read`, `tail`, `grep`, `json_query`, `diff`,
`go_outline`). `allow-always` on one behaves as `allow-once`: the live call
proceeds, nothing is written to the store, and the next sensitive call asks
again — including when the same tool was already always-allowed for an
ordinary file earlier in the session. `.env.example` is exempt (a published
template, not a secret).

To grant a sensitive file for real, write an explicit permission rule —
that is a decision made in config, reviewable in config, and revocable in
config; a button click is none of those. Note what the rule achieves: it
settles the **read** family outright, but a mutating call answers to the
approval posture as well, and the verdict is the stricter of the two
(`permissions.Stricter`). With `approval: ask` — the default —
`write: { ".env": allow }` still asks on every write, and the carve-out
means that ask can never be remembered. Silencing it for the write family
takes a non-asking posture, which is a session-wide decision rather than a
per-file one.

### Harness path protection

Role files, skills, plans, and yaah's own configuration decide what an agent
may do — a role file names the toolset, provider, budgets and permission
rules a dispatch runs under. They are therefore **not** writable as ordinary
workspace content. The guard lives in the path resolver
(`internal/tools/substrate/harness.go`), not in the ruleset above, so no
rule, persona, or approval setting can widen it, and it is checked after
symlink resolution and workspace containment.

| Path | Agent write |
|---|---|
| `~/.yaah/**` (`$YAAH_HOME`, config and state) | **denied** |
| `~/.agents/roles/**`, `~/.agents/skills/**` | **denied** |
| `~/.agents/plans/**` | ask |
| `<workspace>/.agents/roles/**`, `.agents/skills/**` | ask |
| `<workspace>/.agents/plans/**`, `.yaah/**` | ask |
| anything else | unaffected |

`ask` reaches the same approval surface as a workspace-access prompt (TUI,
web, ACP, or a stdin prompt in plain REPL). With no surface attached — a
headless run, or a sub-agent whose dispatch has no bridge — the write is
refused instead of silently allowed. A grant is cached per path for the
session, so a create followed by an edit does not prompt twice.

Reads are never gated: inspecting a role file or the config is how an agent
explains its own behavior. The `plan` tool remains the sanctioned writer for
plan files (plan mode is built around it); a direct `write`/`edit` to a plan
file goes through the ask above. `apply_out` refuses a container delta that
would land any protected path, because the accept prompt is one batch
decision and cannot show a per-file subset. `bash`/`powershell` are not
covered — as with `--workspace`, the guard bounds file tools, not shell
commands.

## Workspace reference


**`workspace`** — where the session's tools execute. Without the stanza
everything runs on the host filesystem, as it always has.

| Field | Default | Description |
|---|---|---|
| `backend` | `local` | `local` runs tools on the host; `containerd` runs them inside a per-session containerd sandbox |
| `root` | `/workspace` | Containment root inside the sandbox. Must be absolute and not `/` when a container sandbox is in effect (`backend: containerd`, or `containerd.image` set in a local session) — config validation refuses otherwise |
| `containerd.address` | containerd's own default | containerd socket or endpoint; supports `${ENV_VAR}` substitution |
| `containerd.namespace` | `default` | containerd namespace for the session's containers |
| `containerd.image` | (required for `containerd`) | Base image. Must satisfy the tool contract (sh, GNU stat/find, git, rg, base64, Go toolchain) — verified at startup. In a `local` session, setting it also enables per-dispatch container worlds for both report and proposal lanes under `world: auto` (see below) |
| `containerd.snapshotter` | `overlayfs` | containerd snapshotter plugin |
| `containerd.fifo_dir` | `<yaah home>/containerd-fifo` | Directory the containerd *client* creates the task/exec stdio FIFOs in. containerd's own default (`/run/containerd/fifo`) is root-only, so the yaah default keeps unprivileged sessions working; must be absolute. Avoids a predictable name under the shared temp dir, which another local user could pre-create |

The `containerd` stanza is also the per-dispatch container's config: in a
`local` session (where `backend` stays `local`), setting `containerd.image`
lets a **report-lane** sub-agent dispatch run in its own container instead of a
worktree, and gives a **proposal-lane** dispatch the same world. A report
container is created at dispatch time and destroyed afterwards; it never seals.
A host-session proposal container seals a `GitDelta` change manifest before it is
destroyed, and `select`/`apply` settle that manifest onto the host. In a
container session, report and proposal lanes get a sibling container seeded from
the session's captured state. A host-session proposal settles onto the host git
tree; a container-session proposal settles into the **session container** and
leaves the host untouched until `apply_out`. The manifest and basis live under
`.yaah/proposals/<id>/`: host-target proposals rehydrate after a restart,
session-target proposals are session-lifetime (the parent container is gone, so
they are left for audit rather than rebound to a new world). A preflight failure (open, probe, create, bootstrap) degrades
`world: auto` to the worktree in a host session; in a container session a
report falls back to the session container only when that container still
enforces the dispatch's authority (no grants, no `broker`; `deny_all` is
preserved by its loopback-only netns), and a proposal fails closed.
`world: container` fails closed. `agents.subagent.network` is lowered onto the
container's own posture: `allow_all` shares the host namespace, `deny_all` uses
the container's unshared namespace, and `broker` uses a netns-local proxy that
admits `network_hosts`. Under a restrictive posture, `http`/`webfetch` are
omitted from the dispatch because they run in the yaah process outside the
container. Grants are lowered by masking the container workspace read-only and
remounting only the read-write roots; an all-read-only grant set masks the whole
workspace. This path is Linux-only, like the `containerd` backend itself.

The `containerd` backend requires a Linux containerd daemon: config
validation refuses it at load on any other platform. The daemon's socket is
root-owned by default, so a normal user needs one-time group access or every
session fails its startup probe with `connect: permission denied`. The
adapter builds the init spec from the image config through the daemon, and
`fifo_dir` defaults to a user-writable directory. `yaah doctor` reports the
workspace check when the stanza is present. `--workspace` has no effect in
this mode; the container is the containment boundary. See
[features.md](./features.md) for the user-facing behavior.

## Retained worktrees and turn checkpoints

A retained dispatch runs the sub-agent in its **own world** and returns a
sealed proposal; the orchestrator settles it with
`proposal select|apply|release|discard`. A local session defaults to a git
worktree and settlement writes the host tree; with `containerd.image` set,
`auto` may use a container proposal that still settles onto the host. In a
container session the world is a sibling container and settlement writes the
**session container**, leaving the host untouched until `apply_out`. Retained mode is available whenever the
shepherd root scope exists and the lane can provide a world; otherwise use
`agents.subagent.mode: shared` or expect the direct lane.

```yaml
agents:
  default:
    retained_repo_path: ""              # "" = the working directory
    retained_worktree_root: ""
    retained_worktree_bootstrap: ""
    turn_checkpoint_max: 0
    max_turn_restores: 3
```

| Key | Default | Meaning |
|---|---|---|
| `retained_repo_path` | `""` | Repository sub-agent worktrees are cut from, and where per-turn checkpoints operate. Empty uses yaah's working directory. |
| `retained_worktree_root` | `""` | Parent directory for sub-agent worktrees. Empty uses a `.yaah/shepherd-worktrees` directory inside the repository. |
| `retained_worktree_bootstrap` | `""` | Shell command run inside each fresh worktree — see below. |
| `turn_checkpoint_max` | `0` | Live turn checkpoints per sub-agent run; reaching the cap prunes/resets all live checkpoints. `0` = unlimited. |
| `max_turn_restores` | `3` | Turn-level restores per run, so a deterministically failing turn cannot rewind forever. |

### Speculating on a task

N dispatches of the same task issued in one turn run concurrently — one sub-agent
slot each, so `agents.subagent.max_concurrency` bounds the fan-out — and each
seals its own proposal. The orchestrator reads them, settles one, and discards
the rest; the losers' worktrees go with them.

There is deliberately no `attempts` knob on `delegate`. Parallel calls
already fan out, and a tool-level multiplier would duplicate that mechanism while
bypassing the concurrency accounting. What bounds speculation is your budget, not
the tool: three attempts cost three dispatches.

### What turn checkpoints cover

A turn checkpoint captures **workspace state plus the conversation**, so a turn
that ends in a tool-phase error or an exhausted iteration budget can rewind to
the state before it, rather than losing the whole attempt. Checkpoints are
single-use: a restore consumes one, and a fresh checkpoint is taken before the
next turn. `turn_checkpoints` (set per role) enables them, bounded by
`max_turn_restores`.

### Worktree isolation

Every retained dispatch runs in a detached `git worktree` — an isolated child
scope seeded with the parent's state — and the sub-agent's tools operate inside
it. Nothing it writes reaches the parent tree until the proposal is settled
(`select` fast-forwards, `apply` three-way merges, `discard` removes the
worktree).

Two consequences worth knowing:

1. **A worktree contains tracked files only.** `node_modules`, build caches,
   `.env`, and anything else gitignored are absent, so a dispatch that needs them
   cannot build or test. Use `retained_worktree_bootstrap` to recreate them, for
   example `npm ci` or `go mod download`.
2. **Isolation is filesystem-level, not a sandbox.** Worktrees share the host
   process, network, and the repository's object store. Containment is the
   separate `agents.subagent.containment`/`network` axis below.

## Supervision

Supervision is always active: tracing is integral, and the kernel watches every
session's trace. Advisory rule firings are delivered as
steering into the running sub-agent loops.

```yaml
agents:
  default:
    supervisor:
      enabled: true          # master switch (default on when tracing is on)
      halt_root: false       # allow halting the session's root scope
      rules:
        destructive_tool:
          enabled: true
        destructive_guard:
          enabled: true
        high_error_rate:
          enabled: true
          threshold: 0.5
          window: 10
        stuck_detection:
          enabled: true
          repeat_threshold: 3
```

| Key | Default | Meaning |
|---|---|---|
| `supervisor.enabled` | on (tracing-gated) | Master switch for the advisory rules engine |
| `supervisor.halt_root` | `false` | Allow a rule or the `supervisor` tool to halt the session's root scope |
| `supervisor.rules.destructive_tool.enabled` | `true` | Advisory warning on destructive tool declarations; inject-only. A `write` is classified by what the tool observed: overwriting an existing file warns, first-time creation passes, and a missing observation warns conservatively. Shell and `delete` matches are unchanged. |
| `supervisor.rules.destructive_guard.enabled` | `true` | Blocking pre-dispatch net for sub-agent `bash`/`powershell` and command-running `background_process` calls, with a synthesized denial result |
| `supervisor.rules.high_error_rate.{enabled,threshold,window}` | `true`, `0.5`, `10` | Guidance when a scope's recent tool error rate crosses the threshold |
| `supervisor.rules.stuck_detection.{enabled,repeat_threshold}` | `true`, `3` | Guidance when a scope repeats the same tool call |

## Sub-agent dispatch lane and confinement

Two `agents.subagent` keys (not `agents.default` — see the field table above)
decide *where a sub-agent runs* and *what it may touch*. They apply to
dispatched sub-agents only: the orchestrator runs on the host, unconfined.
That split is the point — the orchestrator is the part that must reach
everything.

**Retention — `mode: retained` (the default in local sessions).** Each dispatch
runs in its own `git worktree` and returns a proposal; the host tree is untouched until the
orchestrator applies it with the `proposal` tool. Read-only roles return an
empty changeset — retained always, so "did anything change?" is never the
question. `mode: shared` is the direct-to-host lane: the explicit opt-out from
retention.

**Confinement — `containment`, default `auto`.** Where the host can jail
(Landlock on Linux — syscall filtering; winacl on Windows — write-restricted
access checks; Seatbelt on macOS — per-process SBPL profiles) a dispatch runs
its shell inside a kernel-enforced confinement jail whose sole writable root is
its worktree. `auto` jails where the host can and **degrades visibly to
advisory** where it cannot, recording which happened on the sub-agent's trace;
`paddock` (alias `jail`) fails closed instead of degrading; `off` (or
`none`/`advisory`) always runs on the host. The orchestrator is never jailed
either way.

**When the backend cannot keep its promise.** Being *available* is not the same
as being *enforceable*: a host can mint a restricted token and still fail its own
grant-conformance probe (winacl refuses when a declared writable root rejects the
canary write — "writable root … DENIES writes (profile too strict)"). That state
used to cost a dispatch lane **every** execution tool it had, because the
availability check had already passed and nothing re-evaluated afterwards: `go
build`, `go test`, `staticcheck` and `go_mod` all failed with `confinement:
fail-closed`, which reads like an agent error rather than a host one. The probe
failure is now what `auto` degrades on, exactly like an unavailable backend: the
command runs on the advisory host path, the reason is recorded once per workspace
(and reported by `tools.WorkspaceDegraded`), and one WARN names the backend. The
fallback is still withheld whenever it would lose something you asked for — a
declared `grants:` set or `network: deny_all`/`broker` — so `paddock`, narrowed
grants and egress rules keep failing closed and naming the fix.

> **The git rule.** A retained dispatch works in a
> **linked** `git worktree`, whose index and object store live in the *original*
> repository's `.git` — outside the jail's writable root. So a confined
> **`git add`/`commit`/`stash` is refused by the kernel**; read-only git
> (`status`, `diff`, `log`, `show`) is fine. This is **by design**: the
> sub-agent prepares changes and the orchestrator commits when it settles the
> proposal — which is what retention already meant. Granting `<repo>/.git` as a
> second writable root would fix the symptom and break the promise, letting a
> confined sub-agent rewrite the host repository's refs and objects.
>
> Because the raw failure is just `Permission denied`, the refusal now carries an
> explanation naming the writable root and the rule, and a confined sub-agent's
> prompt states the rule up front. `TestConfinedBuild_Acceptance` exercises the
> real linked-worktree layout and pins both.

Housekeeping writes are **redirected into** the worktree rather than the jail
being widened, on every confinement host: `HOME`, `TMPDIR`, `XDG_CACHE_HOME`,
`GOCACHE`, and `npm_config_cache` point at a `.yaah-scratch/` inside it (on
Windows the redirect adds `TEMP`, `TMP`, and the profile trees
`APPDATA`/`LOCALAPPDATA`/`USERPROFILE`, because winacl launches the child with
the caller's environment and those would otherwise point outside the writable
root). The scratch is scrubbed
before the proposal is sealed, so it never reaches the delta. Two *reads* the
redirect would otherwise hide are handled explicitly:

- `GOMODCACHE` is pinned against the *pre-redirect* environment, so a confined
  Go build reads the shared module cache instead of silently re-downloading into
  the scratch.
- `GIT_CONFIG_GLOBAL` is pointed at a small **shim** in the scratch that includes
  the user's real global git config files, resolved before the redirect. The
  moved `HOME` would otherwise hide the identity a commit needs ("Author identity
  unknown"). A shim rather than a direct pointer, because `GIT_CONFIG_GLOBAL`
  replaces *both* default files and takes a single path — naming one would drop
  the other, and a name in `~/.gitconfig` with the email in
  `$XDG_CONFIG_HOME/git/config` is a real configuration. The shim carries paths,
  not values, so no config content — `http.<url>.extraheader` tokens included —
  lands in the worktree, and each included file's own `include` paths still
  resolve against its real location.

The scratch itself is created defensively: **any** symlinked path component from
the worktree down to the scratch is refused — not only links that point outside.
A symlinked scratch is never intended, these writes happen in the unconfined
parent (where `MkdirAll` would follow the link), and telling a safe link from an
unsafe one is not worth the risk. A refusal **fails the dispatch** rather than
quietly confining without the redirect — a jailed body whose `HOME`/`TMPDIR`
still point outside the writable root would just fail confusingly. (`containment:
off` is the way to run unconfined.)

Credential *stores* (`~/.ssh`, `~/.git-credentials`, `~/.npmrc`, `~/.netrc`) are
deliberately not made reachable: the worktree bootstrap — which runs unconfined
before the body — is where authenticated inputs get prepared.

**Egress (`network`, default `allow_all`).** The jail governs the network as well as the
filesystem, because a sub-agent that cannot be trusted with your working tree
generally cannot be trusted with your network either:

- `deny_all` — no TCP egress at all. A dependency download or an HTTP call fails
  inside the sandbox.
- `broker` — a session egress broker is started and the jail pins egress **to its
  port**, so the only reachable destination is the broker, which admits just
  `network_hosts` (exact host names; patterns are rejected at validation rather
  than silently admitting nothing). `HTTP_PROXY`/`HTTPS_PROXY` are pointed at it
  inside the sandbox, so tools that honour the usual proxy variables route
  through it without configuration; hosts outside the list are refused by the
  broker.
- `allow_all` (the default) — ungoverned.

Four things to know about the guarantee:

- **The pin is by port, not by address.** Landlock's network rules are
  port-scoped, so "broker" means "the broker's port and nothing else", not
  "loopback only" — a confined body could still reach *some other host* on that
  port. True loopback-only pinning needs a network namespace (croft). The broker's
  host allowlist is what does the actual policy work.
- **It rides the jail, and it does not ride it silently.** `deny_all` and
  `broker` are kernel rules; the jail is where they live. So a restrictive posture
  that cannot be applied — `containment: off`/`advisory`/`none`, a workdir-less
  dispatch with no world to jail, a host with no usable jail, a backend with no
  network axis (winacl), or a Landlock ABI below 4 on Linux (filesystem-only:
  the jail exists, the network rule does not) —
  **fails the dispatch**, naming the fix, rather than running with open egress
  after you asked for none. `yaah doctor` reports the same conditions as a `FAIL`
  before you hit them.
- **Tools that run in this process are outside the jail.** A sub-agent's `http`
  and `webfetch` calls are made by the yaah process, not by a sandboxed child, so
  a jail cannot govern them — a restrictive posture therefore **omits those tools**
  from the dispatch (and says so in the sub-agent's prompt) rather than leaving a
  one-call hole. A client the sub-agent runs *itself* is governed normally.
- **This key governs host and container dispatches.** It is decided per
  dispatch, before any lane gate, so a workdir-less dispatch counts too (shared
  mode, or a model-supplied empty workdir): a restrictive posture there is
  refused, since nothing would jail the sub-agent. A per-dispatch container
  lowers the posture onto its netns: `allow_all` is host networking,
  `deny_all` is loopback-only, and `broker` is a netns-local proxy pinned to the
  session allowlist. The host-run `http`/`webfetch` tools are omitted under a
  restrictive posture in every lane, because those requests happen in the yaah
  process outside the container. An isolated session's shell uses that
  container's own posture; a restrictive posture omits the same two tools from
  its dispatches.

A confined sub-agent is told its posture up front ("there is no network egress"
or "egress is limited to an allowlist"), so a blocked fetch is not something it
has to discover by retrying.

Two limits to know about:

- **`/dev` is writable, not just `/dev/null`.** Every profile grants writes to
  the whole `/dev` subtree, because Landlock's path rule requires a *directory*
  (a rule on the device file is rejected with `EINVAL`) and Landlock has no deny
  rules, so `/dev/shm` cannot be carved back out. Seatbelt's Python profiles make
  the same allowance. So "sole writable root" means "sole root under the
  worktree's own tree": `/dev` is the one exception, and it comes with a
  world-writable tmpfs.
- **Git index writes in a linked worktree** are refused, as above.

### Containment across platforms

The containment family is one contract with per-platform mechanisms
(`goatherd-runtime/sandbox/*`). yaah picks the member per platform: `landlock`
on Linux, `winacl` on Windows, `seatbelt` on macOS, and nothing (advisory only)
elsewhere.

| | Linux | Windows | macOS |
|---|---|---|---|
| Mechanism | Landlock rulesets: kernel-enforced filesystem rights, port-scoped network rules | Write-restricted token, per-root write capability SIDs, Low integrity level, no-write-up label | Seatbelt SBPL profiles |
| Privilege | none | none (no elevation, container, or VM) | none (`sandbox-exec` ships with macOS) |
| Writable root | worktree plus redirected scratch | worktree only (per-root write grants) | worktree plus platform temp directories |
| Git rule | commit refusal with an explanation | same refusal; denials classified as discarded runs | profile denies writes under the repository's `.git` |
| Egress | `allow_all` / `deny_all` / `broker` (port pin) | `allow_all` only — no kernel network axis; restrictive postures fail closed | `deny_all` enforceable; broker allowlists are cooperative, not kernel-pinned |
| Placement & probe | `paddock` / `auto` / `advisory`, fail-closed probe | same contract, same fail-closed probe | same contract, same fail-closed probe |

Mechanism differences are deliberate and reported with distinct enforcement
tiers: Linux denies syscalls (`native-syscall-deny`), Windows denies access
checks (`native-access-check-deny`), macOS denies policy evaluations
(`native-sandbox-deny`). Seatbelt profiles need broad read access so toolchains
work; that is the documented trade-off, not a silent widening.

### The isolation vs egress matrix

**Isolation is a filesystem and workspace property. It is not "no network."**
Every mode below scopes *where files and processes live*; egress is a separate
axis with separate controls, and no workspace mode turns the network off. This
subsection is the one authoritative statement of the split — other docs point
here rather than restating it.

| Where the work runs | Filesystem boundary | Egress of the processes it starts | Egress of `http` / `webfetch` |
|---|---|---|---|
| Orchestrator / root session (local) | path containment only: `--workspace`, `agents.default.workspace_deny_patterns` | none — unconfined, no jail | the URL guard (below) |
| `mode: shared` dispatch | the host tree, same as the root | none — and a restrictive `network` is **refused**, because there is no workdir to jail | the URL guard |
| Retained dispatch (`proposal` / `report` lane, worktree) | worktree-only writable root under `containment`; advisory where no jail exists | `agents.subagent.network`: `allow_all` (default), `deny_all`, or `broker` + `network_hosts` | the URL guard — and a *restrictive* posture **omits** both tools, because they would bypass the jail |
| Per-dispatch container (`world: container`, or `auto` for a report lane) | the container, grant-shaped where configured | `agents.subagent.network` lowered onto the container: `allow_all` host netns, `deny_all` loopback-only, `broker` + `network_hosts` netns-local proxy | the URL guard — a *restrictive* posture **omits** both tools, because the request is made by the yaah process, outside the container |
| Isolated session (`workspace.backend: containerd`), and every dispatch inside it | the container | the container's own posture; `agents.subagent.network` does not govern the session container's shell | the URL guard — under a restrictive posture both tools are omitted, because the request is still made by the yaah process **outside** the container |

**What the URL guard blocks (`http`, `webfetch`; always on, in every lane).**
Both tools validate the URL before any request and share one guarded HTTP
client, so the policy holds in the host, retained and isolated lanes alike:

| Range | Examples |
|---|---|
| Loopback | `127.0.0.0/8`, `::1/128`, the names `localhost` and `*.localhost` |
| This host | `0.0.0.0/8` |
| RFC-1918 private | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` |
| Link-local / cloud metadata | `169.254.0.0/16` (e.g. `169.254.169.254`) |
| IPv6 link-local and ULA | `fe80::/10`, `fc00::/7` |

Non-`http`/`https` schemes (`file:`, `ftp:`) are refused outright. The three
legs are: the pre-flight check, a redirect policy that re-validates every hop
(10 redirects, then refused), and a dial-time check of the IP actually
connected to — which is what closes the DNS-rebinding TOCTOU between the name
validated and the address opened. A host that cannot be resolved **fails
closed**. `blockedCIDRs` in `internal/tools/urlguard.go` is the authoritative
list; the table above summarizes it and must not be read as finer-grained than
the code.

**What the guard is not.** It is not a firewall and it governs no child process.
`bash`/`powershell` (`curl`, `npm i`, `go get`), `git` fetch/push, the Go tool
chain (`go_mod`, `go_test`, `staticcheck`), and MCP servers are reached through
processes the jail — not the guard — governs, which is exactly why a restrictive
dispatch omits `http`/`webfetch` on both host and container lanes instead of
trusting the guard there. Nor does
anything here reach traffic that is not a tool call: model requests (including
embeddings used by `memory_search`) and the update check are made by the yaah
process itself, so **`deny_all` does not make a dispatch silent** — the
sub-agent still talks to its provider. Finally, the retained-lane bootstrap
(`retained_worktree_bootstrap`) runs unconfined, before the body, and therefore
has full egress — that is where authenticated inputs are prepared on purpose.

The practical reading: `--workspace .` or a container backend confines *files*;
`agents.subagent.network` governs *jailed child processes*; the URL guard bounds
*the two tools that fetch URLs in this process*; and none of the three is a
complete network policy for the session.

Each dispatch records the resolved placement on the sub-agent's trace
(`yaah.subagent.confinement.v1`: requested vs resolved, backend, enforcement
tier, `enforced`, writable root), so an advisory run is never misread as jailed
— including the case where a backend is present but unusable. `yaah doctor`
reports the effective placement and this host's capability: `auto` with no
usable backend is a WARN, `paddock` is a FAIL.

## MCP servers

`mcp_servers` configures MCP servers that yaah connects to as a client.
Tools from connected servers are registered alongside built-in tools.

| Field | Default | Description |
|---|---|---|
| `command` | — | Executable for a stdio server |
| `args` | — | Arguments for a stdio server |
| `env` | — | Environment variables for a stdio server |
| `url` | — | Endpoint for an HTTP server |
| `transport` | inferred | `stdio` or `http` |
| `framing` | auto | stdio framing: newline-delimited or Content-Length |
| `headers` | — | Extra HTTP headers for an HTTP server |

Use either `command` + `args` (stdio) or `url` (HTTP). `agents.default.mcp_approval`
controls whether MCP-served tools are auto-approved, prompted, or denied.
`${VAR}` references are substituted from the environment in `command`,
`args`, `env` values, `url`, and `headers` values at connection time —
for example `Authorization: Bearer ${GITHUB_TOKEN}`. Map keys (env names,
header names) are literal. An unset variable expands to the empty string,
which surfaces as a connection/auth failure rather than being sent as
literal `${...}`.

## Observability reference

```yaml
observability:
  otel:
    enabled: false
    endpoint: localhost:4318     # OTLP/HTTP endpoint (SigNoz: 4318)
    service_name: yaah
    traces: true                 # emit trace spans (default: true)
    metrics: false               # emit OTLP metrics (default: false)
    verbose: false               # record full conversations + summaries
```

`YAAH_OTEL_ENABLED=true` enables tracing, and
`OTEL_EXPORTER_OTLP_ENDPOINT` overrides the configured endpoint. Both take
precedence over the YAML values.

## Hooks reference

```yaml
hooks:
  dir: ""                        # JSONL event log; empty disables hooks
```

Events: `session.start`, `session.end`, `turn.start`, `tool.start`,
`tool.end`, `conflict.check`, `conflict.detect`, `context.prune`,
`wait.start`, `wait.end`, `watch.arm`, `watch.fire`, and `watch.cancel` —
with timestamps, model, tool results, durations, and prune counters. Wait
and watch events carry the predicate kind, and outcome events also carry
the state, never the target (a URL, command or file path).

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
