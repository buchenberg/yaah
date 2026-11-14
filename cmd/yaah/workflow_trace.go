package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/trace"
	"github.com/spf13/cobra"
)

// workflow_trace.go is `yaah workflow explain --from-trace <run_id>`
// (bead yaah-0u0v.17): the read side of the trace projection exec writes into
// the run's durable record.
//
// Nothing here re-runs the workflow, re-derives a node's disposition, or reads
// a model transcript. The record is the whole input: the reducer already folded
// every event into node states and appended one typed record per event, so this
// file JOINs what the run recorded and states what it means. The only additional
// input is the document, and only when the caller supplies one — a spec in hand
// lets the surface name divergences between the plan the document states and the
// run the record describes.
//
// The command deliberately reuses `explain`'s flags and its --json convention
// (one object on stdout, lists never null) rather than introducing a second
// reporting dialect; the report kind differs because the subject does.

const traceJSONKind = "yaah.workflow.trace.v1"

// Divergence kinds. They are a closed vocabulary for the same reason the rule
// codes are: a reader (or a gate) matches on the word, so the word is stable.
const (
	// divSpecHash: the document in hand is not the document the run recorded.
	divSpecHash = "spec_hash"
	// divSpecNodeMissing: the document declares a node the record never saw.
	divSpecNodeMissing = "node_absent_from_trace"
	// divNodeAbsentFromSpec: the record holds a node the document does not
	// declare, which after a spec edit is the shape of a removed step.
	divNodeAbsentFromSpec = "node_absent_from_spec"
	// divNodeNotSucceeded: a node ended skipped, failed, or cancelled.
	divNodeNotSucceeded = "node_not_succeeded"
	// divNodeUnfinished: a node never terminated at all.
	divNodeUnfinished = "node_unfinished"
	// divProposalOpen: a proposal the run sealed had no disposition.
	divProposalOpen = "proposal_left_open"
)

// workflowExplainOptions is the flag set `explain` takes: the shared analysis
// flags plus the two the trace reader needs.
type workflowExplainOptions struct {
	workflowOptions
	fromTrace string
	statePath string
}

// workflowTraceReport is the trace.v1 report: what the run recorded, rendered as
// the DAG it actually walked, its outcomes, its proposals and their
// dispositions, and — when a document is in hand — where the run diverged from
// that document. Every list is non-nil so it serializes as [] rather than null.
type workflowTraceReport struct {
	Kind      string `json:"kind"`
	RunID     string `json:"run_id"`
	GraphName string `json:"graph_name,omitempty"`
	SpecPath  string `json:"spec_path,omitempty"`
	SpecHash  string `json:"spec_hash,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	// Dispatches is the run's own dispatch tally, recorded by the reducer.
	Dispatches int `json:"dispatches"`

	Nodes       []workflowTraceNodeView       `json:"nodes"`
	Records     []trace.Record                `json:"records"`
	Proposals   []workflowTraceProposalView   `json:"proposals"`
	Settlements []workflowTraceSettlementView `json:"settlements"`
	Divergences []workflowDivergenceView      `json:"divergences"`
	Warnings    []string                      `json:"warnings"`
}

type workflowTraceNodeView struct {
	Path     string   `json:"path"`
	ID       string   `json:"id,omitempty"`
	Kind     string   `json:"kind"`
	State    string   `json:"state"`
	Reason   string   `json:"reason,omitempty"`
	ScopeID  string   `json:"scope_id,omitempty"`
	Needs    []string `json:"needs"`
	Attempts int      `json:"attempts"`
	Outputs  []string `json:"outputs"`
	// ProposalID is the handle a proposal-lane node sealed.
	ProposalID string `json:"proposal_id,omitempty"`
	// Settled is how many dispositions the node recorded as a settle node.
	Settled int `json:"settled"`
}

// workflowTraceProposalView is one proposal the run's own ledger knows about:
// what sealed it and what state the ledger gives it (open, claimed, settled).
type workflowTraceProposalView struct {
	ProposalID string `json:"proposal_id"`
	Producer   string `json:"producer,omitempty"`
	State      string `json:"state"`
}

type workflowTraceSettlementView struct {
	NodePath   string   `json:"node_path"`
	ProposalID string   `json:"proposal_id"`
	Producer   string   `json:"producer,omitempty"`
	Action     string   `json:"action,omitempty"`
	Status     string   `json:"status,omitempty"`
	Error      string   `json:"error,omitempty"`
	LeftOpen   bool     `json:"left_open,omitempty"`
	Conflicts  []string `json:"conflicts"`
}

type workflowDivergenceView struct {
	Kind     string `json:"kind"`
	NodePath string `json:"node_path,omitempty"`
	Detail   string `json:"detail"`
}

// runWorkflowExplainFromTrace renders one run's trace. args is the optional spec
// path: a caller that names one gets divergence analysis against that document,
// and a caller that names none is explained from the record alone, which is the
// "no transcript, no document" mode the bead requires to work.
func runWorkflowExplainFromTrace(cmd *cobra.Command, args []string, opts workflowExplainOptions) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := workflowResumeStore(opts.statePath)
	if err != nil {
		return fmt.Errorf("explain --from-trace %s: %w", opts.fromTrace, err)
	}
	cp, err := st.LoadCheckpoint(ctx, opts.fromTrace)
	if err != nil {
		return err
	}

	specPath := cp.SpecPath
	if len(args) == 1 {
		specPath = args[0]
	}
	var (
		analysis *workflowAnalysis
		warnings []string
	)
	if specPath != "" {
		a, aerr := analyzeWorkflow(specPath, opts.params, workflowEnv(), false)
		switch {
		case aerr != nil:
			warnings = append(warnings, fmt.Sprintf("%s could not be read (%v); divergence analysis reports the run's own record only", specPath, aerr))
		case a == nil || a.graph == nil:
			warnings = append(warnings, fmt.Sprintf("%s did not resolve into a graph; divergence analysis reports the run's own record only", specPath))
		default:
			analysis = a
		}
	} else {
		warnings = append(warnings, "the run records no spec path; pass a spec argument to compare the run against its document")
	}

	rep := traceReportOf(cp, specPath, analysis)
	rep.Warnings = append(rep.Warnings, warnings...)

	out := cmd.OutOrStdout()
	if opts.asJSON {
		if err := writeWorkflowTraceJSON(out, rep); err != nil {
			return err
		}
	} else {
		renderWorkflowTrace(out, rep)
	}
	if rep.Status != string(exec.RunSucceeded) || len(rep.Divergences) > 0 {
		reason := rep.Reason
		if reason == "" {
			reason = fmt.Sprintf("%d divergence(s) from the document", len(rep.Divergences))
		}
		return fmt.Errorf("run %s: status %s — %s", rep.RunID, rep.Status, reason)
	}
	return nil
}

// traceReportOf reconstructs the report from the record. It decides nothing
// about the run: every state, reason, disposition, and ledger entry below is
// quoted from the checkpoint.
func traceReportOf(cp exec.Checkpoint, specPath string, a *workflowAnalysis) workflowTraceReport {
	rep := workflowTraceReport{
		Kind:        traceJSONKind,
		RunID:       cp.RunID,
		GraphName:   cp.GraphName,
		SpecPath:    cp.SpecPath,
		SpecHash:    cp.SpecHash,
		Status:      string(cp.Status),
		Reason:      cp.Reason,
		Dispatches:  cp.Dispatches,
		Nodes:       []workflowTraceNodeView{},
		Records:     append([]trace.Record{}, cp.Trace...),
		Proposals:   []workflowTraceProposalView{},
		Settlements: []workflowTraceSettlementView{},
		Divergences: []workflowDivergenceView{},
		Warnings:    []string{},
	}
	if specPath != "" {
		rep.SpecPath = specPath
	}
	if rep.Records == nil {
		rep.Records = []trace.Record{}
	}
	scope := make(map[string]string, len(cp.Instances))
	for _, in := range cp.Instances {
		scope[in.Path] = in.Owner
	}
	nodes := append([]*exec.NodeState(nil), cp.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	for _, n := range nodes {
		if n == nil {
			continue
		}
		v := workflowTraceNodeView{
			Path:     n.Path,
			ID:       n.ID,
			Kind:     string(n.Kind),
			State:    string(n.State),
			Reason:   n.Reason,
			ScopeID:  scope[n.Path],
			Needs:    append([]string{}, n.Needs...),
			Attempts: len(n.Attempts),
			Outputs:  []string{},
			Settled:  len(n.Settled),
		}
		for _, o := range n.Outputs {
			v.Outputs = append(v.Outputs, o.Name)
		}
		if n.Proposal != nil {
			v.ProposalID = n.Proposal.ProposalID
		}
		rep.Nodes = append(rep.Nodes, v)
		for _, st := range n.Settled {
			rep.Settlements = append(rep.Settlements, workflowTraceSettlementView{
				NodePath:   n.Path,
				ProposalID: st.ProposalID,
				Producer:   st.Producer,
				Action:     st.Action,
				Status:     st.Status,
				Error:      st.Error,
				LeftOpen:   st.LeftOpen,
				Conflicts:  append([]string{}, st.Conflicts...),
			})
		}
	}
	// The ledger is the reducer's own proposal bookkeeping, so the proposal
	// list is a quotation rather than a re-derivation from the node states.
	appendLedger := func(ids map[string]string, state string) {
		for id, owner := range ids {
			rep.Proposals = append(rep.Proposals, workflowTraceProposalView{ProposalID: id, Producer: owner, State: state})
		}
	}
	appendLedger(cp.Ledger.Open, "open")
	appendLedger(cp.Ledger.Claims, "claimed")
	appendLedger(cp.Ledger.Settled, "settled")
	sort.Slice(rep.Proposals, func(i, j int) bool { return rep.Proposals[i].ProposalID < rep.Proposals[j].ProposalID })
	rep.Divergences = traceDivergences(cp, specPath, a, nodes)
	return rep
}

// traceDivergences names where the run the record describes differs from the
// document it was started from. Each kind is a fact the record and the document
// answer differently — never a judgement about whether the run was right.
func traceDivergences(cp exec.Checkpoint, specPath string, a *workflowAnalysis, nodes []*exec.NodeState) []workflowDivergenceView {
	out := []workflowDivergenceView{}
	if a != nil && a.graph != nil {
		// The document in hand must be the document the run recorded: the hash
		// is the content address of the canonical IR, so a mismatch means the
		// spec was edited after the run started and every node comparison below
		// is made against a different plan.
		if specPath != "" && cp.SpecHash != "" {
			if h, err := workflowSpecHash(specPath); err == nil && h != cp.SpecHash {
				out = append(out, workflowDivergenceView{
					Kind: divSpecHash,
					Detail: fmt.Sprintf("the run recorded spec %s but %s hashes to %s — the document changed after the run started",
						cp.SpecHash, specPath, h),
				})
			}
		}
		declared := map[string]bool{}
		containers := map[string]bool{}
		var declWalk func([]*ir.Node)
		declWalk = func(ns []*ir.Node) {
			for _, n := range ns {
				if n == nil {
					continue
				}
				declared[n.Path] = true
				if len(n.Body) > 0 {
					containers[n.Path] = true
				}
				declWalk(n.Body)
			}
		}
		declWalk(a.graph.Steps)
		instances := make(map[string]bool, len(cp.Instances))
		for _, in := range cp.Instances {
			instances[in.Path] = true
		}
		recorded := map[string]bool{}
		for _, n := range nodes {
			if n == nil {
				continue
			}
			recorded[n.Path] = true
			if !declared[n.Path] && !instances[n.Path] {
				out = append(out, workflowDivergenceView{
					Kind:     divNodeAbsentFromSpec,
					NodePath: n.Path,
					Detail:   fmt.Sprintf("the run recorded %s (%s), which %s does not declare", n.Path, n.State, specPath),
				})
			}
		}
		// A declared node with no record is a step the run never reached. A
		// container is exempt: its body instances are the records that exist.
		var missWalk func([]*ir.Node)
		missWalk = func(ns []*ir.Node) {
			for _, n := range ns {
				if n == nil {
					continue
				}
				if len(n.Body) > 0 {
					missWalk(n.Body)
					continue
				}
				if !recorded[n.Path] {
					out = append(out, workflowDivergenceView{
						Kind:     divSpecNodeMissing,
						NodePath: n.Path,
						Detail:   fmt.Sprintf("%s declares %s, which the run never recorded a state for", specPath, n.Path),
					})
				}
			}
		}
		missWalk(a.graph.Steps)
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		switch n.State {
		case exec.StateFailed, exec.StateSkipped, exec.StateCancelled:
			detail := fmt.Sprintf("%s ended %s", n.Path, n.State)
			if n.Reason != "" {
				detail += ": " + oneLine(n.Reason)
			}
			out = append(out, workflowDivergenceView{Kind: divNodeNotSucceeded, NodePath: n.Path, Detail: detail})
		case exec.StatePending, exec.StateReady, exec.StateRunning:
			out = append(out, workflowDivergenceView{
				Kind:     divNodeUnfinished,
				NodePath: n.Path,
				Detail:   fmt.Sprintf("%s is %s in the last recorded state; the run never terminated it", n.Path, n.State),
			})
		}
	}
	if len(cp.Ledger.Open) > 0 {
		ids := make([]string, 0, len(cp.Ledger.Open))
		for id := range cp.Ledger.Open {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out = append(out, workflowDivergenceView{
			Kind: divProposalOpen,
			Detail: fmt.Sprintf("%d proposal(s) had no disposition in the last recorded state: %s",
				len(ids), strings.Join(ids, ", ")),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].NodePath != out[j].NodePath {
			return out[i].NodePath < out[j].NodePath
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

func writeWorkflowTraceJSON(out io.Writer, rep workflowTraceReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// renderWorkflowTrace prints the record as the four things the bead asks a run
// to be explainable by: the DAG it walked, its outcomes, its proposals and their
// settlements, and the divergences from the document when one is in hand.
func renderWorkflowTrace(out io.Writer, rep workflowTraceReport) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "run\t%s\n", rep.RunID)
	if rep.GraphName != "" {
		fmt.Fprintf(w, "graph\t%s\n", rep.GraphName)
	}
	if rep.SpecPath != "" {
		fmt.Fprintf(w, "spec\t%s\n", rep.SpecPath)
	}
	if rep.SpecHash != "" {
		fmt.Fprintf(w, "spec hash\t%s\n", rep.SpecHash)
	}
	fmt.Fprintf(w, "status\t%s\n", rep.Status)
	if rep.Reason != "" {
		fmt.Fprintf(w, "reason\t%s\n", oneLine(rep.Reason))
	}
	fmt.Fprintf(w, "dispatches\t%d\n", rep.Dispatches)
	fmt.Fprintf(w, "records\t%d\n", len(rep.Records))
	w.Flush()

	fmt.Fprintf(out, "\ndag (mermaid)\n")
	fmt.Fprint(out, traceMermaid(rep.Nodes))

	fmt.Fprintf(out, "\nnodes\n")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, n := range rep.Nodes {
		line := fmt.Sprintf("  %s\t%s\t%s\tattempts=%d", n.Path, n.Kind, n.State, n.Attempts)
		if n.ScopeID != "" {
			line += "\tscope=" + n.ScopeID
		}
		if n.ProposalID != "" {
			line += "\tproposal=" + n.ProposalID
		}
		fmt.Fprintln(w, line)
	}
	w.Flush()

	fmt.Fprintf(out, "\ntimeline\n")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, rec := range rep.Records {
		fmt.Fprintf(w, "  %04d\t%s\t%s\n", rec.Seq, rec.Kind, traceRecordDetail(rec))
	}
	w.Flush()

	if len(rep.Proposals) > 0 {
		fmt.Fprintf(out, "\nproposals\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, p := range rep.Proposals {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", p.ProposalID, p.State, p.Producer)
		}
		w.Flush()
	}
	if len(rep.Settlements) > 0 {
		fmt.Fprintf(out, "\nsettlements\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, s := range rep.Settlements {
			line := fmt.Sprintf("  %s\t%s\t%s\t%s", s.NodePath, s.ProposalID, s.Action, s.Status)
			if s.LeftOpen {
				line += "\tleft_open"
			}
			if len(s.Conflicts) > 0 {
				line += "\tconflicts=" + strings.Join(s.Conflicts, ",")
			}
			fmt.Fprintln(w, line)
		}
		w.Flush()
	}
	if len(rep.Divergences) > 0 {
		fmt.Fprintf(out, "\ndivergences\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, d := range rep.Divergences {
			fmt.Fprintf(w, "  %s\t%s\n", d.Kind, d.Detail)
		}
		w.Flush()
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(out, "\nwarnings\n")
		for _, warn := range rep.Warnings {
			fmt.Fprintf(out, "  %s\n", warn)
		}
	}
}

// traceRecordDetail renders one record's own facts as a compact tail. It quotes
// the record and interprets nothing, so two records of different kinds never
// look the same by accident.
func traceRecordDetail(rec trace.Record) string {
	var parts []string
	add := func(v string) {
		if v != "" {
			parts = append(parts, v)
		}
	}
	add(rec.NodePath)
	if rec.ScopeID != "" {
		add("scope=" + rec.ScopeID)
	}
	add(rec.Status)
	if rec.Attempt > 0 {
		add(fmt.Sprintf("attempt=%d", rec.Attempt))
	}
	if rec.Lane != "" {
		add("lane=" + rec.Lane)
	}
	if rec.ProposalID != "" {
		add("proposal=" + rec.ProposalID)
	}
	if rec.Action != "" {
		add("action=" + rec.Action)
	}
	if rec.Reason != "" {
		add("reason=" + oneLine(rec.Reason))
	}
	if rec.OutputDigest != "" {
		add("digest=" + rec.OutputDigest[:12])
	}
	return strings.Join(parts, " ")
}

// traceMermaid draws the graph the record actually walked, with each node's
// recorded state in its label. Edges come from the recorded needs, so a node the
// spec removed is still drawn where the run put it.
func traceMermaid(nodes []workflowTraceNodeView) string {
	var b strings.Builder
	b.WriteString("  flowchart TD\n")
	ids := make(map[string]string, len(nodes))
	for i, n := range nodes {
		ids[n.Path] = fmt.Sprintf("n%d", i)
	}
	for _, n := range nodes {
		label := mermaidEscape(fmt.Sprintf("%s (%s) %s", n.Path, n.Kind, n.State))
		fmt.Fprintf(&b, "    %s[\"%s\"]\n", ids[n.Path], label)
	}
	for _, n := range nodes {
		for _, need := range n.Needs {
			from, ok := ids[need]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "    %s --> %s\n", from, ids[n.Path])
		}
	}
	return b.String()
}
