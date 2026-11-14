// Package exec schedules a resolved workflow graph. It is the deterministic
// core of the workflow runtime: node lifecycle state, the ready queue, the
// concurrency cap, per-node attempt tracking, and the failure, skip, and
// cancellation rules all live here — and nothing else does. Every side effect
// leaves through one narrow door, the Runtime interface, so a run can be
// driven by a scripted fake in a test that never sleeps and by the real
// dispatch lane in production, over the same scheduler code.
//
// # What this package is not
//
// The scheduler schedules NODES, not model turns. Turns, compaction, retries
// inside one dispatch, provider calls, world provisioning, and settlement all
// stay behind Runtime. There is no node-type registry and no plugin table:
// node.Kind is handled by one fixed switch (kinds.go) over the closed
// vocabulary the spec decodes and ir resolves (WD1). There are no concrete
// tool bindings, and this package must never import internal/tools —
// `go list -deps ./internal/workflow/exec | grep internal/tools` must stay
// empty. That prohibition is also why the completion envelope crosses the
// boundary as the Completion mirror in envelope.go instead of as an import of
// internal/dispatch: internal/dispatch links internal/tools/substrate, so
// importing it would break the criterion for the whole package. The mirror is
// not a second envelope — it carries the same JSON field names as the one
// dispatch envelope, and envelope_parity_test.go decodes a real envelope
// rendered by dispatch.DispatchResult.Envelope into it and fails if a
// scheduler-visible field moves or disappears.
//
// # Scope fence
//
// yaah-0u0v.11 is the scheduler core only. Settlement verbs are .13, fan-out
// join semantics are .14, loop/branch/aggregate execution are .15, SQLite
// persistence is .16, and trace records are .17. The kinds this bead cannot
// yet execute are refused as a terminal node failure carrying reason
// not_implemented rather than run as no-ops. The alternative — reporting a
// fan-out, settle, or emit node as succeeded — would contradict what validate
// proves about the graph it ran: WF021 counts dispositions a run really
// applied, and WF033/WF034 bound repetitions a run really performs. A refusal
// is visible to an author, to the trace surface, and to the next bead; a
// silent success is none of those. The refusal still honors the node's
// on_failure policy, so a graph that declares skip or continue behaves the way
// it declares.
//
// # State and events
//
// Apply is the single state mutator. Lifecycle transitions, the ready queue,
// the dispatch counter, the in-flight tally, and the event log are only ever
// changed inside the reducer, so there is no parallel in-memory graph that can
// disagree with it. Events split into two groups:
//
//	input   — what a driver submits: run_started, dispatch_started,
//	        attempt_completed, node_cancelled, budget_exceeded, run_finished
//	derived — what only the reducer emits: node_ready, retry_scheduled,
//	        node_succeeded, node_failed, node_skipped
//
// Submitting a derived event is an error, not a shortcut: a caller that could
// write node_succeeded directly could state a fact the reducer had no chance
// to check. The driver loop is the only goroutine that touches a Run, so the
// reducer needs no lock, and the driver's queue of started-but-uncommitted
// attempts is door bookkeeping, never lifecycle state.
//
// A node's lifecycle is pending → ready → running → succeeded | failed |
// skipped | cancelled. A retried attempt returns its node to ready, so the
// join-all, guard, and kind rules are re-checked for every attempt exactly as
// they were for the first.
//
// # Readiness and skip propagation
//
// Join-all, the same rule WF004 checks in validate/reach.go: a node with needs
// is entered only once EVERY step it needs has terminated, and "terminated"
// means succeeded, failed, skipped, or cancelled — never a state a run must
// wait through, because a readiness rule that can stall forever is the hang
// this bead exists not to have. An entry step (no needs) is the vacuous case
// and is ready at run start. That is also why a need naming no step strands a
// node as surely as a cycle does: reach.go already reports it as WF004, and
// the scheduler leaves the node pending so the run finalizes it as cancelled
// rather than blocking.
//
// When a node becomes ready its needs' terminal states decide what it may do:
//
//   - every need succeeded — dispatch it (a guard and the kind switch still
//     get the last word);
//   - some need skipped — skip this node too, naming the need. v1 has no
//     authoring field that says "run me even though my input was never
//     produced", so propagation is unconditional;
//   - some need cancelled — cancel this node too;
//   - some need failed — run anyway. The need's own on_failure: continue is
//     the declaration that says the run proceeds past it, and that is what
//     "continue exposes the failure downstream" means: the failure is recorded
//     on this node's state as an exposed input, and the node is dispatched.
//
// Guards. A node's when selector is asked of Config.Guard, which owns selector
// evaluation (value plumbing is .13). A run with no Guard schedules guarded
// nodes as if their guard passed and says so in Result.Warnings, because a
// silent "no guard configured, so everything runs" is the kind of capability
// loss a caller cannot detect from the event log.

package exec
