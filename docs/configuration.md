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

    # Supervised execution (the supervised_task tool). Requires
    # shepherd_trace_dir. These knobs are global; per-role routing lives in the
    # role files (subagent.roles.<name>.supervised / .turn_checkpoints).
    supervised_max_retries: 1         # rollback-and-retry cycles after the first attempt
    supervised_repo_path: ""          # repo to checkpoint; "" = working directory
    supervised_worktree_root: ""     # parent dir for variant worktrees; "" = beside the repo
    supervised_worktree_bootstrap: "" # run inside each worktree, e.g. "npm ci"
    turn_checkpoint_max: 0            # live turn checkpoints per run; 0 = unlimited
    max_turn_restores: 3              # turn-level restores per run; 0 = default (3)

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
    mode: retained                    # dispatch lane: retained | shared
    containment: auto                 # syscall jail: auto | off | paddock
    network: allow_all                # confined egress: allow_all | deny_all | broker
    network_hosts: []                 # broker allowlist (exact host names)
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

# ── Workspace ──────────────────────────────────────────────────
workspace:
  backend: local                     # local | containerd
  root: /workspace                  # containment root inside the sandbox
  containerd:                       # only meaningful when backend: containerd
    address: /run/containerd/containerd.sock
    namespace: default
    image: docker.io/library/yaah-workspace:latest
    snapshotter: overlayfs

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
| `mode` | `retained` | Dispatch lane — `retained` (isolated worktree + proposal) or `shared` (direct to host) |
| `containment` | `auto` | Syscall-jail placement — `auto` (jail if the host can, else advisory), `off`, or `paddock` (fails closed). See "Sub-agent dispatch lane and confinement" |
| `network` | `allow_all` | Egress posture for a confined dispatch — `allow_all`, `deny_all`, or `broker` (allowlist via the session's broker) |
| `network_hosts` | `[]` | The broker's allowlist (exact host names); only meaningful with `network: broker` |
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
| `containerd.fifo_dir` | `~/.yaah/containerd-fifo` | Directory the containerd *client* creates the task/exec stdio FIFOs in. containerd's own default (`/run/containerd/fifo`) is root-only, so the yaah default keeps unprivileged sessions working; must be absolute. Avoids a predictable name under the shared temp dir, which another local user could pre-create |

The `containerd` backend requires a Linux containerd daemon: config
validation refuses it at load on any other platform, naming the platform.
The daemon's socket is root-owned by default — a normal user needs
one-time group access (see "Daemon socket access" in
`docs/isolated-workspace-live-testing.md`), or every session fails its
startup probe with `connect: permission denied`. Everything else an
unprivileged session touches is handled in code: the adapter builds the
init spec from the image config through the daemon (no client-side
snapshot-store access), and `fifo_dir` defaults to a user-writable
directory. `yaah doctor` reports
the workspace check (config validity plus the reachable backend) when
the stanza is present. See
`docs/features.md` "Isolated workspace" for the behaviour and
`docs/isolated-workspace-live-testing.md` for the end-to-end live test.

## Supervised execution reference

The `supervised_task` tool (and the `supervisor` verdict tool) turns a sub-agent
run into a checkpointed, reviewable unit. It requires `shepherd_trace_dir` (see
the `agents.default` block above) and a role marked `supervised: true`.

```yaml
agents:
  default:
    shepherd_trace_dir: ~/.yaah/traces
    supervised_max_retries: 1
    supervised_repo_path: ""              # "" = the working directory
    supervised_worktree_root: ""
    supervised_worktree_bootstrap: ""
    turn_checkpoint_max: 0
    max_turn_restores: 3
```

| Key | Default | Meaning |
|---|---|---|
| `supervised_max_retries` | `1` | Rollback-and-retry cycles after the first attempt. |
| `supervised_repo_path` | `""` | Repository the checkpoints operate on. Empty uses yaah's working directory. |
| `supervisor.halt_root` | `false` | Opt in to halting the session's root scope — by the manual `supervisor` tool and, from adoption plan B3 on, by rule-driven halt interventions: the session's current run stops (the ctrl-C equivalent) and scope-tree supervision ends for the process. Sub-agent halts need no gate. An explicit value wins over the deprecated flat `supervisor_halt_root` key; unset falls back to it. |
| `supervisor.enabled` | on (tracing-gated) | Master switch for the advisory rules engine (adoption plan B1). With `shepherd_trace_dir` set, the kernel supervisor watches the session's trace and a service delivers rule firings as steering into the running sub-agent loops; `false` disables the whole engine. |
| `supervisor.rules.destructive_tool.enabled` | `true` | Advisory warning on destructive tool declarations (bash/powershell command patterns, write, delete) — inject-only guidance, recorded in the target scope's trace. Rule-driven firings against the session's own scope are dropped: the parent's calls already passed the approval middleware. |
| `supervisor.rules.destructive_guard.enabled` | `true` | Blocking pre-dispatch net (adoption plan B2): sub-agent `bash`/`powershell` calls are denied before execution — default-deny, because a shell can execute anything a pattern matcher cannot see — with a synthesized denied-call result so the model can take the structured path. Structured tools (`write`/`edit`/`delete`) are the approved path for changes; their review is the merge gate's job. Tracing-off sessions dispatch unchanged. |
| `supervisor.rules.high_error_rate.{enabled,threshold,window}` | `true`, `0.5`, `10` | Guidance when a scope's recent tool error rate crosses the threshold within the sliding window. `0` values mean the defaults. |
| `supervisor.rules.stuck_detection.{enabled,repeat_threshold}` | `true`, `3` | Guidance when a scope repeats the same tool call `repeat_threshold` times in a row. `0` means the default. |
| `supervised_worktree_root` | `""` | Parent directory for variant worktrees. Empty uses a `shepherd-worktrees` directory beside the repository. |
| `supervised_worktree_bootstrap` | `""` | Shell command run inside each fresh worktree — see below. |
| `turn_checkpoint_max` | `0` | Live turn checkpoints per sub-agent run; the oldest are pruned. `0` = unlimited. |
| `max_turn_restores` | `3` | Turn-level restores per run, so a deterministically failing turn cannot rewind forever. |

### What checkpointing covers

A checkpoint captures **workspace state plus the conversation**. Rolling back
rewinds both, so the sub-agent restarts from the same files *and* the same
context it had at the unit boundary. Checkpoints are single-use: a restore
consumes one, and a fresh checkpoint is taken before the next attempt.

`turn_checkpoints` (set per role) extends this to individual turns, bounded by
`max_turn_restores`.

### Worktree isolation

Every `fork` variant runs in a detached `git worktree` — an isolated child scope
seeded with the parent's exact state at the fork point — and the variant's tools
operate inside it. Nothing a variant writes can reach the parent tree until
`choose` settles it (a fast-forward when the parent has not moved, a three-way
merge when it drifted); the settlement itself removes the winner's worktree, and
discarding removes the loser's. `review`, `continue`, and `rollback` behave as
before.

Two consequences worth knowing:

1. **A worktree contains tracked files only.** `node_modules`, build caches,
   `.env`, and anything else gitignored are absent, so a variant that needs them
   cannot build or test. Use `supervised_worktree_bootstrap` to recreate them,
   for example `npm ci` or `go mod download`.
2. **Isolation is filesystem-level, not a sandbox.** Variants still share the
   host process, network, and the repository's object store. Worktrees do not
   confine processes.

The former `supervised_worktree` opt-in is gone — isolation is always on. A
config that still sets the key gets one startup warning; the key does nothing.

## Sub-agent dispatch lane and confinement

Two `agents.subagent` keys (not `agents.default` — see the field table above)
decide *where a sub-agent runs* and *what it may touch*. They apply to
dispatched sub-agents only: the orchestrator runs on the host, unconfined,
exactly as before — which is the whole point of the split, since the
orchestrator is the part that must reach everything.

**Retention — `mode: retained` (the default).** Each dispatch runs in its own
`git worktree` and returns a proposal; the host tree is untouched until the
orchestrator applies it with the `proposal` tool. Read-only roles return an
empty changeset — retained always, so "did anything change?" is never the
question. `mode: shared` is the older direct-to-host lane: the escape hatch, and
the benchmark baseline.

**Confinement — `containment`, default `auto`.** Where the host can jail
(Landlock on Linux) a dispatch runs its shell inside a syscall jail whose sole
writable root is its worktree. `auto` jails where the host can and **degrades
visibly to advisory** where it cannot (macOS, Windows, a kernel without
Landlock), recording which happened on the sub-agent's trace; `paddock` (alias
`jail`) fails closed instead of degrading; `off` (or `none`/`advisory`) always
runs on the host. The orchestrator is never jailed either way.

> **The git rule (why this needed a decision).** A retained dispatch works in a
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
- `allow_all` (the default) — ungoverned, as before.

Three things to know about the guarantee:

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
  container's own egress controls. That is a pre-existing property of the isolated
  lane, recorded here rather than implied.

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

## Observability reference

```yaml
observability:
  otel:
    enabled: false
    endpoint: localhost:4318     # OTLP HTTP endpoint (OpenObserve: localhost:5080)
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
