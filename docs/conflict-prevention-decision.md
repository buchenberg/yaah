# Decision: preventive dispatch path claims

**Status:** Declined for now  
**Bead:** `yaah-6xyl.4`  
**Date:** 2026-11-01

## Question

Should yaah prevent parallel sub-agent write conflicts before they happen by
having dispatches declare write paths, scheduling overlapping claims, or
assigning path ownership to roles?

## Evidence available today

The conflict system now records evidence rather than guessing:

- `yaah.proposal.conflict.v1` facts for divergent proposal paths;
- evidence-backed execution tracking for shared writes, retained speculative
  overlaps, and unknown shell mutations;
- bounded base/current/proposed views on divergent proposal paths;
- explicit path-disjoint settlement by default and an opt-in worktree-only
  three-way apply.

There is **no production aggregate** of conflict rates yet. The facts exist,
but they are per-session trace records and are not rolled up into a metric such
as "conflicts per speculation fan-out" or "repeated path pairs." The facts are
also emitted only when `proposal list`/`show` observes them, not at seal or
settlement, and there is no dispatch/fan-out denominator. Local tests and the
development sessions that produced this feature are not a representative
sample.

That absence is itself the decisive evidence: adopting a scheduler or requiring
up-front claims now would be based on intuition, not observed conflict rates.
Measuring the criteria is a precondition, not a detail; it is tracked as
`yaah-6xyl.5` (aggregate conflict facts).

## Options

| Option | Decision | Reason |
|---|---|---|
| Optional `delegate` write-path hints | **Not now** | The model often does not know its write set before reading the code. A hint that is wrong is worse than no hint because it creates false confidence. |
| Scheduler that delays or rejects overlapping claims | **No** | The scheduler cannot see shell/script/generated writes, can serialize unrelated work, and adds latency/deadlock risk. The current path-disjoint settlement already fails closed. |
| Role-level path ownership | **No** | Roles are capability profiles, not repository ownership boundaries. Ownership would be stale as soon as a task crosses modules. |
| Visibility + explicit resolution | **Adopt** | This is already implemented and matches the observed failure mode: discover overlap before settlement, then choose/discard/release or explicitly three-way merge. |
| Hard prevention | **Decline for now** | No measured case yet justifies the false-confidence and scheduling costs. |

## Decision

Keep **visibility and resolution** as the control:

1. `delegate` does not require or accept path claims.
2. There is no path-claim scheduler and no role-level path ownership.
3. Parallel dispatches remain isolated; path-disjoint `apply` remains the
   default; `three_way` remains an explicit worktree-only opt-in.
4. Conflict facts stay advisory evidence. They do not block dispatch or
   settlement by themselves.

## Revisit criteria

Revisit this decision only after conflict facts have been aggregated across
real sessions. The trigger is at least one of:

- a meaningful share of parallel dispatches/fan-outs produces a
  `same_path_divergent`, `delete_modify`, `parent_overlap`, or `path_overlap`
  conflict;
- the same path pair repeats across sessions (proposal ids are per-process, so
  repetition is measured by path, not proposal id);
- discarded or released work is repeatedly attributed to avoidable overlap;
- users routinely ask for a way to declare or reserve paths before dispatch.

The first step is the measurement path in `yaah-6xyl.5`, not a scheduler. It
must provide a fan-out/dispatch denominator and record conflicts when they are
detected rather than only when a caller happens to inspect a proposal.

## If prevention is later adopted

The minimum honest design would be:

- optional `write_paths` hints on `delegate`, never mandatory;
- hints are advisory and recorded as claims with a dispatch id;
- overlapping claims warn or queue, but never silently block a dispatch;
- claims are released on settle/discard/release and reconciled against the
  actual changed paths;
- unknown shell mutations remain visible rather than being treated as covered
  by a claim;
- shared-lane writes are never described as prevented unless every write path
  is actually intercepted.

Until then, the safe control is the one already shipped: show the conflict,
keep settlement fail-closed, and make three-way merging explicit.
