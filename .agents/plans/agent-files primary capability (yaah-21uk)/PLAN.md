---
name: agent-files primary capability (yaah-21uk)
description: Implement epic yaah-21uk: extend .agents/roles/*.md with mode: primary|subagent|all; retire the build/plan session-mode axis in favor of selectable primary agents (build/plan ship as built-ins). Enforcement machinery (ToolsLevel + GateWithheld) is kept, re-keyed to the selected agent.
status: in_progress
---

# Plan: role files gain primary/orchestrating capability

Epic: yaah-21uk. Reference design: OpenCode V2 agents. Decisions D1–D4 RESOLVED
by Greg 2026-11-08 (recorded on yaah-21uk.2).

## Goal

One artifact type — `.agents/roles/*.md` — defines a **subagent** (today's meaning)
or the **primary/orchestrating** agent via `mode: primary|subagent|all` (OpenCode
vocabulary adopted). The session plan/build axis is **retired as a user-facing
concept**: `build` and `plan` ship as built-in primary agents, and agent selection
replaces mode switching. Repos without primary files keep today's behavior,
modulo the renamed UX surface.

## Resolved decisions (yaah-21uk.2, closed)

- **D1** — frontmatter key IS `mode: primary|subagent|all`. The collision with
  session modes dissolves because session modes stop existing as a separate
  axis. Unknown values clamp-warn to subagent.
- **D2** — unified `.agents/roles/`; no new directory.
- **D3** — compose: persona body layers after the embedded identity;
  replacement deferred.
- **D4** — built-in primary agents: `build` (FullTools, current identity.md)
  and `plan` (ReadOnlyTools + plan directive). Default = `build`. Fallback
  chain: configured selection → must exist + be primary-capable → `build`.
- **Follow-on (backburner)**: provider/model routing in persona files —
  config-only for this epic; filed separately.
- **No mid-session primary switching**: agent fixed at session creation;
  mid-session `/agent` sets a preference effective next session (refused with
  in-band notice). Mid-session **subagent role adds remain fully supported**
  (role tool + `/reload-roles` — unchanged behavior, now an explicit guarantee).

## Session-mode retirement (scope added by D1)

Enforcement machinery KEPT; user-facing axis RETIRED:

- `ToolsLevel` + `dangerousGate`→`GateWithheld` + always-on `admitTool`
  (internal/agent/tools.go, agent_safety.go:123) stay exactly as-is —
  enforcement never leaves code.
- `planModeSpec` (cmd/yaah/mode.go:27) generalizes to a per-agent tool level:
  built-ins `build`=FullTools, `plan`=ReadOnlyTools. User files may only
  NARROW (read-only request or tools ⊂ belt); FullTools is the ceiling.
- `regimeSentence` (mode.go:79) keeps generating directive wording from the
  level — now bound to the selected agent, still injected after identity.
- Persistence: session mode column migrates to selected agent
  (mode=plan → agent=plan). `ModeChangedEvent` → `AgentChangedEvent`.
- UX retirement path: REPL `/plan` `/build` → deprecated aliases of
  `/agent plan` / `/agent build` (notice one release), then removed.
  TUI/web palettes list primary agents. ACP keeps `session/set_mode` on the
  wire for protocol compat, mapping mode values to built-in agents;
  `session/set_agent` is the preferred new method.

## Phase 0 — foundations

- **0.1** Land **yaah-gqqo** (user-tier role path bug) — same discovery code.
- **0.2** Replace `InjectAfterIdentity` substring anchor (prompts.go:498) with
  explicit delimiter/layer slot; consumers = directives injection
  (wiring_prompt.go:164) + per-agent directive (mode.go:55). Test with
  non-default identity content. [yaah-21uk.1]
- ~~0.3 decisions~~ — DONE (yaah-21uk.2 closed).

## Phase 1 — schema, registry, retirement groundwork

- **1.1** `rolefile.Frontmatter` gains `mode`; clamp-and-warn (unknown →
  subagent + warning). Round-trip via extra-key machinery. [yaah-21uk.3]
- **1.2** `RoleDef`/`RoleProfile` carry capability. Existing built-ins
  (analyst/developer/reviewer/tester) stay subagent; NEW embedded built-ins
  `build` + `plan` marked primary (internal until phase 3 UX). [yaah-21uk.3]
- **1.3** Delegate-surface filter: primary-only excluded from
  `BuildDelegateSchema`/`RoleDescriptionsFor`/`list_subagents`; `all` stays
  delegatable. [yaah-21uk.4]
- **1.4** Persona registry (separate from `RoleRegistry`), `RoleSearchPaths`
  reuse, project-over-user; default resolution → `build` per D4. [yaah-21uk.5]
- **1.5** Per-agent tool level plumbing: generalize `planModeSpec`;
  agent definition → `ToolsLevel`; files narrow-only. [yaah-21uk.12 front-half]

## Phase 2 — binding into the session

- **2.1** Persona prompt layer after identity (new `Layers` slot; precedent:
  unused Skills field prompts.go:561). Sub-agent prompts untouched. Golden
  gate: no primary files → byte-identical prompt. [yaah-21uk.6]
- **2.2** Persona `tools` intersects the belt (`agents.default.tools`) —
  narrow-only; removed tools named in prompt; per-agent level applied on top. [yaah-21uk.7]
- **2.3** Budgets via `budget.Resolve`, config-authoritative; provider/model
  keys in files ignored with warning (follow-on bead tracks the revisit). [yaah-21uk.8]

## Phase 3 — selection, persistence, UX

- **3.1** Session row stores selected agent; migrate mode column; resume
  restores agent; config default changes never rewrite existing sessions. [yaah-21uk.9]
- **3.2** Config: `agents.default.agent` (default `build`); validate against
  registry at startup.
- **3.3** UX: REPL `/agents` + `/agent <name>`; `/plan` `/build` deprecated
  aliases; TUI palette lists agents; web palette; ACP `set_agent` +
  `set_mode` compat mapping. `AgentChangedEvent` → `allEvents()` exhaustive
  wiring. No mid-session switch: refusal notice + next-session preference. [yaah-21uk.10]
- **3.4** Hot reload: subagent adds (existing two triggers); persona registry
  rebuilds for NEXT session only. [yaah-21uk.11]

## Phase 4 — composition, tooling, docs

- **4.1** Mode retirement completion: enforcement matrix keyed by agent;
  `regimeSentence` per level authoritative; `/plan` `/build` alias removal
  plan; migration for persisted sessions + ACP clients. [yaah-21uk.12]
- **4.2** `role` tool: create/edit/list handle `mode`; list annotates. [yaah-21uk.13]
- **4.3** Docs: agents guide, configuration keys, architecture §RoleRegistry,
  AGENTS.md, README, migration notes. [yaah-21uk.14]

## Validation

- Per phase: `go test ./...`, `go vet ./...`, staticcheck, gofmt.
- New tests: anchor positioning; `mode` parse/fallback; delegate filter both
  ways; golden no-primary prompt; belt intersection; budget precedence;
  mode→agent migration; ACP `set_mode` compat mapping; `/plan` alias
  deprecation notice; no-mid-session-switch refusal; persona-level enforcement
  (ReadOnlyTools agent strips + gates dangerous calls); exhaustive events.

## Non-goals (this epic)

- Provider/model routing in persona files (follow-on bead; backburner).
- Identity replacement (vs compose).
- Namespaced/nested role IDs, definition merging, per-tool allow/ask/deny,
  hidden/disabled/color fields.
- Mid-session primary switching.
- dotagent (`.agent/`) native ingestion.
