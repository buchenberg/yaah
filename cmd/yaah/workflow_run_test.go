package yaah

import (
	"context"
	"encoding/json"

	"github.com/buchenberg/yaah/internal/workflow/host"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/jobs"
)

// Tests for `yaah workflow run` (bead yaah-0u0v.12). The runner seam is
// injected with a deterministic closure — the same seam the host package's
// end-to-end tests use — so the golden run makes no model calls and never
// sleeps. Fresh command per run, per the cli_audit_test.go convention.

func swapRunRunner(t *testing.T, fn jobs.TaskRunner, calls *int) {
	t.Helper()
	prev := workflowRunRunner
	workflowRunRunner = func() (jobs.TaskRunner, error) {
		return func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
			if calls != nil {
				*calls++
			}
			return fn(ctx, prompt, params)
		}, nil
	}
	t.Cleanup(func() { workflowRunRunner = prev })
}

func runWfRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWorkflowCmd(t, newWorkflowRunCmd(), args...)
}

func echoRunner(prefix string) jobs.TaskRunner {
	return func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		return prefix + strings.TrimSpace(prompt), nil
	}
}

// TestWorkflowRunRefusesInvalidSpec pins the validate-before-execute gate:
// an invalid document is refused with its diagnostics and the runner is
// never constructed, let alone called.
func TestWorkflowRunRefusesInvalidSpec(t *testing.T) {
	calls := 0
	swapRunRunner(t, echoRunner(""), &calls)
	out, err := runWfRun(t, wfAdversarial+"needs_self_cycle.yaml")
	if err == nil {
		t.Fatalf("expected refusal for an invalid spec, got success:\n%s", out)
	}
	if calls != 0 {
		t.Errorf("runner called %d times for a refused spec", calls)
	}
	if !strings.Contains(out, "WF003") {
		t.Errorf("refusal output missing the diagnostics:\n%s", out)
	}
}

// TestWorkflowRunRefusesWithoutRunner pins the honest standalone default:
// with no runner configured the command says so instead of half-bootstrapping
// a composition root or silently no-oping.
func TestWorkflowRunRefusesWithoutRunner(t *testing.T) {
	_, err := runWfRun(t, "--param", "task=verify", wfGoldens+"sequential.yaml")
	if err == nil {
		t.Fatal("expected an error with no runner configured")
	}
	if !strings.Contains(err.Error(), "no sub-agent runner configured") {
		t.Errorf("error = %v, want the missing-runner explanation", err)
	}
}

// TestWorkflowRunGoldenEndToEnd pins the acceptance criterion: the sequential
// golden runs end-to-end against the deterministic runner with exactly one
// dispatch (the delegate — emit renders in the reducer and dispatches
// nothing), and the run id, outputs, and node states appear in the report.
func TestWorkflowRunGoldenEndToEnd(t *testing.T) {
	calls := 0
	swapRunRunner(t, echoRunner("PLAN "), &calls)
	out, err := runWfRun(t, "--run-id", "e2e-1", "--param", "task=verify", wfGoldens+"sequential.yaml")
	if err != nil {
		t.Fatalf("run: %v\noutput:\n%s", err, out)
	}
	if calls != 1 {
		t.Errorf("runner calls = %d, want exactly 1 (one delegate; emit does not dispatch)", calls)
	}
	for _, want := range []string{"e2e-1", "succeeded", "PLAN", "outputs", "steps.plan", "steps.announce"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestWorkflowRunJSON pins the run.v1 report shape over the same golden run.
func TestWorkflowRunJSON(t *testing.T) {
	swapRunRunner(t, echoRunner("PLAN "), nil)
	out, err := runWfRun(t, "--json", "--run-id", "e2e-2", "--param", "task=verify", wfGoldens+"sequential.yaml")
	if err != nil {
		t.Fatalf("run --json: %v\noutput:\n%s", err, out)
	}
	var rep workflowRunReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("decoding run report: %v\noutput:\n%s", jerr, out)
	}
	if rep.Kind != runJSONKind {
		t.Errorf("kind = %q, want %q", rep.Kind, runJSONKind)
	}
	if !rep.OK || rep.Status != "succeeded" || rep.RunID != "e2e-2" {
		t.Errorf("ok/status/run_id = %v/%s/%s, want true/succeeded/e2e-2", rep.OK, rep.Status, rep.RunID)
	}
	if rep.Dispatches != 1 {
		t.Errorf("dispatches = %d, want 1", rep.Dispatches)
	}
	names := map[string]bool{}
	for _, o := range rep.Outputs {
		names[o.Name] = true
		if o.NodePath == "steps.plan" && !strings.HasPrefix(o.Value, "PLAN") {
			t.Errorf("plan output = %q, want the runner's report text", o.Value)
		}
	}
	if !names["Plan"] || !names["announce"] {
		t.Errorf("outputs = %v, want the Plan binding and the announce emit", names)
	}
	states := map[string]string{}
	for _, n := range rep.Nodes {
		states[n.Path] = n.State
	}
	if states["steps.plan"] != "succeeded" || states["steps.announce"] != "succeeded" {
		t.Errorf("node states = %v, want both succeeded", states)
	}
	if rep.Outputs == nil || rep.Nodes == nil || rep.Artifacts == nil || rep.Warnings == nil {
		t.Error("lists must serialize as [], never null")
	}
}

// TestWorkflowRunStrictRefusesWarnings pins that --strict promotes the
// host-relative warning (WF030 for a role this host may not register) to a
// refusal before any dispatch.
func TestWorkflowRunStrictRefusesWarnings(t *testing.T) {
	calls := 0
	swapRunRunner(t, echoRunner(""), &calls)
	probe, err := runWfValidate(t, "--strict", wfGoldens+"sequential.yaml")
	if err == nil {
		t.Skipf("host role registry makes sequential warning-free; nothing to promote (%s)", strings.TrimSpace(probe))
	}
	out, err := runWfRun(t, "--strict", "--param", "task=verify", wfGoldens+"sequential.yaml")
	if err == nil {
		t.Fatalf("expected --strict refusal, got success:\n%s", out)
	}
	if calls != 0 {
		t.Errorf("runner called %d times for a --strict refusal", calls)
	}
}

// TestRunReportCarriesStrict pins that the validation gate's --strict flag
// reaches the run.v1 report (review #1644: it was declared required by the
// schema and never populated).
func TestRunReportCarriesStrict(t *testing.T) {
	if rep := runReportOf("spec.yaml", host.Result{}, true); !rep.Strict {
		t.Error("strict = false in report, want the gate flag threaded through")
	}
	if rep := runReportOf("spec.yaml", host.Result{}, false); rep.Strict {
		t.Error("strict = true without the flag")
	}
}
