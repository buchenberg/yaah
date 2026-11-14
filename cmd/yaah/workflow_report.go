package yaah

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/validate"
)

// workflow_report.go renders what the validator families already computed.
// It contains no analysis: every field it prints is read off ir.Graph,
// validate.ExplainResult, or validate.SettlementProof, and the only
// computation here is turning containment and step references into the
// edges a graph picture needs — a presentation of structure the graph
// already states.

// The --json shapes are versioned by the kind field so a consumer can pin
// them. Both are written with HTML escaping off, sorted keys where a Go map
// backs a list, and empty slices rather than null, so one document always
// produces the same bytes.
const (
	validateJSONKind = "yaah.workflow.validate.v1"
	explainJSONKind  = "yaah.workflow.explain.v1"
)

type validateReport struct {
	Kind     string                `json:"kind"`
	Spec     string                `json:"spec"`
	Name     string                `json:"name,omitempty"`
	Status   string                `json:"status"`
	OK       bool                  `json:"ok"`
	Strict   bool                  `json:"strict"`
	Errors   []validate.Diagnostic `json:"errors"`
	Warnings []validate.Diagnostic `json:"warnings"`
}

type explainReport struct {
	Kind       string                `json:"kind"`
	Spec       string                `json:"spec"`
	Name       string                `json:"name,omitempty"`
	Status     string                `json:"status"`
	OK         bool                  `json:"ok"`
	Strict     bool                  `json:"strict"`
	Env        envView               `json:"host"`
	Params     []paramView           `json:"params"`
	Graph      graphView             `json:"graph"`
	Nodes      []nodeView            `json:"nodes"`
	Edges      []edgeView            `json:"edges"`
	Mermaid    string                `json:"mermaid"`
	Fanout     fanoutView            `json:"fanout"`
	Cost       costView              `json:"cost"`
	Authority  authorityView         `json:"authority"`
	Settlement settlementView        `json:"settlement"`
	Errors     []validate.Diagnostic `json:"errors"`
	Warnings   []validate.Diagnostic `json:"warnings"`
}

// envView records which host axes the run was checked against. A zero or
// absent entry means the rule declined that axis, which a reader must be
// able to tell apart from a pass.
type envView struct {
	RolesLoaded         bool     `json:"roles_loaded"`
	Worlds              []string `json:"worlds,omitempty"`
	DefaultWorld        string   `json:"default_world,omitempty"`
	HostNetwork         string   `json:"host_network,omitempty"`
	BrokerHosts         []string `json:"broker_hosts,omitempty"`
	SecondsPerDispatch  int      `json:"seconds_per_dispatch,omitempty"`
	MaxTurnsPerDispatch int      `json:"max_turns_per_dispatch,omitempty"`
}

type paramView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Source   string `json:"source"`
	Value    any    `json:"value,omitempty"`
}

type graphView struct {
	Steps       int `json:"steps"`
	Nodes       int `json:"nodes"`
	Unreachable int `json:"unreachable_nodes"`
}

type nodeView struct {
	Path    string   `json:"path"`
	ID      string   `json:"id,omitempty"`
	Kind    string   `json:"kind"`
	Needs   []string `json:"needs,omitempty"`
	Role    string   `json:"role,omitempty"`
	Lane    string   `json:"lane,omitempty"`
	Network string   `json:"network,omitempty"`
	World   string   `json:"world,omitempty"`
	// WorldInherited and NetworkInherited are separate flags: the two columns
	// inherit independently, and one shared flag let a declared network ride
	// on an inherited world's star (and vice versa).
	WorldInherited   bool        `json:"world_inherited,omitempty"`
	NetworkInherited bool        `json:"network_inherited,omitempty"`
	Grants           []grantView `json:"grants,omitempty"`
	Notes            []string    `json:"notes,omitempty"`
	ProducesProposal bool        `json:"produces_proposal,omitempty"`
}

type grantView struct {
	Path   string `json:"path"`
	Access string `json:"access"`
}

// Edge Via is one of needs, body, case, default, on_pass, on_fail — the six
// step references the vocabulary has. An edge is emitted only when the
// reference names a step the graph actually carries; a dangling name is
// reported by WF002 or WF001, not drawn here.
type edgeView struct {
	From string `json:"from"`
	To   string `json:"to"`
	Via  string `json:"via"`
}

type fanoutView struct {
	MaxItems   int              `json:"max_items"`
	MaxNodes   []string         `json:"max_nodes"`
	Containers []fanoutInstance `json:"containers"`
}

type fanoutInstance struct {
	Path string `json:"path"`
	// Kind is "fanout" or "loop": a fan-out's bound is parallel width, a
	// loop's is sequential iterations — the same survey, different units,
	// and the report must not blur them.
	Kind        string `json:"kind"`
	MaxParallel *int   `json:"max_parallel,omitempty"`
	Items       string `json:"items"`
	// Literal is the statically known item count for a fan-out, or the
	// declared max_iterations for a loop.
	Literal int  `json:"literal_items,omitempty"`
	RunTime bool `json:"run_time_list,omitempty"`
}

type costView struct {
	Dispatches      int      `json:"dispatches"`
	DispatchBound   string   `json:"dispatch_bound"`
	Unbound         []string `json:"unbound,omitempty"`
	Turns           int      `json:"turns,omitempty"`
	TurnsPriced     bool     `json:"turns_priced"`
	WallClock       int      `json:"wall_clock_seconds,omitempty"`
	WallClockPriced bool     `json:"wall_clock_priced"`
	Ceiling         int      `json:"ceiling,omitempty"`
	ExceedsCeiling  bool     `json:"exceeds_ceiling"`
	DeclaredCeiling *int     `json:"declared_ceiling,omitempty"`
	BudgetChecked   bool     `json:"budget_checked"`
}

type authorityView struct {
	Grants       []unionGrant `json:"grants"`
	Artifacts    []string     `json:"artifacts"`
	Hosts        []string     `json:"hosts"`
	NetworkModes []string     `json:"network_modes"`
	OpenNetwork  bool         `json:"open_network"`
	Unreachable  int          `json:"unreachable_nodes"`
}

type unionGrant struct {
	World  string `json:"world,omitempty"`
	Path   string `json:"path"`
	Access string `json:"access"`
}

type settlementView struct {
	Proposals  []proposalView `json:"proposals"`
	OpenAtExit []string       `json:"open_at_exit"`
	Untracked  []string       `json:"producers_without_record,omitempty"`
}

// proposalView carries the proof's own fields. The state word a reader sees
// in the text report is ProposalProof.String()'s to render; the booleans
// here are the data, so the two cannot drift apart in meaning.
type proposalView struct {
	Proposal           string   `json:"proposal"`
	Dispositions       []string `json:"dispositions"`
	Actions            string   `json:"actions"`
	OnceOnEveryPath    bool     `json:"once_on_every_path"`
	SettledOnEveryPath bool     `json:"settled_on_every_path"`
	Stranded           bool     `json:"stranded"`
}

// ------------------------------------------------------------------ shapes

func (a *workflowAnalysis) validateReport() validateReport {
	return validateReport{
		Kind:     validateJSONKind,
		Spec:     a.path,
		Name:     a.name,
		Status:   a.status(),
		OK:       !a.failed(),
		Strict:   a.strict,
		Errors:   a.errors,
		Warnings: a.warnings,
	}
}

func (a *workflowAnalysis) explainReport() explainReport {
	view := a.workflowView()
	rep := explainReport{
		Kind:       explainJSONKind,
		Spec:       a.path,
		Name:       a.name,
		Status:     a.status(),
		OK:         !a.failed(),
		Strict:     a.strict,
		Env:        a.envView(),
		Params:     a.paramViews(),
		Graph:      view.graph,
		Mermaid:    view.mermaid,
		Fanout:     view.fanout,
		Cost:       a.costView(),
		Authority:  a.authorityView(),
		Settlement: a.settlementView(),
		Errors:     a.errors,
		Warnings:   a.warnings,
	}
	rep.Nodes = view.nodeViews
	rep.Edges = view.edges
	return rep
}

func (a *workflowAnalysis) envView() envView {
	return envView{
		RolesLoaded:         len(a.env.Roles) > 0,
		Worlds:              a.env.Worlds,
		DefaultWorld:        a.env.DefaultWorld,
		HostNetwork:         string(a.env.HostNetwork),
		BrokerHosts:         a.env.BrokerHosts,
		SecondsPerDispatch:  a.env.SecondsPerDispatch,
		MaxTurnsPerDispatch: a.env.MaxTurnsPerDispatch,
	}
}

func (a *workflowAnalysis) paramViews() []paramView {
	out := make([]paramView, 0, len(a.params))
	for _, p := range a.params {
		out = append(out, paramView(p))
	}
	return out
}

func (a *workflowAnalysis) costView() costView {
	v := costView{
		Dispatches:      a.cost.Dispatches,
		DispatchBound:   "at most",
		Unbound:         a.cost.Unbound,
		Turns:           a.cost.Turns,
		TurnsPriced:     a.cost.TurnsKnown,
		WallClock:       a.cost.WallClock,
		WallClockPriced: a.cost.WallClockKnown,
		Ceiling:         a.cost.Ceiling,
		ExceedsCeiling:  a.cost.ExceedsCeiling,
	}
	if !a.cost.Complete {
		v.DispatchBound = "at least"
	}
	if a.graph != nil && a.graph.Budgets != nil && a.graph.Budgets.MaxDispatches != nil {
		d := *a.graph.Budgets.MaxDispatches
		v.DeclaredCeiling = &d
	}
	// BudgetChecked mirrors WF033's decline: Ceiling is 0 with
	// ExceedsCeiling false exactly when neither side declared a bound,
	// which is an unchecked axis rather than a passing one.
	v.BudgetChecked = a.cost.Ceiling > 0 || a.cost.ExceedsCeiling
	return v
}

func (a *workflowAnalysis) authorityView() authorityView {
	v := authorityView{
		Grants:       []unionGrant{},
		Artifacts:    a.authority.Artifacts,
		Hosts:        a.authority.Hosts,
		NetworkModes: make([]string, 0, len(a.authority.NetworkModes)),
		OpenNetwork:  a.authority.OpenNetwork,
		Unreachable:  a.authority.Unreachable,
	}
	if v.Artifacts == nil {
		v.Artifacts = []string{}
	}
	if v.Hosts == nil {
		v.Hosts = []string{}
	}
	for _, gr := range a.authority.Grants {
		v.Grants = append(v.Grants, unionGrant{World: gr.World, Path: gr.Path, Access: string(gr.Access)})
	}
	for _, m := range a.authority.NetworkModes {
		v.NetworkModes = append(v.NetworkModes, string(m))
	}
	return v
}

func (a *workflowAnalysis) settlementView() settlementView {
	v := settlementView{Proposals: []proposalView{}, OpenAtExit: []string{}}
	if a.proof != nil {
		for _, p := range a.proof.Proposals {
			// Dispositions keeps the never-null contract: an undisposed token
			// (a producer nothing settles — the WF026 leak case) serializes
			// as [], not null.
			dispositions := p.Dispositions
			if dispositions == nil {
				dispositions = []string{}
			}
			v.Proposals = append(v.Proposals, proposalView{
				Proposal:           p.Proposal,
				Dispositions:       dispositions,
				Actions:            p.Actions,
				OnceOnEveryPath:    p.OnceOnEveryPath,
				SettledOnEveryPath: p.SettledOnEveryPath,
				Stranded:           p.Stranded,
			})
		}
		// OpenAtExit keeps its [] seed when the proof found nothing open —
		// nil from the proof means "no producer can reach exit unsettled",
		// which serializes as []. A null open_at_exit in the report means
		// only "no proof ran" (an unresolved document), never "all settled".
		if a.proof.OpenAtExit != nil {
			v.OpenAtExit = a.proof.OpenAtExit
		}
	}
	// The cross-check the contract asks for: every proposal-producing node
	// should have a disposition record. Listed, not inferred — the proof is
	// the authority on dispositions, so a missing record is reported as
	// missing rather than as a guessed disposition.
	recorded := map[string]bool{}
	for _, p := range v.Proposals {
		recorded[p.Proposal] = true
	}
	for _, n := range a.producingNodes() {
		if !recorded[n.Path] {
			v.Untracked = append(v.Untracked, n.Path)
		}
	}
	return v
}

// producingNodes lists the reachable-by-document-order nodes the document
// itself claims produce a proposal: a dispatch on the proposal lane, or one
// whose declared output type is Proposal. Lane and output type are graph
// fields, so this is a filter over the graph, not a second analysis.
func (a *workflowAnalysis) producingNodes() []*ir.Node {
	var out []*ir.Node
	if a.graph == nil {
		return nil
	}
	for _, n := range a.walk().order {
		if n.Lane == ir.LaneProposal || (n.Output != nil && n.Output.Type == ir.OutputProposal) {
			out = append(out, n)
		}
	}
	return out
}

// ------------------------------------------------------------------ render

func writeWorkflowJSON(out io.Writer, a *workflowAnalysis, explain bool) error {
	var payload any
	if explain {
		payload = a.explainReport()
	} else {
		payload = a.validateReport()
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// renderWorkflowValidate prints the finding list, one per line, prefixed by
// its severity. Each line is validate.Diagnostic's own rendering — code,
// path, message — so the CLI and the package cannot disagree about how a
// finding reads.
func renderWorkflowValidate(out io.Writer, a *workflowAnalysis) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "spec\t%s\n", a.path)
	if a.name != "" {
		fmt.Fprintf(w, "workflow\t%s\n", a.name)
	}
	fmt.Fprintf(w, "status\t%s\n", a.status())
	fmt.Fprintf(w, "errors\t%d\n", len(a.errors))
	fmt.Fprintf(w, "warnings\t%d\n", len(a.warnings))
	if a.graph == nil {
		fmt.Fprintf(w, "note\tthe document never became a graph, so the node-level families (WF002-WF036) could not run\n")
	}
	w.Flush()
	writeFindings(out, a.errors, a.warnings)
	if !a.failed() {
		fmt.Fprintf(out, "\nno findings\n")
	}
}

func writeFindings(out io.Writer, errors, warnings []validate.Diagnostic) {
	line := func(severity string, ds []validate.Diagnostic) {
		for _, d := range ds {
			fmt.Fprintf(out, "  %s  %s\n", severity, d.String())
		}
	}
	line("error", errors)
	line("warning", warnings)
}

// renderWorkflowExplain prints the resolved graph, the two summary
// artifacts, and the findings — the whole report a human needs before
// approving a permission plan, assembled from the validators' own data.
func renderWorkflowExplain(out io.Writer, a *workflowAnalysis) {
	view := a.workflowView()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "spec\t%s\n", a.path)
	if a.name != "" {
		fmt.Fprintf(w, "workflow\t%s\n", a.name)
	}
	fmt.Fprintf(w, "status\t%s\n", a.status())
	fmt.Fprintf(w, "graph\t%d top-level steps, %d nodes, %d unreachable\n",
		view.graph.Steps, view.graph.Nodes, view.graph.Unreachable)
	w.Flush()

	if len(a.params) > 0 {
		fmt.Fprintf(out, "\nparameters\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, p := range a.params {
			req := ""
			if p.Required {
				req = "required"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", p.Name, p.Type, req, p.Source, workflowDisplayValue(p.Value))
		}
		w.Flush()
	}

	fmt.Fprintf(out, "\nnodes\n")
	for _, n := range view.nodeViews {
		fmt.Fprintf(out, "  %s\n", n.line())
	}

	fmt.Fprintf(out, "\ndag (mermaid)\n```mermaid\n%s```\n", view.mermaid)

	fmt.Fprintf(out, "\nfan-out\n  maximum concurrent items\t%d\n", view.fanout.MaxItems)
	if len(view.fanout.MaxNodes) > 0 {
		fmt.Fprintf(out, "  declared by\t%s\n", strings.Join(view.fanout.MaxNodes, ", "))
	}
	for _, c := range view.fanout.Containers {
		bound := "undeclared"
		switch c.Kind {
		case "loop":
			if c.Literal > 0 {
				bound = fmt.Sprintf("max_iterations %d", c.Literal)
			} else {
				bound = "iteration bound undeclared"
			}
		default:
			if c.MaxParallel != nil {
				bound = fmt.Sprintf("max_parallel %d", *c.MaxParallel)
			}
		}
		fmt.Fprintf(out, "  %s\t%s\t%s\n", c.Path, bound, c.Items)
	}

	fmt.Fprintf(out, "\ncost\n")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  dispatches\t%s %d\n", a.costView().DispatchBound, a.cost.Dispatches)
	if len(a.cost.Unbound) > 0 {
		fmt.Fprintf(w, "  not bounded by the document\t%s\n", strings.Join(a.cost.Unbound, ", "))
	}
	switch {
	case a.cost.TurnsKnown:
		fmt.Fprintf(w, "  turns\tat most %d\n", a.cost.Turns)
	default:
		fmt.Fprintf(w, "  turns\tnot priced (agents.subagent.default_max_turns unset)\n")
	}
	switch {
	case a.cost.WallClockKnown:
		fmt.Fprintf(w, "  wall clock\t%s\n", secondsText(a.cost.WallClock))
	default:
		fmt.Fprintf(w, "  wall clock\tnot priced (no timeout on every dispatch and no agents.subagent.default_timeout)\n")
	}
	switch {
	case !a.costView().BudgetChecked:
		fmt.Fprintf(w, "  budget\tunchecked (neither budgets.max_dispatches nor a host ceiling)\n")
	case a.cost.ExceedsCeiling:
		fmt.Fprintf(w, "  budget\tOVER the effective ceiling %d\n", a.cost.Ceiling)
	default:
		fmt.Fprintf(w, "  budget\tfits (effective ceiling %d)\n", a.cost.Ceiling)
	}
	w.Flush()

	fmt.Fprintf(out, "\nauthority union (reachable nodes)\n")
	if a.authority.Unreachable > 0 {
		fmt.Fprintf(out, "  %d node(s) excluded: WF004 says no scheduler can enter them\n", a.authority.Unreachable)
	}
	av := a.authorityView()
	if len(av.Grants) == 0 {
		fmt.Fprintf(out, "  filesystem\tnone declared\n")
	}
	for _, g := range av.Grants {
		world := g.World
		if world == "" {
			world = "(unnamed world)"
		}
		fmt.Fprintf(out, "  filesystem\t%s\t%s\t%s\n", world, g.Path, g.Access)
	}
	if len(av.Artifacts) > 0 {
		fmt.Fprintf(out, "  artifacts\t%s\n", strings.Join(av.Artifacts, ", "))
	}
	if len(av.NetworkModes) > 0 {
		fmt.Fprintf(out, "  network\t%s\n", strings.Join(av.NetworkModes, ", "))
	}
	if av.OpenNetwork {
		fmt.Fprintf(out, "  network\tallow_all: reachable dispatches may address any host\n")
	} else if len(av.Hosts) > 0 {
		fmt.Fprintf(out, "  broker hosts\t%s\n", strings.Join(av.Hosts, ", "))
	}
	fmt.Fprintf(out, "  checked against\troles %s, world %s, network ceiling %s\n",
		yesNo(a.envView().RolesLoaded), orDefault(a.env.DefaultWorld), orDefault(string(a.env.HostNetwork)))

	fmt.Fprintf(out, "\nproposal dispositions\n")
	sv := a.settlementView()
	if a.proof == nil || len(a.proof.Proposals) == 0 {
		fmt.Fprintf(out, "  none: no dispatch in this workflow retains a proposal\n")
	}
	if a.proof != nil {
		for _, p := range a.proof.Proposals {
			fmt.Fprintf(out, "  %s\n", p.String())
		}
	}
	if len(sv.OpenAtExit) > 0 {
		fmt.Fprintf(out, "  open at run exit\t%s\n", strings.Join(sv.OpenAtExit, ", "))
	}
	if len(sv.Untracked) > 0 {
		fmt.Fprintf(out, "  producers with no disposition record\t%s\n", strings.Join(sv.Untracked, ", "))
	}

	writeFindings(out, a.errors, a.warnings)
}
