package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/host"
)

// Tests for `yaah workflow run` (bead yaah-0u0v.12). The runner seam is
// injected with a deterministic closure — the same seam the host package's
// end-to-end tests use — so the golden run makes no model calls and never
// sleeps. The seam's production default (workflow_runner.go) is exercised on
// its own: its refusal is pinned against an empty $YAAH_HOME, and its
// composition against a temp home holding one usable provider. Fresh command
// per run, per the cli_audit_test.go convention.

func swapRunRunner(t *testing.T, fn jobs.TaskRunner, calls *int) {
	t.Helper()
	prev := workflowRunRunner
	workflowRunRunner = func() (jobs.TaskRunner, func(), error) {
		return func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
			if calls != nil {
				*calls++
			}
			return fn(ctx, prompt, params)
		}, func() {}, nil
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

// TestWorkflowRunStandaloneRunnerNeedsProvider pins the honest half of the
// standalone default: a configuration with no usable credential is refused
// with the missing fact named, rather than dispatched on the stub that only
// fails at the first Send or papered over with an invented provider.
// $YAAH_HOME points at an empty directory so the answer comes from an empty
// configuration, not from this machine's ~/.yaah.
func TestWorkflowRunStandaloneRunnerNeedsProvider(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())
	_, err := runWfRun(t, "--param", "task=verify", wfGoldens+"sequential.yaml")
	if err == nil {
		t.Fatal("expected a refusal with no provider configured")
	}
	if !strings.Contains(err.Error(), "no provider configured") {
		t.Errorf("error = %v, want the missing provider named", err)
	}
}

// TestStandaloneWorkflowRunnerComposesFromConfig pins the other half: one
// usable provider in the configuration composes a runner from the same three
// sources the one-shot CLI uses (config, provider resolution, store) and
// nothing beyond them. No model is called — the runner is only built.
func TestStandaloneWorkflowRunnerComposesFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	cfg := `providers:
  standalone:
    api: openai
    base_url: http://127.0.0.1:1/v1
    api_key: test-key
agents:
  default:
    provider: standalone
    model: standalone-model
`
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	r, cleanup, err := newStandaloneWorkflowRunner()
	if err != nil {
		t.Fatalf("compose the standalone runner: %v", err)
	}
	if r == nil {
		t.Fatal("composed a nil runner")
	}
	if cleanup == nil {
		t.Fatal("composed a runner with no teardown; the store handle would leak")
	}
	cleanup()
}

// TestWorkflowRunScriptedChainEndToEnd drives a two-delegate document through
// the run command with a scripted closure: the second dispatch's prompt is the
// first node's recorded report, so the value flow the reducer promises is
// proven at the command's own seam, with no provider and no model call.
func TestWorkflowRunScriptedChainEndToEnd(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "chain.yaml")
	const doc = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: chain
  version: 1.0.0
spec:
  budgets:
    max_dispatches: 4
    max_wall_clock_seconds: 120
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
`
	if err := os.WriteFile(spec, []byte(doc), 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}

	var prompts []string
	scripted := func(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
		prompts = append(prompts, strings.TrimSpace(prompt))
		return fmt.Sprintf("RESULT-%d", len(prompts)), nil
	}
	swapRunRunner(t, scripted, nil)
	out, err := runWfRun(t, "--run-id", "chain-1", spec)
	if err != nil {
		t.Fatalf("run: %v\noutput:\n%s", err, out)
	}
	if len(prompts) != 2 {
		t.Fatalf("runner calls = %d (%q), want 2 — one per delegate", len(prompts), prompts)
	}
	if prompts[0] != "do the first thing" {
		t.Errorf("first prompt = %q, want the document's prompt verbatim", prompts[0])
	}
	if prompts[1] != "review RESULT-1" {
		t.Errorf("second prompt = %q, want the first node's report rendered in", prompts[1])
	}
	for _, want := range []string{"chain-1", "succeeded", "steps.first", "steps.second"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
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

// TestWorkflowRunRefusesCostBombAndEscapes pins the §9 acceptance at the
// command seam. Authority and cost findings are warnings by contract — the
// host they will run on may have looser ceilings — so the gate refuses
// them only under --strict, which these fixtures exercise with the runner
// never constructed. Without --strict, the cost-bomb half is defused the
// other way: the run proceeds, but the runtime honors the document's own
// declared budget of three dispatches.
func TestWorkflowRunRefusesCostBombAndEscapes(t *testing.T) {
	refusals := []struct {
		fixture string
		code    string
	}{
		{"cost_bomb_over_budget.yaml", "WF033"},
		{"artifact_path_escape.yaml", "WF031"},
	}
	for _, tc := range refusals {
		t.Run("strict refuses "+tc.fixture, func(t *testing.T) {
			calls := 0
			swapRunRunner(t, echoRunner(""), &calls)
			out, err := runWfRun(t, "--strict", wfAdversarial+tc.fixture)
			if err == nil {
				t.Fatalf("expected --strict refusal, got success:\n%s", out)
			}
			if calls != 0 {
				t.Errorf("runner called %d times for a --strict refusal", calls)
			}
			if !strings.Contains(out, tc.code) {
				t.Errorf("refusal output missing %s:\n%s", tc.code, out)
			}
		})
	}
	t.Run("declared budget bounds the bomb at runtime", func(t *testing.T) {
		// The budget stop cancels in-flight attempts, so a dispatch
		// goroutine can still be winding down when the command returns —
		// an atomic count reads cleanly after a killed run.
		var calls atomic.Int64
		swapRunRunner(t, func(ctx context.Context, prompt string, params jobs.SubAgentParams) (string, error) {
			calls.Add(1)
			return "done", nil
		}, new(int))
		out, err := runWfRun(t, wfAdversarial+"cost_bomb_over_budget.yaml")
		if err == nil {
			t.Errorf("cost bomb ran to success (want the declared budget to bite):\n%s", out)
		}
		if got := calls.Load(); got > 3 {
			t.Errorf("runner called %d times, want at most the 3 dispatches the document budgets", got)
		} else if got == 0 {
			t.Errorf("runner never called; the fixture was refused outright:\n%s", out)
		}
	})
}
