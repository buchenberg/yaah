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
    small_model: deepseek-v4-flash     # compaction model
    max_iterations: 50                 # hard loop cap
    max_turns: 0                       # 0 = no soft tool-turn cap
    context_window: 1048576            # 0 = use the model's known window
    approval: ask                      # ask | allow | deny
    mcp_approval: ask                  # ask | allow | deny for MCP tools
    workspace_ask: false               # prompt before denying out-of-workspace paths
    max_inline_tools_per_turn: 0       # 0 = unlimited
    estimate_factor: 1.3               # token estimate multiplier (0 = 1.3)

    compaction_threshold: 0.5          # fraction of context_window
    raw_compaction_threshold: 0.5      # ignore cached tokens
    compact_max_messages: 0            # force compaction above N messages

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
    mode: retained                     # retained | shared
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

  middleware:
    enabled: [prompt_caching]          # additive, orchestrator only
    disabled: []                       # removes from both pipelines

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
| `small_model` | `deepseek/deepseek-v4-flash` | Cheaper model for context compaction |
| `max_iterations` | 50 | Safety cap on loop turns |
| `max_turns` | 0 | Soft cap; tools are stripped at this iteration. `0` = no soft cap (the derive rule applies only to `agents.subagent.default_max_turns`) |
| `context_window` | 1048576 | Caps the model's resolved window. `0` = use the model's known window, not "disabled" |
| `approval` | `ask` | `allow`, `ask`, or `deny` |
| `mcp_approval` | `ask` | `allow`, `ask`, or `deny` for MCP-served tools |
| `workspace_ask` | `false` | With `--workspace`: prompt before denying out-of-bounds access |
| `workspace_deny_patterns` | nil (built-in secret defaults) | Globs denied even inside the workspace. `nil` applies `.env`, `*.pem`, `*.key`, SSH keys; `[]` disables the built-ins |
| `max_inline_tools_per_turn` | 0 (unlimited) | Cap inline tool calls per turn |
| `estimate_factor` | 1.3 | Token estimate multiplier for preflight compaction |
| `compaction_threshold` | 0.5 | Fraction of `context_window` that triggers summarisation (effective tokens after cache subtraction) |
| `raw_compaction_threshold` | 0.5 | Same, but ignores cached tokens |
| `compact_max_messages` | 0 (off) | Force compaction above N messages |
| `directives` | — | Policy statements injected into the orchestrator prompt only |
| `shepherd_trace_path` | ~/.yaah/trace.db | Path to the Shepherd trace store **file**. Tracing is always on — this only relocates the store |
| `retained_repo_path` | cwd | Repository retained worktrees are cut from |
| `retained_worktree_root` | .yaah/shepherd-worktrees | Parent directory for retained worktrees |
| `retained_worktree_bootstrap` | — | Command run inside each retained worktree, e.g. `npm ci` |
| `turn_checkpoint_max` | 0 (unlimited) | Live turn checkpoints per run |
| `max_turn_restores` | 3 | Turn-level restores per run |
| `loop_detect_count` | 5 | Identical tool-call hashes within the window that trigger a halt |
| `loop_detect_window` | 10 | Sliding window size for loop detection |
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

Sub-agent inheritance is selective. `max_retries`, `retry_backoff_secs`,
`max_tool_concurrency`, tool-result limits, prune settings, context window,
and turn-checkpoint limits are passed down. `loop_detect_*`, compaction
tuning, `prompt_caching`, `reasoning_protect_turns`,
`max_inline_tools_per_turn`, approval policy, and `directives` are not.
Sub-agents use their own curated pipeline and per-role directives.

**`agents.subagent`** — team configuration:

| Field | Default | Description |
|---|---|---|
| `provider` | inherits | Provider for sub-agents |
| `model` | inherits | Model for sub-agents |
| `max_concurrency` | 3 | Max simultaneous `delegate` calls; 0 = unlimited |
| `default_timeout` | 0 (none) | Total wall-clock seconds per sub-agent dispatch, including provider thinking time; 0 = none |
| `stuck_child_timeout` | 60 | Seconds without a heartbeat before a sub-agent is force-cancelled (0 = off) |
| `default_max_turns` | 0 (derive) | Default soft turn cap; 0 derives `iterations - 1` |
| `default_min_turns` | 0 (none) | Global turn floor — no per-call override or role budget may drop below it |
| `output_limit` | 51200 | Byte cap on sub-agent reports; 0 = built-in 50 KiB cap |
| `json_mode` | false | Force structured JSON output |
| `mode` | `retained` | Dispatch lane — `retained` (isolated worktree + proposal) or `shared` (direct to host) |
| `containment` | `auto` | Syscall-jail placement — `auto` (jail if the host can, else advisory), `off`/`none`/`advisory`, `paddock`/`jail` (fails closed) |
| `network` | `allow_all` | Egress posture for a confined dispatch — `allow_all`, `deny_all`, or `broker` (allowlist via the session's broker) |
| `network_hosts` | `[]` | The broker's allowlist (exact host names); only meaningful with `network: broker`. Empty = the broker admits nothing; setting hosts with another mode is a validation error |
| `roles.<name>.timeout` | — | Per-role total wall-clock budget in seconds; overrides both the role file and `default_timeout` |
| `roles.<name>.max_iterations` | — | Per-role iteration cap |
| `roles.<name>.min_iterations` | 0 (none) | Per-role iteration floor; beats per-call overrides and the role file's floor |
| `roles.<name>.max_turns` | — | Per-role turn cap |
| `roles.<name>.min_turns` | 0 (none) | Per-role turn floor; beats per-call overrides and the role file's floor |
| `roles.<name>.provider` | — | Per-role provider override |
| `roles.<name>.model` | — | Per-role model override |
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

### Middleware

**`agents.middleware`** — control the pipeline. `enabled` is **additive
and orchestrator-only**: listed middleware are added to the default
orchestrator pipeline (duplicates collapse). `disabled` removes middleware
from both the orchestrator and sub-agent pipelines. The default orchestrator
pipeline is
`steer → followup → compaction → soft_prune → approval → inline_limit → tool_concurrency → loop_detection → evidence_guard → conflict_detect`.

| Middleware | Default | Purpose |
|---|---|---|
| `steer` | on | High-priority mid-turn steering input |
| `followup` | on | Queued between-turn messages, coalesced |
| `compaction` | on | LLM-powered context summarization |
| `soft_prune` | on | Elide stale tool-output content (no LLM) |
| `approval` | on | Gate dangerous tools |
| `inline_limit` | on | Cap tool calls dispatched per turn (0 = unlimited) |
| `loop_detection` | on | Halt identical tool+result loops |
| `evidence_guard` | on | Steer/halt when repeated calls or unchanged re-reads produce no new evidence |
| `conflict_detect` | on | Flag files touched by multiple sub-agents |
| `permission` | off | Path-pattern allow/deny rules (auto-added when parent rules exist for sub-agents) |
| `tool_concurrency` | on | Cap concurrent tool goroutines |
| `prompt_caching` | off | Anthropic cache-control breakpoints — include via `agents.default.prompt_caching: true`; naming it in `enabled` also works |
| `supervisor_guard` | sub-agents only | Blocking destructive-shell guard; present in the sub-agent pipeline when tracing/supervision is active |

## Workspace reference

**`workspace`** — where the session's tools execute. Without the stanza
everything runs on the host filesystem, as it always has.

| Field | Default | Description |
|---|---|---|
| `backend` | `local` | `local` runs tools on the host; `containerd` runs them inside a per-session containerd sandbox |
| `root` | `/workspace` | Containment root inside the sandbox. Must be absolute and not `/` when `containerd` is in effect (config validation refuses otherwise) |
| `containerd.address` | containerd's own default | containerd socket or endpoint; supports `${ENV_VAR}` substitution |
| `containerd.namespace` | `default` | containerd namespace for the session's containers |
| `containerd.image` | (required for `containerd`) | Base image. Must satisfy the tool contract (sh, GNU stat/find, git, rg, base64, Go toolchain) — verified at startup |
| `containerd.snapshotter` | `overlayfs` | containerd snapshotter plugin |
| `containerd.fifo_dir` | `<yaah home>/containerd-fifo` | Directory the containerd *client* creates the task/exec stdio FIFOs in. containerd's own default (`/run/containerd/fifo`) is root-only, so the yaah default keeps unprivileged sessions working; must be absolute. Avoids a predictable name under the shared temp dir, which another local user could pre-create |

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

A retained dispatch runs the sub-agent in its **own git worktree** and
returns a sealed proposal; the orchestrator settles it with
`proposal select|apply|release|discard`. Nothing reaches the host tree until
settlement, so a rejected attempt costs a dispatch rather than a rollback.
Retained mode is available in local (non-containerd) sessions whenever the
shepherd root scope exists — which it now always does, unless the trace store
fails to open; otherwise use `agents.subagent.mode: shared` or expect the
direct lane.

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
(Landlock on Linux) a dispatch runs its shell inside a syscall jail whose sole
writable root is its worktree. `auto` jails where the host can and **degrades
visibly to advisory** where it cannot (macOS, Windows, a kernel without
Landlock), recording which happened on the sub-agent's trace; `paddock` (alias
`jail`) fails closed instead of degrading; `off` (or `none`/`advisory`) always
runs on the host. The orchestrator is never jailed either way.

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
being widened: `HOME`, `TMPDIR`, `XDG_CACHE_HOME`, `GOCACHE`, and
`npm_config_cache` point at a `.yaah-scratch/` inside it. The scratch is scrubbed
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
  dispatch with no world to jail, a host with no usable jail, or a Landlock ABI
  below 4 (filesystem-only: the jail exists, the network rule does not) —
  **fails the dispatch**, naming the fix, rather than running with open egress
  after you asked for none. `yaah doctor` reports the same conditions as a `FAIL`
  before you hit them.
- **Tools that run in this process are outside the jail.** A sub-agent's `http`
  and `webfetch` calls are made by the yaah process, not by a sandboxed child, so
  a jail cannot govern them — a restrictive posture therefore **omits those tools**
  from the dispatch (and says so in the sub-agent's prompt) rather than leaving a
  one-call hole. A client the sub-agent runs *itself* is governed normally.
- **This key governs the host lanes.** It is decided per dispatch, before any lane
  gate, so a workdir-less dispatch counts too (shared mode, or a model-supplied
  empty workdir): a restrictive posture there is refused as well, since nothing
  would jail the sub-agent. An isolated session is the exception — its sub-agents
  run in a container whose network posture is the container's, so this key is
  neither applied nor refused there. It does not omit tools or describe an egress
  rule the container does not have either (the container's loopback is not the
  host's, so a broker proxy address would be wrong there) — and note that the
  container's posture does **not** cover `http`/`webfetch`, which still make their
  request in this process, outside the container. So in an isolated session a
  restrictive `network` neither protects those two tools nor blocks them: use the
  container's own egress controls.

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
`headers` values are literal; `${VAR}` substitution applies only to provider
connection fields and workspace containerd fields.

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
`tool.end`, `conflict.check`, `conflict.detect`, and `context.prune` — with
timestamps, model, tool results, durations, and prune counters.

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
