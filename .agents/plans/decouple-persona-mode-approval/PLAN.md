---
name: decouple-persona-mode-approval
description: Decouple persona, mode, and approval into three orthogonal session axes switchable via ACP configOptions.
status: approved
---

# Decouple persona, mode, and approval into three orthogonal session axes

## Summary

Today the session "mode" is a lossy projection of the persona (derived from the
persona **name** `== "plan"`, not from the read-only **capability**), the
`read_only` flag lives on persona files, and approval is fixed at loop-build
time. This plan separates them into three independent, mid-session-switchable
axes — **agent** (who), **mode** (may I mutate), **approval** (must writes ask)
— each surfaced through the same ACP `configOptions` / `session/set_config_option`
seam.

## Companion plan

This plan is executed as **workstream W0** of the master plan
*opencode-family-agent-parity* (at `../opencode-family-agent-parity/PLAN.md`
— a sibling directory in this plans dir, inside the yaah repo).
Division of labor: this plan owns the axis-restructure scope below — its
Phases 1–5 are W0 verbatim; the master plan owns sequencing, milestones, and
the parity workstreams W1–W7 that build on this plan's seams (W4 model
persistence rides Phase 3's configOptions composition; W5 permission rulesets
resolve against Phase 1's session `readOnly` and compose their `ask` verdicts
with the approval axis). Terminology: the `mode` axis here is the **session**
axis (read-only/full) — unrelated to the role-file `mode:` key
(primary/subagent/all) that master W3 changes.

## Motivation

1. **`mode` is derived from identity, not capability.** `personaLevel`
   (`cmd/yaah/mode.go:29`) enforces `ReadOnly` → `ReadOnlyTools`, but `Mode()`
   (`cmd/yaah/mode.go:70`) reports `PersonaName() == subagent.PlanPersonaName`.
   Every read-only persona not literally named "plan" (e.g. `architect`)
   reports `mode: "build"` to ACP clients. The header comment admits it.
2. **Persona and regime are fused.** `read_only: true` on a persona bakes the
   regime into the identity, so there is no "architect, but writable" or
   "build, but read-only".
3. **Approval is not switchable mid-session.** `ApprovalMode` is resolved once
   at loop-build time (`build_loop.go:37` / `serve.go:289` →
   `WithApprovalMode`), unlike persona/mode which re-apply at the next loop
   build.

## Target model

| Axis | Question it answers | ACP surface | Values | Where it lives after |
|---|---|---|---|---|
| **agent** (persona) | who am I / what do I advertise | `session/set_agent` + `configOptions` `id: "agent"` | persona names | persona file (body, `tools:`) |
| **mode** | may I mutate | `session/set_mode` + `configOptions` `id: "mode"` | wire `build`/`plan`; internal read-only/full | **session field** (`readOnly`) |
| **approval** | must dangerous calls ask | `configOptions` `id: "approval"` | `allow` / `ask` / `deny` | **session field** (`approvalMode`) |

Core decoupling: `read_only` comes **off the persona**. "Plan mode" means only
"read-only" (mutating tools withheld); the planning directive and the `plan`
tool stay properties of the `plan` persona body and the tool registry,
respectively.

## Decisions

| Id | Decision | Chosen (recommended) |
|---|---|---|
| D1 | mode naming (internal + display) | **"Read-only" / "Full"** — matches `ReadOnlyTools`/`FullTools` and ACP's own "read-only mode" language. Alternative: "Inspect" / "Modify". Wire values stay `build`/`plan` either way. |
| D2 | `read_only` on personas | **Remove entirely** — read-only-ness is a session mode set independently (default via new `agents.default.mode`). Alternative: keep a `default_mode` hint so `plan`/`architect` start read-only but remain switchable. Behavioral change to flag: picking the `plan` persona will no longer *imply* read-only. |
| D3 | approval axis scope | **Included** (Phase 3) — same seam as mode. May be sliced off as a follow-up to land agent+mode first. |

> These are the recommended defaults; override before Phase 1 if you disagree.

## Non-goals

- Wire mode ids stay `build`/`plan` (ACP spec conformance); rename internal +
  display only.
- Per-origin `MCPApproval` policy stays config-only (a trust-boundary decision,
  not a runtime toggle). It is checked before the global gate in `classifyGate`
  (`internal/agent/agent_safety.go:79`).
- No new axes beyond agent/mode/approval; no web/gateway surface; no license or
  version changes.
- Permission rulesets (per-tool allow/deny/ask globs, last-match-wins) and
  per-session model persistence are **master-plan scope** (W5 / W4), building
  on this plan's approval axis and configOptions seam — out of scope here so
  W0 stays landable in ~1 week.

## Phases

### Phase 1 — session-level mode + approval state
- Add `readOnly bool` and `approvalMode string` to the session struct; default
  from config (`agents.default.mode`, existing `approval` resolution chain).
- Rewrite `Mode()` (`cmd/yaah/mode.go:60`) to derive from `readOnly`, **not**
  `PersonaName() == subagent.PlanPersonaName`. Fix the header comment (it
  currently claims the mode seam was "retired").
- `personaLevel` reads the session `readOnly`, not `persona.ReadOnly`.
- Loop builder passes `ApprovalMode` from the session field instead of the
  initial `resolveApproval(cfg, opts)` (`build_loop.go:37`, `serve.go:289`).

### Phase 2 — persona schema change
- Remove `ReadOnly` from `rolefile.Frontmatter` (it currently marshals
  unconditionally — `Marshal` output changes; pin tests on parsed values, not
  bytes) and from `subagent.Persona`.
- Drop `read_only: true` from `.agents/roles/architect.md` and the shipped
  `plan` persona. Architect stays structurally read-only via its `tools:` list;
  its body already says it never writes.
- Update `role_def.go` / `normalizeRoleMode` / the primary-capable warnings
  that reference `read_only`.
- `subagent.Personas()` and `ResolvePersona` no longer carry/consult `ReadOnly`;
  `SetAgentResult.ReadOnly` reports the session mode instead (or is dropped —
  per D2).

### Phase 3 — ACP `configOptions` surface
- `config_options.go`: replace `buildModeConfigOptions` with three builders —
  `buildAgentConfigOptions` (enumerates `subagent.Personas()`),
  `buildModeConfigOptions` (read-only/full, wire `build`/`plan`),
  `buildApprovalConfigOptions` (`allow`/`ask`/`deny`). `buildSessionConfigOptions`
  composes agent + mode + approval + model, with `currentValue` from session
  fields.
- `protocol.go`: add `ConfigOptionAgentID`/`ConfigOptionCategoryAgent` and
  `ConfigOptionApprovalID`/`ConfigOptionCategoryApproval` (extensions); keep
  `ConfigOptionModeID`/`ConfigOptionCategoryMode` (spec).
- `prompt.go` `handleSetConfigOption`: add `agent` (→ `ResolvePersona` +
  `SetPersona`) and `approval` (→ validate `allow|ask|deny` + set session field)
  cases; the mode case sets `readOnly` directly. Update the `default:`
  "supported" string.
- Keep `handleSetMode` (`session/set_mode` → mode axis) and `handleSetAgent`
  (`session/set_agent` → agent axis) as shortcuts. Update the six
  `buildSessionConfigOptions` call sites (`prompt.go:93,150,220,224,243`,
  `session_lifecycle.go:208,395`) to pass session values (`PersonaName()`,
  `readOnly`, `approvalMode`) instead of `Mode()`/`p.Value`.

### Phase 4 — directive + docs
- `regimeSentence`/`personaLabel` (`cmd/yaah/mode.go`): directive becomes
  "read-only mode — inspect and report, do not modify" vs "full access",
  decoupled from the word "plan".
- Docs: `docs/agents.md` (persona reference — drop `read_only` row, document the
  mode/agent split), `docs/acp.md` (configOptions table), `docs/configuration.md`
  (new `agents.default.mode`), `docs/prompts.md`.
- **`internal/parity/doc_inventory_test.go` pins `docs/features.md`'s
  "## Plan mode" section byte-for-byte** (heading, the marker "narrows the
  toolset to the **inspection set**", the inspection-set paragraph, and it
  forbids the phrase "read-only tools"). Rewrite `features.md` and the parity
  test **in lockstep** — this is the single most fragile spot in the plan.
  The lockstep rewrite is **owned here, at M0**; the master plan's W7 docs
  pass is additive-only and must not re-touch the pinned section or reopen
  the parity test.

### Phase 5 — tests + validation
- Update `mode_test.go`, `persona_test.go`, `persona_belt_test.go`, `rolefile`
  tests, `internals_test.go` (`buildModeConfigOptions` callers), `server_test.go`
  (configOptions assertions, `sess.Mode()` semantics), `session_lifecycle_test.go`.
- Gates: `gofmt -l .`, `go vet ./...`, `staticcheck ./...`, `go test ./...`.

## Known gotchas (verified)

- `Mode()`'s name-vs-capability mismatch is the root defect (admitted in its own
  comment); Phase 1 fixes it.
- `read_only` in `rolefile` emits unconditionally (no `omitempty`), so dropping
  it changes `Marshal` output.
- Approval has a second, per-origin knob — `MCPApproval` — checked before the
  global gate in `classifyGate`. Keep it config-only.
- Read-only mode **preempts** approval (`dangerousGate` → `GateWithheld` before
  `GateGlobal`), so the axes compose cleanly — no six-way fake modes.
- `tools.NewRegistry()` installs all built-ins (not empty); `NewLeafTool(name)`
  returns nil for runtime-wired tools (`memory_*`, `delegate`) — only leaf
  factories resolve.

