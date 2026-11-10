# Agent personas

A persona is an identity the session's orchestrator runs as. The persona is
the only session axis: it decides the system-prompt body, the tool gate, and
the loop budget. Sub-agent roles and personas share the same role-file
format (see [sub-agents.md](./sub-agents.md#custom-roles)), but a primary-only
persona is not a dispatch target — `delegate` never reaches it (`mode: all`
personas remain dispatchable).

## Built-in personas

Two synthetic personas ship with no role file. Like the registry's built-in
roles, they shadow a same-named user file — a role named `build` or `plan`
is never merged, only displaced.

| Persona | Default | Behavior |
|---|---|---|
| `build` | yes | Today's full-surface orchestrator: no body, no narrowing, no budget |
| `plan` | — | Planning: a planning directive; pair it with read-only mode to withhold the mutating tools |

## Role files as personas

Any role file in `.agents/roles/*.md` (project) or `~/.agents/roles/*.md`
(user) whose frontmatter carries `mode: primary` or `mode: all` — or omits
the key — is primary-capable and joins the persona list. `mode: subagent`
(explicit) keeps the file delegate-only. The missing-key default is `all`
(opencode-family parity): files are orchestrator-capable unless marked
otherwise, so a project that wants a file delegate-only says
`mode: subagent` explicitly.

| `mode` | Orchestrator persona | Delegatable | Notes |
|---|---|---|---|
| `primary` | yes | no | Never appears in the delegate schema or `list_subagents` |
| `all` | yes | yes | Selectable and dispatchable |
| `subagent` | no | yes | Explicit only |
| missing | yes | yes | Defaults to `all` (opencode-family parity W3); an unknown value warns and falls back to `all` |

The persona's name is the file stem. Built-in roles always win on name
conflict with user files; the `build`/`plan` built-ins win over everything.

## Persona file effects

What a persona file changes for the session that runs it:

| Field | Effect |
|---|---|
| body | Layers into the orchestrator system prompt immediately after the identity. Sub-agent prompts never carry it. |
| `tools:` | Narrows the advertised tool belt. The list is intersected with `agents.default.tools`, so a persona can only shrink the surface, never grow it; an empty list means no narrowing, never zero tools. |
| `max_iterations:` / `max_turns:` | Bound the orchestrator loop, config-authoritatively: an explicit `agents.default` value wins, the persona file fills the gap, and the shipped defaults apply last. |
| `timeout:` | Carried but unconsumed — the orchestrator loop has no wall-clock knob yet. The `min_*` floors are dispatch-specific and do not apply. |
| `provider:` / `model:` | **Accepted but ignored, with a warning.** Routing is config-only: a persona session uses `agents.default.provider`/`model` like any other session. |

There is no `read_only` persona field: read-only-ness is the session's
**mode axis**, not a persona property. Any persona can run read-only or
full; see [Mode and approval axes](#mode-and-approval-axes).

## Mode and approval axes

A session has three independent axes: the **agent** (which persona runs),
the **mode** (may it mutate the workspace), and **approval** (the posture
dangerous calls meet). The mode defaults from `agents.default.mode`
(`full`, or `read-only`); in read-only mode the loop's tool gate withholds
the mutating set and admits the inspection set (see
[features.md](./features.md#plan-mode)) — the gate is enforced at dispatch,
so `middleware.disabled: [approval]` cannot bypass it. The approval axis
defaults from `agents.default.approval` (`ask`). Both are switchable
mid-session through ACP `session/set_mode`, `session/set_config_option`
(ids `mode` and `approval`), and — in a future change — the interactive
surfaces.

**Upgrading from a persona-carried `read_only`:** earlier builds let a
persona file carry `read_only: true`, so `agents.default.agent: plan` (or
ACP `session/set_agent("plan")`) implied a read-only posture. That
implication is gone: the persona no longer carries mutability, and a
`read_only` key in a role file is ignored. If you relied on it, set
`agents.default.mode: read-only` (or switch the mode per session) after
upgrading — otherwise those sessions silently gain full mutation access.

## Selection

How a session picks its persona, in precedence order:

| Order | Source | Scope |
|---|---|---|
| 1 | `agents.default.agent` in config | Durable default for fresh sessions |
| 2 | The stored `sessions.persona` value | Resumed sessions only — the stored persona always wins on resume; a config change never rewrites an existing session |
| fallback | `build` | An unknown or delegate-only name warns and falls back (never fails the session) |

The persona is persisted on the session row at creation. Resume restores it:
the budget and tool narrowing re-derive from the stored name, while the
prompt stays the recorded bytes. The mode axis is session state, not persona
state: it re-derives from `agents.default.mode`, not from the stored persona.
The `sessions.mode` column
is vestigial: nothing writes it and no behavior keys off it (the column
survives in older stores only); an empty persona reads as `build`.

## Switching

| Surface | Action | Scope |
|---|---|---|
| REPL `/agents` | List personas, current marked | — |
| REPL `/agent [name]` | Report status (bare) or switch this session | Current session, next run |
| TUI `:agent` palette | Pick a persona | Current session, next run |
| ACP `session/set_agent` | Switch the addressed session | Current session, next run |
| ACP `session/set_mode`, `session/set_config_option("mode")` | Set the mode axis: `plan` = read-only, `build` = full access; the persona is untouched | Current session, next run |
| ACP `session/set_config_option("approval")` | Set the approval axis: `allow` / `ask` / `deny` | Current session, next run |

A switch is effective at the next loop build: the persona body rebinds into
the prompt and the budget re-resolves; a mode switch re-derives the tool
gate and the regime directive. An in-flight turn finishes on the axes it
started with.

`AgentChangedEvent` announces non-`build` personas at session start and
every switch, including a switch back to `build`; the in-band notice names
the persona and the regime it enforces. Plain `build` sessions announce
nothing at start.
