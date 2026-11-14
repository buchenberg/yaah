package exec

import (
	"fmt"
	"sort"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// ScopeSnapshot is the immutable view of run state one attempt is dispatched
// against. ir.Template documents that "the scheduler implements [Scope] over
// run state; the graph never holds run state", and this is that
// implementation: the reducer builds it from its own state at the moment the
// attempt starts, and a Runtime that reads it later cannot observe a value
// that changed underneath it.
//
// What it resolves, and what it refuses:
//
//   - params.* — bound at New, defaults folded in;
//   - run.id and run.attempt — the run's label and this attempt's ordinal;
//   - steps.<id>.output — the recorded value of a node that succeeded. A
//     proposal-lane delegate's value is its typed handle, a structured Report's
//     is decoded by field name, a settle's is its settled record, an emit's is
//     the rendered text, and a fan-out's is its collected items. One place
//     decides those readings (Run.stepValue), so a template and a selector can
//     never disagree about what a step produced;
//   - steps.<id>.collect — a fan-out's joined value: its item values in INPUT
//     order (yaah-0u0v.14). It is present once the fan-out has closed, and
//     absent — an honest error, not an empty list — before that: an empty list
//     would let a template render "no workstreams" and a selector filter to
//     nothing, and both look like a successful run to the log;
//   - item.<as> and index — the item scope of the fan-out body this attempt
//     belongs to. A top-level node has none, and asking for one is an error
//     rather than a zero value.
//
// The bindings are looked up by node PATH, not by step id, because a fan-out
// instance's id is body-local: two items' bodies may both declare `review`, and
// only the item's own scope may resolve it. A body-local id shadows a top-level
// one of the same name, which is the shadowing rule ir.resolveBody's child
// scope applies at compile time.
type ScopeSnapshot struct {
	runID   string
	attempt int
	params  map[string]any
	// outputs is keyed by node path: every succeeded node's recorded value.
	outputs map[string]any
	// collect is each closed fan-out's joined value, keyed by node path.
	collect map[string]any
	// ids maps a top-level step id to its node path.
	ids map[string]string
	// locals maps a body-local step id to the instance path inside the ITEM
	// this attempt belongs to.
	locals map[string]string
	// items is the item binding chain, outermost container first, so a name
	// resolves to the innermost container that declared it.
	items []itemBinding
}

// itemBinding is one container's per-item binding: the `as` name, the value it
// bound for this item, and the item index `index` resolves to.
type itemBinding struct {
	name  string
	value any
	index int
}

func (s *ScopeSnapshot) Param(name string) (any, bool) {
	v, ok := s.params[name]
	return v, ok
}

func (s *ScopeSnapshot) StepField(id, field string) (any, error) {
	path, ok := s.stepPath(id)
	if !ok {
		return nil, fmt.Errorf("step %q is not in scope", id)
	}
	switch field {
	case "output":
		v, ok := s.outputs[path]
		if !ok {
			return nil, fmt.Errorf("step %q has no recorded output", id)
		}
		return v, nil
	case "collect":
		v, ok := s.collect[path]
		if !ok {
			return nil, fmt.Errorf("step %q has no recorded collect", id)
		}
		return v, nil
	}
	return nil, fmt.Errorf("step %q has no field %q", id, field)
}

// stepPath resolves a step id to its node path in this scope: the item's own
// body-local ids first, then the top-level steps.
func (s *ScopeSnapshot) stepPath(id string) (string, bool) {
	if p, ok := s.locals[id]; ok {
		return p, true
	}
	p, ok := s.ids[id]
	return p, ok
}

// Item resolves a fan-out item by its `as` name, innermost binding first.
func (s *ScopeSnapshot) Item(name string) (any, bool) {
	for i := len(s.items) - 1; i >= 0; i-- {
		if s.items[i].name != "" && s.items[i].name == name {
			return s.items[i].value, true
		}
	}
	return nil, false
}

// Index is the innermost item's index in its fan-out's `over` list. A node
// outside a container body reports 0, which is what it always reported.
func (s *ScopeSnapshot) Index() int {
	if len(s.items) == 0 {
		return 0
	}
	return s.items[len(s.items)-1].index
}

func (s *ScopeSnapshot) RunField(field string) (any, error) {
	switch field {
	case "id":
		return s.runID, nil
	case "attempt":
		return s.attempt, nil
	}
	return nil, fmt.Errorf("run has no field %q", field)
}

// bindParams folds a graph's declared defaults into the caller's bindings and
// refuses a run whose required parameters are missing. This is a New-time
// check, not a runtime one: a run that cannot bind its inputs has not started,
// and the caller learns that from an error rather than from a node that failed
// three events later.
func bindParams(params map[string]ir.Param, given map[string]any) (map[string]any, error) {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)

	bound := make(map[string]any, len(given)+len(params))
	for name, v := range given {
		bound[name] = v
	}
	for _, name := range names {
		p := params[name]
		if _, ok := bound[name]; ok {
			continue
		}
		if p.HasDefault {
			bound[name] = p.Default
			continue
		}
		if p.Required {
			return nil, fmt.Errorf("workflow/exec: required param %q is unbound", name)
		}
	}
	// A binding the graph never declared is kept: the scope only answers for
	// names a compiled reference asks about, and refusing an extra value
	// would make the scheduler stricter than the document it runs.
	return bound, nil
}
