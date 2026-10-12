# Development Plan: Shepherd Kernel Adoption (yaah side)

**Status:** drafted 2026-10-11; decisions SETTLED 2026-10-11 (see §4) · **Author:** buchenberg + Mistral Vibe
**Kernel dependency:** `shepherd-kernel-go` core **v0.10.0** (pinned; containerd nested v0.1.5) — see §8
**Related:** `supervised-task-plan.md` (the supervision/rollback substrate this builds on),
`isolated-workspace-activation.md` (complete; its §9 fenced substrate adoption to a later plan — this is that plan's container, though materialization itself stays fenced here too)

---

## 1. Why this plan exists — the adoption audit

> **Frozen snapshot.** The table below is the pre-adoption audit, kept
> exactly as written — it is the record of why this plan existed, and its
> verdicts describe yaah *before* the adoption, not today. The plan
> completed: rows 6, 7, 9, 10, 11 closed through phases A–D; rows 2 and 8
> shipped with the rules-engine phases; the follow-up gaps the review
> found are planned in `docs/plans/post-adoption-hardening.md`, and the
> kernel-side fixes landed in the kernel's plan 06 (v0.14.0, re-pinned in
> phase D). Rewriting history in place would erase the audit; the ticks
> below stay frozen, the same convention the kernel used for its
> archived plans.

The kernel (the Go implementation of the Shepherd trace kernel, arxiv 2605.10913)
ships a full supervision surface. yaah uses roughly half of it, and — one finding
from this audit — the half it uses most visibly (the `supervisor` tool) is not
actually wired to execution. The audit, verified by reading both repos:

| # | Kernel capability | Designed use | yaah today | Verdict |
|---|---|---|---|---|
| 1 | Trace store: declare→capture, causal DAG | every action durable, causally linked | `ShepherdTraceMiddleware` (`internal/agent/pipeline/trace.go`): per-tool-call declaration+capture, turn roots, per-turn frontiers; sub-agent middleware shares the store | **Aligned** |
| 2 | Effect bus | real-time observation, no polling | created and attached (`scope_init.go`) but **zero subscribers** — inert | Unused |
| 3 | Checkpoints / restore | workspace + conversation rewind | `supervised_task` attempt rollback, `ShepherdTurnCheckpointer`, review sessions | **Aligned** |
| 4 | Reusable workspace states | fork-and-choose | review-session fork variants (`supervised_session.go:555+`) | **Aligned** |
| 5 | Worktree isolation per scope | physical rollback of speculative branches | supervised workdirs (`subAgentSandbox`), fork variants | **Aligned** |
| 6 | Scope tree: Fork / Merge / Discard | causally-linked execution branches | flat `mgr.Create` per sub-agent (`runner.go`), joined by yaah's own `RecordTraceOwner` table | **Partial** — no tree |
| 7 | Supervisor inject/halt | intervene in a running scope | `supervisor` tool calls `scope.Inject`/`scope.Halt` — but **nothing reads either back into the loop**: Inject appends a trace fact; Halt flips scope state and (for isolated children) destroys the sandbox. No guidance reaches the sub-agent; no run is cancelled. The tool reports success | **Inert** — the plan's hardest finding |
| 8 | Rules engine (`Supervisor`, built-in rules) | automatic meta-agent supervision | not used; `approval` middleware acts pre-dispatch on the parent loop only — sub-agent loops are hard-coded `ApprovalMode: "allow"` (`subagent_loop.go:111`), so a sub-agent's destructive call dispatches ungated today | Unused — with a **coverage gap** the blocking guard closes |
| 9 | Merge gate (`ProposeMerge`/`CommitMerge` + reviewer) | nothing merges without review | review sessions accept via a hand-rolled checkpoint-restore+apply path | Reimplemented by hand |
| 10 | Settlement (`Seal`/`Settle`, best-of-n) | consume-once output selection | fork/choose replays captured trees by hand; no sealed outputs | Reimplemented by hand |
| 11 | Recovery (`RecoverScopes`) | the trace is the registry; restart-safe scopes/checkpoints/outputs | not used; a restart reopens the store and reconstructs nothing | Unused |
| 12 | Handles (`StartTask`/`Run`/`TaskControl`) | kernel-driven task execution with frontier cuts | not used; yaah's loop is provider-driven with its own runner | Unused — assessed, likely stays unused (D6) |
| 13 | Materialization / substrates | declarations are commitments; idempotent apply with `clean_failure` honesty | fenced by the isolation plan §9; tools write directly, the trace observes | Fenced (D7) |

**The goal of this plan:** close rows 6, 7 (Phase A), 2, 8 (Phase B), 9, 10
(Phase C), 11 (Phase D) — without regressing rows 1, 3, 4, 5, and without
duplicating anything the yaah pipeline already does better. The bus's first
consumer (row 2) arrives with the rules engine (row 8), not before it.

---

## 2. Principles

1. **The trace is the registry.** Any state the trace can rebuild, the trace
   should own. yaah-side persistence (memory DB) keeps only what the trace
   cannot express (summaries, embeddings, session metadata).
2. **One source of truth per concern.** The kernel's rules engine becomes the
   *in-trace* supervisor; yaah's `approval` middleware remains the
   *pre-dispatch policy* gate. They watch different things: approval decides
   before a call dispatches (parent session policy), rules watch what actually
   happened — including sub-agent captures the parent's middleware never sees.
   Neither replaces the other, and no third engine is introduced.
3. **Supervision must reach execution.** A scope intervention that only
   appends a trace fact is a log line, not supervision. Every kernel
   supervision verb adopted here gets an execution-side bridge or it does not
   ship.
4. **Local mode stays the reference; isolated mode gets parity where the
   containerd backend supports it.** The containerd sandbox implements
   Capture/Apply (the D1 bootstrap uses both), with single-use-per-state Apply
   semantics — good enough for fork/seal/settle, and the plan says where the
   parity line is drawn per phase.
5. **Every phase lands green and shippable**, on the same PR-per-step cadence
   and gates as the isolation plan.

---

## 3. Current state — the facts the plan builds on

- Infra: `pipeline.InitShepherdInfrastructure(traceDir, buf)` creates store,
  bus (inert), scope manager; wired only when `shepherd_trace_dir` is set
  (`cmd/yaah/wiring.go` ~460). `tools.SharedTraceStore`/`SharedScopeManager`
  are the global seams the supervisor tools read.
- Main session: **no root scope**. Sub-agent scopes are flat
  `mgr.Create(subTraceID, subAgentSandbox(...))` (`runner.go` ~344); the
  memory join is `db.RecordTraceOwner(subTraceID, sessionID, role)`.
- Review sessions (`internal/tools/supervised_session.go`): own scope
  (`mgr.Create` at :301, LocalGitSandbox or worktree), checkpoint/restore,
  hand-rolled fork state (`CaptureWorkspace`/`ApplyWorkspace`), state machine
  continue/rollback/fork/choose/review_diff/accept/abort.
- `supervised_task` (`internal/tools/supervised_task.go`): attempt-level
  checkpoint/restore + retry; worktree variants.
- Steering: the parent loop has `SteerMiddleware` (drains a channel in
  `PrepareStep`); `loop_detection` already injects steering messages
  (`loopdetect.go:154`). **Sub-agent loops have no steering channel and no
  per-scope cancellation** — the task tool's ctx is the only lever.
- Trace reads used: `ReadOwnerPrefix` (sub-agent trace profiles, memory);
  frontiers published per turn (`trace.go` publishFrontier).
- Config: `observability.otel`, `agents.default.shepherd_trace_dir`,
  supervised settings under `agents.default` (`supervised_*`).

---

## 4. Decisions (settled 2026-10-11)

**D1. Scope tree — DECIDED: the session gets a root scope; sub-agent scopes become forks of it. Snapshot is nil.**
`mgr.Create(sessionID, sandbox)` at infra init. Local mode: `NewLocalGitSandbox(repo)`
(already the supervised default). Isolated mode: a pure-causal root (nil sandbox)
— the container is the substrate and must not be checkpointed by host-git (the
isolation plan's D2 reasoning). `spawn_subagent` dispatches `mgr.Fork(root,
subTraceID, nil)` — shared sandbox, matching one-sandbox-per-session; supervised
workdir dispatches become `mgr.ForkIsolated(root, subTraceID, nil,
worktreeSandbox)`. `RecordTraceOwner` stays — the memory join is orthogonal to
kernel causality. **The Fork snapshot is nil, settled:** seed messages stay
yaah-side (`InitialMessages`) — the sub-agent's own trace records its prompt,
sub-agent runs never resume, so persisting the seed in the fork record buys
nothing and bloats the trace store.
*Revisit if:* recovery ever needs to reconstruct a sub-agent's seed
conversation (e.g. resumable sub-agent dispatches).

**D2. Execution bridge — DECIDED: scopeID → running loop registry, with two verbs.**
A `supervision.Registry` (yaah-side, in `internal/agent/`): the task runner
registers each active sub-agent run as `scopeID → {injectCh chan string, cancel context.CancelFunc}`,
deregisters on exit. Bridges:
- **Inject → steering.** `SubAgentConfig` gains a steering channel drained
  between turns (same shape as `SteerMiddleware`); the registry forwards
  guidance as a high-priority user message. This first makes the *existing*
  manual `supervisor` tool honest — its inject currently goes nowhere.
- **Halt → cancel.** The registry cancels the run's context; the task tool
  maps cancellation to its existing "halted by supervisor" result wording;
  the kernel's `scope.Halt` state change and sandbox teardown ride along.
  For the *root* scope, halt maps to the session-level cancellation path
  (ctrl-C equivalent) — deliberately: halting the session from a rule is a
  big hammer, gated behind config (D3).
*Revisit if:* sub-agent mid-turn injection granularity (per-turn, not
mid-turn) proves too coarse for stuck-loop rescue; the loop already
intercepts between tool turns, which is where loop rescue needs it.

**D3. Rules engine — DECIDED: advisory rules async via the bus, plus the blocking `DestructiveToolGuard` synchronous at sub-agent dispatch; on when tracing is on; root halt opt-in.**
Two layers, because the kernel ships two kinds and they close different gaps:
- **Advisory (async):** `NewSupervisor(mgr, bus)` at infra init;
  `AddRule(DestructiveToolRule(), HighErrorRateRule(threshold, window),
  StuckDetectionRule(repeatThreshold))` — all three emit inject-only
  interventions, drained by a goroutine into the D2 bridge. They watch the
  trace, so they see sub-agent captures the parent's middleware never does.
- **Blocking (sync):** `DestructiveToolGuard()` — default-deny for bash,
  `InterventionDeny` — evaluated via `Supervisor.CheckCall` in a pre-dispatch
  middleware on **sub-agent loops**. This closes a live coverage gap: sub-agent
  loops are hard-coded `ApprovalMode: "allow"` (`subagent_loop.go:111`), so a
  sub-agent's destructive call dispatches ungated today. A denial synthesizes
  the tool result (the approval middleware's own denied-call shape), so the
  model sees a response for every call. The parent loop keeps `approval` as
  its policy gate — the guard is the per-scope net, not a replacement.
- **Defaults:** rules are on when `shepherd_trace_dir` is set (tracing is
  already the opt-in), per-rule disables and thresholds in config (§7);
  `supervisor.halt_root` default **off** — root-scope halt maps to
  session-level cancellation and needs an explicit opt-in.
*Revisit if:* rule noise (error-rate on flaky test runs) → per-role rule
disables rather than weakening the bridge.

**D4. Merge gate + settlement — DECIDED: replace the review session's accept and fork/choose mechanics with `CommitMerge` and `Seal`/`Settle`. Local mode only — see C3's fence.**
- `accept` → `mgr.ProposeMerge` + `CommitMerge` with the review session's
  verdict as the `MergeReviewer`; a denial keeps today's behavior
  (checkpoint restore + denial reason injected) via the kernel's documented
  `*SupervisorDeniedError` path.
- `fork`/`choose(a|b)` → variants run as isolated child scopes, `Seal` each
  (`RetainedOutput` freezes `Changes`/`Diff` — the review UI reads them
  instead of re-capturing trees), `Settle` the winner (Select when the
  parent is unmoved — the common case — Apply on path-disjoint parent
  drift, surfaced as an error the session can retry), `Settle(SettleDiscard)`
  the loser. Consume-once (`ErrOutputConsumed`) replaces the hand-rolled
  "already chose" guard.
- The session's verb surface (continue/rollback/fork/choose/review_diff/
  accept/abort) is unchanged — only the mechanics underneath.
*Revisit if:* `Settle(Select)`'s parent-at-baseline requirement fights
concurrent parent turns more often than the current forkState replay does
(it should be identical in practice — same constraint, typed error).

**D5. Recovery — DECIDED: `RecoverScopes` at startup; the trace becomes the registry for scopes, checkpoints, and sealed outputs. Orphans are warned about, never auto-deleted.**
When `shepherd_trace_dir` is set, startup calls `shepherd.RecoverScopes(store,
RecoverOptions{Resolver})`. Resolver:
- `git` roots: re-adopt `NewLocalGitSandbox(repoPath)`.
- `worktree` children: re-adopt by the recorded worktree path if present on
  disk, else `(nil, nil)` — deliberate non-adoption, kernel-casual only.
- containerd scopes: `(nil, nil)` for now (the backend does not yet record a
  re-adoptable container identity); revisit when the kernel exposes one.
**Orphan handling is warn-only, settled:** an unadopted worktree may hold the
only copy of unmerged agent work, so recovery lists it (scope, path, size) and
leaves it on disk; an explicit `shepherd prune` verb (own step, Phase D)
deletes by name. Never silent deletion.
`CheckpointInvalid` (recorded revision ≠ re-adopted workspace) maps to the
review session refusing rollback with the recorded reason instead of a silent
stale restore. `--resume` reattaches recovered scopes to session records via
the existing `RecordTraceOwner` join. `mgr.PruneTerminal()` on clean session
close; a `doctor` check reports recovery counts (adopted / causal-only /
orphaned).
*Revisit if:* multi-process concurrent access to one trace dir becomes a
requirement — SQLite locking is single-writer; fence it explicitly instead.

**D6. Handles — DECIDED: assessed, not adopted.**
`StartTask`/`Run`/`TaskControl` execute tasks through the kernel with frontier
cuts; yaah's runner is provider-driven, budgeted, and mid-turn-steerable
already. Adopting handles would rebuild the dispatch path for no new
capability this plan needs. Recorded as a deliberate no with this rationale;
*revisit if* cross-process orchestration (a yaah daemon dispatching to yaah
workers) becomes a plan of its own.

**D7. Materialization — DECIDED: stays fenced, with the seam recorded.**
The isolation plan §9 already fenced `WorkspaceSubstrate`. This plan does not
unfence it: routing file writes through `materialize.Materialize` changes the
tool write path and the crash-consistency story, and deserves its own plan
(its basis: the declare→capture rhythm rows 1 already records, and the
v0.11 component packages — see §8). What this plan *does* do: nothing that
would block it — declarations keep their schemas stable so a later
materialization pass can replay them.

**D8. Kernel version — DECIDED: stay on v0.10.0 for this entire plan.**
Everything phases A–D need (`Supervisor`, `Fork`/`ForkIsolated`, `Seal`/
`Settle`, `ProposeMerge`/`CommitMerge`, `RecoverScopes`) is root-package at
v0.10.0. v0.11.0 (unreleased) breaks import paths by moving components to
`trace.`/`sandbox.`/`substrate.`/`materialize.` packages; that mechanical
sweep bundles with D7's plan, not here. If v0.11 tags before phase C lands,
re-pin mid-plan only if a needed fix rides in it.
*Superseded 2026-10-11:* v0.11.0 tagged right after A1 merged. The revisit
condition fired — not for a fix, but because the A1/A2 boundary made the
sweep cheapest exactly then (every later step writes against the new
paths). The re-pin landed as its own PR; see §8.

---

## 5. Phases

Dependency order is A → B → C → D. E is fenced/deferred. Each step is one PR
on the established cadence (branch, full gates, bot-review rounds, explicit
merge authorization).

### Phase A — The scope tree (D1) and the honest supervisor tool (D2)

The inert-tool fix rides first: it is small, user-visible, and everything
else (rules, halt semantics) hangs off the bridge.

- **A1. Root scope.** `InitShepherdInfrastructure` (or the wiring right after
  it) creates the session root scope: local mode `NewLocalGitSandbox(repo)`,
  isolated mode nil sandbox. Sub-agent dispatch switches from `mgr.Create`
  to `mgr.Fork(root, …)` / `mgr.ForkIsolated(root, …, worktree)` — the fork
  snapshot is nil (D1); seed messages stay in the loop config.
  `supervisor list_scopes` now shows a real tree. Teardown: root scope
  sandbox handling on session close.
  *Tests:* tree shape (root + N children, parent linkage), fork causality in
  the trace (the parent's branch-point record exists), supervised worktree
  dispatch still isolates, isolated mode: pure-causal root + pure-causal
  children, no container checkpoint attempts.
- **A2. Run registry + steering channel for sub-agent loops.**
  `supervision.Registry` in `internal/agent/`; `SubAgentConfig` gains the
  steering channel; the task runner registers/unregisters runs. The root
  scope is registered too — the session's steering channel and its
  cancellation path — so root `inject`/`halt` are honest from the start,
  not trace-only (the execution-bridge guarantee applies to every scope
  the supervisor tool can name, root included). Manual `supervisor inject`
  now reaches the loop; `supervisor status` reports bridge state per scope.
  *Tests:* inject lands as a steering message between turns; inject into a
  finished scope errors; registry cleanup on normal exit, on error exit, on
  ctx cancel; root injection lands on the session steering channel.
- **A3. Halt bridge.** `supervisor halt` cancels the registered run's
  context; task tool wording ("halted by supervisor"); scope state + sandbox
  teardown verified. Root-scope halt maps to session cancellation and is
  gated by `supervisor.halt_root` — the gate (default off) lands in this
  step, not B1, so an A3 stopping-point release never exposes un-gated
  session cancellation.
  *Tests:* halt mid-run cancels within a bounded window; halt of an isolated
  child destroys its worktree; root halt without the opt-in refuses; with
  it, triggers session cancel; double halt is idempotent.

**Phase A exit criteria:** `supervisor` tool interventions actually
intervene; the scope tree is causal; no behavior change when tracing is off.

### Phase B — The rules engine (D3)

- **B1. Supervisor service + config.** `NewSupervisor` + the three advisory
  rules + `Start` at infra init; drain goroutine → D2 bridge; config surface
  with defaults (§7); rule firings recorded in the trace (they are, by
  construction — assert it).
  *Tests:* each built-in rule fires on a scripted trace stream (fake store);
  thresholds from config; drain goroutine shutdown clean.
- **B2. Blocking guard at sub-agent dispatch.** `DestructiveToolGuard()` in
  `Supervisor.CheckCall`, wired as a pre-dispatch middleware on sub-agent
  loops (the seam: the loop already builds a pipeline;
  `ApprovalMode: "allow"` at `subagent_loop.go:111` is the gap it closes).
  A denial synthesizes the tool result in the approval middleware's
  denied-call shape; the parent loop's `approval` gate is untouched.
  *Tests:* a sub-agent bash `rm -rf` is denied before execution, the tree is
  byte-identical, the model sees a denied-call result; non-destructive calls
  pass; the guard is off when tracing is off.
- **B3. Intervention dispatch.** Rule → inject (guidance text includes the
  rule and the offending event); root-scope halt behind `supervisor.halt_root`
  (default off).
  *Tests:* end-to-end with a fake provider: a sub-agent that keeps failing a
  tool gets HighErrorRate guidance injected and recovers; parent loop
  unaffected by sub-agent rule noise; halt_root off refuses a root halt.
- **B4. Visibility.** `supervisor status` lists rule firings this session;
  TUI event surface deferred (bus pane is E).

**Phase B exit criteria:** a stuck sub-agent is rescued with zero model
effort; a destructive sub-agent call is blocked before execution; both are
visible in the trace; the parent session is untouched.

### Phase C — Merge gate + settlement (D4) — local mode only

- **C1. accept via CommitMerge.** Review-session reviewer = the session's
  verdict; denial → checkpoint restore + reason injected (existing UX);
  approval → merge + existing post-accept flow.
  *Tests:* accept/deny end-to-end against fake sandboxes; denial leaves the
  parent tree byte-identical; deny-then-retry with injected reason.
- **C2. fork/choose via Seal/Settle.** Variants as isolated child scopes;
  sealed outputs back the review UI (`review_diff` reads `RetainedOutput.Diff`);
  choose → Settle(Select) / Settle(Apply) / discard loser; `ErrOutputConsumed`
  mapped to the session's already-chose error.
  *Tests:* select fast-path; parent-drift Apply path; path-overlap conflict
  surfaces typed and leaves the output unconsumed; double-choose refused;
  worktree teardown on discard.
- **C3. Isolated-mode settlement parity — FENCED, moot for now:** review
  sessions are driven by `supervised_task`, which isolated mode omits, so
  there are no fork/choose flows inside the container to settle. Revisit
  only if supervised flows ever run in isolated mode (its own project).

**Phase C exit criteria:** review/fork flows run on kernel mechanics; the
hand-rolled forkState/apply path is deleted, not parallel-lived.

### Phase D — Recovery (D5; steps R1–R3, distinct from the decision IDs)

- **R1. RecoverScopes + resolvers.** Startup recovery when trace dir set;
  git/worktree re-adoption; causal-only for containerd; **orphans listed and
  left on disk (warn-only, D5)** — a missing worktree is reported with its
  scope, path, and size, never deleted.
  *Tests:* crash-simulated restart rebuilds tree/terminals/checkpoints/
  outputs; stale checkpoint → CheckpointInvalid refused with reason;
  resolver error fails recovery loudly; orphaned worktree survives recovery
  byte-identical and is listed.
- **R2. Resume integration.** `--resume` reattaches recovered scopes;
  review-session rollback across a restart uses the recovered checkpoint or
  explains why not; `PruneTerminal` on close.
  *Tests:* restart mid-review-session → continue/rollback still work; memory
  join intact; prune idempotent.
- **R3. `shepherd prune` verb (optional, own PR).** Lists and, with an
  explicit name argument, deletes an orphaned worktree — the only deletion
  path, by name, user-invoked.
  *Tests:* list matches recovery's orphan report; prune requires the exact
  name; prune of an adopted worktree refuses.

**Phase D exit criteria:** a killed yaah comes back with its supervision
state; nothing claims restorable that is not; nothing is deleted without an
explicit command naming it.

### Phase E — Fenced / deferred (D6, D7, bus pane)

- Materialization plan (D7): separate doc, prerequisite v0.11 re-pin.
- Effect-bus TUI pane: live event stream from the bus (now that B1 gives the
  bus its first consumer, a pane is cheap and optional).
- Handles: recorded no (D6).

---

## 6. Test strategy

- **Fakes first:** every phase unit-tests against `internal/sandboxfake` and
  a fake provider; the supervision bridge tests use scripted intervention
  streams. No test requires a containerd daemon.
- **Live containerd:** extend the env-gated live harness
  (`docs/isolated-workspace-live-testing.md`) only where a phase touches the
  sandbox seam (A1 isolated root, C3 if attempted).
- **Gates per PR:** build, `go vet`, `gofmt -l` empty, `staticcheck`, full
  `go test ./...`, windows + darwin cross-compile, `-race` on touched
  packages. CI matrix already runs all three OSes.
- **Crash tests (Phase D):** kill -9 style (process exit between turns),
  plus torn-write at the store level via the kernel's own law tests — do not
  re-test kernel guarantees, test yaah's resolver behavior around them.

---

## 7. Configuration surface (lands with the phase that needs it)

`shepherd_trace_dir` lives under `agents.default`
(`internal/config/load.go` — `agents.default.shepherd_trace_dir`), not under
`observability`; the supervisor settings nest beside it so one block owns
the whole feature:

```yaml
agents:
  default:
    shepherd_trace_dir: ~/.yaah/shepherd   # existing — gates everything here
    supervisor:                            # Phase B (needs tracing on)
      enabled: true                         # on when tracing is on
      halt_root: false                      # root-scope halt needs opt-in (D3)
      rules:
        destructive_tool: { enabled: true }            # advisory, inject-only
        destructive_guard: { enabled: true }            # blocking, default-deny (B2)
        high_error_rate:  { enabled: true, threshold: 0.5, window: 10 }
        stuck_detection:  { enabled: true, repeat_threshold: 3 }
```

**Defaults, precisely:** tracing **off** is byte-identical to today — the
stanza and everything it gates does not exist without
`shepherd_trace_dir`. With tracing **on** and the stanza absent, the D3
defaults apply: advisory rules on and the blocking guard on. That is a
deliberate behavior change for tracing sessions (a destructive sub-agent
call that dispatched ungated yesterday is denied after B2), which is the
point of the coverage gap — it is not behavior parity, and this plan does
not pretend otherwise. `halt_root` defaults off everywhere. `doctor` grows
a shepherd check (Phase D): store open, recovery counts, supervisor wiring.

---

## 8. Kernel dependency

- **Pinned (refreshed 2026-10-11, plan 06 phase D):** core `v0.14.0`,
  containerd nested `v0.2.1`. v0.14.0 ships the kernel's plan 06 —
  settlement over dirty baselines and the kernel-owned sandbox address —
  plus the v0.13 packaging sweep this pin absorbs in the same move:
  `Supervisor` and the rules live in the `supervisor` package, the git
  backend in `sandbox/git`, and `RecoverOptions.Resolver` gained the
  recorded `path` parameter (breaking; yaah is the sole consumer, adapted
  in the same PR). The yaah.worktree.provisioned.v1 record is retired for
  new forks — the kernel's fork record carries the address — and stays
  readable as a fallback for traces written before the bump, deleted at
  the next minor.
- **v0.11.0's component packages** (`trace.`, `sandbox.`, `substrate.`,
  `materialize.`) remain available without a second re-pin; D7's
  materialization plan inherits them directly.
- **Kernel gaps this plan works around, worth upstreaming:**
  - containerd scopes have no re-adoptable identity for `RecoverScopes`
    (D5 returns causal-only; fine, but an upstream record of container
    name/ID would close it). The kernel's plan 06 additionally records
    the containerd backend's per-capture sequence keys as a digest-
    stability violation — its own follow-up there.
  - `Scope.Halt` on a shared-sandbox child does not distinguish "halt the
    run" from "halt the scope" — D2's bridge handles it yaah-side; an
    upstream run-handle would make it first-class.
- **Closed upstream (kernel plan 06, v0.14.0):** a fork point with
  uncommitted content settles deterministically — captures are content
  identities (`tree_sha`), `Diff` and `ApplyDelta` are baseline-relative,
  and the fork record carries the sandbox address. yaah's C2 workaround
  (the dirty-fork test removal and the `chooseVariant` gap comment) is
  reverted in the plan-06 phase D PR.

---

## 9. Non-goals

- No second supervision policy engine beside the kernel's rules (Principle 2).
- No materialization/substrate adoption in this plan (D7 — its own plan).
- No handles adoption (D6).
- No cross-process / multi-daemon trace sharing (single-writer SQLite).
- No new TUI surfaces beyond the deferred bus pane.
- Local mode's byte-for-byte behavior outside the supervision seam is not
  touched.

---

## 10. Execution order

1. **A1** root scope + fork-linked sub-agent scopes (nil snapshot, D1)
2. **A2** run registry + sub-agent steering (makes `supervisor inject` real)
3. **A3** halt bridge (makes `supervisor halt` real; root halt opt-in)
4. **B1** supervisor service + advisory rules + config
5. **B2** blocking `DestructiveToolGuard` at sub-agent dispatch
6. **B3** intervention dispatch end-to-end
7. **B4** visibility polish
8. **C1** accept via CommitMerge (local mode)
9. **C2** fork/choose via Seal/Settle (local mode)
10. **R1** RecoverScopes + resolvers (warn-only orphans)
11. **R2** resume integration + PruneTerminal + doctor
12. **R3** `shepherd prune` verb (optional)

**Stopping points:** after A3 (the supervisor tool becomes honest — user-
visible win, everything else optional); after B2 (sub-agent destructive calls
are gated — the coverage gap closes); after B4 (automatic supervision
complete); after C2 (review flows on kernel mechanics). Each is shippable.

**Exit criteria (plan-level):** the audit table's rows 2, 6, 7, 8, 9, 10, 11
read **Aligned**; row 12 reads **Deliberately unused, recorded rationale**
(D6); row 13 still reads **Fenced (unchanged, D7)** — this plan does not
adopt materialization; a killed session recovers its supervision state; no
supervision verb ships without an execution-side effect; and no destructive
sub-agent call dispatches ungated.
