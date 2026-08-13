---
name: per-turn-checkpoint-restore
description: Add per-turn checkpoint/restore (and optional fork) to sub-agent loops so an ill-advised turn can be rewound without restarting the whole attempt.
status: in_progress
---

# Per-Turn Checkpoint & Restore for Sub-Agents

## 1. Goal

Today `supervised_task` restores at **attempt** granularity: one git checkpoint
before the retry loop, restored only when a retry follows. This plan adds the
ability to checkpoint **inside a sub-agent loop**, so a bad turn (hard tool
error, iteration exhaustion, quality-gate rejection) can be rewound to the
state just before that turn — without discarding the whole attempt.

Stretch goal (phase 7): use `Scope.Fork` to branch from a pre-turn snapshot and
try multiple alternatives, rather than one-shot restore.

## 2. Verified current state

- `internal/tools/supervised_task.go`
  - `Execute` creates orchestrator scope `supervised:<role>:<nanos>` and takes
    one attempt-level checkpoint before the retry loop.
  - `RestoreCheckpoint` is called only on the retry path (restore-before-retry).
  - Snapshot is always `nil`; `RestoreCheckpoint`'s returned `[]byte` is ignored
    → files are reverted but **conversation state is not**.
- `internal/agent/runner/runner.go` → `makeTaskRunner`
  - Creates a separate sub-agent scope `sub-<role>-<parentSession>-<nanos>`
    via `tools.SharedScopeManager.Create(subTraceID)`.
  - Builds `agent.NewSubAgentLoop(...)` with `SubAgentConfig`; the loop gets
    **no** scope ID or checkpointer today.
- `internal/agent/subagent_loop.go`
  - `SubAgentConfig` + `NewSubAgentLoop` → `LoopConfig{IsSubAgent:true}`.
- `internal/agent/loop.go` → `Loop.Run`
  - Outer `for {` + inner `for iter := 0; iter < MaxLoopCycles; iter++`.
  - Final answer branch returns `msg.Content` when `len(ToolCalls)==0`.
  - `executeToolPhase` runs tool calls; inner-loop exhaustion → `MaxIterationsError`.
- Library `github.com/buchenberg/shepherd-kernel-go v0.2.1`
  - `CreateCheckpoint(scopeID, repoPath, snapshot []byte) (*GitCheckpoint, error)`
  - `RestoreCheckpoint(id) ([]byte, error)` — **single-use** (consumes checkpoint).
  - `LatestCheckpoint(scopeID)`, `PruneCheckpoints(scopeID)`.
  - `Scope.Fork/Merge/Discard/Inject` for branching/guidance.

## 3. Design decisions

1. **Interface lives in `agent`, adapter lives in `runner`.**
   `agent` already imports `tools`; `tools` must not import `agent` (would cycle).
   Define a narrow `TurnCheckpointer` interface in `agent` using `[]byte`
   snapshots; implement the shepherd adapter in `runner` (which imports both).
2. **Snapshot = serialized `LoopState.Messages`**, not `nil`. This makes restore
   rewind both files *and* context.
3. **One-shot restore for v1.** Checkpoints are single-use by library design;
   that matches "rewind once, re-run the turn." Branching is deferred to phase 7.
4. **Scope separation is preserved.** Attempt-level checkpoints stay on the
   `supervised:*` scope (full redo). Turn-level checkpoints live on the
   `sub-*` scope and are invisible to `supervised_task` — it still only sees
   final success/failure.
5. **Conservative triggers only.** Automatic restore fires on:
   - hard `executeToolPhase` error,
   - `MaxIterationsError` at inner-loop exhaustion,
   - (optional) quality-gate rejection.
   Silent textual "FAILED" never triggers restore — it's a normal result.

## 4. Phased implementation

### Phase 1 — Checkpointer interface + adapter

- Add to `internal/agent` (new file `turn_checkpoint.go`):
  ```go
  type TurnCheckpointer interface {
      Checkpoint(ctx context.Context, snapshot []byte) (id string, err error)
      Restore(ctx context.Context, id string) ([]byte, error)
  }
  ```
- Add adapter in `internal/agent/runner/checkpoint.go`:
  - Holds `*shepherd.ScopeManager`, `scopeID`, `repoPath`.
  - `Checkpoint` → `mgr.CreateCheckpoint(scopeID, repoPath, snapshot)`.
  - `Restore` → `mgr.RestoreCheckpoint(id)`, return snapshot bytes.
- Unit test adapter against the in-memory/test git repo used by
  `internal/tools/supervised_task_test.go`.

### Phase 2 — Plumb through SubAgentConfig

- Add fields to `SubAgentConfig`:
  ```go
  TurnCheckpointer     agent.TurnCheckpointer
  TurnCheckpointEnabled bool
  TurnCheckpointMax     int   // cap concurrent turn checkpoints; 0 = unlimited
  ```
- Add same fields to `LoopConfig` (in `internal/agent/types.go`).
- `NewSubAgentLoop` copies them into `LoopConfig`.
- `makeTaskRunner` constructs the adapter from `tools.SharedScopeManager`,
  the `subTraceID` scope, and the resolved `repoPath`, only when
  `TurnCheckpointEnabled` and the scope manager is non-nil.

### Phase 3 — Checkpoint inside Loop.Run

- In `Loop.Run`, before each model turn (or before each tool batch — see phase
  9 policy), when `TurnCheckpointer != nil`:
  1. Marshal `l.State.Messages` to `[]byte` (JSON, stable field order).
  2. `id, err := checkpointer.Checkpoint(ctx, snap)`.
  3. Append `id` to `l.State.TurnCheckpoints` (new field).
- Enforce `TurnCheckpointMax`: after appending, prune oldest still-unused IDs
  via `PruneCheckpoints` to bound git/stash accumulation.

### Phase 4 — Restore on failure

- Add `restoreOnTurnFailure(ctx, id)` helper:
  1. `snap, err := checkpointer.Restore(ctx, id)`.
  2. Unmarshal `snap` into `l.State.Messages`.
  3. Remove restored + newer checkpoint IDs from `l.State.TurnCheckpoints`.
  4. Increment a `RestoreCount` and record `RestoredFrom` (for diagnostics).
- Trigger points in `Loop.Run`:
  - `executeToolPhase` returns error → restore last checkpoint, then retry the
    turn once with a guidance suffix ("previous attempt failed: <err>").
  - Inner-loop exhaustion (`MaxIterationsError`) → restore last checkpoint and
    continue (respecting an outer cap to avoid infinite rewind loops).
- Add a `MaxTurnRestores` guard (default e.g. 3) so a deterministically broken
  turn cannot loop forever.

### Phase 5 — Diagnostics + envelope

- Extend `structuredSupervisedResult` output with optional fields:
  `restores`, `restored_from`, `checkpoint_scope`.
- These come from `LoopState` via `Run`'s return path; keep the uniform JSON
  shape (`status`, `attempts`, `result`/`error`/`partial`) intact.

### Phase 6 — Tests

- Unit (`internal/agent`): fake `TurnCheckpointer` asserting:
  - called once per turn when enabled;
  - not called when disabled or nil;
  - on injected `executeToolPhase` error, restore is called, messages rehydrated,
    turn retried, and `MaxTurnRestores` is honored.
- Snapshot round-trip: marshal/unmarshal a realistic `[]types.Message`, assert
  fidelity (IDs, roles, tool calls, content).
- Integration (`internal/tools`): `supervised_task` with a sub-agent that writes
  a file then fails a later turn; assert:
  - file reverted to pre-turn state,
  - attempt completed (or failed) with `restores >= 1`,
  - attempt-level rollback behavior unchanged for a full failure.
- Single-use: restoring the same checkpoint twice returns an error/empty and
  does not corrupt state.

### Phase 7 — Fork-based branching (stretch)

- Add `TurnCheckpointer.Fork(snapshot []byte) (scopeID string, err error)`.
- On a rejected turn, `Fork` from the pre-turn snapshot instead of one-shot
  restore; run the alternative on the child scope; `Merge` on success,
  `Discard` on failure.
- Expose via `supervisor` tool (`list_scopes` already exists) so an orchestrator
  can drive counterfactual retries.

### Phase 8 — Docs + tool description

- Update `internal/prompts/tools/supervised_task.md` and
  `docs/supervised-task-plan.md` with:
  - per-attempt vs per-turn checkpoint table,
  - restore policy and triggers,
  - single-use semantics vs `Fork`,
  - new envelope fields.

### Phase 9 — Checkpoint-frequency policy & cost

- Benchmark `CreateCheckpoint` (git stash) cost on the target repos.
- Decide default: checkpoint **before each turn** vs **before each tool batch**.
  Start with "before each turn," gated behind `TurnCheckpointEnabled=false` by
  default until benchmarks are green.
- Add config knobs (`turn_checkpoint`, `turn_checkpoint_max`,
  `max_turn_restores`) to `config.yaml` and `SubAgentParams` so callers opt in.

#### Phase 9 results (2026-08-12)

Benchmarked in `internal/agent/runner/checkpoint_bench_test.go`
(Intel Core Ultra 7 265H, Windows, Go 1.25.8, `-benchtime=10x`):

| Operation | 50 files | 500 files |
|---|---|---|
| Checkpoint, clean tree | ~241 ms/op | ~320 ms/op |
| Checkpoint, dirty tree | ~304 ms/op | ~385 ms/op |
| Restore cycle | ~447 ms/op | ~1542 ms/op |

Cost is dominated by spawning three git subprocesses per checkpoint
(Windows process creation is expensive), not by git's work — the clean
50-file case is almost all startup. Restore is rarer (failed turns only)
but 2–4× the checkpoint cost and grows sharply with repo size.

**Decision: keep turn checkpointing OFF by default.** Frequency stays
"before each turn" (the simpler policy); a 240–385 ms checkpoint per turn
is a meaningful fraction of a fast turn and the restore path can exceed a
second on medium repos. It is deliberately NOT exposed as a per-call tool
parameter, so the model cannot over-enable an expensive feature.
Revisit the default only if a lower-overhead backend lands (in-process
libgit2, or collapsing the three subprocesses into one `git stash`
invocation) or on Linux, where git spawns are several times cheaper.

#### Config model update (2026-08-12): per-role shepherding

The global `turn_checkpoint` boolean was replaced by **per-role**
shepherding in `agents.subagent.roles.<name>`:

- `supervised` (default false) — routes the role exclusively through
  `supervised_task` (attempt checkpointing always on) when true; plain
  `spawn_subagent` (no checkpoints) when false. Routing is exclusive.
- `turn_checkpoints` (default false) — enables the loop-level per-turn
  checkpoint/restore for that role.

The global numeric knobs (`supervised_max_retries`, `turn_checkpoint_max`,
`max_turn_restores`) and `supervised_repo_path` remain global. A per-call
`SubAgentParams` override is still intentionally deferred.

## 5. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Git checkpoint per turn is expensive | Default off; `TurnCheckpointMax` cap; benchmark before enabling |
| `agent`↔`tools` import cycle | Interface in `agent` (`[]byte` snapshots), adapter in `runner` |
| Infinite rewind loop on deterministic failure | `MaxTurnRestores` guard + guidance suffix |
| Single-use checkpoints surprise callers | Document; `Fork` for branching (phase 7) |
| Snapshot drift (messages vs files out of sync) | Restore does both in one `RestoreCheckpoint` call; unit test fidelity |
| Attempt vs turn scope confusion | Keep scopes separate; name them clearly in diagnostics |

## 6. Rollout

1. ✅ Phases 1–5 behind `TurnCheckpointEnabled` (default false).
2. ✅ Land unit + integration tests (phase 6).
3. ✅ Benchmark (phase 9). Decision: keep off by default (see phase 9
   results); overhead too high for default-on on Windows.
4. ✅ Docs + tool description (phase 8).
5. ⬜ Fork branching (phase 7) as a separate follow-up.

