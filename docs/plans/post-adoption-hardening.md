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

**F2 — features.md covers the supervision surface only in passing
(docs).** `docs/features.md` mentions pieces of it — the tools table
row for `supervised_task`/`supervisor` names the review verbs and the
merge gate (features.md:156), and the isolation section notes the
doctor workspace check (features.md:234) — but there is no
user-facing section for the supervision *lifecycle*: startup recovery
and its adopted / orphaned / missing / unadopted report, what a user
should do with each bucket, the doctor check's recovery pass, or the
`yaah shepherd` list and prune commands. A user who hits any of those —
a warning at startup, an orphaned worktree on disk — has no operational
explanation. All exist and are tested; H1 expands the existing entry
rather than pretending the surface is entirely undiscoverable.

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
`RecoverScopes` scans every record of five lifecycle schemas across
four scan sites (shepherd-kernel-go `recovery.go:263,295,346,428` — the
second site covers merged, discarded, and halt in one call). The scans
are SQL-filtered per schema (`ScanRecordsBySchema`, `WHERE schema_ref IN
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
(design).** `createSubAgentScope` (runner.go:776-817) has three
`mgr.Create` fallbacks: tracing-off (runner.go:779 — the deliberate
degraded mode), the `ForkIsolated` failure on the workdir path
(runner.go:800-801 — not halt-specific; worktree provisioning can fail
on its own), and the plain `Fork` refusal after a supervisor halt makes
the root terminal (runner.go:812-815). The last two are policy
questions hiding in fallbacks: A3 calls halting the root "a
session-level kill for supervision", yet a dispatch that races the kill
— or a worktree that fails to provision — silently lands as a flat
root-level scope: superviseable, but outside the session tree, and
recovery will rebuild it as a sibling of the session root rather than a
child of anything.

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

Expand features.md's existing coverage into a supervision section:
what the tools-table row (features.md:156) already names, plus the
operational half — startup recovery and its report buckets with what a
user should do with each, the doctor check's recovery pass, and the
`yaah shepherd` list and prune commands — written for the user who just
saw the startup warning or found a stray worktree, not for the kernel's
maintainer.

### H2 — close the shepherd infrastructure on session end (F3)

A `CloseShepherdInfrastructure(store, bus)` teardown, called from the
session teardown path (`cmd/yaah/session.go`'s close), with the shape the
clean-exit path already uses — **warn-only**: `close()` returns no error
and the workspace sandbox destroy there only warns
(session.go:235-240), so the shepherd close reports its failure the same
way rather than inventing an error-propagation contract the callers do
not have. Ordering: close the **store first** (no more publishes), then
the **bus** — the kernel's `EffectBus.Close` already exists
(trace/bus.go:106) and is what the supervision tests already use
(`t.Cleanup(bus.Close)`); no upstream follow-up is needed. Idempotence
against the recovery-failure close (wiring.go:439 already closed the
store; closing twice must not fabricate a failure).

The writer-join question the drain leaves open: the background-jobs
`CloseAndWait` times out and *continues* (session.go:206-208) — jobs
that survive the drain share this store and can still write. Closing
the store under a live writer turns its final writes into
corruption-shaped errors, which is worse than an unclosed store, so the
policy is: **if the drain timed out, the infrastructure stays open** —
the process exit reaps it — and the teardown warns that it did not
close. A timeout-survivor test pins the branch.

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

`shepherd.RecoverScopes` returns only a manager and an error — no scan
counts — and yaah's `Report` counts scopes and worktree outcomes, not
records scanned, so the O(history) metric F4 identifies has no existing
source. The kernel therefore gains a small recovery-stats surface in
the same v0.15.0 train as H3: `RecoverScopes` (or `RecoverOptions`)
reports records scanned per schema and total scan duration —
instrumentation at the point that does the work, not a duplicated
count query in yaah. Startup logs it next to the existing recovery
report. The growth bound is documented where the report is produced:
lifecycle events only, never per-tool-call facts. The compaction
design — a `shepherd`-verb that deletes the records of fully-terminal,
fully-pruned subtrees past a retention window, with the trace store
gaining its only deliberate deletion surface — is recorded in the
kernel's backlog with its trigger: when startup recovery measurably
exceeds a second on a real trace. Not before; no speculative deletion
surface for a cost nobody has observed.

### H6 — decide the post-halt dispatch policy (F6)

The plan's position: a dispatch that arrives after the root was halted
should fail loudly, not silently degrade to a flat scope. A3's halt is
a session-level kill; a racing dispatch that lands as a rootless
root-level scope is superviseable-but-orphaned — the shape recovery
then rebuilds as a sibling of a dead session. The policy applies to
**both** tree-escaping fallbacks, not just the plain fork: the
`Fork`-refused path (runner.go:812-815) and the workdir
`ForkIsolated`-failure path (runner.go:800-801) both fall to flat
`mgr.Create` today, and a halted root can fail either. The fix: both
distinguish "root terminal" (return `ErrRootTerminal` — it exists for
exactly this, runner.go:719) from other fork failures, and the dispatch
path surfaces the terminal error to the sub-agent's parent as an
actionable "the session was halted; this dispatch cannot run" instead
of a silent scope. Non-terminal `ForkIsolated` failures (worktree
provisioning) keep their flat fallback but log the degradation — the
sub-agent still runs, superviseable, with its escape from the tree
visible. "Tracing off" (runner.go:779) is untouched: the deliberate
degraded mode. If a review disagrees and wants mid-kill dispatches to
survive, the fallbacks stay but must log; the decision is the phase's
review question, not its implementation.

---

## 3. Phases

### Phase H1 — documentation (yaah, docs-only)
- Adoption plan §1: frozen-snapshot note; no in-place verdict rewrites.
- features.md: the supervision section.
- Own PR; no code.

### Phase H2 — happy-exit teardown (yaah)
- `CloseShepherdInfrastructure`: store first, then `EffectBus.Close`
  (trace/bus.go:106); warn-only failures, matching the clean-exit
  path's shape; idempotent against the recovery-failure close.
- Wired into the session teardown, after the background-jobs drain —
  and only when the drain succeeded: a timed-out drain leaves the
  infrastructure open with a warning, never closed under live writers.
- Tests: a session that ends cleanly closes the store (a post-close
  write fails); a double close does not fabricate an error; a
  drain-timeout survivor leaves it open.
- Own PR.

### Phase H3 — typed partial settlement (kernel v0.15.0, then yaah)
- Kernel: `ErrCausalMergeUnrecorded` in both settle cases; law test.
- Kernel minor release (v0.15.0) per the release procedure.
- yaah: re-pin; `settlementLanded` deleted, replaced by `errors.Is`.
- Kernel PR, release PR, yaah PR — the same train plan 06 used.

### Phase H5 — recovery observability (kernel, then yaah)
- Kernel: `RecoverScopes` reports records scanned per schema and scan
  duration (rides the v0.15.0 train with H3 — the stats surface and
  the typed error are one kernel release).
- yaah: startup logs them beside the existing recovery report.
- yaah's consumption lands with the H3 re-pin PR.

### Phase H6 — post-halt dispatch policy (yaah)
- `ErrRootTerminal` distinguishes a halted root (both tree-escaping
  fallbacks: the `Fork` refusal and the workdir `ForkIsolated`
  failure) from tracing-off and from non-terminal provisioning
  failures.
- The terminal error surfaces as actionable; non-terminal fallbacks
  log their degradation; the tracing-off fallback is untouched.
- Tests: a dispatch after halt fails with the typed error on both
  paths; a worktree-provisioning failure still degrades with a log; a
  tracing-off dispatch still gets its flat scope.
- Own PR.

### Phase H4 — seam retirement (yaah, last)
- Manager and store injected; package globals deleted.
- Tests own their instances.
- Own PR; deliberately last.

---

## 4. Risks

- **H2's close-on-exit could regress crash-adjacent paths**: a close
  that runs while a background job still writes would surface as write
  failures that look like corruption. The drain's timeout does not
  guarantee the writer is gone (session.go:206-208 continues after
  `CloseAndWait`), so the teardown closes only when the drain
  succeeded; a timed-out drain leaves the infrastructure open and warns.
  A timeout-survivor test pins the branch, and the suite covers the
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
- A clean session exit closes the store and the bus (warn-only, like
  the rest of the clean-exit path); a double close is a no-op; a
  drain-timeout leaves it open with a warning rather than closing
  under a live writer.
- A settle whose causal merge fails to record is detected by the
  kernel's typed error, and yaah's heuristic is gone.
- Startup shows the recovery scan's cost from the kernel's own stats
  surface; the compaction trigger is recorded, not built.
- A dispatch after a supervisor halt fails with `ErrRootTerminal` and
  an actionable message on both tree-escaping paths; worktree
  provisioning failures degrade loudly (logged); tracing-off
  dispatches are unchanged.
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
