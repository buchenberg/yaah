---
name: settlement-honesty-pr
description: PR: typed Outcome for settle/lifecycle verbs — release honesty (yaah-0e4, yaah-wpf, yaah-lka3, yaah-37s)
status: approved
---

# PR: typed Outcome for settle/lifecycle verbs — release honesty

Covers beads **yaah-lka3** (core), **yaah-0e4** (P1/CRITICAL), **yaah-wpf**, **yaah-37s**.
One mechanism + its instances: "tools report state, not completion."

## Branch / dependencies

- New branch `fix/settlement-outcome`. Base it on whatever carries yaah-ebqk's
  honesty kit (`internal/tools/result.go` multiResult + `conformance_test.go`,
  currently on `feat/acp-compaction-updates` @ 863b24c — not yet on local main).
  If that branch merges first, rebase onto main. If not, stack on it and say so
  in the PR description.
- Disjoint from open PR #587 (`cli-audit-tidy`, `cmd/yaah/` surface) — no conflicts.

## Design decisions (record in PR description)

1. **Outcome shape** (yaah-lka3): one struct in new `internal/tools/outcome.go`:
   `type Outcome struct { Work, Scope, Record, Notes string }` with a single
   multi-line renderer used by every verb. `Work` = what happened to the files,
   `Scope` = which tree/paths changed, `Record` = what was persisted (ids, paths),
   `Notes` = caveats. No parallel custom-render shapes. Failures never truncate
   (reuse result.go's never-truncate-failures rule).
2. **Release semantics** (yaah-0e4): option (a) — `release` re-exports the sealed
   delta to a retention path **before** teardown destroys the worktree, reports
   that path in `Record`, and `Notes` states plainly that the worktree was
   destroyed. If export fails, the release reports failure (multiResult partial)
   instead of "settled". The delta must never be irretrievable while the tool
   claims a graceful release.

## Steps (red-first)

1. **Claim beads**: `bd update yaah-lka3 yaah-0e4 yaah-wpf yaah-37s --claim`
   (via bd; move to in_progress).
2. **Red tests first** — extend `internal/tools/conformance_test.go` (yaah-ebqk
   kit) with settlement scenarios that FAIL against current one-word statuses:
   - `release` reports where the delta went; teardown failure is visible.
   - teardown failure puts the stranded worktree path in the output.
   - `subagent_jobs wait` timeout and `status` of a running job report
     `timed_out` + `still_running` explicitly.
3. **Core yaah-lka3**: add `Outcome` + renderer; convert settle verbs
   (`select`/`apply`/`release`/`discard` in `internal/tools/retained.go`
   `settleRetainedProposal` :285-353) and lifecycle status paths to it.
4. **yaah-0e4**: in `settleRetainedProposal` + `teardownReleasedProposal`
   (`retained.go`:355-373): export delta to a retention path before teardown,
   report it in `Record`; on teardown error report failure, not "settled".
   Test pins the reported outcome (0e4 AC).
5. **yaah-wpf**: teardown error surfaces in `Notes`; `Work` includes
   `Retained worktree: path` when teardown fails (worktree stays). Test with an
   injected/failing teardown.
6. **yaah-37s**: `internal/tools/subagent_jobs.go` — `executeWait` (:128-146)
   keeps explicit `timed_out` but adds `still_running: true` instead of
   implying completion; `executeStatus` (:104-111) gains `still_running`;
   `executeCancel` (:113-126) stops fabricating `cancelled` while the job may
   still be running (report `still_running`, borrow Outcome's Notes). Status
   vocabulary aligned with `internal/jobs/manager.go`:19-22. Tests cover
   wait-timeout, status-while-running, cancel-while-running.
7. **Docs from truth** (verification, not generation — match the schemas):
   - `internal/prompts/tools/delegate.md`:5 — settle list omits `release`;
     fix the list AND its semantics (release = export + destroy, not "keep the
     landed work").
   - `internal/prompts/tools/subagent_jobs.md` — document `timed_out` /
     `still_running` and what `cancel` does and does not guarantee.
   - Grep `internal/prompts/` + `docs/` for the old release/status wording and
     fix every teaching surface (the memory lesson: impl + embedded .md +
     any directive/role prompt that teaches the verb).
8. **Gates**: `gofmt -w .`, `go vet ./...`, `staticcheck ./...`, `go test ./...`;
   conformance kit green. Cross-check the 4 acceptance criteria verbatim.
9. **Handoff** (conservative profile): report changed files + validation +
   proposed commit/push/PR commands; commit on `fix/settlement-outcome` and
   open the PR when the user says go. PR title:
   `fix(tools): typed Outcome for settle verbs — release honesty (yaah-lka3, yaah-0e4, yaah-wpf, yaah-37s)`.

## Acceptance criteria (from the beads)

- lka3: one shared `Outcome{Work,Scope,Record,Notes}` shape across settle and
  lifecycle verbs; release cannot report "settled" while discarding the delta;
  a teardown failure always reaches Notes. Tests pin reported outcome, not just
  exit status.
- 0e4: release never leaves the delta irretrievable while claiming graceful
  release. Test pins the reported outcome.
- wpf: teardown failure surfaced; worktree retention stated. Test covers a
  teardown failure.
- 37s: explicit `timed_out`, `still_running` field in wait/status; cancel
  honesty. Tests cover timeout and cancellation reporting.

## Notes

- yaah-ebqk already proved the pattern (result.go single owner, multiResult
  partial = error); Outcome plugs into the same conventions — say so in the PR.
- If time is short, 0e4+wpf can ship alone as a hotfix first (same two
  functions in retained.go); lka3+37s follow. Default is the full cluster.
