package yaah

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/host"
	"github.com/spf13/cobra"
)

// workflow_run.go is `yaah workflow run`: validate, resolve, execute, report
// (bead yaah-0u0v.12). The command is host-side and not retained: it calls
// the host door (internal/workflow/host), which dispatches through
// internal/dispatch.Run — never through internal/tools directly.
//
// The execution order is a contract, not a convenience: the document is
// analyzed with the same pipeline `validate` uses BEFORE anything dispatches,
// and a document with error-severity findings is refused with its diagnostics
// and exit 1. --strict promotes warnings to refusal. A run that reached the
// door and failed there reports the scheduler's own verdict (status, reason,
// per-node states) — the two failure classes stay distinguishable.

const runJSONKind = "yaah.workflow.run.v1"

// workflowRunRunner is the seam the run command takes its sub-agent runner
// from. A host session's composition (serve/TUI wiring) sets it; tests inject
// a deterministic runner, which is how the golden end-to-end runs with no
// model calls beyond the injected provider. The standalone default refuses
// honestly rather than half-bootstrapping a composition root: building a
// production runner needs the session's provider, database, workspace, and
// scope machinery, and a run command that silently invented those would
// dispatch under facts the user never chose.
var workflowRunRunner = func() (jobs.TaskRunner, error) {
	return nil, errors.New("no sub-agent runner configured: 'yaah workflow run' dispatches through the host composition's runner; standalone composition is not wired yet")
}

type workflowRunOptions struct {
	workflowOptions
	runID        string
	artifactRoot string
	statePath    string
}

func newWorkflowRunCmd() *cobra.Command {
	var opts workflowRunOptions
	cmd := &cobra.Command{
		Use:   "run <spec.yaml|spec.json>",
		Short: "Validate, then execute a workflow spec and report its outputs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowRun(cmd, args[0], opts)
		},
		Long: fmt.Sprintf(`Execute one workflow document.

The document is analyzed exactly as 'yaah workflow validate' analyzes it —
every rule family over the host facts — and a document with error-severity
findings is refused before anything dispatches. --strict promotes warnings
to refusal. Only a document that passes reaches the scheduler
(internal/workflow/exec) through the host door (internal/workflow/host),
which dispatches delegate nodes through internal/dispatch in the report
lane and renders emit values in the reducer.

Outputs:
  the run id, the final status and reason, every recorded output binding
  (delegate reports and emit values, canonical node order), the artifacts
  written, and door-side warnings. With --json, one report of kind
  %s on stdout.

Flags:
  --param key=value    bind a declared parameter (repeatable; same coercion
                       as validate/explain)
  --run-id id          label the run (default: minted from the graph name)
  --artifact-root dir  where emit artifact paths are resolved; empty records
                       outputs without writing files (declined writes are
                       listed as warnings)
  --state path         persist the run's reducer state to this database so
                       'yaah workflow resume <run_id>' can continue it
  --strict             warnings refuse the run like errors
  --json               the machine-readable run report

%s

%s`, runJSONKind, workflowSeverityHelp, workflowExitHelp),
	}
	opts.bind(cmd, false)
	cmd.Flags().StringVar(&opts.runID, "run-id", "", "label the run (default: minted from the graph name)")
	cmd.Flags().StringVar(&opts.artifactRoot, "artifact-root", "", "resolve emit artifact paths under this directory")
	cmd.Flags().StringVar(&opts.statePath, "state", "", "persist the run's reducer state to this database for `workflow resume`")
	return cmd
}

func runWorkflowRun(cmd *cobra.Command, path string, opts workflowRunOptions) error {
	out := cmd.OutOrStdout()

	a, err := analyzeWorkflow(path, opts.params, workflowEnv(), opts.strict)
	if err != nil {
		return err
	}
	// Refuse before dispatch: the analysis is the same gate validate runs,
	// so the refusal prints the same report.
	if a.failed() || a.graph == nil {
		if opts.asJSON {
			// The refusal report is the validate shape: the run never
			// started, so claiming a run.v1 report would invent a run id
			// and a status for work that did not happen.
			if werr := writeWorkflowJSON(out, a, false); werr != nil {
				return werr
			}
		} else {
			renderWorkflowValidate(out, a)
		}
		return workflowExitError(a)
	}

	runner, rerr := workflowRunRunner()
	if rerr != nil {
		return rerr
	}

	params := make(map[string]any, len(a.params))
	for _, p := range a.params {
		if p.Source != "unset" {
			params[p.Name] = p.Value
		}
	}

	// cmd.Context() is nil when the command is invoked without cobra's
	// Execute (tests, embedding); the door and the scheduler need a real
	// context.
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st, serr := openWorkflowState(opts.statePath)
	if serr != nil {
		return serr
	}
	if st != nil {
		defer st.Close()
	}
	runID := opts.runID
	if st != nil && runID == "" {
		// A durable run needs a stable id the resume command can name; the
		// one-shot default (graph name) would collide across runs.
		runID = runIdentify(a.graph.Name)
	}
	specHash, herr := workflowSpecHash(path)
	if herr != nil {
		return herr
	}
	res, xerr := host.Execute(ctx, host.Options{
		Graph:        a.graph,
		RunID:        runID,
		Params:       params,
		Runner:       runner,
		ArtifactRoot: opts.artifactRoot,
		Store:        st,
		SpecHash:     specHash,
		SpecPath:     path,
	})
	if xerr != nil {
		return xerr
	}

	report := runReportOf(path, res, opts.strict)
	if opts.asJSON {
		if werr := writeRunJSON(out, report); werr != nil {
			return werr
		}
	} else {
		renderWorkflowRun(out, report)
	}
	if !report.OK {
		return fmt.Errorf("%s: run %s finished %s — %s", path, report.RunID, report.Status, report.Reason)
	}
	return nil
}

// workflowRunReport is the run.v1 JSON shape. Every list is non-nil so the
// report renders [] rather than null, matching the validate/explain contract.
type workflowRunReport struct {
	Kind   string `json:"kind"`
	Spec   string `json:"spec"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Strict bool   `json:"strict"`

	Dispatches int   `json:"dispatches"`
	ElapsedMs  int64 `json:"elapsed_ms"`

	Outputs   []workflowOutputView   `json:"outputs"`
	Nodes     []workflowRunNodeView  `json:"nodes"`
	Artifacts []workflowArtifactView `json:"artifacts"`
	Warnings  []string               `json:"warnings"`
}

type workflowOutputView struct {
	NodePath string `json:"node_path"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Artifact string `json:"artifact_path,omitempty"`
	Contract string `json:"contract,omitempty"`
}

type workflowRunNodeView struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type workflowArtifactView struct {
	NodePath string `json:"node_path"`
	Name     string `json:"name,omitempty"`
	Declared string `json:"declared"`
	Path     string `json:"path"`
}

func runReportOf(specPath string, res host.Result, strict bool) workflowRunReport {
	rep := workflowRunReport{
		Kind:       runJSONKind,
		Spec:       specPath,
		RunID:      res.Run.RunID,
		Status:     string(res.Run.Status),
		OK:         res.Run.Status == exec.RunSucceeded,
		Reason:     res.Run.Reason,
		Strict:     strict,
		Dispatches: res.Run.Dispatches,
		ElapsedMs:  res.Run.Elapsed.Milliseconds(),
		Outputs:    []workflowOutputView{},
		Nodes:      []workflowRunNodeView{},
		Artifacts:  []workflowArtifactView{},
		Warnings:   []string{},
	}
	for _, o := range res.Run.Outputs {
		rep.Outputs = append(rep.Outputs, workflowOutputView{
			NodePath: o.NodePath,
			Kind:     string(o.Kind),
			Name:     o.Name,
			Value:    o.Value,
			Artifact: o.Artifact,
			Contract: o.Contract,
		})
	}
	for _, n := range res.Run.Nodes {
		rep.Nodes = append(rep.Nodes, workflowRunNodeView{
			Path:   n.Path,
			Kind:   string(n.Kind),
			State:  string(n.State),
			Reason: n.Reason,
		})
	}
	for _, art := range res.Artifacts {
		rep.Artifacts = append(rep.Artifacts, workflowArtifactView{
			NodePath: art.NodePath,
			Name:     art.Name,
			Declared: art.Declared,
			Path:     art.Path,
		})
	}
	rep.Warnings = append(rep.Warnings, res.Run.Warnings...)
	rep.Warnings = append(rep.Warnings, res.Warnings...)
	sort.Strings(rep.Warnings)
	return rep
}

func writeRunJSON(out io.Writer, rep workflowRunReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func renderWorkflowRun(out io.Writer, rep workflowRunReport) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "spec\t%s\n", rep.Spec)
	fmt.Fprintf(w, "run\t%s\n", rep.RunID)
	fmt.Fprintf(w, "status\t%s\n", rep.Status)
	if rep.Reason != "" {
		fmt.Fprintf(w, "reason\t%s\n", rep.Reason)
	}
	fmt.Fprintf(w, "dispatches\t%d\n", rep.Dispatches)
	fmt.Fprintf(w, "elapsed\t%s\n", (time.Duration(rep.ElapsedMs) * time.Millisecond).String())
	w.Flush()

	if len(rep.Outputs) > 0 {
		fmt.Fprintf(out, "\noutputs\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, o := range rep.Outputs {
			name := o.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", o.NodePath, name, oneLine(o.Value))
		}
		w.Flush()
	}
	if len(rep.Artifacts) > 0 {
		fmt.Fprintf(out, "\nartifacts\n")
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, art := range rep.Artifacts {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", art.NodePath, art.Declared, art.Path)
		}
		w.Flush()
	}
	fmt.Fprintf(out, "\nnodes\n")
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, n := range rep.Nodes {
		line := fmt.Sprintf("  %s\t%s\t%s", n.Path, n.Kind, n.State)
		if n.Reason != "" {
			line += "\t" + oneLine(n.Reason)
		}
		fmt.Fprintln(w, line)
	}
	w.Flush()
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(out, "\nwarnings\n")
		for _, warn := range rep.Warnings {
			fmt.Fprintf(out, "  %s\n", warn)
		}
	}
}
