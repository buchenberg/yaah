# Agent personas

A persona is an identity the session's orchestrator runs as. The persona is
the only session axis: it decides the system-prompt body, the tool gate, and
the loop budget. Sub-agent roles and personas share the same role-file
format (see [sub-agents.md](./sub-agents.md#custom-roles)), but a persona is
not a dispatch target — `delegate` never reaches it.

## Built-in personas

Two synthetic personas ship with no role file. Like the registry's built-in
roles, they shadow a same-named user file — a role named `build` or `plan`
is never merged, only displaced.

| Persona | Default | Behavior |
|---|---|---|
| `build` | yes | Today's full-surface orchestrator: no body, no narrowing, no budget |
| `plan` | — | Read-only planning: the inspection tool set plus a planning directive; announces with a `(read-only)` marker |

## Role files as personas

Any role file in `.agents/roles/*.md` (project) or `~/.agents/roles/*.md`
(user) whose frontmatter carries `mode: primary` or `mode: all` is
primary-capable and joins the persona list. `mode: subagent` — explicit or
the missing-key default — keeps the file delegate-only.

| `mode` | Orchestrator persona | Delegatable | Notes |
|---|---|---|---|
| `primary` | yes | no | Never appears in the delegate schema or `list_subagents` |
| `all` | yes | yes | Selectable and dispatchable |
| `subagent` / missing | no | yes | Historical behavior; a missing key is silent, an unknown value warns and falls back to `subagent` |

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
| read-only | Only the `plan` persona sets it: the loop's tool gate withholds the mutating set and admits the inspection set (see [features.md](./features.md#plan-mode)); the gate is enforced at dispatch, so `middleware.disabled: [approval]` cannot bypass it. |

## Selection

How a session picks its persona, in precedence order:

| Order | Source | Scope |
|---|---|---|
| 1 | `agents.default.agent` in config | Durable default for fresh sessions |
| 2 | The stored `sessions.persona` value | Resumed sessions only — the stored persona always wins on resume; a config change never rewrites an existing session |
| fallback | `build` | An unknown or delegate-only name warns and falls back (never fails the session) |

The persona is persisted on the session row at creation. Resume restores it:
the budget, tool narrowing, and read-only gate re-derive from the stored
name, while the prompt stays the recorded bytes. The `sessions.mode` column
is vestigial: nothing writes it and no behavior keys off it (the column
survives in older stores only); an empty persona reads as `build`.

## Switching

| Surface | Action | Scope |
|---|---|---|
| REPL `/agents` | List personas, current marked | — |
| REPL `/agent [name]` | Report status (bare) or switch this session | Current session, next run |
| TUI `:agent` palette | Pick a persona | Current session, next run |
| ACP `session/set_agent` | Switch the addressed session | Current session, next run |
| ACP `session/set_mode`, `session/set_config_option("mode")` | Spec mode ids map onto the personas of the same name | Current session, next run |

A switch is effective at the next loop build: the persona body rebinds into
the prompt, the budget re-resolves, and the tool narrowing and read-only
gate re-derive. An in-flight turn finishes on the persona it started with.

`AgentChangedEvent` announces non-`build` personas at session start and
every switch, including a switch back to `build`; the in-band notice names
the persona and the regime it enforces. Plain `build` sessions announce
nothing at start.
