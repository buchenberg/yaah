package yaah

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/host"
	"github.com/buchenberg/yaah/internal/workflow/spec"
	"github.com/buchenberg/yaah/internal/workflow/store"
	"github.com/spf13/cobra"
)

// workflow_resume.go is `yaah workflow resume <run_id>` (bead yaah-0u0v.16). A
// run that has a state store is durable: the reducer's committed state is a row
// set keyed by run_id, so a killed run continues from the node it stopped on
// instead of starting over and duplicating dispatches or settlements.
//
// The command is deliberately the twin of `yaah workflow run`: it validates and
// resolves the document with the same gate, then hands the graph to the host
// door's Resume instead of its Execute. What it refuses, it refuses loudly:
//
//   - no --state, because a resume without a record has nothing to continue;
//   - a document whose spec hash differs from the run's, because the state was
//     built for the document that was resolved when the run started;
//   - a run that already finished, because its disposition is final.
//
// Parameters are the run's own, restored from the record: templates already
// rendered against them, and a run resumed under other values would be a
// different run with the same id. --param is still accepted so the analysis gate
// can resolve the document, and a value that disagrees with the record is called
// out as a warning rather than silently applied.

// workflowResumeStore is the seam the resume command takes its run store from.
// A caller that already holds a session store sets it; the standalone default
// refuses rather than opening the session database behind the user's back,
// because which database a run lives in is a decision, not a default.
var workflowResumeStore = func(path string) (exec.Store, error) {
	if path == "" {
		return nil, errors.New("no run store: pass --state <path> with the state database the run was started against")
	}
	db, err := memory.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open workflow state %s: %w", path, err)
	}
	return store.New(db)
}

type workflowResumeOptions struct {
	workflowOptions
	runID        string
	statePath    string
	specPath     string
	artifactRoot string
}

func newWorkflowResumeCmd() *cobra.Command {
	var opts workflowResumeOptions
	cmd := &cobra.Command{
		Use:   "resume <run_id>",
		Short: "Continue a workflow run from its durable state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowResume(cmd, args[0], opts)
		},
		Long: fmt.Sprintf(`Continue one workflow run from the state it recorded.

The run's own state is the authority: every node that terminated stays
terminated, every proposal handle the run sealed is still recorded, and only
the work the record does not already show is dispatched. That is what makes a
killed fan-out resumable — no node it completed is dispatched twice, and a
proposal is settled at most once.

The document is resolved again and must be the one the run was started from:
the recorded spec hash is compared and a mismatch is refused. --spec names the
document when the record's own path is not what you want to resolve (a moved
checkout, a renamed file); the hash still has to agree.

A disposition whose outcome the door never reported is NOT asked for a second
time. The settle node fails with settlement_outcome_unknown and the proposal
stays open, which the run reports rather than papering over.

Flags:
  --state <path>       the state database the run was started against (required)
  --spec <path>        resolve this document instead of the recorded path
  --param key=value    bind a declared parameter for resolution (the run's own
                       recorded values are what it executes under)
  --artifact-root dir  where emit artifact paths are resolved; artifacts are
                       rewritten from the recorded values, so a partial write is
                       repaired rather than duplicated
  --json               the machine-readable run report

Exit codes:
  0  the resumed run finished succeeded
  1  the run finished with any other disposition, or the resume was refused
     (no record, a spec hash mismatch, a run that already finished)

%s`, workflowSeverityHelp),
	}
	opts.bind(cmd, false)
	cmd.Flags().StringVar(&opts.statePath, "state", "", "state database the run was started against (required)")
	cmd.Flags().StringVar(&opts.specPath, "spec", "", "resolve this document instead of the recorded spec path")
	cmd.Flags().StringVar(&opts.artifactRoot, "artifact-root", "", "resolve emit artifact paths under this directory")
	return cmd
}

func runWorkflowResume(cmd *cobra.Command, runID string, opts workflowResumeOptions) error {
	out := cmd.OutOrStdout()

	st, err := workflowResumeStore(opts.statePath)
	if err != nil {
		return err
	}
	runner, rerr := workflowRunRunner()
	if rerr != nil {
		return rerr
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// The record names the document; --spec overrides where it is READ from, but
	// never which document the state belongs to.
	cp, err := st.LoadCheckpoint(ctx, runID)
	if err != nil {
		return err
	}
	path := opts.specPath
	if path == "" {
		path = cp.SpecPath
	}
	if path == "" {
		return fmt.Errorf("run %s records no spec path; pass --spec <path>", runID)
	}

	a, aerr := analyzeWorkflow(path, opts.params, workflowEnv(), opts.strict)
	if aerr != nil {
		return aerr
	}
	if a.failed() || a.graph == nil {
		if opts.asJSON {
			if werr := writeWorkflowJSON(out, a, false); werr != nil {
				return werr
			}
		} else {
			renderWorkflowValidate(out, a)
		}
		return workflowExitError(a)
	}
	hash, herr := workflowSpecHash(path)
	if herr != nil {
		return herr
	}

	res, xerr := host.Resume(ctx, host.Options{
		Graph:        a.graph,
		Params:       paramsOf(a),
		Runner:       runner,
		ArtifactRoot: opts.artifactRoot,
		Store:        st,
		SpecHash:     hash,
		SpecPath:     path,
	}, runID)
	if xerr != nil {
		return xerr
	}

	report := runReportOf(path, res, opts.strict)
	if opts.asJSON {
		if werr := writeRunJSON(out, report); werr != nil {
			return werr
		}
	} else {
		renderWorkflowResume(out, report, cp)
	}
	if !report.OK {
		return fmt.Errorf("resume %s: run finished %s — %s", runID, report.Status, report.Reason)
	}
	return nil
}

// renderWorkflowResume prints the run report prefixed by what the record said
// before the resume, so a reader can tell which nodes this process actually ran.
func renderWorkflowResume(out io.Writer, rep workflowRunReport, cp exec.Checkpoint) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "resumed\t%s\n", cp.RunID)
	if cp.GraphName != "" {
		fmt.Fprintf(w, "graph\t%s\n", cp.GraphName)
	}
	fmt.Fprintf(w, "restored\t%d node(s), %d instance(s), %d dispatche(s) so far\n",
		len(cp.Nodes), len(cp.Instances), cp.Dispatches)
	w.Flush()
	renderWorkflowRun(out, rep)
}

// paramsOf flattens the analysis' bound parameters into the map the door takes.
func paramsOf(a *workflowAnalysis) map[string]any {
	params := make(map[string]any, len(a.params))
	for _, p := range a.params {
		if p.Source != "unset" {
			params[p.Name] = p.Value
		}
	}
	return params
}

// workflowSpecHash returns the digest of the document at path, computed the way
// the run computed it when it persisted the run. It is a separate read from the
// analysis for one reason: the analysis answers "is this document valid", and this
// answers "is this the same document", and a resume needs both answers.
func workflowSpecHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	doc, err := spec.ParseYAML(data)
	if err != nil {
		return "", err
	}
	return spec.SpecHash(doc)
}

// openWorkflowState opens the optional state database `workflow run` records
// into. An empty path declines persistence, which is the one-shot run: state that
// is not asked for is not written, and the run says nothing about resuming it.
func openWorkflowState(path string) (exec.Store, error) {
	if path == "" {
		return nil, nil
	}
	db, err := memory.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open workflow state %s: %w", path, err)
	}
	return store.New(db)
}

// runIdentify is the timestamped id a run uses when the caller named none and
// --state is set, so two runs against one database cannot collide on the graph
// name alone.
func runIdentify(name string) string {
	return fmt.Sprintf("%s@%s", name, time.Now().UTC().Format("20060102T150405"))
}

// unusedResumeSeam keeps the jobs import meaningful if the runner seam moves.
var _ jobs.TaskRunner
