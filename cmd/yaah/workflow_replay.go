package yaah

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/buchenberg/yaah/internal/workflow/exec"
	"github.com/buchenberg/yaah/internal/workflow/host"
	"github.com/buchenberg/yaah/internal/workflow/replay"
	"github.com/spf13/cobra"
)

// workflow_replay.go is `yaah workflow run --replay <run_id>`
// (bead yaah-0u0v.25). A completed run's own trace is a script: every answer
// the run's dispatches and settlements produced is on the record, so the
// same graph can be walked again with the answers substituted and no model
// called. What the replay is FOR is the equivalence claim — same node
// outcomes, same settlements, same outputs — which is what makes the
// deterministic skeleton testable and reproducible without paying for it
// twice.
//
// The command is deliberately one-way about the record: it READS the
// completed run's checkpoint from the state database and writes nothing back
// (the replay runs with no store at all), so the original run's rows are
// untouched — acceptance is "--replay does not mutate the original run".
// The refusals are as loud as resume's:
//
//   - no --state, because the answers live where the run recorded them;
//   - an unfinished run, because a run still in flight is resumed, not
//     replayed — and replaying an unfinished one would refuse at the first
//     unrecorded answer anyway;
//   - a spec hash mismatch, for the same reason resume refuses one: the
//     record was built for the document that was resolved when the run
//     started;
//   - a --param override, because the recorded parameters ARE the run's —
//     the templates already rendered against them.
func runWorkflowReplay(cmd *cobra.Command, path string, opts workflowRunOptions) error {
	out := cmd.OutOrStdout()
	if len(opts.params) > 0 {
		return errors.New("--replay: the recorded run's parameters are the run it replays; bind no --param against them")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st, err := openWorkflowState(opts.statePath)
	if err != nil {
		return err
	}
	if st == nil {
		return errors.New("replay needs the state database the run recorded into: pass --state <path>")
	}
	defer st.Close()

	cp, cerr := st.LoadCheckpoint(ctx, opts.replayRunID)
	if cerr != nil {
		return cerr
	}
	if cp.FinishedAt.IsZero() {
		return fmt.Errorf("replay %s: the run has no final disposition to replay — 'yaah workflow resume %s' continues an unfinished run",
			opts.replayRunID, opts.replayRunID)
	}
	if cp.SpecHash == "" {
		return fmt.Errorf("replay %s: the record carries no spec hash, so no document can be proven to be the one it ran", opts.replayRunID)
	}

	// The analysis gate runs on the recorded parameters, so the documents the
	// replay and the original bound are the same document — required ones
	// included — and the graph the door replays resolves exactly as the
	// recorded one did.
	flags, perr := recordedParamFlags(cp.Params)
	if perr != nil {
		return perr
	}
	calls, oerr := openWorkflowRegistry(opts.workflowRoot)
	if oerr != nil {
		return oerr
	}
	a, aerr := analyzeWorkflow(path, flags, workflowEnv(), opts.strict, calls)
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
	if hash != cp.SpecHash {
		return fmt.Errorf("replay %s: the run was started from spec %s but %s hashes to %s — the record belongs to the document that ran",
			opts.replayRunID, cp.SpecHash, path, hash)
	}

	door, derr := replay.New(cp)
	if derr != nil {
		return derr
	}
	runID := opts.runID
	if runID == "" {
		runID = "replay:" + cp.RunID
	}
	hostOpts := host.Options{
		Graph:        a.graph,
		RunID:        runID,
		Params:       paramsOf(a),
		Door:         door,
		ArtifactRoot: opts.artifactRoot,
		SpecHash:     hash,
		SpecPath:     path,
		// Store stays nil on purpose: the replay reads the original run's
		// records and writes nothing, and a store would hand it the original
		// run's namespace to mutate.
	}
	res, xerr := host.Execute(ctx, hostOpts)
	if xerr != nil {
		return xerr
	}

	report := runReportOf(path, res, opts.strict)
	if opts.asJSON {
		if werr := writeRunJSON(out, report); werr != nil {
			return werr
		}
	} else {
		renderWorkflowReplay(out, report, cp)
	}
	if !report.OK {
		return fmt.Errorf("%s: replay of run %s finished %s — %s", path, cp.RunID, report.Status, report.Reason)
	}
	return nil
}

// renderWorkflowReplay prints the run report prefixed by the run it replays,
// so a reader can hold the original and its projection apart.
func renderWorkflowReplay(out io.Writer, rep workflowRunReport, cp exec.Checkpoint) {
	fmt.Fprintf(out, "replay of\t%s\n", cp.RunID)
	renderWorkflowRun(out, rep)
}

// recordedParamFlags renders a recorded run's parameters back into the
// --param key=value form the analysis gate binds. The recorded values are the
// already-coerced ones (the run's own bound state), so this is the inverse of
// coerceParam's forward direction: strings pass through, scalars format
// back, list and map values take the same JSON form they were coerced from.
func recordedParamFlags(params map[string]any) ([]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	flags := make([]string, 0, len(names))
	for _, name := range names {
		text, err := paramFlagText(params[name])
		if err != nil {
			return nil, fmt.Errorf("replay: recorded parameter %s: %w", name, err)
		}
		flags = append(flags, name+"="+text)
	}
	return flags, nil
}

// paramFlagText formats one recorded parameter value as flag text, in the
// shape coerceParam re-coerces into the same value the record holds.
func paramFlagText(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
