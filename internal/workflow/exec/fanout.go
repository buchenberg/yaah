package exec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// Fan-out execution (yaah-0u0v.14).
//
// A fan-out node dispatches nothing itself. Once its `over` source resolves it
// becomes a set of ITEMS, and each item becomes its own instance of the body: a
// copy of every body node with an item-scoped path, its own lifecycle state, its
// own attempts and retries, and its own failure disposition. From that point the
// run's ordinary machinery schedules them — the ready queue, join-all over
// needs, the global MaxConcurrentDispatches cap, budget accounting — because an
// instance is a node like any other. What is specific to fan-out is therefore
// small and lives here:
//
//   - the item scope (`item.<as>` and `index`) is per instance;
//   - a nested cap keeps at most the declared max_parallel of one fan-out's
//     dispatches in flight, alongside the global cap rather than instead of it;
//   - the collected value is assembled in INPUT order, never in completion or
//     dispatch order;
//   - on_item_failure decides what a failed item does to its siblings and to
//     the collected value.
//
// Nothing here starts a goroutine, opens a channel, or takes a lock. Fan-out
// parallelism is logical: the items are ordinary ready-queue entries, the caps
// are counts the reducer owns, and the sequence stays reproducible.
//
// # Item paths
//
// The IR gives every body node a stable path ("steps.build.body[0]"). An
// instance path inserts the item discriminator between the container and the
// body index:
//
//	steps.build                    the fan-out node itself
//	steps.build.items[2]           the third item — a path PREFIX, not a node
//	steps.build.items[2].body[0]   the item's first body node, instantiated
//
// The convention is (a) deterministic: the item index is the index in the
// resolved `over` list, so the same graph and the same inputs always mint the
// same paths; (b) collision-free: no authored path can contain "items[", because
// ir.Resolve only ever appends ".body[i]"; (c) greppable: one prefix selects one
// item, one suffix selects one body node, and a dispatch id
// ("steps.build.items[2].body[0]#1") names the attempt absolutely; and (d)
// input-ordered by index — item[j] is always the j-th input element, so a
// trace entry names the element that produced it even though the ready queue
// sorts by path (lexicographically) and starts items[10] before items[2].
// A nested container chains the same way: steps.outer.items[0].body[1].items[3]
// .body[0], and its scope keeps the outer bindings behind its own.

// Reasons the fan-out family states.
const (
	// ReasonFanoutOverUnavailable prefixes a fan-out whose `over` source this
	// run cannot resolve: no source, an out-of-scope step, a producer that did
	// not succeed, or a field the value does not carry. Fanning out to zero
	// items would make an unreadable source look like "no work to do", which is
	// the quiet outcome this reason exists to prevent.
	ReasonFanoutOverUnavailable = "fanout_over_unavailable"
	// ReasonFanoutBodyMissing prefixes a fan-out with no body to run per item.
	ReasonFanoutBodyMissing = "fanout_body_missing"
	// ReasonFanoutBodyUnresolved prefixes a body node whose need names nothing
	// in its item scope. Such an instance could never become ready, so the
	// fan-out would stay open forever; the expansion refuses instead.
	ReasonFanoutBodyUnresolved = "fanout_body_unresolved"
	// ReasonFanoutItemFailed names the item behind a failed fan-out: under
	// on_item_failure: abort the container fails with this reason and the run
	// stops scheduling.
	ReasonFanoutItemFailed = "fanout_item_failed"
	// ReasonFanoutCollectFailed prefixes a collected value that could not be
	// encoded as the node's recorded text.
	ReasonFanoutCollectFailed = "fanout_collect_failed"
)

// FanoutItem is one expanded item of a fan-out node: the element of `over` it
// bound, the body instances it became, and its own outcome. It is the item
// ledger a trace reads and the source the collected value is assembled from, so
// it is written only inside Apply, like every other piece of lifecycle state.
type FanoutItem struct {
	// Index is the item's position in the resolved `over` list. The collected
	// value is assembled in this order.
	Index int `json:"index"`
	// Bound is the value the fan-out's `as` name resolved to for this item.
	Bound any `json:"bound,omitempty"`
	// Paths are the item's body instances in body order. The last one is the
	// item's PRODUCT: its recorded value is what the item contributes to the
	// collect, because the body is ordered as a sequence (§5.3) and its earlier
	// nodes are inputs to the last.
	Paths []string `json:"paths,omitempty"`
	// State is the item's outcome, folded from its instances once they all
	// terminated: failed outranks cancelled outranks skipped, and success means
	// the body ran to its product.
	State State `json:"state,omitempty"`
	// Reason names the instance that decided the outcome, with its own reason.
	Reason string `json:"reason,omitempty"`
	// Value is the item's collected value: its product instance's recorded
	// value, and nil for an item that did not succeed.
	Value any `json:"value,omitempty"`
}

// instance is the run-state record of one body node instantiated for one
// fan-out item.
type instance struct {
	// owner is the path of the fan-out node that minted it.
	owner string
	// item is the item index, and bound the value `as` resolved to.
	item  int
	bound any
	// name is the fan-out's `as` binding; empty leaves Item(name) unbound while
	// still giving the body its `index`.
	name string
	// locals maps the item's body-local step ids to their instance paths, so a
	// body template or a sibling's need resolves inside its own item. A nested
	// container's locals start from its parent's, so the inner body keeps the
	// outer bindings behind its own.
	locals map[string]string
}

// effectiveItemFailure folds the one default the scheduler owns for items: an
// author who declared no on_item_failure did not authorize the run to carry on
// with a missing workstream, so the disposition is abort — the same reasoning
// effectivePolicy applies to a node's on_failure. join: all is the matching
// default, and "all of them delivered" is what an undeclared disposition says.
func effectiveItemFailure(f ir.ItemFailure) ir.ItemFailure {
	if f == "" {
		return ir.OnItemAbort
	}
	return f
}

// itemFailureString renders a fan-out's effective item disposition for a reason
// line, so a reader sees the folded default rather than an empty field.
func itemFailureString(n *ir.Node) string {
	if n == nil {
		return string(ir.OnItemAbort)
	}
	return string(effectiveItemFailure(n.OnItemFailure))
}

// maxParallelOf reads a fan-out's nested ceiling: the declared max_parallel, or
// 0 for unbounded — which still means bounded by the run's
// MaxConcurrentDispatches, because the nested cap is added to the global one,
// never traded against it.
func maxParallelOf(n *ir.Node) int {
	if n == nil || n.MaxParallel == nil || *n.MaxParallel <= 0 {
		return 0
	}
	return *n.MaxParallel
}

// itemPath mints one instance path: the body node's own path with the item
// discriminator inserted between its container and its body index. It is a pure
// function of the graph and the item index, which is what makes it stable across
// runs and greppable in a trace.
func itemPath(containerPath string, item int, bodyPath string) string {
	return instanceChildPath(containerPath, ".items["+strconv.Itoa(item)+"]", bodyPath)
}

// instanceChildPath splices one instance discriminator into a body node's
// template path, at the container's own boundary.
//
// A nested container is itself an instance, so its path already carries its
// ancestors' ".items[j]"/".iters[k]" segments while its body nodes still carry
// authoring template paths. The prefix is therefore the container's TEMPLATE
// path, not its instance path: for container `steps.outer.items[0].body[1]` and
// body node `steps.outer.body[1].body[0]` the result is
// `steps.outer.items[0].body[1].items[k].body[0]` — not the malformed
// `...items[k].steps.outer...` a naive instance-path prefix would mint.
func instanceChildPath(containerPath, discriminator, bodyPath string) string {
	tmpl := templatePath(containerPath)
	rest := bodyPath
	if len(tmpl) <= len(bodyPath) && bodyPath[:len(tmpl)] == tmpl {
		rest = bodyPath[len(tmpl):]
	}
	return containerPath + discriminator + rest
}

// templatePath recovers a container's authoring template path from its instance
// path by stripping every instance discriminator it carries — ".items[j]" for a
// fan-out item and ".iters[k]" for a loop iteration, at any depth. Removal is
// exact (nothing authored can contain either marker: ir.Resolve only appends
// ".body[i]") and the result is always a prefix of the container's own body-node
// template paths.
func templatePath(path string) string {
	for {
		cut := -1
		for _, marker := range []string{".items[", ".iters["} {
			if i := strings.LastIndex(path, marker); i > cut {
				cut = i
			}
		}
		if cut < 0 {
			return path
		}
		j := strings.IndexByte(path[cut:], ']')
		if j < 0 {
			return path
		}
		path = path[:cut] + path[cut+j+1:]
	}
}

// itemBindings returns the item scope chain for one node path: outermost
// container first, innermost last, so a binding name resolves to the innermost
// container that declared it. A top-level node has an empty chain, which is why
// Item(name) is unbound and Index() is 0 outside a body.
func (r *Run) itemBindings(path string) []itemBinding {
	var chain []itemBinding
	for p := path; ; {
		inst := r.instances[p]
		if inst == nil {
			break
		}
		chain = append(chain, itemBinding{name: inst.name, value: inst.bound, index: inst.item})
		p = inst.owner
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// itemMaxParallel is the nested ceiling that governs one instance: its
// fan-out's declared max_parallel, 0 when unbounded or when the path is not an
// instance.
func (r *Run) itemMaxParallel(path string) int {
	inst := r.instances[path]
	if inst == nil {
		return 0
	}
	return maxParallelOf(r.byPath[inst.owner])
}

// itemSlotFree reports whether the fan-out that owns path has room under its
// nested cap. The tally counts the fan-out's in-flight DISPATCHES, which is at
// least as strict as counting items that have one in flight: a body of several
// parallel nodes is throttled by dispatch, so no reading of "max_parallel
// items" can be exceeded by the looser one either.
func (r *Run) itemSlotFree(path string) bool {
	max := r.itemMaxParallel(path)
	if max <= 0 {
		return true
	}
	return r.itemInFlight[r.instances[path].owner] < max
}

// fanoutStepOnce drives one fan-out cycle: it expands the first ready fan-out
// whose `over` list can be resolved. A fan-out whose producer has not terminated
// yet is a WAIT, not an empty list, so the cycle falls through and the run does
// whatever else it can — the same rule settlement planning follows.
//
// Closing a fan-out is deliberately not a driver step: it is the consequence of
// its last item terminating, and consequences belong to the reducer
// (noteItemOutcome). Nothing here mutates state.
func (r *Run) fanoutStepOnce() (bool, error) {
	for _, p := range r.ready {
		n := r.byPath[p]
		if n == nil || n.Kind != ir.KindFanout {
			continue
		}
		st := r.state[p]
		if st == nil || st.State != StateReady || st.Expanded {
			continue
		}
		items, deferred, refusal := r.fanoutItems(n)
		if deferred {
			continue
		}
		return true, r.Apply(FanoutExpandedEvent{NodePath: p, Items: items, Refusal: refusal})
	}
	return false, nil
}

// fanoutItems resolves a fan-out node's `over` source into the list of item
// values, in input order. The three answers are deliberate: a producer that has
// not terminated DEFERS (a wait, not an empty list), an unreadable source is a
// REFUSAL the reducer turns into the node's terminal state, and anything else is
// the list — which may legitimately be empty, because "none" is a fact the
// author is entitled to and the run states in a warning.
func (r *Run) fanoutItems(n *ir.Node) (items []any, deferred bool, refusal string) {
	if len(n.Body) == 0 {
		return nil, false, ReasonFanoutBodyMissing + ": " + n.Path + " declares no body to run per item"
	}
	if n.Over == nil {
		return nil, false, ReasonFanoutOverUnavailable + ": " + n.Path + " declares no over source"
	}
	if n.Over.Selector == nil {
		return append([]any(nil), n.Over.Literals...), false, ""
	}
	sel := n.Over.Selector
	if sel.From == nil {
		return nil, false, ReasonFanoutOverUnavailable + ": " + n.Path + " declares an over selector with no source"
	}
	if p := r.awaitingProducer(sel); p != "" {
		return nil, true, ""
	}
	v, why := r.overValue(sel)
	if why != "" {
		return nil, false, why
	}
	elems, why := elementsFromValue(v)
	if why != "" {
		return nil, false, ReasonFanoutOverUnavailable + ": " + n.Path + " over " + sel.From.String() + " " + why
	}
	if sel.AsList {
		// as_list wraps the whole value as ONE item: the author asked for a
		// single workstream that happens to be a list. A predicate is then
		// meaningless — there is nothing to filter, only the one wrapped value —
		// so it is skipped rather than read against the whole list.
		return []any{v}, false, ""
	}
	if selected, why := filterElements(elems, sel.Predicate); why != "" {
		return nil, false, why
	} else {
		elems = selected
	}
	return elems, false, ""
}

// overValue resolves a fan-out selector's source against recorded run state. It
// reads through the same value extraction a template and a settle handle set
// read (Run.stepValue), so `over: {from: steps.plan.output.workstreams}` and
// `{{ steps.plan.output.workstreams }}` cannot disagree about what the plan
// produced.
func (r *Run) overValue(sel *ir.Selector) (any, string) {
	src := sel.From
	var v any
	switch src.Kind {
	case ir.SourceParams:
		p, ok := r.params[src.Name]
		if !ok {
			return nil, ReasonFanoutOverUnavailable + ": " + src.String() + " names an unbound param"
		}
		v = p
	case ir.SourceStepOutput, ir.SourceStepCollect:
		dep, ok := r.byID[src.StepID]
		if !ok {
			return nil, ReasonFanoutOverUnavailable + ": " + src.String() + " names an out-of-scope step"
		}
		st := r.state[dep.Path]
		if st == nil || st.State != StateSucceeded {
			return nil, ReasonFanoutOverUnavailable + ": " + src.String() + ": " + dep.Path + " did not succeed"
		}
		if src.Kind == ir.SourceStepCollect {
			if st.Collect == nil {
				return nil, ReasonFanoutOverUnavailable + ": " + src.String() + ": " + dep.Path +
					" collected nothing (it declares no collect)"
			}
			v = st.Collect
			break
		}
		v = r.stepValue(dep, st)
	case ir.SourceItem:
		return nil, ReasonFanoutOverUnavailable + ": " + src.String() +
			" is an item binding; a container's own `over` is compiled in its outer scope, so no item is bound here"
	}
	walked, err := walkValue(v, src.Path)
	if err != nil {
		return nil, ReasonFanoutOverUnavailable + ": " + src.String() + ": " + err.Error()
	}
	return walked, ""
}

// elementsFromValue normalizes a resolved `over` value into the item list. A
// list is the item list in its own order; a mapping is ONE item, because
// `over: {from: steps.plan.output}` with a single workstream is one workstream
// and not its values; any other scalar is likewise one item, so a scalar source
// does not fan out to nothing. nil is the empty list and is reported as such.
func elementsFromValue(v any) ([]any, string) {
	switch t := v.(type) {
	case nil:
		return nil, ""
	case []any:
		return append([]any(nil), t...), ""
	case []string:
		out := make([]any, 0, len(t))
		for _, s := range t {
			out = append(out, s)
		}
		return out, ""
	}
	return []any{v}, ""
}

// filterElements applies the selector's predicate to the item list. The elements
// are arbitrary values, so the filter reads the predicate's `field` out of a
// mapping element and compares the element itself otherwise — the element-wise
// rule ir.Predicate.Field documents, and the same one settle's where clause
// applies to proposal handles.
func filterElements(elems []any, p ir.Predicate) ([]any, string) {
	if p.Op == ir.PredNone && p.Field == "" {
		return elems, ""
	}
	out := make([]any, 0, len(elems))
	for _, e := range elems {
		v, why := elementField(e, p.Field)
		if why != "" {
			return nil, ReasonFanoutOverUnavailable + ": " + why
		}
		ok, err := predicateMatch(v, p)
		if err != nil {
			return nil, ReasonFanoutOverUnavailable + ": over " + err.Error()
		}
		if ok {
			out = append(out, e)
		}
	}
	return out, ""
}

// elementField reads the predicate's field out of one element of a filtered
// selector set. A field no element carries is refused rather than compared as
// absent: an `over` filter that matched nothing because a name was mistyped is
// exactly the quiet outcome a fan-out of zero items would produce, and a loop
// terminator that never matched because of the same typo would run to its bound
// and call that an answer. The caller prefixes the reason it reports this under.
func elementField(e any, field string) (any, string) {
	if field == "" {
		return e, ""
	}
	m := objectOf(e)
	if m == nil {
		return nil, fmt.Sprintf("an element is not a mapping, so it has no field %q", field)
	}
	v, ok := m[field]
	if !ok {
		return nil, fmt.Sprintf("no element carries the field %q", field)
	}
	return v, ""
}

// objectOf reads a value as a mapping: a decoded object, or a string holding
// one. It is how a recorded value that arrived as JSON text is read by name,
// the same decoding stepValue applies to a structured report body.
func objectOf(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if s, isStr := v.(string); isStr {
		if decoded, ok := decodeJSONObject(s); ok {
			return decoded
		}
	}
	return nil
}

// onFanoutExpanded instantiates one body instance per item and hands them to the
// ordinary scheduler. An empty item list is a completed fan-out of nothing: the
// node closes succeeded with an empty collect, and the run states the fact as a
// warning, because "the plan produced no workstreams" is something an author
// wants to see and a silent success would hide.
func (r *Run) onFanoutExpanded(e FanoutExpandedEvent) error {
	st := r.state[e.NodePath]
	if st == nil {
		return fmt.Errorf("workflow/exec: fanout_expanded for unknown node %q", e.NodePath)
	}
	if st.State != StateReady {
		return fmt.Errorf("workflow/exec: fanout_expanded for node %s in state %s", e.NodePath, st.State)
	}
	n := r.byPath[e.NodePath]
	if e.Refusal != "" {
		return r.failTerminal(e.NodePath, e.Refusal)
	}
	if n == nil || len(n.Body) == 0 {
		return r.failTerminal(e.NodePath, ReasonFanoutBodyMissing+": "+e.NodePath+" declares no body to run per item")
	}
	if st.Expanded {
		return fmt.Errorf("workflow/exec: node %s is already expanded", e.NodePath)
	}
	st.Expanded = true
	for j, bound := range e.Items {
		if err := r.instantiateItem(n, st, j, bound); err != nil {
			return err
		}
	}
	if len(st.Items) == 0 {
		r.warnOnce("fanout-empty:"+n.Path, n.Path+": over resolved to zero items; the fan-out closes with an empty collect")
		return r.closeFanout(n, st)
	}
	// Entry instances — the ones with no needs — become ready in item order and
	// then body order, which is the collection order rather than the path order
	// the ready queue sorts by. Both orders are deterministic; this one is the
	// one a reader of the document expects.
	for i := range st.Items {
		for _, p := range st.Items[i].Paths {
			if len(r.byPath[p].Needs) != 0 {
				continue
			}
			if err := r.derive(NodeReadyEvent{NodePath: p}); err != nil {
				return err
			}
		}
	}
	return nil
}

// instantiateItem creates the body instances of one item and records the item in
// the fan-out's ledger.
func (r *Run) instantiateItem(n *ir.Node, st *NodeState, j int, bound any) error {
	paths, unresolved, err := r.instantiateBody(n, instancePlan{
		path:  func(b string) string { return itemPath(n.Path, j, b) },
		item:  j,
		bound: bound,
		name:  n.As,
	})
	if err != nil {
		return err
	}
	st.Items = append(st.Items, FanoutItem{Index: j, Bound: bound, Paths: paths, State: StatePending})
	if unresolved != "" {
		return r.failTerminal(n.Path, ReasonFanoutBodyUnresolved+": "+unresolved+
			" needs a step that is not in its item scope")
	}
	return nil
}

// allItemsTerminal reports whether every instance of one fan-out has terminated.
func (r *Run) allItemsTerminal(owner string) bool {
	return r.scopeTerminal(r.instanceOf[owner])
}

// noteItemOutcome is the container's reaction to one of its instances
// terminating, dispatched by the container's kind. Under abort a failed fan-out
// item fails the fan-out immediately, so the run stops scheduling instead of
// finishing work nobody will collect; otherwise the last item to terminate closes
// the fan-out. A loop folds one ITERATION when its last instance is done and lets
// the driver ask for the next one; an aggregate folds its whole combine body. It
// runs inside Apply, after the instance's own terminal transition, and is a no-op
// when the container is already terminal or the run is closing.
func (r *Run) noteItemOutcome(path string) error {
	inst := r.instances[path]
	if inst == nil || r.closing {
		return nil
	}
	f := r.byPath[inst.owner]
	st := r.state[inst.owner]
	if f == nil || st == nil || st.State != StateReady {
		return nil
	}
	switch f.Kind {
	case ir.KindLoop:
		return r.noteLoopInstance(f, st, inst)
	case ir.KindAggregate:
		return r.noteAggregateInstance(f, st)
	case ir.KindFanout:
		// handled below
	default:
		return nil
	}
	if r.state[path].State == StateFailed && effectiveItemFailure(f.OnItemFailure) == ir.OnItemAbort {
		return r.failTerminal(inst.owner, ReasonFanoutItemFailed+": item "+strconv.Itoa(inst.item)+
			" "+path+": "+r.state[path].Reason)
	}
	if !r.allItemsTerminal(inst.owner) {
		return nil
	}
	return r.closeFanout(f, st)
}

// closeFanout folds the items into the node's joined value and closes it.
//
// The collect is assembled in ITEM INDEX order — the order the author's list had
// — so it never depends on which item the door happened to finish first, and the
// node's recorded text is that list encoded, which makes `steps.<id>.output` and
// `steps.<id>.collect` two readings of one value.
//
// A failed item is handled by the fan-out's on_item_failure and not by the body
// node's own on_failure: the item is one of N, and whether N-1 siblings still
// count is the container's declaration. A SKIPPED item is not a failure — the
// author declared it — so it never trips abort; it simply contributes no value,
// exactly as a skipped node contributes nothing downstream.
func (r *Run) closeFanout(n *ir.Node, st *NodeState) error {
	policy := effectiveItemFailure(n.OnItemFailure)
	collect := make([]any, 0, len(st.Items))
	failed := 0
	for i := range st.Items {
		it := &st.Items[i]
		r.itemOutcome(it)
		switch it.State {
		case StateFailed:
			failed++
			if policy == ir.OnItemAbort {
				return r.failTerminal(n.Path, ReasonFanoutItemFailed+": item "+strconv.Itoa(it.Index)+
					" failed under on_item_failure: abort: "+it.Reason)
			}
			if policy == ir.OnItemCollectFailures {
				collect = append(collect, failureRecord(it))
			}
		case StateSucceeded:
			collect = append(collect, it.Value)
		}
	}
	st.Collect = collect
	encoded, err := json.Marshal(collect)
	if err != nil {
		return r.failTerminal(n.Path, ReasonFanoutCollectFailed+": encoding the collected value: "+err.Error())
	}
	st.Result = string(encoded)
	r.recordOutput(st, NodeOutput{
		NodePath: n.Path,
		Kind:     n.Kind,
		Name:     fanoutBindingName(n),
		Value:    st.Result,
	})
	if failed > 0 {
		r.warnOnce("fanout-failed:"+n.Path, fmt.Sprintf(
			"%s: %d of %d item(s) failed under on_item_failure: %s; the collect carries %d value(s)",
			n.Path, failed, len(st.Items), policy, len(collect)))
	}
	return r.derive(NodeSucceededEvent{NodePath: n.Path, Outputs: outputNames(st)})
}

// failureRecord is what on_item_failure: collect_failures puts in the collected
// list in place of a failed item's value: the item's index and the reason its
// product did not arrive. It is a mapping rather than a null so a consumer can
// tell "this item failed" from "this item produced null".
func failureRecord(it *FanoutItem) map[string]any {
	return map[string]any{
		"index":  it.Index,
		"failed": true,
		"reason": it.Reason,
		"paths":  append([]string(nil), it.Paths...),
	}
}

// itemOutcome folds one item's instance states into the item's own outcome,
// through the fold every container execution shares (foldScope).
func (r *Run) itemOutcome(it *FanoutItem) {
	it.State, it.Reason, it.Value = r.foldScope(it.Paths)
}

// fanoutBindingName is the name a fan-out's collected value is bound under: its
// declared `collect` name, else its step id, else its path — the same rule emit
// and settle follow, so `steps.<id>.output` and a named binding read one value.
func fanoutBindingName(n *ir.Node) string {
	if n.Collect != "" {
		return n.Collect
	}
	if n.ID != "" {
		return n.ID
	}
	return n.Path
}

// collectValue is a fan-out's recorded joined value: the item values in input
// order. A fan-out that closed with nothing collected reports an empty list
// rather than nil, so a template renders "[]" and a selector filters an empty
// set instead of failing on a value that does not exist — an empty collect is a
// fact, and "no value at all" is a different one.
func (st *NodeState) collectValue() []any {
	if st.Collect == nil {
		return []any{}
	}
	return st.Collect
}
