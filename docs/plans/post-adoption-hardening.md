# Plan 07 — Post-adoption hardening (architectural findings F1–F7)

Status: **proposed**. The shepherd-kernel adoption and the kernel's plan 06
are complete; this plan hardens what the end-to-end architectural review
(2026-10-11) found around the adopted surface. Each finding was re-verified
against the current trees before this plan was written; where plan 06
already closed a finding, it is recorded as closed, not re-planned.

---

## 1. Findings, re-verified

**F1 — the adoption plan's audit table has drifted from reality (docs).**
`docs/plans/shepherd-kernel-adoption.md` §1's table still reads as the
present tense — "flat `mgr.Create` per sub-agent", "nothing merges without
review... Reimplemented by hand", "not used; a restart reopens the store
and reconstructs nothing" — but every one of those verdicts describes the
pre-adoption snapshot. Rows 6, 7, 9, 10, 11 are the plan's own exit
criteria and are done; rows 2 and 8 shipped with the rules-engine phases.
The table is history presented as an audit. The kernel solved the same
problem by archiving its completed plans "with their ticks frozen" — yaah
should do the same here rather than rewriting history in place.

**F2 — features.md does not mention the supervision surface (docs).**
`docs/features.md` documents yaah's user-facing capabilities but says
nothing about: fork-and-choose review sessions, supervised sub-agent
tasks with checkpoint/rollback/retry, startup recovery (the adopted /
orphaned / missing / unadopted report), the doctor check, or the
`yaah shepherd` list and prune commands. A user who hits any of them —
a warning at startup, an orphaned worktree on disk — has no documented
explanation. All six exist and are tested; none are discoverable.

**F3 — the trace store and effect bus are never closed on a happy exit
(lifecycle).** `cmd/yaah/wiring.go` creates them once
(`InitShepherdInfrastructure`, wiring.go:415) and closes the store on
exactly one path: recovery failure (wiring.go:439). A session that ends
cleanly leaves the store open, the bus's subscriber goroutines running,
and the WAL to whatever the process's exit does to them. Every other
session-scoped resource got a bounded teardown treatment — the workspace
sandbox destroy joins its error and is guarded by a timeout
(wiring.go:779-786); the shepherd infrastructure deserves the same.

**F4 — the recovery scan at startup is unbounded (scale).**
`RecoverScopes` scans every record of five lifecycle schemas
(shepherd-kernel-go `recovery.go:263,295,346,428`). The scans are
SQL-filtered per schema (`ScanRecordsBySchema`, `WHERE schema_ref IN
(...)`), so the cost grows with supervision lifecycle events — forks,
settles, checkpoints — not with per-tool-call facts, and in practice it
stays small for a long time. But it is O(history) with no bound and no
visibility: nothing measures it, so nobody knows when it starts to
matter. The honest first step is measurement, not a compaction design
nobody can yet justify; the compaction path is recorded below with its
trigger.

**F5 — `tools.SharedScopeManager` / `tools.SharedTraceStore` are
process-global mutable state (seam).** Wiring assigns the package
globals (wiring.go:447-449); every tool reaches through them
(`supervised_session.go:290,374,419,452,507`...). The supervision bridge
already did this right — "session-owned and injected into the task
runner, the supervisor tool, and the session teardown — never a process
global" (wiring.go:452-454). The manager and store are the last
session-scoped resources still living in package scope, which is why
tests must reset globals between runs and a second session in one
process would stomp the first.

**F6 — supervised_task's flat-scope fallbacks are undocumented policy
(design).** `createSubAgentScope` (runner.go:776-817) has two
`mgr.Create` fallbacks: tracing-off (no root — deliberate degraded
mode) and the fork-refused path after a supervisor halt makes the root
terminal (runner.go:810-815). The second one is a real policy question
hiding in a fallback: A3 calls halting the root "a session-level kill
for supervision", yet a dispatch that races the kill silently lands as
a flat root-level scope — superviseable, but outside the session tree,
and recovery will rebuild it as a sibling of the session root rather
than a child of anything.

**F7 — `settlementLanded` guesses what the kernel knows (upstream).**
When a settle's physical effect lands but the causal-merge record
fails, the kernel returns an error whose text says exactly that —
"the parent's workspace now holds this output's changes, but recording
the causal merge failed" (settlement.go, select and apply cases) — but
the error is a plain `fmt.Errorf`. yaah therefore guesses
(`settlementLanded`, supervised_session.go:1017-1055): the Select half
is exact since plan 06 (digest equality on content identity), but the
Apply half is a path-subset heuristic that a same-path parent edit
could fool. The kernel should say what happened with a typed error;
yaah should stop guessing. The finding's second half — the unpinned
dirty-fork boundary — was closed by plan 06 phase D (kernel v0.14.0
pinned, the dirty-fork regression test restored) and is not re-planned.

---

## 2. Design

### H1 — freeze the audit table; document the supervision surface (F1, F2)

Mark the adoption plan's §1 table as the frozen pre-adoption snapshot —
the same convention the kernel used for its archived plans — with a
one-line completion note pointing at the phases that closed each row.
Do not rewrite the verdicts in place: the table is the record of why
the plan existed.

Add the missing documentation to features.md: one supervision section
covering fork-and-choose review sessions, supervised tasks, startup
recovery and its report buckets, the doctor check, and the
`yaah shepherd` command pair — written for the user who just saw the
startup warning or found a stray worktree, not for the kernel's
maintainer.

### H2 — close the shepherd infrastructure on session end (F3)

A `CloseShepherdInfrastructure(store, bus)` teardown, called from the
session teardown path where the workspace sandbox destroy already
lives, with the same shape: a bounded timeout (cancel-free context —
teardown must not be abandoned because the caller's context expired),
errors joined into the teardown result, and idempotence against the
recovery-failure close (the store is already closed there; closing
twice must not fabricate a failure). The bus has no Close today — the
kernel's `EffectBus` needs a shutdown or the store's Close must drain
it; whichever the kernel already supports wins, and if neither exists,
the bus is a recorded kernel follow-up and the plan closes only the
store (the store is the resource with durability semantics; the bus is
goroutines the process exit reaps).

### H3 — typed partial settlement, kernel then consumer (F7)

The kernel's settle failures that leave the physical effect landed wrap
a new sentinel — `ErrCausalMergeUnrecorded` — in both the select and
apply cases, keeping the current message text (it already says the
right thing). A law test pins the typed surface: a settle whose causal
merge fails to record reports the sentinel, and the output stays
unconsumed (already the contract).

yaah re-pins the kernel minor, replaces `settlementLanded` with
`errors.Is(err, shepherd.ErrCausalMergeUnrecorded)`, and deletes the
heuristic. The select-side digest check and the apply-side path-subset
check both go — the kernel's own error is the authority for what
happened, which is the entire point of a typed error. One coordinated
minor (v0.15.0), same pattern as plan 06.

### H4 — retire the shared-global seam (F5)

Thread the manager and store through the tool structs the way the
supervision registry already is: wiring owns them, injects them into the
tools that need them, and clears them on teardown. The package globals
die. Tests construct their own instances instead of resetting globals.
This is mechanical but broad — every `tools.Shared*` reference — and it
lands after H2 (the teardown it feeds exists first) and after the
behavior phases (it must not rebalance diffs that change behavior).

### H5 — measure the recovery scan; record the compaction trigger (F4)

Startup logs the recovery's scan duration and recovered-record counts
(one line, next to the existing recovery report). The growth bound is
documented where the report is produced: lifecycle events only, never
per-tool-call facts. The compaction design — a `shepherd`-verb that
deletes the records of fully-terminal, fully-pruned subtrees past a
retention window, with the trace store gaining its only deliberate
deletion surface — is recorded in the kernel's backlog with its
trigger: when startup recovery measurably exceeds a second on a real
trace. Not before; no speculative deletion surface for a cost nobody
has observed.

### H6 — decide the post-halt dispatch policy (F6)

The plan's position: a dispatch that arrives after the root was halted
should fail loudly, not silently degrade to a flat scope. A3's halt is
a session-level kill; a racing dispatch that lands as a rootless
root-level scope is superviseable-but-orphaned — the shape recovery
then rebuilds as a sibling of a dead session. The fix: the fork-refused
fallback distinguishes "root terminal" (return
`ErrRootTerminal` — it exists for exactly this, runner.go:714) from
"tracing off" (keep the flat Create — the deliberate degraded mode),
and the dispatch path surfaces the error to the sub-agent's parent as
an actionable "the session was halted; this dispatch cannot run"
instead of a silent scope. If a review disagrees and wants mid-kill
dispatches to survive, the fallback stays but must log its degradation;
the decision is the phase's review question, not its implementation.

---

## 3. Phases

### Phase H1 — documentation (yaah, docs-only)
- Adoption plan §1: frozen-snapshot note; no in-place verdict rewrites.
- features.md: the supervision section.
- Own PR; no code.

### Phase H2 — happy-exit teardown (yaah)
- `CloseShepherdInfrastructure` with bounded, cancel-free close;
  idempotent against the recovery-failure close.
- Wired into the session teardown; failure joins the teardown result.
- Test: a session that ends cleanly closes the store (a post-close
  write fails); a double close does not fabricate an error.
- Own PR.

### Phase H3 — typed partial settlement (kernel v0.15.0, then yaah)
- Kernel: `ErrCausalMergeUnrecorded` in both settle cases; law test.
- Kernel minor release (v0.15.0) per the release procedure.
- yaah: re-pin; `settlementLanded` deleted, replaced by `errors.Is`.
- Kernel PR, release PR, yaah PR — the same train plan 06 used.

### Phase H5 — recovery observability (yaah)
- Startup logs scan duration and record counts beside the report.
- Own PR (may ride with H2 if the review prefers one lifecycle PR).

### Phase H6 — post-halt dispatch policy (yaah)
- `ErrRootTerminal` distinguishes halted-root from tracing-off.
- The dispatch error surfaces as actionable; the flat fallback remains
  for tracing-off only.
- Test: a dispatch after halt fails with the typed error; a
  tracing-off dispatch still gets its flat scope.
- Own PR.

### Phase H4 — seam retirement (yaah, last)
- Manager and store injected; package globals deleted.
- Tests own their instances.
- Own PR; deliberately last.

---

## 4. Risks

- **H2's close-on-exit could regress crash-adjacent paths**: a close
  that runs while a sub-agent still writes would surface as write
  failures that look like corruption. Mitigation: the teardown runs
  only after the session's loops are joined — the same ordering the
  sandbox teardown already depends on — and the test suite covers the
  resumed-session path.
- **H3's typed error is a contract the kernel must get right**: if the
  sentinel wraps a merge failure whose physical effect did NOT land,
  yaah would treat a half-settled fork as settled. Mitigation: the law
  test covers both directions — sentinel present only when the
  physical effect landed.
- **H4 is broad and mechanical**: rebalancing a large diff against
  behavior phases invites conflicts. Mitigation: it lands last, alone.

## 5. Acceptance criteria

- The adoption plan's table reads as history, and features.md explains
  every user-visible supervision surface.
- A clean session exit closes the store; nothing writes after; a double
  close is a no-op, not an error.
- A settle whose causal merge fails to record is detected by the
  kernel's typed error, and yaah's heuristic is gone.
- Startup shows the recovery scan's cost; the compaction trigger is
  recorded, not built.
- A dispatch after a supervisor halt fails with `ErrRootTerminal` and
  an actionable message; tracing-off dispatches are unchanged.
- No `tools.Shared*` package globals remain.

## 6. Non-goals

- No trace-store deletion surface (compaction) until the measured
  trigger says so.
- No change to the recovery contract's "recover everything" semantics —
  bounding recovery would orphan old sessions' worktrees, the exact
  thing the design refuses to do.
- No containerd work; its capture-identity follow-up stays where
  plan 06 recorded it.
- No materialization/substrate work (still fenced, D7).
