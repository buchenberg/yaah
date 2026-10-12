# Agent Instructions — the supervision surface

This document writes up, in one place, the instructions the yaah model
actually receives about the supervised sub-agent surface: which prompt
blocks exist, what they tell the model to do, when each is injected, and
where its source of truth lives. It is a map of the instruction text, not
a second copy of it — when behavior changes, the embedded files in
`internal/prompts/` and the tool descriptions in code change, and this
document follows.

The injection architecture (which layer assembles what, in order) is
covered separately in [prompts.md](prompts.md).

## Where the instructions live

| Instruction block | Source | Injected when |
| --- | --- | --- |
| Orchestrator guidance: `spawn_subagent` vs `supervised_task` | `internal/prompts/subagent_tools.md` (`prompts.SubAgentToolsGuidance`) | Local sessions, when the session's scope manager is live (`cmd/yaah/wiring.go`) — an isolated session takes the workspace delta below instead |
| `supervised_task` tool description | `internal/prompts/tools/supervised_task.md` (`prompts.ToolDescription`) | Always with the tool, when registered |
| `supervisor` tool description + schema | In code: `internal/tools/supervisor.go` (`Description()`, `Schema()`) | Always with the tool, when registered |
| `spawn_subagent` (plain `task`) tool description | `internal/prompts/tools/task.md` | Always — the unsupervised path exists in every mode |
| Sub-agent identity (batching, codebase respect, reporting) | `internal/prompts/subagent_identity.md` | Every sub-agent dispatch; orchestration guidance is deliberately omitted |
| Loop-detection steering message (convergence rule) | `internal/prompts/steering_message.md`, emitted by `LoopDetectionMiddleware` (`internal/agent/pipeline/loopdetect.go`) | When one tool dominates consecutive turns |
| Injected guidance (supervisor `inject`, advisory rules) | `SteerMiddleware` (`internal/agent/pipeline/steer.go`) | Next turn, as a `[STEER]`-prefixed user message, when guidance arrives mid-run |
| Isolated-mode deltas (host-only tools, `apply_out`) | `prompts.IsolatedWorkspaceGuidance` / `IsolatedSubAgentGuidance` (built in code) | Isolated sessions only, replacing the orchestrator guidance block |

## The orchestrator's decision rule

The one-sentence version the model is given: **parallel work uses
`spawn_subagent`; work that must succeed or roll back cleanly uses
`supervised_task`.**

From `subagent_tools.md`:

- **`spawn_subagent`** is non-blocking. It supports parallel and
  background execution and is the tool for independent tasks that can
  run concurrently. It has no rollback safety — bad changes persist.
- **`supervised_task`** is BLOCKING. It runs a single sub-agent to
  completion with an automatic git checkpoint and rollback: a failed
  attempt is reverted and retried with guidance. It is the tool when
  correctness matters more than speed and a failed attempt must not
  leave the workspace broken.
- The model is told explicitly NOT to use `supervised_task` for parallel
  work — it blocks the orchestrator's own loop.

This block is only appended when the supervision infrastructure is
live. With tracing off, the `supervised_task` and `supervisor` tools are
not registered and every role stays on `spawn_subagent`, so the
instruction and the tool belt cannot disagree.

## `supervised_task` instructions

The tool description (`tools/supervised_task.md`) pins three contracts:

**Automatic mode (default).** One attempt = one git checkpoint. On
failure the attempt's changes are rolled back and the sub-agent is
retried with the failure's guidance. The result is a JSON envelope:
`status` (`completed`, `failed`, `cancelled`, `rollback_failed`,
`recheckpoint_failed`, `exhausted`), `attempts`, and `result` on success
or `error`/`partial` on failure. The model is told to check `status`
first — a failed-and-rolled-back run is a normal outcome, not an error
to retry blind. With per-role turn checkpointing enabled, `restores` and
`restored_from` report in-attempt turn rewinds.

**Review mode (`review: true`).** Instead of automatic retry, the
sub-agent completes ONE work unit and the model receives a review
envelope: `session_id`, `status`, `unit`, `result` (the sub-agent's
report), `diff` (bounded patch), `files`, optional `error`, and `next`
(the allowed verdicts). The model then drives the cycle through the
supervisor tool.

**Workspace ownership.** While a review session is open, the session
owns the workspace: the model is instructed not to edit files itself
between verdicts, because `rollback` and `choose` overwrite the working
tree. Work units should be scoped so each stays reviewable — one
coherent change per unit.

## The supervisor tool's verbs

From the description in `internal/tools/supervisor.go`:

- **Scope inspection**: `list_scopes` (all scopes and their state),
  `status` (supervision system status: advisory-rule firings, bridged
  runs).
- **Intervention**: `inject` (push guidance into a running sub-agent's
  loop — delivered between turns as a high-priority user message),
  `halt` (force-stop a scope; the run reports a deliberate
  "halted by supervisor" stop, not raw cancellation noise).
- **Review-session verdicts** (session started via
  `supervised_task review:true`): `continue` (keep the unit's files and
  conversation, run the next unit with optional guidance), `rollback`
  (revert the unit's files AND conversation, rerun with the more
  specific `guidance`), `review_diff` (re-fetch the current diff),
  `fork` (rewind and run two competing variants from the same
  checkpoint), `choose` (apply the winning variant), `accept` (keep the
  work, close the session), `abort` (rewind the unaccepted unit, close
  the session).

Two behaviors the model will meet and should not fight:

- **A merge-gate denial is a verdict, not an error.** If the gate
  refuses the final accept, the tree is rolled back to the session
  start, the denial reason is injected into the re-run's prompt, and the
  session stays open — the model re-runs the unit addressing the reason.
- **After a halt, the session refuses new work loudly.** A dispatch
  after the root was halted fails with the root-terminal explanation
  ("start a new session") instead of silently running unsupervised.
  Likewise a verdict against a session lost to a crash refuses with
  "start one with `supervised_task review:true`" rather than a mystery
  error.

Injecting into the root scope steers the orchestrator's own session —
that is why root `halt` is gated behind the
`supervisor_halt_root` opt-in, off by default.

## What a sub-agent is told

Sub-agents get the lean identity (`subagent_identity.md`), not the
orchestration guidance — they cannot act on any of it (and cannot spawn
further sub-agents). Its operating rules:

- **Batch tool calls** — the cardinal rule: fire all independent reads,
  greps, and globs in one turn; plan all file reads before reading any;
  answer from context first; stop searching when the question is
  resolved.
- **Respect the codebase** — read before editing, match existing style,
  minimal diffs, stay inside the assigned task.

The runner prepends an environment header and appends the role
guidance, the evidence/interpretation contract
(`contract_rules.md`), and the escalation format (`escalation.md`) on
top of this.

Guidance that arrives while the sub-agent runs — a supervisor `inject`,
or an advisory rule's named guidance — is surfaced by the loop's steer
middleware as a `[STEER]`-prefixed user message next turn. Separately,
when loop detection sees one tool dominating consecutive turns, its
convergence message (`steering_message.md`) tells the sub-agent to
apply its findings and act.

## Isolated sessions

`supervised_task` cannot exist in isolated mode — it provisions sandboxes
itself, so it cannot run inside the isolation it creates — and with it
the review-session cycle (sessions are started through it). The
`supervisor` tool stays registered whenever the scope manager is live:
scope inspection and inject/halt work on container-side scopes as they
do locally. The orchestrator guidance block is replaced by the isolated
workspace delta (`IsolatedWorkspaceGuidance`): the model is told which
host-only tools are missing and why, that `spawn_subagent` runs inside
the same container, and that changes reach the host only through
`apply_out` (dry run first, user approval, never claim work is saved
before it lands).

## Keeping this document honest

The instruction text is compiled in — a doc that restates it verbatim
would drift. This document therefore cites the embedded files and the
injection conditions; if you change instruction behavior, update the
source files and the tables here in the same PR.
