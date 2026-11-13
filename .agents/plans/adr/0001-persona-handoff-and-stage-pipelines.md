---
name: 0001-persona-handoff-and-stage-pipelines
description: "Decide whether persona switches should create a model-written handoff summary and whether an agent-callable persona switch or first-class stage pipeline should be added."
status: proposed
date: 2026-11-10
deciders: Greg
---

# ADR 0001: Persona handoff and stage pipelines

## Status

Proposed. This ADR records the analysis and the recommended scope; it does not
authorize implementation beyond the delegation-based workflow described below.

## Context

yaah already has:

- **Personas** — primary-capable role files (`mode: primary` or `all`) that
  decide the session's system-prompt body, tool narrowing, budget, permissions,
  and provider/model routing. `SetPersona` switches mid-session and takes effect
  at the next loop build (`cmd/yaah/persona.go`). The prompt currently says only
  the user switches agents (`cmd/yaah/mode.go`).
- **Three independent session axes** — agent (persona), mode (read-only vs
  full), and approval (`docs/agents.md`). Persona switching does not change the
  mode axis.
- **Delegation** — the orchestrator dispatches specialist sub-agents into
  isolated worktrees or containers. Sub-agents return reports or sealed
  proposals; they never commit. Roles carry their own tools, budgets, routing,
  permissions, response contract, and escalation format.
- **Proposal threading** — `delegate(seed_from: <proposal_id>, seed_mode:
  workspace)` starts a new dispatch, with its own role and toolset, from a
  retained proposal's sealed workspace (`docs/sub-agents.md`). This is a real
  handoff artifact: the next stage sees the actual changed files, not a prose
  summary.
- **Compaction** — pressure-driven summarization into an anchored brief (Goal,
  Constraints, Progress, Decisions, Next Steps, Critical Context, Relevant
  Files) that rebases persistence (`internal/agent/context_manager.go`,
  `internal/prompts/summary_template.md`).
- **Session-scoped state** — proposals, background sub-agent jobs, wake
  watches, approvals, usage, and the session ID belong to the session, not the
  persona. A proposal tool can only list, show, or settle its own session's
  proposals (`internal/dispatch/proposal.go`).

The proposal under consideration was:

1. When switching personas, have the model produce a compact summary and pass
   it into the new persona's prompt as a "refresh."
2. Give the agent a tool to switch personas, taking a session summary as a
   parameter, so a session could relay work through roles — for example,
   analyst → senior developer (who dispatches implementation sub-agents) → QA.

The motivating intuition is that a persona is effectively a fresh session: what
matters to an analyst is not what matters to a developer, and not what matters
to QA. The question is whether yaah should make that first-class.

## Problem

A first-class persona relay would need to define and maintain:

- a **stage contract** (declared sequence, required artifacts, entry/exit
  criteria);
- **context segmentation** (what the incoming persona sees; whether the old
  transcript is dropped, summarized, or retained);
- **handoff policy** (who may hand off to whom; whether a switch may widen
  tools, permissions, routing, or mode);
- **pipeline state** (which stage is active, what is pending, what was
  rejected, how to resume);
- **landing authority** (who may apply a sealed proposal);
- **cross-surface behavior** (REPL, TUI, web, ACP, serve) and **persistence**
  (resume, mode/approval, audit).

The existing delegation path already expresses the analyst → developer → QA
sequence, but only as an orchestration convention: the orchestrator (the model)
is the state machine, and the procedure lives in its prompt or a skill. The
step-level guardrails are strong; the pipeline-level ones are absent.

## Decision

**Do not add an agent-callable persona switch or a first-class stage/handoff
engine at this time.**

Adopt the delegation-based pipeline as the supported pattern:

1. Express multi-role workflows as **sub-agent delegation**: the orchestrator
   dispatches each role in sequence and settles the result.
2. Use **sealed proposals + `seed_from`** as the handoff artifact. The next
   stage is seeded from the previous stage's real workspace, not from a
   model-written summary.
3. Put the procedure in a **skill** and the stage state in a **plan**. The
   skill defines the stages, required outputs, and gates; the plan records what
   is done, pending, and blocked.
4. Keep the existing per-dispatch guardrails: role tool lists, budgets,
   permissions, isolation, confinement, supervision, evidence contracts,
   escalation, proposal settlement, conflict detection, and traces.

If a user-facing gap remains after that, the **only** permitted follow-up is a
small, harness-generated, role-scoped handoff brief on **user-initiated**
persona switches, plus optional compaction when the incoming persona's model or
context window requires it. That follow-up must not add a new session row, a
context-segment store, a handoff graph, or an agent tool.

The full stage engine is explicitly deferred, not rejected forever. It may be
revisited only under the conditions in "Revisiting" below.

## Options considered

### A. Full persona relay / stage engine

Persona switch starts a new context segment; the model writes a summary; an
agent-callable `handoff` tool switches roles; role files declare a stage graph;
stages carry mode, permissions, and landing authority.

**Rejected for now.** It adds a fourth session axis and a second way to compose
agents (relay alongside hub-and-spoke delegation). It also has to solve
session-scoped ownership, mode persistence, landing authority, and resume. The
cost is a subsystem, not a feature.

### B. User-initiated persona switch with a handoff brief

Keep the existing switch; generate a role-scoped brief from a canonical task
record and recent transcript; optionally compact when the incoming model needs
it. No new tool, no graph, no segments.

**Acceptable fallback.** It delivers the "refresh" intuition with little new
state. It does not give role-scoped omission of the transcript unless compaction
runs, and it does not enforce a stage sequence.

### C. Delegation pipeline with skill + plan + proposal handoff

The orchestrator delegates analyst → developer → QA; each stage is a role file;
the handoff is a sealed proposal seeded into the next stage; a skill defines the
procedure and a plan holds the state.

**Chosen.** It uses existing primitives, preserves the hub-and-spoke model, and
gives QA the real workspace and test tools.

### D. Status quo only

Keep delegating ad hoc with no skill or plan.

**Insufficient.** It leaves the procedure and evidence expectations implicit, so
quality depends entirely on the orchestrator's prompt adherence. A skill plus a
plan is a cheap improvement over pure convention.

## Rationale

### Complexity

yaah already asks users to understand personas, roles, modes, approval,
delegation, and proposals. `docs/agents.md` already presents three independent
axes. A stage engine would add stage, handoff, segment, handoff graph, brief
schema, and landing authority, and every new axis multiplies the cross-surface
test matrix (REPL, TUI, web, ACP, serve, resume). That is too much surface for
the workflow value it adds.

### Session ownership

Proposals and background jobs are session-scoped. A genuinely new session would
not see the developer's open proposals or in-flight sub-agents, which is exactly
what QA needs. A context segment inside the same session avoids that, but it is
a new persistence and resume mechanism. Delegation keeps the same session and
the same proposal registry throughout.

### Evidence fidelity

`seed_from` gives the QA stage the developer's sealed workspace. A model-written
summary gives QA the developer's account of the work. For a review stage, the
former is strictly better: QA can inspect the diff and run the tests rather
than trusting a narrative that may omit failures.

### Per-stage tools are already flexible

A sub-agent role can carry `go_test`, `bash`, and other verification tools
without changing the session's mode. By contrast, the read-only inspection set
excludes `go_test`, `bash`, `delegate`, and `proposal`
(`internal/agent/tools.go`), so a persona-relay QA stage could not run tests in
read-only mode and would need an authority transition. The delegation path is
more flexible here, not less.

### Convention is still fragile

A skill and a plan are guidance, not enforcement. The orchestrator can still
skip a stage or apply without a QA verdict. That is an acceptable starting
point, but it should be acknowledged. If gate-skipping becomes a real failure
mode, add **one** enforcement point (for example, a proposal-apply check that
requires a recorded QA verdict) rather than a workflow engine.

## Consequences

### Positive

- No new session axis and no second orchestration model.
- Reuses isolation, budgets, permissions, contracts, proposals, settlement,
  background jobs, and traces.
- QA receives the actual workspace and can run verification tools.
- No new authority path: the orchestrator remains the only landing authority.
- The workflow can be documented and versioned as a skill plus a plan.

### Negative

- The pipeline remains model-discretionary; stages can be skipped.
- Stage state lives in a plan and the orchestrator's context, not in a
  resumable runtime record.
- Context is not role-filtered by default; every sub-agent sees the task it is
  given, and the orchestrator sees the full transcript.
- There is no stage-level UI ("2 of 3") beyond plans, traces, and events.
- There is still no agent-callable persona switch for unattended sessions.

### Risks and mitigations

| Risk | Mitigation |
|---|---|
| Orchestrator skips QA or applies without a verdict | Skill requires a verdict; plan records it; add one apply gate if observed |
| Developer's summary hides a failure | QA is seeded from the proposal workspace and inspects the diff/tests |
| Stage state drifts after compaction or resume | Keep stage status in a plan; reference proposal IDs and commands |
| Skill is not loaded in a session | Reference it from the orchestrator prompt or project instructions |
| `seed_from` is worktree-only; report-lane roles cannot be sources | Use worktree proposals for handoffs; report roles remain terminal reviewers |
| Cost grows from repeated dispatch | Role budgets, per-call caps, and background jobs stay in force |

## Revisiting

Revisit this decision only with concrete evidence, such as:

1. Real workflows repeatedly fail because users need to **interact with each
   stage directly**, not just receive the orchestrator's synthesis.
2. Production models reliably **skip gates** even with a skill and plan, and a
   single enforcement point cannot fix it.
3. A stage genuinely needs a **session-level mode or permission transition**
   that a sub-agent role cannot express.
4. Long pipelines need **resumable stage state** beyond what a plan provides.

If revisited, cap the scope:

- linear stages only; one optional back-edge; a hop limit;
- declared successors in role files, not "switch to any persona";
- one new user-visible concept;
- reuse existing state (plan, todo, memory, compacted summary) before adding a
  store;
- default off; opt-in per role file;
- no general DAG or workflow engine.

The revisit must answer four questions explicitly: does the new stage start a
**context segment** or keep the transcript, **who may land** the result, how are
**proposals and jobs** transferred, and how is **mode** persisted across resume.

## Implementation notes (for the chosen option)

- Define the procedure in a project skill (for example,
  `.agents/skills/role-pipeline/SKILL.md`) or a plan template: stages, role
  names, required outputs, and gates.
- Create role files for the stages (`analyst`, `senior-dev`, `qa`) with
  appropriate `tools:` lists. QA's list includes `go_test` and any inspection
  tools it needs.
- Record stage state with the `plan` tool or a dedicated workflow plan; include
  proposal IDs and test commands so the next stage can verify rather than
  paraphrase.
- Dispatch sequentially. Use `delegate(seed_from: <proposal_id>, seed_mode:
  workspace)` to hand the developer's sealed workspace to QA.
- Settle with the `proposal` tool only after the required verdict exists. If
  enforcement is needed, add it at the apply path, not as a new engine.
- Emit/retain traces for each dispatch and settlement so the pipeline is
  auditable after the fact.

## References

- `cmd/yaah/persona.go` — `SetPersona`, prompt rebuild, routing, persistence.
- `cmd/yaah/mode.go` — mode directive; "Only the user switches agents."
- `docs/agents.md` — persona selection, switching, and the three session axes.
- `internal/agent/context_manager.go` — compaction and anchored summary.
- `internal/prompts/summary_template.md` — the summary/handoff-shaped template.
- `internal/dispatch/proposal.go` — session-scoped proposal registry.
- `internal/dispatch/proposal_rehydrate.go` — durable proposals across restarts.
- `docs/sub-agents.md` — `seed_from` / `seed_mode` threading.
- `internal/tools/delegate_schema.go` — delegate schema.
- `internal/agent/tools.go` — read-only inspection set and persona tool gate.
- `cmd/yaah/persona_belt.go` — merged permission rulesets.
- `cmd/yaah/build_loop.go` — context window resolved from the active model.
- `cmd/yaah/session.go` — session-owned background jobs, wake manager, and axes.
- `cmd/yaah/resume.go` — resume; mode re-derives from config rather than the
  vestigial `sessions.mode` column.
