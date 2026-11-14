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
// What it resolves in this bead, and what it refuses:
//
//   - params.* — bound at New, defaults folded in;
//   - run.id and run.attempt — the run's label and this attempt's ordinal;
//   - steps.<id>.output — the recorded result text of a node that succeeded.
//     Per-binding output values (an `output:` name inside a declared Report or
//     Verdict) are value extraction, and value extraction is the bead that
//     reads the result payload (.13);
//   - steps.<id>.collect — refused. A container's collect is fan-out join
//     semantics (.14), and refusing is the only honest answer: returning an
//     empty list would let a template render "no workstreams" and a selector
//     filter to nothing, and both look like a successful run to the log.
//   - item.* and index — refused the same way, since bodies are entered
//     through containers this bead does not execute.
type ScopeSnapshot struct {
	runID   string
	attempt int
	params  map[string]any
	outputs map[string]any
}

func (s *ScopeSnapshot) Param(name string) (any, bool) {
	v, ok := s.params[name]
	return v, ok
}

func (s *ScopeSnapshot) StepField(id, field string) (any, error) {
	switch field {
	case "output":
		v, ok := s.outputs[id]
		if !ok {
			return nil, fmt.Errorf("step %q has no recorded output", id)
		}
		return v, nil
	case "collect":
		return nil, fmt.Errorf("step %q collect is not available: fan-out join semantics are yaah-0u0v.14", id)
	}
	return nil, fmt.Errorf("step %q has no field %q", id, field)
}

func (s *ScopeSnapshot) Item(string) (any, bool) {
	return nil, false
}

func (s *ScopeSnapshot) Index() int {
	return 0
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
