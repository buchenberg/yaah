package yaah

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/workflow/trace"
)

// workflow_bench_spec_test.go is the CI gate for the spec-driven bench
// (bead yaah-0u0v.18).
//
// The DSL is not real until an existing bench task runs from a document, so
// this file pins two things and nothing else:
//
//  1. every checked-in bench workflow validates — the same gate
//     `yaah workflow validate` and `yaah workflow run` apply, so a spec that
//     drifts out of the vocabulary fails CI instead of failing a bench run
//     that costs model time;
//  2. `orch-fanout` runs end to end from that document through the run and
//     explain-surfaces a bench driver uses, and the run's trace records are
//     the facts ../_lib/orch_trace.py asserts over — the dispatch tally, the
//     concurrency the document declared, and the artifact each emit wrote.
//
// No model is called: the runner seam is injected with a deterministic
// closure, exactly as the host package's own end-to-end tests do. The
// dispatcher is the document — every orchestration fact asserted below comes
// from the spec, never from a Go-side graph built for the test.

// wfBenchDir is the orchestration bench root, relative to this package.
const wfBenchDir = "../../bench/orchestration-tasks"

// wfBenchTask is the bench task this bead made spec-driven.
const wfBenchTask = "orch-fanout"

// wfBenchSpecs lists every checked-in bench workflow document, in a stable
// order so a failing run names files in the order a reader expects.
func wfBenchSpecs(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(wfBenchDir, "*", "spec", "workflow.yaml"))
	if err != nil {
		t.Fatalf("globbing bench specs: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no bench workflow specs found under %s/*/spec/workflow.yaml", wfBenchDir)
	}
	sort.Strings(paths)
	return paths
}

// TestBenchSpecsValidate is the model-free CI gate: every bench spec decodes,
// resolves, and passes every validator family at error severity. Warnings are
// allowed — several codes are host-relative by design — but an error is a
// document defect that must never reach a bench run.
func TestBenchSpecsValidate(t *testing.T) {
	for _, path := range wfBenchSpecs(t) {
		name := filepath.Base(filepath.Dir(filepath.Dir(path)))
		t.Run(name, func(t *testing.T) {
			a, err := analyzeWorkflow(path, nil, workflowEnv(), false)
			if err != nil {
				t.Fatalf("analyze %s: %v", path, err)
			}
			for _, d := range a.errors {
				t.Errorf("%s: error %s at %s: %s", path, d.Code, d.Path, d.Message)
			}
			if a.graph == nil {
				t.Fatalf("%s did not resolve into a graph; status %s", path, a.status())
			}
			if got := a.status(); got != "valid" {
				t.Errorf("status = %q, want valid", got)
			}
			if len(a.graph.Steps) == 0 {
				t.Errorf("%s resolved to a graph with no steps", path)
			}
		})
	}
}

// wfBenchModuleRE finds the module a bench delegate prompt names. The prompt
// template is what makes each dispatch its own module, so the runner reads it
// back rather than the test assuming six independent prompts rendered.
var wfBenchModuleRE = regexp.MustCompile(`inventory/([a-z_]+\.py)`)

// wfBenchAuditRunner is the deterministic stand-in for the model: it answers a
// prompt with the four lines the bench task asks for, naming the module its
// prompt carried. It is a test double for one dispatch, not a dispatcher —
// which module runs when is the document's business.
func wfBenchAuditRunner(_ context.Context, prompt string, _ jobs.SubAgentParams) (string, error) {
	m := wfBenchModuleRE.FindStringSubmatch(prompt)
	if m == nil {
		return "", fmt.Errorf("bench runner received a prompt naming no inventory module: %q", prompt)
	}
	return fmt.Sprintf("module: %s\nlines: 1\nfunctions: none\nbare_except: no", m[1]), nil
}

// runWfExplainFromTrace runs `explain --from-trace <id> --state <db> --json
// [spec]`. The shared runWorkflowCmd helper insists on exactly one positional
// argument, and the trace form takes zero or one, so the flags and the
// positional are parsed here the same way cobra parses them.
func runWfExplainFromTrace(t *testing.T, runID, statePath string, args ...string) (string, error) {
	t.Helper()
	c := newWorkflowExplainCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetContext(context.Background())
	flags := append([]string{"--json", "--from-trace", runID, "--state", statePath}, args...)
	if err := c.ParseFlags(flags); err != nil {
		return buf.String(), err
	}
	positional := c.Flags().Args()
	if err := c.ValidateArgs(positional); err != nil {
		return buf.String(), err
	}
	err := c.RunE(c, positional)
	return buf.String(), err
}

// tracePeakInFlight replays the dispatch records and reports the highest
// number of attempts that were open at once. A record with status "started" is
// an attempt handed to the door; any other status on a dispatch record is that
// attempt's envelope coming back, so the pair closes.
func tracePeakInFlight(records []trace.Record) int {
	inFlight, peak := 0, 0
	for _, rec := range records {
		if rec.Kind != trace.KindDispatch {
			continue
		}
		if rec.Status == trace.StatusStarted {
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			continue
		}
		inFlight--
	}
	return peak
}

// TestBenchSpecOrchFanoutRunsFromTheDocument is the acceptance criterion: the
// task runs end to end from the checked-in document, and the trace the run
// records is the shape the bench harness asserts over.
func TestBenchSpecOrchFanoutRunsFromTheDocument(t *testing.T) {
	spec := filepath.Join(wfBenchDir, wfBenchTask, "spec", "workflow.yaml")
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.db")
	artifactRoot := filepath.Join(dir, "app")

	calls := 0
	swapRunRunner(t, wfBenchAuditRunner, &calls)

	out, err := runWfRun(t, "--json", "--run-id", "bench-orch-fanout",
		"--state", statePath, "--artifact-root", artifactRoot, spec)
	if err != nil {
		t.Fatalf("bench run from %s: %v\noutput:\n%s", spec, err, out)
	}
	rep := wfJSONReport[workflowRunReport](t, out)
	if rep.Kind != runJSONKind || !rep.OK || rep.Status != "succeeded" {
		t.Fatalf("run report kind/ok/status = %q/%v/%q, want %q/true/succeeded\n%s",
			rep.Kind, rep.OK, rep.Status, runJSONKind, out)
	}
	if calls != 6 || rep.Dispatches != 6 {
		t.Errorf("runner calls / dispatches = %d / %d, want 6 / 6 (one per module)", calls, rep.Dispatches)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("run reported warnings: %v (a bench spec must ask for nothing this lane cannot honor)", rep.Warnings)
	}

	// Layer 1 of the bench verifier, model-free: the six artifact files the
	// task's end state names, each carrying the dispatcher's own answer.
	audits := filepath.Join(artifactRoot, "audits")
	for _, module := range []string{"a_models", "b_storage", "c_pricing", "d_reports", "e_cli", "f_utils"} {
		path := filepath.Join(audits, module+".md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading artifact %s: %v", path, rerr)
		}
		if want := "module: " + module + ".py"; !strings.HasPrefix(string(data), want) {
			t.Errorf("%s = %q, want it to start with %q", path, string(data), want)
		}
	}
	entries, derr := os.ReadDir(audits)
	if derr != nil {
		t.Fatalf("reading %s: %v", audits, derr)
	}
	if len(entries) != 6 {
		t.Errorf("%s holds %d file(s), want exactly 6 (the task's end state)", audits, len(entries))
	}
	if len(rep.Artifacts) != 6 {
		t.Errorf("run report lists %d artifact(s), want 6", len(rep.Artifacts))
	}

	// Layer 3 of the bench verifier over the run's trace, exported exactly the
	// way the driver exports it for orch_assert.py.
	traceOut, terr := runWfExplainFromTrace(t, "bench-orch-fanout", statePath, spec)
	if terr != nil {
		t.Fatalf("explain --from-trace: %v\noutput:\n%s", terr, traceOut)
	}
	tr := wfJSONReport[workflowTraceReport](t, traceOut)
	if tr.Kind != traceJSONKind {
		t.Errorf("trace report kind = %q, want %q", tr.Kind, traceJSONKind)
	}
	if tr.RunID != "bench-orch-fanout" || tr.Status != "succeeded" || tr.Dispatches != 6 {
		t.Errorf("trace run/status/dispatches = %q/%q/%d, want bench-orch-fanout/succeeded/6",
			tr.RunID, tr.Status, tr.Dispatches)
	}
	if len(tr.Divergences) != 0 {
		t.Errorf("the run diverged from its document: %+v", tr.Divergences)
	}
	if len(tr.Nodes) != 12 {
		t.Errorf("trace reports %d node(s), want 12 (six delegates + six emits)", len(tr.Nodes))
	}
	for _, n := range tr.Nodes {
		if n.State != "succeeded" {
			t.Errorf("node %s ended %s, want succeeded", n.Path, n.State)
		}
		if n.Kind == "emit" && len(n.Outputs) == 0 {
			t.Errorf("emit node %s recorded no output binding", n.Path)
		}
	}
	if len(tr.Proposals) != 0 || len(tr.Settlements) != 0 {
		t.Errorf("report-lane bench sealed or settled something: proposals=%+v settlements=%+v",
			tr.Proposals, tr.Settlements)
	}

	// The document's concurrency: three attempts open at once, never more.
	if peak := tracePeakInFlight(tr.Records); peak != 3 {
		t.Errorf("peak concurrent dispatches in the trace = %d, want the 3 the document's max_concurrent_dispatches declares", peak)
	}
	if len(tr.Records) == 0 {
		t.Fatal("the run recorded no trace records")
	}
	if first := tr.Records[0]; first.Kind != trace.KindRunStarted {
		t.Errorf("first record kind = %q, want %q", first.Kind, trace.KindRunStarted)
	}
	if last := tr.Records[len(tr.Records)-1]; last.Kind != trace.KindRunEnded {
		t.Errorf("last record kind = %q, want %q", last.Kind, trace.KindRunEnded)
	}

	// Every delegate dispatch the trace records names its node kind and lane,
	// which is what lets a harness read delegation out of the record alone.
	dispatched := map[string]bool{}
	for _, rec := range tr.Records {
		if rec.Kind != trace.KindDispatch || rec.Status != trace.StatusStarted {
			continue
		}
		if rec.NodeKind != "delegate" {
			t.Errorf("dispatch %s names node kind %q, want delegate", rec.NodePath, rec.NodeKind)
		}
		if rec.Lane != "report" {
			t.Errorf("dispatch %s names lane %q, want report", rec.NodePath, rec.Lane)
		}
		dispatched[rec.NodeID] = true
	}
	if len(dispatched) != 6 {
		t.Errorf("trace records %d dispatched step(s), want 6: %v", len(dispatched), dispatched)
	}
}
