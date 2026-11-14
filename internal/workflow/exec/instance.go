package exec

import (
	"fmt"
	"sort"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Body instances (yaah-0u0v.14, extended by yaah-0u0v.15).
//
// Three kinds run a body: a fan-out once per item, a loop once per iteration,
// and an aggregate once for the body of its combine strategy. All three
// instantiate the same way — a copy of every body node under a path that says
// which execution it belongs to, its own lifecycle state, its own attempts and
// retries, its own failure disposition, its own scope — and from there the run's
// ordinary machinery schedules them: the ready queue, join-all over needs, the
// global MaxConcurrentDispatches cap, budget accounting. A container that
// dispatches nothing itself is therefore an EXPANSION, never an attempt, and
// what differs per kind is only the path discriminator and the scope binding. So
// that is all instancePlan holds, and the per-kind ledger (a fan-out's items, a
// loop's iterations) stays with its kind.
//
// Nothing here starts a goroutine, opens a channel, or takes a lock. Container
// parallelism is a property of how many instances the container hands the
// scheduler at once — a fan-out hands them all, a loop hands one iteration's
// worth — never of how the instances run.

// instancePlan is one container execution's shape: how its body's instance paths
// are minted and what the body's scope binds for it.
type instancePlan struct {
	// path mints one instance path from a body node's template path.
	path func(bodyPath string) string
	// item is the index the body's `index` reads — the fan-out item index, the
	// loop iteration — and 0 for a body that numbers nothing.
	item int
	// bound is the value a fan-out's `as` name binds per item, nil for a
	// container that binds none.
	bound any
	// name is the fan-out's `as` binding; empty leaves Item(name) unbound while
	// still giving the body its `index`.
	name string
}

// instantiateBody copies a container's body into instances and registers their
// dependencies. The body node is COPIED: the resolved graph is immutable, and an
// instance differs from its template in exactly two fields — its path, and the
// scope it is dispatched against.
//
// It returns the instance paths in body order and, when a body node's need
// resolves to nothing in its own execution's scope, the path of the first such
// instance. That refusal is stated by the CALLER in its own vocabulary, because
// which container cannot resolve it is the container kind's fact — a fan-out
// item, a loop iteration, and an aggregate body each say so in their own words —
// while the reason it cannot be resolved is the same one in all three: an
// instance whose need names nothing could never become ready, so the container
// would stay open forever. A minted path that already exists is returned as an
// error instead: the instance paths are a pure function of the container path and
// the execution index, so a collision is a defect in this package and not a
// declaration the author can fix.
func (r *Run) instantiateBody(n *ir.Node, plan instancePlan) (paths []string, unresolved string, err error) {
	locals := map[string]string{}
	if outer := r.instances[n.Path]; outer != nil {
		// A container nested in a body chains its scope: the inner body sees its
		// own bindings first and the outer ones behind it.
		for k, v := range outer.locals {
			locals[k] = v
		}
	}
	paths = make([]string, 0, len(n.Body))
	for _, b := range n.Body {
		if b == nil {
			continue
		}
		ip := plan.path(b.Path)
		if _, dup := r.byPath[ip]; dup {
			return paths, "", fmt.Errorf("workflow/exec: duplicate instance path %q", ip)
		}
		ic := *b
		ic.Path = ip
		r.byPath[ip] = &ic
		r.instances[ip] = &instance{
			owner:  n.Path,
			item:   plan.item,
			bound:  plan.bound,
			name:   plan.name,
			body:   b.Path,
			locals: locals,
		}
		r.state[ip] = &NodeState{
			Path:        ip,
			ID:          b.ID,
			Kind:        b.Kind,
			State:       StatePending,
			Needs:       append([]string(nil), b.Needs...),
			Policy:      effectivePolicy(b.OnFailure),
			MaxAttempts: attemptCeiling(b.Retry),
		}
		if b.ID != "" {
			locals[b.ID] = ip
		}
		paths = append(paths, ip)
	}
	r.instanceOf[n.Path] = append(r.instanceOf[n.Path], paths...)
	// A branch instantiated inside a body routes too, and its arms are top-level
	// steps (ir.resolveBranch validates them against the top-level ids), so the
	// routing relation is registered here for the same reason it is registered at
	// New: before any arm can become startable.
	if n.Kind == ir.KindBranch {
		r.registerArms(n)
	}
	// Dependencies are registered only once the whole local map exists: a body
	// node may need a sibling declared after it, and the row a need resolves
	// against is THIS execution's, never a same-named step in another.
	for _, ip := range paths {
		needs, ok := r.needPaths(r.byPath[ip])
		if !ok {
			return paths, ip, nil
		}
		for _, dep := range needs {
			r.dependents[dep] = append(r.dependents[dep], ip)
			// Canonical order, as New leaves the top-level lists: the release
			// order of ready events must not depend on expansion order.
			sort.Strings(r.dependents[dep])
		}
	}
	return paths, "", nil
}

// readyEntries makes a fresh execution's entry instances ready, in body order.
// An entry instance is one with no needs; every other instance waits for its own
// scope's needs through join-all.
func (r *Run) readyEntries(paths []string) error {
	for _, p := range paths {
		if n := r.byPath[p]; n == nil || len(n.Needs) != 0 {
			continue
		}
		if err := r.derive(NodeReadyEvent{NodePath: p}); err != nil {
			return err
		}
	}
	return nil
}

// scopeTerminal reports whether every instance of one execution has terminated.
func (r *Run) scopeTerminal(paths []string) bool {
	for _, p := range paths {
		if st := r.state[p]; st == nil || !st.State.Terminal() {
			return false
		}
	}
	return true
}

// foldScope folds one container execution's instance states into its outcome:
// failure outranks cancellation outranks skip — an execution that lost its
// product is the fact a reader needs first — and success carries the product's
// recorded value. The PRODUCT is the last body instance, because a body is
// ordered as a sequence (§5.3) and its earlier nodes are inputs to the last.
func (r *Run) foldScope(paths []string) (State, string, any) {
	state := StateSucceeded
	reason := ""
	note := func(p string, s State) {
		if state == StateFailed || (state != StateSucceeded && s != StateFailed) {
			return
		}
		state = s
		if st := r.state[p]; st != nil {
			reason = p + ": " + st.Reason
		} else {
			reason = p
		}
	}
	for _, p := range paths {
		st := r.state[p]
		if st == nil {
			continue
		}
		switch st.State {
		case StateFailed:
			note(p, StateFailed)
		case StateCancelled:
			note(p, StateCancelled)
		case StateSkipped:
			note(p, StateSkipped)
		}
	}
	var value any
	if state == StateSucceeded && len(paths) > 0 {
		last := paths[len(paths)-1]
		if st := r.state[last]; st != nil && st.State == StateSucceeded {
			value = r.stepValue(r.byPath[last], st)
		}
	}
	return state, reason, value
}
