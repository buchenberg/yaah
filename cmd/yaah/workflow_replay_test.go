package yaah

// workflow_replay_test.go pins `yaah workflow run --replay <run_id>`
// (yaah-0u0v.25) through the command surface: the recorded run reproduces
// itself with zero runner calls, the original run's rows are untouched, and
// every refusal the replay owes an operator is loud — no state database, an
// unfinished run, a --param override, and a spec hash mismatch.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/exec"
)

const replayChainDoc = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: replay-chain
spec:
  steps:
    - id: first
      kind: delegate
      role: planner
      lane: report
      prompt: do the first thing
    - id: second
      kind: delegate
      role: planner
      lane: report
      needs: [first]
      prompt: review {{ steps.first.output }}
    - id: done
      kind: emit
      needs: [second]
      output: summary
      value: "both done: {{ steps.second.output }}"
`

func replayChainSpec(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chain.yaml")
	if err := os.WriteFile(path, []byte(replayChainDoc), 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}
	return path
}

// recordedRunJSON runs the chain once against the scripted runner and returns
// its parsed report, the state database path, and the spec path.
func recordedRunJSON(t *testing.T) (workflowRunReport, string, string) {
	t.Helper()
	spec := replayChainSpec(t)
	state := filepath.Join(t.TempDir(), "state.db")

	var calls int
	swapRunRunner(t, func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		calls++
		return resultOf(calls), nil
	}, nil)
	out, err := runWfRun(t, "--run-id", "chain-recorded", "--state", state, "--json", spec)
	if err != nil {
		t.Fatalf("recording run: %v\noutput:\n%s", err, out)
	}
	var rep workflowRunReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("decoding run report: %v\noutput:\n%s", jerr, out)
	}
	return rep, state, spec
}

func resultOf(n int) string {
	switch n {
	case 1:
		return "RESULT-1"
	case 2:
		return "RESULT-2"
	default:
		return "UNEXPECTED"
	}
}

// TestWorkflowReplayReproducesTheRunWithZeroRunnerCalls is the command-level
// acceptance: the replay walks the same graph over the recorded answers — the
// runner is never called, the reports agree on status, dispatches, outputs,
// and node states, and the run id says it is a replay.
func TestWorkflowReplayReproducesTheRunWithZeroRunnerCalls(t *testing.T) {
	rep, state, spec := recordedRunJSON(t)

	// The replay composes no runner at all; the seam's counter proves none
	// was invoked even if composition reached for one.
	var replayCalls int
	swapRunRunner(t, func(_ context.Context, _ string, _ jobs.SubAgentParams) (string, error) {
		replayCalls++
		return "MODEL CALL — a replay must not reach a model", nil
	}, nil)
	out, err := runWfRun(t, "--replay", "chain-recorded", "--state", state, "--json", spec)
	if err != nil {
		t.Fatalf("replay run: %v\noutput:\n%s", err, out)
	}
	if replayCalls != 0 {
		t.Errorf("runner called %d time(s) during the replay; a replay answers from the record", replayCalls)
	}

	var replayed workflowRunReport
	if jerr := json.Unmarshal([]byte(out), &replayed); jerr != nil {
		t.Fatalf("decoding replay report: %v\noutput:\n%s", jerr, out)
	}
	if replayed.RunID != "replay:chain-recorded" {
		t.Errorf("run id = %q, want the replay namespace of the original", replayed.RunID)
	}
	if replayed.Status != rep.Status || replayed.OK != rep.OK || replayed.Dispatches != rep.Dispatches {
		t.Errorf("status/ok/dispatches = %s/%v/%d, want %s/%v/%d",
			replayed.Status, replayed.OK, replayed.Dispatches, rep.Status, rep.OK, rep.Dispatches)
	}
	if len(replayed.Outputs) != len(rep.Outputs) {
		t.Fatalf("outputs = %d, want %d", len(replayed.Outputs), len(rep.Outputs))
	}
	for i := range rep.Outputs {
		if replayed.Outputs[i] != rep.Outputs[i] {
			t.Errorf("output[%d] = %+v, want the recorded %+v", i, replayed.Outputs[i], rep.Outputs[i])
		}
	}
	if len(replayed.Nodes) != len(rep.Nodes) {
		t.Fatalf("nodes = %d, want %d", len(replayed.Nodes), len(rep.Nodes))
	}
	for i := range rep.Nodes {
		if replayed.Nodes[i] != rep.Nodes[i] {
			t.Errorf("node[%d] = %+v, want the recorded %+v", i, replayed.Nodes[i], rep.Nodes[i])
		}
	}
}

// TestWorkflowReplayDoesNotMutateTheOriginalRun pins the read-only half: after
// a replay, the original run's checkpoint holds the same disposition, the
// same sequence, and the same finished instant it had before.
func TestWorkflowReplayDoesNotMutateTheOriginalRun(t *testing.T) {
	_, state, spec := recordedRunJSON(t)

	st, err := openWorkflowState(state)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	before, lerr := st.LoadCheckpoint(context.Background(), "chain-recorded")
	if lerr != nil {
		t.Fatalf("load before replay: %v", lerr)
	}
	st.Close()

	runnerCalls := 0
	swapRunRunner(t, func(_ context.Context, _ string, _ jobs.SubAgentParams) (string, error) {
		runnerCalls++
		return "never", nil
	}, nil)
	if out, err := runWfRun(t, "--replay", "chain-recorded", "--state", state, spec); err != nil {
		t.Fatalf("replay run: %v\noutput:\n%s", err, out)
	}
	if runnerCalls != 0 {
		t.Errorf("runner called %d time(s); a replay reaches no model", runnerCalls)
	}

	st, err = openWorkflowState(state)
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer st.Close()
	after, lerr := st.LoadCheckpoint(context.Background(), "chain-recorded")
	if lerr != nil {
		t.Fatalf("load after replay: %v", lerr)
	}
	if after.Status != before.Status || after.Seq != before.Seq ||
		!after.FinishedAt.Equal(before.FinishedAt) || len(after.Nodes) != len(before.Nodes) {
		t.Errorf("the original run's record changed across the replay:\nbefore: %s seq=%d nodes=%d\nafter:  %s seq=%d nodes=%d",
			before.Status, before.Seq, len(before.Nodes), after.Status, after.Seq, len(after.Nodes))
	}
}

// TestWorkflowReplayRefusals pins the loud gates: no --state, an unknown run,
// an unfinished run, a --param override, and a spec hash mismatch.
func TestWorkflowReplayRefusals(t *testing.T) {
	t.Run("no state database", func(t *testing.T) {
		spec := replayChainSpec(t)
		swapRunRunner(t, echoRunner(""), nil)
		out, err := runWfRun(t, "--replay", "chain-recorded", spec)
		if err == nil {
			t.Fatalf("replay without --state succeeded:\n%s", out)
		}
		if !strings.Contains(err.Error(), "--state") {
			t.Errorf("refusal = %q, want it to name the missing --state", err.Error())
		}
	})
	t.Run("unknown run id", func(t *testing.T) {
		_, state, spec := recordedRunJSON(t)
		swapRunRunner(t, echoRunner(""), nil)
		out, err := runWfRun(t, "--replay", "never-recorded", "--state", state, spec)
		if err == nil {
			t.Fatalf("replay of an unknown run succeeded:\n%s", out)
		}
		if !strings.Contains(err.Error(), "never-recorded") {
			t.Errorf("refusal = %q, want it to name the unknown run id", err.Error())
		}
	})
	t.Run("unfinished run", func(t *testing.T) {
		spec := replayChainSpec(t)
		state := filepath.Join(t.TempDir(), "state.db")
		st, err := openWorkflowState(state)
		if err != nil {
			t.Fatalf("open state: %v", err)
		}
		if serr := st.SaveCheckpoint(context.Background(), exec.Checkpoint{RunID: "unfinished", Status: exec.RunRunning}); serr != nil {
			t.Fatalf("seeding an unfinished run: %v", serr)
		}
		st.Close()
		swapRunRunner(t, echoRunner(""), nil)
		out, err := runWfRun(t, "--replay", "unfinished", "--state", state, spec)
		if err == nil {
			t.Fatalf("replay of an unfinished run succeeded:\n%s", out)
		}
		for _, want := range []string{"unfinished", "resume"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal = %q, want it to name %q", err.Error(), want)
			}
		}
	})
	t.Run("param override refused", func(t *testing.T) {
		_, state, spec := recordedRunJSON(t)
		swapRunRunner(t, echoRunner(""), nil)
		out, err := runWfRun(t, "--replay", "chain-recorded", "--state", state, "--param", "akey=aval", spec)
		if err == nil {
			t.Fatalf("replay with --param succeeded:\n%s", out)
		}
		if !strings.Contains(err.Error(), "--param") {
			t.Errorf("refusal = %q, want it to name the --param conflict", err.Error())
		}
	})
	t.Run("spec hash mismatch", func(t *testing.T) {
		_, state, spec := recordedRunJSON(t)
		// The document changed since the run recorded it: the replay is for
		// the document that ran, and another one hashes differently. (A YAML
		// comment would not — the hash is of the canonical JSON, which the
		// decode strips — so the change is one the document itself carries.)
		doc, rerr := os.ReadFile(spec)
		if rerr != nil {
			t.Fatalf("read spec: %v", rerr)
		}
		doc = []byte(strings.Replace(string(doc), "do the first thing", "do a different thing", 1))
		if werr := os.WriteFile(spec, doc, 0o644); werr != nil {
			t.Fatalf("modify spec: %v", werr)
		}
		swapRunRunner(t, echoRunner(""), nil)
		out, err := runWfRun(t, "--replay", "chain-recorded", "--state", state, spec)
		if err == nil {
			t.Fatalf("replay of a changed document succeeded:\n%s", out)
		}
		if !strings.Contains(err.Error(), "hashes to") {
			t.Errorf("refusal = %q, want it to name the hash mismatch", err.Error())
		}
	})
}
