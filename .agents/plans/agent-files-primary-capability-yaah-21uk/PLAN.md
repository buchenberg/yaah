---
name: agent-files-primary-capability-yaah-21uk
description: Epic yaah-21uk: mode: primary|subagent|all on role files; build/plan session modes retired in favor of switchable personas (mid-session switching supported — decision reversal 2026-11-08).
status: completed
---

# Plan: role files gain primary/orchestrating capability

Epic: yaah-21uk. Reference design: OpenCode V2 agents. Decisions D1–D4 RESOLVED
by Greg 2026-11-08 (recorded on yaah-21uk.2). **Decision reversal 2026-11-08
(later same day): mid-session persona switching IS supported** — see
"Persona switching" below; it supersedes the earlier no-mid-session rule.

## Goal

One artifact type — `.agents/roles/*.md` — defines a **subagent** (today's
meaning) or the **primary/orchestrating** agent via `mode: primary|subagent|all`
(OpenCode vocabulary adopted). The session plan/build axis is **retired as a
user-facing concept**: `build` and `plan` ship as built-in primary agents, and
persona selection — switchable mid-session, like modes were — replaces mode
switching. Repos without primary files keep today's behavior, modulo the
renamed UX surface.

## Resolved decisions (yaah-21uk.2, closed; reversal appended)

- **D1** — frontmatter key IS `mode: primary|subagent|all`.
- **D2** — unified `.agents/roles/`; no new directory.
- **D3** — compose: persona body layers after the embedded identity.
- **D4** — built-ins: `build` (FullTools) and `plan` (ReadOnlyTools); default
  `build` + fallback chain.
- **Follow-on (backburner, yaah-21uk.15)**: provider/model routing in files.
- **REVERSED — persona switching**: personas ARE switchable mid-session
  (Greg, 2026-11-08 later): switch takes effect at the next loop build
  (mode-snapshot pattern), rebuilds prompt/belt/budget effects, persists on
  the session row, fires AgentChangedEvent. `/plan` `/build` become aliases
  of `/agent plan` / `/agent build`; ACP `session/set_mode` maps to persona
  switching for wire compat. This supersedes "no mid-session switching" in
  every earlier note (.9/.10/.11 pins were written against the old rule and
  are relaxed where they asserted immutability).

## Persona switching mechanics (the .12 design)

- `SetPersona(name)`: validate primary-capable → swap `s.persona` → rebuild
  systemPrompt from the RETAINED construction-time Layers with the new
  Persona body (byte-exact otherwise; identity anchor unchanged) → update
  mainPrompt + persisted SystemPrompt on the session row → publish
  AgentChangedEvent. Next loop build picks up budgets (already per-build)
  and the narrowed tool set.
- Tool narrowing moves from the construction-time belt to the per-run
  buildToolsForLevel filter (persona-allowed set joins the read-only level
  filter) so a switch re-narrows without registry surgery.
- Persistence: sessions.persona updated on switch (SetSessionPersona);
  resume restores the switched-to persona; legacy mode=plan rows migrate to
  persona=plan.
- Enforcement unchanged: ToolsLevel + GateWithheld stay code-owned; the
  level derives from the persona (plan persona → ReadOnlyTools).

## Phase status (11 of 14 beads closed)

- Phase 0 ✅ (gqqo, .1 anchor, .2 decisions)
- Phase 1 ✅ (.3 schema, .4 delegate filter, .5 persona registry)
- Phase 2 ✅ (.6 prompt layer + golden gate, .7 belt narrowing, .8 budgets)
- Phase 3 ✅ (.9 persistence, .10 UX + AgentChangedEvent, .11 reload)
- Phase 4 IN PROGRESS: **.12 = persona switching + mode retirement**
  (re-scoped by the reversal; dispatched in two parts: switching machinery,
  then aliases/migration/removal) · .14 docs after .12 lands

## Validation

Per phase: go test/vet/staticcheck/gofmt; golden gates (no primary files →
byte-identical) stay pinned throughout; new: switch rebuilds byte-exact
except the persona block; enforcement matrix persona×level.

## Non-goals

Provider/model routing in persona files (.15); identity replacement;
namespaced IDs; per-tool allow/ask/deny; dotagent ingestion.
