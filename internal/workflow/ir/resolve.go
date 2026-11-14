package ir

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// ResolveError locates a resolution failure at a node path. Paths use the
// resolved graph form ("steps.build.body[0].prompt").
type ResolveError struct {
	Path    string
	Message string
}

func (e *ResolveError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

func resolveErrf(path, format string, args ...any) *ResolveError {
	return &ResolveError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// stepInfo is the scope-visible summary of a step: enough to check step
// references in templates and selectors without resolving the node.
type stepInfo struct {
	id         string
	kind       spec.NodeKind
	hasCollect bool
}

// lexscope is the lexical scope during resolution. steps holds every
// visible step id; items is the stack of in-scope fan-out `as` bindings,
// innermost last. Bodies see earlier local ids only, per §5.3.
type lexscope struct {
	params map[string]spec.ParamSpec
	steps  map[string]*stepInfo
	items  []string
	// indexed marks a scope nested inside a fan-out or loop body, where
	// `index` names the current iteration. It is not derived from items:
	// a loop iterates without binding an item, and a fan-out numbers its
	// items whether or not the author named the binding (yaah-0u0v.33).
	// The flag travels inward through child, so a nested body keeps the
	// enclosing containers exactly as it keeps the enclosing bindings.
	indexed bool
}

func (l *lexscope) child() *lexscope {
	steps := make(map[string]*stepInfo, len(l.steps)+4)
	for k, v := range l.steps {
		steps[k] = v
	}
	items := make([]string, len(l.items))
	copy(items, l.items)
	return &lexscope{params: l.params, steps: steps, items: items, indexed: l.indexed}
}

// Resolve compiles a decoded workflow document into the immutable graph.
// The document must have passed the spec decoder; Resolve re-checks only
// what resolution itself needs (reference existence, lexical scope,
// predicate and selector types).
func Resolve(doc *spec.Document) (*Graph, error) {
	if doc == nil {
		return nil, resolveErrf("", "nil document")
	}
	g := &Graph{Name: doc.Metadata.Name}

	lex := &lexscope{params: doc.Spec.Params, steps: map[string]*stepInfo{}}

	g.Params = make(map[string]Param, len(doc.Spec.Params))
	for name, p := range doc.Spec.Params {
		pt, err := convertParamType(p.Type)
		if err != nil {
			return nil, resolveErrf("params."+name, "%v", err)
		}
		g.Params[name] = Param{
			Name:       name,
			Type:       pt,
			Required:   p.Required,
			HasDefault: p.Default != nil,
			Default:    p.Default,
		}
	}

	if b := doc.Spec.Budgets; b != nil {
		g.Budgets = &Budgets{
			MaxDispatches:           b.MaxDispatches,
			MaxWallClockSeconds:     b.MaxWallClockSeconds,
			MaxTokens:               b.MaxTokens,
			MaxConcurrentDispatches: b.MaxConcurrentDispatches,
		}
	}

	// Duplicate top-level ids break pathing and scope; reject before
	// resolving anything.
	seen := map[string]bool{}
	for i := range doc.Spec.Steps {
		id := doc.Spec.Steps[i].ID
		if id == "" {
			return nil, resolveErrf(fmt.Sprintf("spec.steps[%d].id", i), "top-level step has no id")
		}
		if seen[id] {
			return nil, resolveErrf(fmt.Sprintf("spec.steps[%d].id", i), "duplicate step id %q", id)
		}
		seen[id] = true
	}
	// All top-level ids are visible everywhere (ordering across them is a
	// validator concern, not a lexical one).
	for i := range doc.Spec.Steps {
		s := &doc.Spec.Steps[i]
		lex.steps[s.ID] = &stepInfo{id: s.ID, kind: s.Kind, hasCollect: s.Collect != ""}
	}

	for i := range doc.Spec.Steps {
		s := &doc.Spec.Steps[i]
		n, err := resolveNode(s, "steps."+s.ID, lex, seen, doc.Spec.Defaults)
		if err != nil {
			return nil, err
		}
		g.Steps = append(g.Steps, n)
	}
	return g, nil
}

// resolveNode resolves one authoring node into a graph node at path.
func resolveNode(in *spec.Node, path string, lex *lexscope, topIDs map[string]bool, defaults *spec.Defaults) (*Node, error) {
	kind, err := convertKind(in.Kind)
	if err != nil {
		return nil, resolveErrf(path, "%v", err)
	}
	n := &Node{
		Path:           path,
		ID:             in.ID,
		Kind:           kind,
		TimeoutSeconds: in.TimeoutSeconds,
	}
	for _, ref := range in.Needs {
		// A need names a step id in the node's lexical scope: a top-level id,
		// or — for a container body — an earlier body-local id. lex.steps holds
		// every visible id at this point (all top-level ids are registered
		// up-front, and resolveBody registers each body-local id before its
		// later siblings resolve), so it is the one authority for both.
		if _, ok := lex.steps[ref]; !ok {
			return nil, resolveErrf(path+".needs", "unknown step id %q", ref)
		}
		n.Needs = append(n.Needs, ref)
	}
	if n.When, err = compileSelector(path+".when", in.When, lex); err != nil {
		return nil, err
	}
	if in.Retry != nil {
		n.Retry = &Retry{MaxAttempts: in.Retry.MaxAttempts, BackoffSeconds: in.Retry.BackoffSeconds}
	}
	n.OnFailure, err = convertFailurePolicy(in.OnFailure)
	if err != nil {
		return nil, resolveErrf(path+".on_failure", "%v", err)
	}
	if in.Output != nil {
		ot, err := convertOutputType(in.Output.Type)
		if err != nil {
			return nil, resolveErrf(path+".output.type", "%v", err)
		}
		n.Output = &OutputSpec{Name: in.Output.Name, Type: ot, Schema: in.Output.Schema}
	}

	switch kind {
	case KindDelegate:
		if err := resolveDelegate(n, in, path, lex, defaults); err != nil {
			return nil, err
		}
	case KindFanout:
		if err := resolveFanout(n, in, path, lex, topIDs, defaults); err != nil {
			return nil, err
		}
	case KindBranch:
		if err := resolveBranch(n, in, path, lex, topIDs); err != nil {
			return nil, err
		}
	case KindLoop:
		if err := resolveLoop(n, in, path, lex, topIDs, defaults); err != nil {
			return nil, err
		}
	case KindGate:
		if err := resolveGate(n, in, path, lex, topIDs); err != nil {
			return nil, err
		}
	case KindSettle:
		if err := resolveSettle(n, in, path, lex); err != nil {
			return nil, err
		}
	case KindSelect:
		if err := resolveSelect(n, in, path, lex); err != nil {
			return nil, err
		}
	case KindAggregate:
		if err := resolveAggregate(n, in, path, lex, topIDs, defaults); err != nil {
			return nil, err
		}
	case KindCall:
		if err := resolveCall(n, in, path, lex); err != nil {
			return nil, err
		}
	case KindEmit:
		if n.Value, err = compileTemplate(path+".value", in.Value, lex); err != nil {
			return nil, err
		}
		n.ArtifactPath = in.Path
	case KindHuman:
		if n.Prompt, err = compileTemplate(path+".prompt", in.Prompt, lex); err != nil {
			return nil, err
		}
		n.OnTimeout, err = convertFailurePolicy(in.OnTimeout)
		if err != nil {
			return nil, resolveErrf(path+".on_timeout", "%v", err)
		}
	}
	return n, nil
}

// resolveDelegate folds shared defaults (lane, network, grants) into the
// dispatch node; node-level settings win.
func resolveDelegate(n *Node, in *spec.Node, path string, lex *lexscope, defaults *spec.Defaults) error {
	var err error
	n.Role = in.Role
	if n.Prompt, err = compileTemplate(path+".prompt", in.Prompt, lex); err != nil {
		return err
	}
	switch {
	case in.Lane != "":
		if n.Lane, err = convertLane(in.Lane); err != nil {
			return resolveErrf(path+".lane", "%v", err)
		}
	case defaults != nil && defaults.Lane != "":
		if n.Lane, err = convertLane(defaults.Lane); err != nil {
			return resolveErrf(path+".lane", "%v", err)
		}
	}
	if n.SeedFrom, err = compileTemplate(path+".seed_from", in.SeedFrom, lex); err != nil {
		return err
	}
	if n.ContinueFrom, err = compileTemplate(path+".continue_from", in.ContinueFrom, lex); err != nil {
		return err
	}
	if n.ForkFrom, err = compileTemplate(path+".fork_from", in.ForkFrom, lex); err != nil {
		return err
	}
	n.Provider = in.Provider
	n.Model = in.Model
	n.MaxTurns = in.MaxTurns
	switch {
	case in.Grants != nil:
		if n.Grants, err = convertGrants(in.Grants); err != nil {
			return resolveErrf(path+".grants", "%v", err)
		}
	case defaults != nil && defaults.Grants != nil:
		if n.Grants, err = convertGrants(defaults.Grants); err != nil {
			return resolveErrf(path+".grants", "%v", err)
		}
	}
	switch {
	case in.Network != "":
		if n.Network, err = convertNetwork(in.Network); err != nil {
			return resolveErrf(path+".network", "%v", err)
		}
	case defaults != nil && defaults.Network != "":
		if n.Network, err = convertNetwork(defaults.Network); err != nil {
			return resolveErrf(path+".network", "%v", err)
		}
	}
	n.World = in.World
	return nil
}

func resolveFanout(n *Node, in *spec.Node, path string, lex *lexscope, topIDs map[string]bool, defaults *spec.Defaults) error {
	var err error
	if in.Over != nil {
		over := &Over{}
		if in.Over.Selector != nil {
			if over.Selector, err = compileSelector(path+".over", in.Over.Selector, lex); err != nil {
				return err
			}
		} else {
			over.Literals = in.Over.Values
		}
		n.Over = over
	}
	n.As = in.As
	n.MaxParallel = in.MaxParallel
	n.Collect = in.Collect
	if n.Join, err = convertJoin(in.Join); err != nil {
		return resolveErrf(path+".join", "%v", err)
	}
	n.Quorum = in.Quorum
	if n.OnItemFailure, err = convertItemFailure(in.OnItemFailure); err != nil {
		return resolveErrf(path+".on_item_failure", "%v", err)
	}
	if n.Body, err = resolveBody(in.Body, path, lex, topIDs, defaults, n.As, true); err != nil {
		return err
	}
	return nil
}

func resolveBranch(n *Node, in *spec.Node, path string, lex *lexscope, topIDs map[string]bool) error {
	var err error
	if n.On, err = compileSelector(path+".on", in.On, lex); err != nil {
		return err
	}
	for i, c := range in.Cases {
		if !topIDs[c.Then] {
			return resolveErrf(fmt.Sprintf("%s.cases[%d].then", path, i), "unknown step id %q", c.Then)
		}
		n.Cases = append(n.Cases, Case{Value: c.Value, Then: c.Then})
	}
	if in.Default != "" && !topIDs[in.Default] {
		return resolveErrf(path+".default", "unknown step id %q", in.Default)
	}
	n.Default = in.Default
	return nil
}

func resolveLoop(n *Node, in *spec.Node, path string, lex *lexscope, topIDs map[string]bool, defaults *spec.Defaults) error {
	var err error
	n.MaxIterations = in.MaxIterations
	n.Collect = in.Collect
	if n.Until, err = compileSelector(path+".until", in.Until, lex); err != nil {
		return err
	}
	if n.ContinueFrom, err = compileTemplate(path+".continue_from", in.ContinueFrom, lex); err != nil {
		return err
	}
	// A loop body numbers its iterations and binds no item (yaah-0u0v.33).
	if n.Body, err = resolveBody(in.Body, path, lex, topIDs, defaults, "", true); err != nil {
		return err
	}
	return nil
}

func resolveGate(n *Node, in *spec.Node, path string, lex *lexscope, topIDs map[string]bool) error {
	var err error
	if in.Assert != nil {
		n.Assert = &Assert{Name: in.Assert.Name, Args: in.Assert.Args}
	}
	if in.Judge != nil {
		j := &Judge{Role: in.Judge.Role}
		if j.Prompt, err = compileTemplate(path+".judge.prompt", in.Judge.Prompt, lex); err != nil {
			return err
		}
		n.Judge = j
	}
	if in.OnPass != "" && !topIDs[in.OnPass] {
		return resolveErrf(path+".on_pass", "unknown step id %q", in.OnPass)
	}
	n.OnPass = in.OnPass
	if in.OnFail != "" && !topIDs[in.OnFail] {
		return resolveErrf(path+".on_fail", "unknown step id %q", in.OnFail)
	}
	n.OnFail = in.OnFail
	return nil
}

func resolveSettle(n *Node, in *spec.Node, path string, lex *lexscope) error {
	var err error
	if n.Proposals, err = compileSelector(path+".proposals", in.Proposals, lex); err != nil {
		return err
	}
	if n.Where, err = compileSelector(path+".where", in.Where, lex); err != nil {
		return err
	}
	if n.Action, err = convertSettleAction(in.Action); err != nil {
		return resolveErrf(path+".action", "%v", err)
	}
	// The authoring `policy` key is shared by select and settle (see
	// spec.Policy). A settle writes it into SettlePolicy, and a value from
	// the selection half of the vocabulary is rejected rather than dropped.
	if in.Policy != "" {
		if n.SettlePolicy, err = convertSettlePolicy(in.Policy); err != nil {
			return resolveErrf(path+".policy", "%v", err)
		}
	}
	if n.Order, err = convertSettleOrder(in.Order); err != nil {
		return resolveErrf(path+".order", "%v", err)
	}
	if n.OnConflict, err = convertConflictPolicy(in.OnConflict); err != nil {
		return resolveErrf(path+".on_conflict", "%v", err)
	}
	if n.OnEmpty, err = convertEmptyPolicy(in.OnEmpty); err != nil {
		return resolveErrf(path+".on_empty", "%v", err)
	}
	return nil
}

func resolveSelect(n *Node, in *spec.Node, path string, lex *lexscope) error {
	var err error
	if n.Candidates, err = compileSelector(path+".candidates", in.Candidates, lex); err != nil {
		return err
	}
	if in.Policy != "" {
		if n.Policy, err = convertSelectPolicy(in.Policy); err != nil {
			return resolveErrf(path+".policy", "%v", err)
		}
	}
	if in.Judge != nil {
		j := &Judge{Role: in.Judge.Role}
		if j.Prompt, err = compileTemplate(path+".judge.prompt", in.Judge.Prompt, lex); err != nil {
			return err
		}
		n.Judge = j
	}
	return nil
}

func resolveAggregate(n *Node, in *spec.Node, path string, lex *lexscope, topIDs map[string]bool, defaults *spec.Defaults) error {
	var err error
	for i := range in.Inputs {
		sel, err := compileSelector(fmt.Sprintf("%s.inputs[%d]", path, i), &in.Inputs[i], lex)
		if err != nil {
			return err
		}
		n.Inputs = append(n.Inputs, sel)
	}
	if n.Strategy, err = convertAggregateStrategy(in.Strategy); err != nil {
		return resolveErrf(path+".strategy", "%v", err)
	}
	// An aggregate combines its inputs; there is no iteration to number.
	if n.Body, err = resolveBody(in.Body, path, lex, topIDs, defaults, "", false); err != nil {
		return err
	}
	return nil
}

func resolveCall(n *Node, in *spec.Node, path string, lex *lexscope) error {
	n.Workflow = in.Workflow
	for k, v := range in.Params {
		pv := ParamValue{}
		if s, ok := v.(string); ok {
			t, err := compileTemplate(path+".params."+k, s, lex)
			if err != nil {
				return err
			}
			if t != nil {
				pv.Template = t
			} else {
				pv.Const = v
			}
		} else {
			pv.Const = v
		}
		if n.CallParams == nil {
			n.CallParams = map[string]ParamValue{}
		}
		n.CallParams[k] = pv
	}
	n.MaxDepth = in.MaxDepth
	return nil
}

// resolveBody expands a container body into graph nodes. Each body node
// gets a stable path (parent + ".body[i]"), and its local id joins the
// child scope before the node resolves, so a container's own until can
// address its collect and later siblings see earlier local ids — but a
// sibling never sees a later id (§5.3).
//
// itemName is the fan-out binding the body reads per item, and numbers
// records whether this container's body iterates at all, which is where
// `index` gets its scope: fan-out and loop bodies say true, an aggregate
// body says false, and a container's own fields are compiled in the outer
// scope and so never see either.
func resolveBody(body spec.Body, parentPath string, outer *lexscope, topIDs map[string]bool, defaults *spec.Defaults, itemName string, numbers bool) ([]*Node, error) {
	if len(body) == 0 {
		return nil, nil
	}
	child := outer.child()
	if itemName != "" {
		child.items = append(child.items, itemName)
	}
	child.indexed = child.indexed || numbers
	local := map[string]bool{}
	nodes := make([]*Node, 0, len(body))
	for i := range body {
		bn := &body[i]
		bpath := fmt.Sprintf("%s.body[%d]", parentPath, i)
		if bn.ID != "" {
			if topIDs[bn.ID] {
				return nil, resolveErrf(bpath+".id", "body-local id %q shadows a top-level step id", bn.ID)
			}
			if local[bn.ID] {
				return nil, resolveErrf(bpath+".id", "duplicate body-local id %q", bn.ID)
			}
			local[bn.ID] = true
			child.steps[bn.ID] = &stepInfo{id: bn.ID, kind: bn.Kind, hasCollect: bn.Collect != ""}
		}
		n, err := resolveNode(bn, bpath, child, topIDs, defaults)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}
