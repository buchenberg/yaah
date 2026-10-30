# Agent Instructions — the supervision surface

This document writes up, in one place, the instructions the yaah model
actually receives about the sub-agent surface: which prompt
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
| Orchestrator guidance: delegating to a sub-agent | `internal/prompts/subagent_tools.md` (`prompts.SubAgentToolsGuidance`) | Local sessions (`cmd/yaah/wiring.go`) — an isolated session takes the workspace delta below instead |
| Retained-lane guidance: proposals and settlement | `internal/prompts/retained.md` (`prompts.RetainedModeGuidance`) | Local sessions with the retained lane live (`agents.subagent.mode: retained` and a root scope) |
| `supervisor` tool description + schema | `internal/prompts/tools/supervisor.md` (`prompts.ToolDescription`) plus the schema in `internal/tools/supervisor.go` | Always with the tool, when registered |
| `delegate` tool description | `internal/prompts/tools/delegate.md` | Always |
| Sub-agent identity (batching, codebase respect, reporting) | `internal/prompts/subagent_identity.md` | Every sub-agent dispatch; orchestration guidance is deliberately omitted |
| Loop-detection steering message (convergence rule) | `internal/prompts/steering_message.md`, emitted by `LoopDetectionMiddleware` (`internal/agent/pipeline/loopdetect.go`) | When one tool dominates consecutive turns |
| Injected guidance (supervisor `inject`, advisory rules) | `SteerMiddleware` (`internal/agent/pipeline/steer.go`) | Next turn, as a `[STEER]`-prefixed user message, when guidance arrives mid-run |
| Isolated workspace delta (host-only tools, `apply_out`) | `prompts.IsolatedWorkspaceGuidance` (built in code) | Isolated sessions only, appended to the main prompt in place of the orchestrator guidance block (`cmd/yaah/wiring.go`) |
| Isolated sub-agent delta | `prompts.IsolatedSubAgentGuidance` (built in code) | Isolated sessions only, appended to each sub-agent's own system prompt (`internal/agent/runner/runner.go`) — it replaces nothing; sub-agents never receive the orchestrator guidance |

## The orchestrator's decision rule

The one-sentence version the model is given: **delegate exploration and
long-horizon work to a sub-agent; the orchestrator judges and lands the
result.**

From `subagent_tools.md`:

- **`delegate`** blocks until the dispatch returns unless `background: true`
  is set, in which case it returns a `job_id` immediately. Multiple calls in
  one turn run concurrently, each in its own world. Use it for independent
  work that can run concurrently, and for exploration whose raw tool calls
  would otherwise fill the orchestrator's context.
- The sub-agent never lands work: in the retained lane it returns a sealed
  proposal and the orchestrator settles it. That is the only gate; there is no
  mandatory review round. A reviewer role can still be dispatched explicitly or
  through a quality gate.

## The retained lane's contract

From `retained.md` (injected when the retained lane is live):

- A dispatch returns `status: "proposal"` with a `proposal_id`, the changed
  `files`, and a `diff`. **Nothing has changed on the working tree yet.**
- Settlement is `proposal` with an action: `select` (fast-forward; fails
  closed if the basis moved), `apply` (path-disjoint — three-way on the
  worktree lane, whole-path post-images on the container lane), `release`
  (export the sealed delta to a retention path; tear down a worktree, or
  consume the already-ended container manifest), `discard` (throw it away and
  free the worktree or manifest directory). A proposal settles exactly once.
- The dispatch envelope names `world` (`worktree` or `container`); a
  container proposal also names `target` (`host` or `session`). It can include
  `warning` for a degraded dispatch or `notes` for settlement caveats.
- Report roles return the report itself. A report that can mutate runs in an
  isolated report world; a read-only report role runs directly on the shared
  workspace. Neither seals a proposal.
- **The orchestrator commits, not the sub-agent.** In the worktree lane the
  child's index and object store live outside its jail, so git write commands
  are refused. In the container lane a commit stays in the disposable world.
  Changes arrive as file edits at settlement.
- Retrying means `discard` + dispatch again with what was learned — a
  discarded attempt costs one dispatch and leaves the tree untouched.

## The supervisor tool's verbs

From `internal/prompts/tools/supervisor.md` and the tool schema:

- **Scope inspection**: `list_scopes` (all scopes and their state),
  `status` (supervision system status: advisory-rule firings, bridged
  runs).
- **Intervention**: `inject` records guidance in the trace and delivers it to
  an active run's loop when possible; with no active run it is trace-only, and
  a full steering buffer means it is recorded but not consumed. Injecting into
  the session root steers the session itself. `halt` cancels a running loop,
  waits up to 10 seconds for quiescence, then records the kernel halt; it
  reports stopped, cancelled-but-not-stopped, or trace-only when there was no
  active run. Root halt requires `supervisor_halt_root`.

Two behaviors the model will meet and should not fight:

- **After a halt, the session refuses new work loudly.** A dispatch
  after the root was halted fails with the root-terminal explanation
  ("start a new session") instead of silently running unsupervised.
- **A confinement or egress posture that cannot be applied refuses the
  dispatch** with the reason and the fix, rather than running open. See
  `configuration.md`.

Injecting into the root scope steers the orchestrator's own session —
that is why root `halt` is gated behind the
`supervisor.halt_root` opt-in, off by default.

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

The `supervisor` tool stays registered whenever the scope manager is live:
scope inspection and inject/halt work on container-side scopes as they
do locally. The orchestrator guidance block is replaced by the isolated
workspace delta (`IsolatedWorkspaceGuidance`): the model is told which
host-only tools are missing and why, that `delegate` runs inside
the same container, and that changes reach the host only through
`apply_out` (dry run first, user approval, never claim work is saved
before it lands).

## Keeping this document honest

The instruction text is compiled in — a doc that restates it verbatim
would drift. This document therefore cites the embedded files and the
injection conditions; if you change instruction behavior, update the
source files and the tables here in the same PR.
