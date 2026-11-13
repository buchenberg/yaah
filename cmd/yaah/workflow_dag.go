package yaah

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// workflow_dag.go turns the resolved graph into the picture `yaah workflow
// explain` prints: the node list, the step references, and the Mermaid
// rendering. This is presentation of structure the graph already states —
// containment is Node.Body, ordering is Needs, branching is Cases and the
// gate dispositions — and it decides nothing a validator owns.

// workflowWalk is the graph flattened in document order, with the two
// lookups a picture needs: a stable short id per node (assigned in walk
// order, so one graph always yields one diagram) and a step-id table for
// resolving the references the graph stores as names.
type workflowWalk struct {
	order []*ir.Node
	idOf  map[*ir.Node]string
	byID  map[string]*ir.Node
}

func (a *workflowAnalysis) walk() workflowWalk {
	w := workflowWalk{idOf: map[*ir.Node]string{}, byID: map[string]*ir.Node{}}
	if a.graph == nil {
		return w
	}
	// Top-level ids are registered first and are unique by construction, so
	// a body-local id can never shadow a step the whole document can name.
	for _, n := range a.graph.Steps {
		a.indexID(w, n)
	}
	var visit func(nodes []*ir.Node)
	visit = func(nodes []*ir.Node) {
		for _, n := range nodes {
			if n == nil || n.Path == "" {
				continue
			}
			w.order = append(w.order, n)
			w.idOf[n] = fmt.Sprintf("n%d", len(w.idOf))
			a.indexID(w, n)
			visit(n.Body)
		}
	}
	visit(a.graph.Steps)
	return w
}

func (a *workflowAnalysis) indexID(w workflowWalk, n *ir.Node) {
	if n == nil || n.ID == "" {
		return
	}
	if _, seen := w.byID[n.ID]; !seen {
		w.byID[n.ID] = n
	}
}

// workflowView is everything explain needs that is derived from the graph
// rather than from the validator summaries.
type workflowView struct {
	graph     graphView
	nodeViews []nodeView
	edges     []edgeView
	mermaid   string
	fanout    fanoutView
}

func (a *workflowAnalysis) workflowView() workflowView {
	w := a.walk()
	v := workflowView{
		graph:     graphView{Nodes: len(w.order), Unreachable: a.authority.Unreachable},
		nodeViews: make([]nodeView, 0, len(w.order)),
		edges:     []edgeView{},
	}
	if a.graph != nil {
		v.graph.Steps = len(a.graph.Steps)
	}
	seen := map[string]bool{}
	for _, n := range w.order {
		v.nodeViews = append(v.nodeViews, a.nodeView(n))
		for _, e := range a.edges(n, w) {
			key := e.From + "\x00" + e.To + "\x00" + e.Via
			if seen[key] {
				continue
			}
			seen[key] = true
			v.edges = append(v.edges, e)
		}
	}
	v.fanout = a.fanoutView(w)
	v.mermaid = a.mermaid(w, v.edges)
	return v
}

// nodeView renders one node's authority and repetition declarations. The
// world and network columns show the value that will actually apply: an
// unset one inherits the host's, and the Inherited flag plus a trailing *
// in the text line says so rather than passing a host default off as the
// author's choice.
func (a *workflowAnalysis) nodeView(n *ir.Node) nodeView {
	v := nodeView{
		Path:   n.Path,
		ID:     n.ID,
		Kind:   string(n.Kind),
		Needs:  n.Needs,
		Role:   n.Role,
		Lane:   string(n.Lane),
		World:  n.World,
		Grants: grantsOf(n.Grants),
		Notes:  a.notes(n),
	}
	if n.Judge != nil {
		v.Role = n.Judge.Role
	}
	if n.Network != "" {
		v.Network = string(n.Network)
	} else {
		v.Network = string(a.env.HostNetwork)
		v.Inherited = n.Kind == ir.KindDelegate
	}
	if v.World == "" {
		v.World = a.env.DefaultWorld
		v.Inherited = v.World != ""
	}
	v.ProducesProposal = n.Lane == ir.LaneProposal || (n.Output != nil && n.Output.Type == ir.OutputProposal)
	return v
}

func grantsOf(grants []ir.Grant) []grantView {
	if len(grants) == 0 {
		return nil
	}
	out := make([]grantView, 0, len(grants))
	for _, g := range grants {
		out = append(out, grantView{Path: g.Path, Access: string(g.Access)})
	}
	return out
}

// notes lists the per-kind declarations that decide what a run does with
// this node: bounds, verbs, dispositions, and the settlement policy with
// its default marked. An empty SettlePolicy is the runtime default
// path_disjoint and resolve does not materialize it, so the display says
// which of the two it is looking at.
func (a *workflowAnalysis) notes(n *ir.Node) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if n.Output != nil {
		if n.Output.Name != "" {
			add("output=%s", n.Output.Name)
		}
		if n.Output.Type != "" {
			add("type=%s", n.Output.Type)
		}
	}
	if n.TimeoutSeconds != nil {
		add("timeout=%ds", *n.TimeoutSeconds)
	}
	if n.Retry != nil && n.Retry.MaxAttempts != nil {
		add("retries=%d", *n.Retry.MaxAttempts)
	}
	if n.MaxTurns != nil {
		add("max_turns=%d", *n.MaxTurns)
	}
	switch n.Kind {
	case ir.KindFanout:
		if n.MaxParallel != nil {
			add("max_parallel=%d", *n.MaxParallel)
		} else {
			add("max_parallel=UNDECLARED")
		}
		add("over=%s", overText(n.Over))
		if n.As != "" {
			add("as=%s", n.As)
		}
		if n.Collect != "" {
			add("collect=%s", n.Collect)
		}
		if n.Join != "" {
			add("join=%s", n.Join)
		}
	case ir.KindLoop:
		if n.MaxIterations != nil {
			add("max_iterations=%d", *n.MaxIterations)
		} else {
			add("max_iterations=UNDECLARED")
		}
		if n.Collect != "" {
			add("collect=%s", n.Collect)
		}
	case ir.KindSettle:
		if n.Action != "" {
			add("action=%s", n.Action)
		}
		policy := string(n.SettlePolicy)
		if policy == "" {
			policy = string(ir.SettlePathDisjoint) + "(default)"
		}
		add("policy=%s", policy)
		if n.OnConflict != "" {
			add("on_conflict=%s", n.OnConflict)
		}
		if n.OnEmpty != "" {
			add("on_empty=%s", n.OnEmpty)
		}
	case ir.KindSelect:
		if n.Policy != "" {
			add("policy=%s", n.Policy)
		}
		if n.Candidates != nil {
			add("candidates=%s", sourceText(n.Candidates))
		}
	case ir.KindGate:
		if n.Assert != nil {
			add("assert=%s", n.Assert.Name)
		}
	case ir.KindBranch:
		if n.On != nil {
			add("on=%s", sourceText(n.On))
		}
	case ir.KindCall:
		if n.Workflow != "" {
			add("call=%s", n.Workflow)
		}
		if n.MaxDepth != nil {
			add("max_depth=%d", *n.MaxDepth)
		}
	case ir.KindEmit:
		if n.ArtifactPath != "" {
			add("path=%s", n.ArtifactPath)
		}
	case ir.KindDelegate:
		if n.ContinueFrom != nil {
			add("continue_from=%s", templateText(n.ContinueFrom))
		}
		if n.ForkFrom != nil {
			add("fork_from=%s", templateText(n.ForkFrom))
		}
		if n.SeedFrom != nil {
			add("seed_from=%s", templateText(n.SeedFrom))
		}
	}
	for _, c := range n.Cases {
		add("case %s->%s", workflowDisplayValue(c.Value), c.Then)
	}
	if n.Default != "" {
		add("default->%s", n.Default)
	}
	if n.OnPass != "" {
		add("on_pass->%s", n.OnPass)
	}
	if n.OnFail != "" {
		add("on_fail->%s", n.OnFail)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func overText(o *ir.Over) string {
	switch {
	case o == nil:
		return "undeclared"
	case o.Literals != nil:
		return fmt.Sprintf("%d literal items", len(o.Literals))
	case o.Selector != nil:
		return sourceText(o.Selector)
	default:
		return "undeclared"
	}
}

func sourceText(s *ir.Selector) string {
	if s == nil || s.From == nil {
		return "undeclared"
	}
	text := s.From.String()
	if s.Predicate.Op != ir.PredNone {
		text += fmt.Sprintf(" %s", s.Predicate.Op)
	}
	if s.Predicate.Field != "" {
		text += " on " + s.Predicate.Field
	}
	return text
}

// templateText returns the author-written template source. ir keeps the raw
// form on the compiled template, which is exactly what a report should
// quote back; Segments is the fallback for a graph built outside the
// resolver.
func templateText(t *ir.Template) string {
	if t == nil {
		return ""
	}
	if t.Raw != "" {
		return t.Raw
	}
	var b strings.Builder
	for _, s := range t.Segments {
		if s.Ref == nil {
			b.WriteString(s.Literal)
		}
	}
	return b.String()
}

// edges lists every step reference one node makes. A reference naming no
// step in the graph is skipped rather than drawn to a phantom: WF001/WF002
// already reports it, and a diagram of a name that resolves to nothing
// would claim a structure the document does not have.
func (a *workflowAnalysis) edges(n *ir.Node, w workflowWalk) []edgeView {
	var out []edgeView
	add := func(via string, ref string) {
		to, ok := w.byID[ref]
		if !ok || to.Path == n.Path {
			return
		}
		out = append(out, edgeView{From: n.Path, To: to.Path, Via: via})
	}
	for _, need := range n.Needs {
		add("needs", need)
	}
	for _, child := range n.Body {
		if child != nil {
			out = append(out, edgeView{From: n.Path, To: child.Path, Via: "body"})
		}
	}
	for _, c := range n.Cases {
		add("case", c.Then)
	}
	add("default", n.Default)
	add("on_pass", n.OnPass)
	add("on_fail", n.OnFail)
	return out
}

// fanoutView reports the widest declared concurrency in the document, which
// is the number the acceptance criterion calls max fan-out. A fan-out that
// declares no max_parallel is listed with its items but contributes no
// width: WF034 has already said that repetition is unbounded, and inventing
// a number here would contradict the finding.
func (a *workflowAnalysis) fanoutView(w workflowWalk) fanoutView {
	v := fanoutView{MaxNodes: []string{}, Containers: []fanoutInstance{}}
	for _, n := range w.order {
		if n.Kind != ir.KindFanout {
			continue
		}
		inst := fanoutInstance{Path: n.Path, MaxParallel: n.MaxParallel, Items: overText(n.Over)}
		if n.Over != nil && n.Over.Literals != nil {
			inst.Literal = len(n.Over.Literals)
		} else if n.Over != nil && n.Over.Selector != nil {
			inst.RunTime = true
		}
		v.Containers = append(v.Containers, inst)
		if n.MaxParallel == nil {
			continue
		}
		switch {
		case *n.MaxParallel > v.MaxItems:
			v.MaxItems = *n.MaxParallel
			v.MaxNodes = []string{n.Path}
		case *n.MaxParallel == v.MaxItems && v.MaxItems > 0:
			v.MaxNodes = append(v.MaxNodes, n.Path)
		}
	}
	sort.Strings(v.MaxNodes)
	return v
}

// mermaid renders the graph as a flowchart. Containers become subgraphs, so
// the body/edge list stays complete for --json while the picture keeps
// containment readable; the body edges are therefore not drawn twice.
func (a *workflowAnalysis) mermaid(w workflowWalk, edges []edgeView) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	if a.graph == nil {
		b.WriteString("  %% the document never resolved, so there is no graph to draw\n")
		return b.String()
	}
	writeMermaidNodes(&b, w, a.graph.Steps, 1)
	ind := strings.Repeat("  ", 1)
	for _, e := range edges {
		if e.Via == "body" {
			continue
		}
		from, to := w.idOf[a.nodeAt(w, e.From)], w.idOf[a.nodeAt(w, e.To)]
		if from == "" || to == "" {
			continue
		}
		fmt.Fprintf(&b, "%s%s -->|%s| %s\n", ind, from, e.Via, to)
	}
	return b.String()
}

func (a *workflowAnalysis) nodeAt(w workflowWalk, path string) *ir.Node {
	for _, n := range w.order {
		if n.Path == path {
			return n
		}
	}
	return nil
}

func writeMermaidNodes(b *strings.Builder, w workflowWalk, nodes []*ir.Node, depth int) {
	ind := strings.Repeat("  ", depth)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		id := w.idOf[n]
		label := fmt.Sprintf("%s (%s)", n.Path, mermaidKindLabel(n))
		if len(n.Body) > 0 {
			fmt.Fprintf(b, "%ssubgraph %s[\"%s\"]\n", ind, id, label)
			writeMermaidNodes(b, w, n.Body, depth+1)
			fmt.Fprintf(b, "%send\n", ind)
			continue
		}
		fmt.Fprintf(b, "%s%s[\"%s\"]\n", ind, id, label)
	}
}

func mermaidKindLabel(n *ir.Node) string {
	switch n.Kind {
	case ir.KindDelegate:
		if n.Role != "" {
			return "delegate " + n.Role
		}
		return "delegate"
	case ir.KindGate, ir.KindSelect:
		if n.Judge != nil && n.Judge.Role != "" {
			return string(n.Kind) + " " + n.Judge.Role
		}
		return string(n.Kind)
	default:
		return string(n.Kind)
	}
}

// line is the text report's one-node line: the authority columns first,
// then the per-kind notes.
func (v nodeView) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s %-9s", v.Path, v.Kind)
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, " %s=%s", name, value)
		}
	}
	field("role", v.Role)
	field("lane", v.Lane)
	world := v.World
	if world != "" && v.Inherited {
		world += "*"
	}
	field("world", world)
	network := v.Network
	if network != "" && v.Inherited {
		network += "*"
	}
	field("network", network)
	if len(v.Grants) > 0 {
		parts := make([]string, 0, len(v.Grants))
		for _, g := range v.Grants {
			parts = append(parts, g.Path+":"+g.Access)
		}
		field("grants", strings.Join(parts, ","))
	}
	if len(v.Notes) > 0 {
		fmt.Fprintf(&b, " %s", strings.Join(v.Notes, " "))
	}
	if len(v.Needs) > 0 {
		fmt.Fprintf(&b, " needs=%s", strings.Join(v.Needs, ","))
	}
	return b.String()
}

// ----------------------------------------------------------------- helpers

func workflowDisplayValue(v any) string {
	if v == nil {
		return "-"
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

func secondsText(n int) string {
	if n <= 0 {
		return "0s"
	}
	switch {
	case n >= 3600 && n%3600 == 0:
		return fmt.Sprintf("%ds (%dh)", n, n/3600)
	case n >= 60 && n%60 == 0:
		return fmt.Sprintf("%ds (%dm)", n, n/60)
	default:
		return fmt.Sprintf("%ds", n)
	}
}

func yesNo(b bool) string {
	if b {
		return "loaded"
	}
	return "not loaded (WF030 name checks declined)"
}

func orDefault(s string) string {
	if s == "" {
		return "not configured"
	}
	return s
}
