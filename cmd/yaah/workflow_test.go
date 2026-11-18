package yaah

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/validate"
	"github.com/spf13/cobra"
	"path/filepath"
)

// Tests for the workflow validate/explain CLI (bead yaah-0u0v.9).
//
// Following the convention pinned by cli_audit_test.go: the package-level
// command tree is shared, so every run here builds a FRESH command through
// its constructor (newWorkflow{Validate,Explain}Cmd) — fresh flag state, no
// carry-over between tests. Flags are parsed through cobra (ParseFlags) so
// --json/--strict/--param take the same path they take in production.

const wfGoldens = "../../internal/workflow/spec/testdata/valid/"
const wfAdversarial = "../../internal/workflow/validate/testdata/adversarial/"

// statusValidRE matches the text report's status line exactly — a plain
// Contains(out, "valid") would also match "invalid".
var statusValidRE = regexp.MustCompile(`(?m)^status\s+valid\b`)

func runWorkflowCmd(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetContext(context.Background())
	if err := c.ParseFlags(args); err != nil {
		return buf.String(), err
	}
	positional := c.Flags().Args()
	if len(positional) != 1 {
		err := c.ValidateArgs(positional)
		return buf.String(), err
	}
	// RunE must run before buf.String() is read — a multi-value return
	// evaluates its operands first, so calling RunE inline in the return
	// would capture the buffer before the command writes to it.
	err := c.RunE(c, positional)
	return buf.String(), err
}

func runWfValidate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWorkflowCmd(t, newWorkflowValidateCmd(), args...)
}

func runWfExplain(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWorkflowCmd(t, newWorkflowExplainCmd(), args...)
}

func wfJSONReport[T any](t *testing.T, out string) T {
	t.Helper()
	var rep T
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("decoding --json output: %v\noutput was:\n%s", err, out)
	}
	return rep
}

// TestWorkflowValidateAcceptsGoldens pins the acceptance criterion that
// every golden spec validates clean: exit success, status valid, ok true,
// zero errors (host-relative warnings may appear and must not fail).
func TestWorkflowValidateAcceptsGoldens(t *testing.T) {
	for _, name := range []string{"sequential", "fanout_settle", "speculative", "bounded_repair"} {
		t.Run(name, func(t *testing.T) {
			out, err := runWfValidate(t, wfGoldens+name+".yaml")
			if err != nil {
				t.Fatalf("validate %s: %v\noutput:\n%s", name, err, out)
			}
			if !statusValidRE.MatchString(out) {
				t.Errorf("expected 'status valid' line in text output, got:\n%s", out)
			}

			out, err = runWfValidate(t, "--json", wfGoldens+name+".yaml")
			if err != nil {
				t.Fatalf("validate --json %s: %v", name, err)
			}
			rep := wfJSONReport[validateReport](t, out)
			if rep.Kind != validateJSONKind {
				t.Errorf("kind = %q, want %q", rep.Kind, validateJSONKind)
			}
			if rep.Status != "valid" || !rep.OK {
				t.Errorf("status/ok = %q/%v, want valid/true (errors: %v)", rep.Status, rep.OK, rep.Errors)
			}
			if len(rep.Errors) != 0 {
				t.Errorf("golden %s has errors: %v", name, rep.Errors)
			}
			// Non-nil contract: warnings must serialize as [] not null.
			if !strings.Contains(out, `"warnings": []`) && len(rep.Warnings) == 0 {
				t.Errorf("warnings should serialize as [] when empty, got:\n%s", out)
			}
		})
	}
}

// TestWorkflowValidateRejectsAdversarial pins stable diagnostic codes for
// every adversarial fixture: exact status, exact multiset of rule codes,
// and well-formed diagnostics (code, path, message all present).
func TestWorkflowValidateRejectsAdversarial(t *testing.T) {
	cases := []struct {
		file   string
		status string
		codes  []string
	}{
		// WF003 for the unbounded self-recursion; WF002 because a call
		// without --workflow-root resolves nothing, which is fail-closed and
		// says so (yaah-0u0v.23).
		{"call_self_no_max_depth.yaml", "invalid", []string{"WF002", "WF003"}},
		{"dangling_needs.yaml", "unresolved", []string{"WF001"}},
		{"duplicate_body_output_names.yaml", "invalid", []string{"WF005", "WF034"}},
		{"duplicate_output_names.yaml", "invalid", []string{"WF005"}},
		{"malformed_output_schema.yaml", "invalid", []string{"WF005"}},
		{"needs_long_cycle.yaml", "invalid", []string{"WF003", "WF004", "WF004", "WF004", "WF004", "WF004"}},
		{"needs_self_cycle.yaml", "invalid", []string{"WF003", "WF004"}},
		{"needs_two_cycle.yaml", "invalid", []string{"WF003", "WF004", "WF004"}},
		{"unreachable_island.yaml", "invalid", []string{"WF003", "WF004", "WF004", "WF004"}},
	}
	codeRE := regexp.MustCompile(`^WF\d{3}$`)
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			out, err := runWfValidate(t, "--json", wfAdversarial+tc.file)
			if err == nil {
				t.Fatalf("expected failure for %s, got success:\n%s", tc.file, out)
			}
			rep := wfJSONReport[validateReport](t, out)
			if rep.OK {
				t.Errorf("ok = true for adversarial fixture %s", tc.file)
			}
			if rep.Status != tc.status {
				t.Errorf("status = %q, want %q", rep.Status, tc.status)
			}
			var got []string
			for _, d := range rep.Errors {
				got = append(got, d.Code)
				if !codeRE.MatchString(d.Code) {
					t.Errorf("diagnostic code %q is not a stable WF### code", d.Code)
				}
				if d.Message == "" {
					t.Errorf("diagnostic %s has empty message", d.Code)
				}
				if d.Path == "" {
					t.Errorf("diagnostic %s has empty path", d.Code)
				}
			}
			sort.Strings(got)
			want := append([]string(nil), tc.codes...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("codes = [%s], want [%s]", strings.Join(got, ","), strings.Join(want, ","))
			}
		})
	}
}

// TestWorkflowValidateUnableToAnalyze covers the non-diagnostic failure
// modes: unreadable file, wrong extension, malformed params, wrong arg
// count. Each must return an error rather than a report.
func TestWorkflowValidateUnableToAnalyze(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := runWfValidate(t, "no/such/spec.yaml"); err == nil {
			t.Error("expected error for missing file")
		}
	})
	t.Run("undecodable content", func(t *testing.T) {
		// readWorkflowSpec gates on readability and size, not extension —
		// this file fails because its content is not a workflow document.
		p := t.TempDir() + "/spec.txt"
		if err := os.WriteFile(p, []byte("version: v1alpha1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runWfValidate(t, p); err == nil {
			t.Error("expected error for undecodable content")
		}
	})
	t.Run("param without equals", func(t *testing.T) {
		if _, err := runWfValidate(t, "--param", "oops", wfGoldens+"sequential.yaml"); err == nil {
			t.Error("expected error for --param without =")
		}
	})
	t.Run("param wrong type", func(t *testing.T) {
		if _, err := runWfValidate(t, "--param", "max_workstreams=abc", wfGoldens+"fanout_settle.yaml"); err == nil {
			t.Error("expected error for non-integer max_workstreams")
		}
	})
	t.Run("wrong arg count", func(t *testing.T) {
		if _, err := runWfValidate(t); err == nil {
			t.Error("expected error with no positional arg")
		}
		if _, err := runWfValidate(t, "a.yaml", "b.yaml"); err == nil {
			t.Error("expected error with two positional args")
		}
	})
}

// TestWorkflowStrictPromotion pins the --strict contract at the analysis
// level: errors always fail; warnings fail only under strict.
func TestWorkflowStrictPromotion(t *testing.T) {
	w := validate.Diagnostic{Code: "WF030", Path: "steps.x.role", Message: "host-relative warning"}
	e := validate.Diagnostic{Code: "WF001", Path: "steps.x", Message: "hard error"}

	cases := []struct {
		name   string
		a      workflowAnalysis
		failed bool
	}{
		{"clean", workflowAnalysis{}, false},
		{"warning lax", workflowAnalysis{warnings: []validate.Diagnostic{w}}, false},
		{"warning strict", workflowAnalysis{strict: true, warnings: []validate.Diagnostic{w}}, true},
		{"error lax", workflowAnalysis{errors: []validate.Diagnostic{e}}, true},
		{"error strict", workflowAnalysis{strict: true, errors: []validate.Diagnostic{e}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.failed(); got != tc.failed {
				t.Errorf("failed() = %v, want %v", got, tc.failed)
			}
		})
	}
}

// TestWorkflowExplainGoldens pins the explain acceptance facts for all
// four goldens: graph size, maximum fan-out, proposal-producing nodes with
// guaranteed dispositions, a real mermaid DAG, and every report section.
// This is also bead yaah-0u0v.10's explain criterion (explain runs over
// the golden corpus with meaningful output).
func TestWorkflowExplainGoldens(t *testing.T) {
	cases := []struct {
		name     string
		nodes    int
		maxFan   int
		proposed int
	}{
		{"sequential", 2, 0, 0},
		{"fanout_settle", 7, 4, 1},
		{"speculative", 6, 3, 2},
		{"bounded_repair", 6, 0, 2},
	}
	sections := []string{"nodes", "dag (mermaid)", "fan-out", "cost", "authority union"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runWfExplain(t, wfGoldens+tc.name+".yaml")
			if err != nil {
				t.Fatalf("explain %s: %v\noutput:\n%s", tc.name, err, out)
			}
			for _, s := range sections {
				if !strings.Contains(out, s) {
					t.Errorf("text report missing %q section:\n%s", s, out)
				}
			}

			out, err = runWfExplain(t, "--json", wfGoldens+tc.name+".yaml")
			if err != nil {
				t.Fatalf("explain --json %s: %v", tc.name, err)
			}
			rep := wfJSONReport[explainReport](t, out)
			if rep.Kind != explainJSONKind {
				t.Errorf("kind = %q, want %q", rep.Kind, explainJSONKind)
			}
			if rep.Status != "valid" || !rep.OK {
				t.Errorf("status/ok = %q/%v, want valid/true", rep.Status, rep.OK)
			}
			if rep.Graph.Nodes != tc.nodes {
				t.Errorf("graph nodes = %d, want %d", rep.Graph.Nodes, tc.nodes)
			}
			if rep.Fanout.MaxItems != tc.maxFan {
				t.Errorf("fanout max_items = %d, want %d", rep.Fanout.MaxItems, tc.maxFan)
			}
			if len(rep.Settlement.Proposals) != tc.proposed {
				t.Errorf("proposal producers = %d, want %d", len(rep.Settlement.Proposals), tc.proposed)
			}
			for _, p := range rep.Settlement.Proposals {
				if len(p.Dispositions) == 0 {
					t.Errorf("proposal %s reports no guaranteed dispositions", p.Proposal)
				}
			}
			if !strings.Contains(rep.Mermaid, "flowchart") {
				t.Errorf("mermaid does not look like a DAG: %q", rep.Mermaid)
			}
			if len(rep.Nodes) != tc.nodes {
				t.Errorf("node list = %d entries, want %d", len(rep.Nodes), tc.nodes)
			}
		})
	}
}

// TestWorkflowExplainParams pins --param wiring: a required string param
// becomes set from the flag (source "param"), while the declared literal
// max_parallel is unaffected — params feed templates, not graph bounds.
func TestWorkflowExplainParams(t *testing.T) {
	out, err := runWfExplain(t, "--json", "--param", "task=verify", "--param", "max_workstreams=2", wfGoldens+"fanout_settle.yaml")
	if err != nil {
		t.Fatalf("explain with params: %v", err)
	}
	rep := wfJSONReport[explainReport](t, out)
	var task *paramView
	for i := range rep.Params {
		if rep.Params[i].Name == "task" {
			task = &rep.Params[i]
		}
	}
	if task == nil {
		t.Fatalf("param task missing from report: %+v", rep.Params)
	}
	if task.Value != "verify" || task.Source != "param" {
		t.Errorf("task = %v (source %s), want verify (source param)", task.Value, task.Source)
	}
	// The golden declares max_parallel: 4 as a literal; params feed the
	// prompt template only, so the fan-out bound must not drift with them.
	if rep.Fanout.MaxItems != 4 {
		t.Errorf("fanout max_items = %d, want 4 (declared literal must not drift with params)", rep.Fanout.MaxItems)
	}
}

// TestWorkflowJSONDeterministic pins that explain --json is byte-identical
// across runs for the same document — the stability contract tools rely on.
func TestWorkflowJSONDeterministic(t *testing.T) {
	out1, err := runWfExplain(t, "--json", wfGoldens+"fanout_settle.yaml")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	out2, err := runWfExplain(t, "--json", wfGoldens+"fanout_settle.yaml")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if out1 != out2 {
		t.Error("explain --json output differs between identical runs")
	}
}

// TestWorkflowWritesNoState pins the static-analysis-only contract: with a
// scratch cwd and scratch YAAH_HOME, validate and explain leave both
// directories empty — no run state, no database, no worktrees.
func TestWorkflowWritesNoState(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	// Read the golden BEFORE chdir — the ../../ constant is relative to the
	// package directory, which t.Chdir leaves behind.
	spec, err := os.ReadFile(wfGoldens + "fanout_settle.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("YAAH_HOME", home)

	specPath := cwd + "/fanout_settle.yaml"
	if err := os.WriteFile(specPath, spec, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runWfValidate(t, specPath); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := runWfExplain(t, "--json", specPath); err != nil {
		t.Fatalf("explain: %v", err)
	}

	for _, dir := range []string{cwd, home} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if dir == cwd && e.Name() == "fanout_settle.yaml" {
				continue // the spec we placed ourselves
			}
			t.Errorf("command wrote %s into %s — static analysis must not write state", e.Name(), dir)
		}
	}
}

// TestWorkflowReviewFixPins pins the review-driven contracts (#1640):
// never-null settlement fields, loops visible in the repetition survey,
// no fabricated world/network facts for non-executing kinds, a JSON report
// even on load failure, and status surviving --strict.
func TestWorkflowReviewFixPins(t *testing.T) {
	t.Run("settlement never null", func(t *testing.T) {
		out, err := runWfExplain(t, "--json", "--param", "task=x", wfGoldens+"fanout_settle.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, `"open_at_exit": null`) || strings.Contains(out, `"dispositions": null`) {
			t.Errorf("settlement fields serialized as null:\n%s", out)
		}
		var rep explainReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.Settlement.OpenAtExit == nil {
			t.Error("open_at_exit is nil after decode; contract says []")
		}
	})
	t.Run("loop in repetition survey", func(t *testing.T) {
		out, err := runWfExplain(t, "--json", "--param", "task=x", wfGoldens+"bounded_repair.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var rep explainReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range rep.Fanout.Containers {
			if c.Kind == "loop" && strings.HasSuffix(c.Path, "repair") {
				found = true
				if c.Literal != 3 {
					t.Errorf("loop literal_items = %d, want declared max_iterations 3", c.Literal)
				}
			}
		}
		if !found {
			t.Errorf("bounded_repair's loop missing from the repetition survey: %+v", rep.Fanout.Containers)
		}
	})
	t.Run("no fabricated authority facts", func(t *testing.T) {
		out, err := runWfExplain(t, "--json", "--param", "task=x", wfGoldens+"fanout_settle.yaml")
		if err != nil {
			t.Fatal(err)
		}
		var rep explainReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		for _, n := range rep.Nodes {
			switch n.Kind {
			case "settle", "emit", "branch", "collect":
				if n.World != "" || n.Network != "" {
					t.Errorf("%s (%s) reports world=%q network=%q — kinds that dispatch nothing must not state an execution environment", n.Path, n.Kind, n.World, n.Network)
				}
			}
			if n.NetworkInherited && rep.Env.HostNetwork != "" && n.Network != rep.Env.HostNetwork {
				t.Errorf("%s: network %q marked inherited but differs from host network %q", n.Path, n.Network, rep.Env.HostNetwork)
			}
			if strings.HasSuffix(n.Path, "body[0]") && n.Network == "deny_all" && n.NetworkInherited {
				// deny_all is declared by the golden's spec defaults — a
				// declared network must never ride on an inherited star
				t.Errorf("%s: declared network %q marked inherited", n.Path, n.Network)
			}
		}
	})
	t.Run("load failure emits report", func(t *testing.T) {
		out, err := runWfValidate(t, "--json", "no/such/file.yaml")
		if err == nil {
			t.Fatal("expected nonzero exit for missing file")
		}
		var rep validateReport
		if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
			t.Fatalf("stdout is not a parseable report (%v):\n%s", jerr, out)
		}
		if rep.Status != "unresolved" || rep.OK {
			t.Errorf("status/ok = %q/%v, want unresolved/false", rep.Status, rep.OK)
		}
		if len(rep.Errors) == 0 || rep.Errors[0].Code != "WF001" {
			t.Errorf("errors = %v, want synthetic WF001", rep.Errors)
		}
	})
	t.Run("strict never rewrites status", func(t *testing.T) {
		out, err := runWfValidate(t, "--strict", "--json", wfGoldens+"sequential.yaml")
		if err == nil {
			t.Skip("host role registry made sequential warning-free; nothing to promote")
		}
		var rep validateReport
		if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
			t.Fatalf("stdout is not a parseable report (%v):\n%s", jerr, out)
		}
		if rep.Status != "valid" {
			t.Errorf("status = %q under --strict with zero errors, want valid (strict flips ok, not status)", rep.Status)
		}
		if rep.OK || len(rep.Errors) != 0 || len(rep.Warnings) == 0 {
			t.Errorf("ok=%v errors=%d warnings=%d, want false/0/>0", rep.OK, len(rep.Errors), len(rep.Warnings))
		}
	})
	t.Run("mermaid escapes labels", func(t *testing.T) {
		// Step ids are not charset-restricted by the decoder, so an
		// adversarial id must not be able to break the diagram.
		p := t.TempDir() + "/esc.yaml"
		src := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata:\n  name: esc\nspec:\n  steps:\n    - id: 'we\"ird#1'\n      kind: delegate\n      role: developer\n      prompt: p\n"
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runWfExplain(t, "--json", p)
		if err != nil {
			t.Fatalf("explain: %v\n%s", err, out)
		}
		var rep explainReport
		if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
			t.Fatal(jerr)
		}
		if strings.Contains(rep.Mermaid, `we"ird`) {
			t.Errorf("raw quote reached the mermaid label:\n%s", rep.Mermaid)
		}
		if !strings.Contains(rep.Mermaid, "#quot;") || !strings.Contains(rep.Mermaid, "#35;") {
			t.Errorf("label not entity-escaped:\n%s", rep.Mermaid)
		}
	})
}

// TestWorkflowExplainMermaidDirection pins the diagram's two direction
// contracts: a needs edge is recorded declarer-first but DRAWN dependency-first
// (so the picture reads in execution order), while a gate's on_pass/on_fail
// arms keep their declared direction. Shape and fan-out width are pinned too.
func TestWorkflowExplainMermaidDirection(t *testing.T) {
	out, err := runWfExplain(t, "--json", "--param", "task=verify", wfGoldens+"bounded_repair.yaml")
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	var rep explainReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatal(jerr)
	}
	m := rep.Mermaid
	// bounded_repair's chain: implement (n0) -> repair (n1, loop) -> land
	// (n4, settle) -> report (n5, emit). Needs edges draw dependency-first.
	if !strings.Contains(m, "n0 -->|needs| n1") {
		t.Errorf("implement->repair needs edge not drawn dependency-first:\n%s", m)
	}
	if !strings.Contains(m, "n1 -->|needs| n4") {
		t.Errorf("repair->land needs edge not drawn dependency-first:\n%s", m)
	}
	if !strings.Contains(m, "n4 -->|needs| n5") {
		t.Errorf("land->report needs edge not drawn dependency-first:\n%s", m)
	}
	// Shapes: decisions are diamonds, outputs are stadiums, settlement is
	// rounded. bounded_repair has no gate, so pin the settle and emit
	// shapes here.
	if !strings.Contains(m, `n4("`) {
		t.Errorf("settle not drawn rounded:\n%s", m)
	}
	if !strings.Contains(m, `n5(["`) {
		t.Errorf("emit not drawn as a stadium:\n%s", m)
	}
	// The loop subgraph names its declared bound.
	if !strings.Contains(m, "loop ×3") {
		t.Errorf("loop width missing from the subgraph label:\n%s", m)
	}
}

// TestWorkflowExplainMermaidLiteralItems pins the per-item expansion: a fan-out
// over a LITERAL list draws its body once per item, labeled with the item it
// binds and carrying the instance path the executor will mint
// ("steps.x.items[i].body[j]"), while the subgraph label keeps its ×N width —
// width at a glance, items in detail. Selector fan-outs and loops keep the
// single-template form (their items and iterations are not static parallel
// items), nested literal fan-outs expand recursively, and a literal list past
// the cap falls back to the template form.
func TestWorkflowExplainMermaidLiteralItems(t *testing.T) {
	mermaidOf := func(t *testing.T, spec string, extra ...string) string {
		t.Helper()
		args := append([]string{"--json"}, extra...)
		out, err := runWfExplain(t, append(args, spec)...)
		if err != nil {
			t.Fatalf("explain %s: %v\n%s", spec, err, out)
		}
		var rep explainReport
		if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
			t.Fatalf("%s: %v", spec, jerr)
		}
		return rep.Mermaid
	}
	// The goldens take a required task parameter; the temp specs declare none.
	goldenMermaid := func(t *testing.T, name string) string {
		t.Helper()
		return mermaidOf(t, wfGoldens+name+".yaml", "--param", "task=t")
	}
	writeSpec := func(t *testing.T, name, src string) string {
		t.Helper()
		p := t.TempDir() + "/" + name
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// declaredIDs returns the node ids the leaf declaration lines open with,
	// for the lines whose label carries an item annotation.
	declaredIDs := func(t *testing.T, m string) map[string]bool {
		t.Helper()
		ids := map[string]bool{}
		for _, line := range strings.Split(m, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, "item: ") || strings.HasPrefix(trimmed, "subgraph ") {
				continue
			}
			end := strings.IndexAny(trimmed, "[({")
			if end <= 0 {
				t.Fatalf("cannot read a node id from %q", line)
			}
			ids[trimmed[:end]] = true
		}
		return ids
	}

	t.Run("literal fan-out draws one body copy per item", func(t *testing.T) {
		m := goldenMermaid(t, "speculative")
		// speculative fans out over [conservative, idiomatic, minimal] with a
		// single delegate body, so each item owns one declared copy.
		for i, style := range []string{"conservative", "idiomatic", "minimal"} {
			path := fmt.Sprintf("steps.candidates.items[%d].body[0]", i)
			if !strings.Contains(m, path) {
				t.Errorf("item %d body copy %q missing:\n%s", i, path, m)
			}
			if !strings.Contains(m, "item: "+style) {
				t.Errorf("item %d not labeled item: %s:\n%s", i, style, m)
			}
		}
		if got := len(declaredIDs(t, m)); got != 3 {
			t.Errorf("declared item copies = %d, want 3 (one per literal item):\n%s", got, m)
		}
		// The synthesized ids are declarations only; the template body is not
		// drawn beside them.
		if strings.Contains(m, `"steps.candidates.body[0] (`) {
			t.Errorf("template body drawn beside the per-item copies:\n%s", m)
		}
		// The width stays on the label, so the diagram states it twice on
		// purpose: at a glance, and item by item.
		if !strings.Contains(m, "(fanout ×3)") {
			t.Errorf("fan-out width dropped from the subgraph label:\n%s", m)
		}
	})

	t.Run("selector fan-out keeps the template", func(t *testing.T) {
		m := goldenMermaid(t, "fanout_settle")
		// fanout_settle fans over selectors: the items are a run-time fact, so
		// drawing invented ones would lie about the document.
		if strings.Contains(m, ".items[") || strings.Contains(m, "item: ") {
			t.Errorf("selector fan-out drew invented items:\n%s", m)
		}
		if !strings.Contains(m, "(fanout ×N)") {
			t.Errorf("selector fan-out width missing from the subgraph label:\n%s", m)
		}
	})

	t.Run("loop gains no per-iteration copies", func(t *testing.T) {
		m := goldenMermaid(t, "bounded_repair")
		// bounded_repair repairs up to three times: iterations are sequential,
		// not parallel items, so the two body nodes stay one template.
		if got := strings.Count(m, "steps.repair.body["); got != 2 {
			t.Errorf("loop body declarations = %d, want 2 (fix and check):\n%s", got, m)
		}
		if strings.Contains(m, "item: ") || strings.Contains(m, ".items[") {
			t.Errorf("loop iterations drawn as items:\n%s", m)
		}
		if !strings.Contains(m, "(loop ×3)") {
			t.Errorf("loop bound missing from the subgraph label:\n%s", m)
		}
	})

	t.Run("nested literal fan-outs expand recursively", func(t *testing.T) {
		src := `apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: nested
spec:
  steps:
    - id: outer
      kind: fanout
      over: [a, b]
      as: x
      max_parallel: 2
      join: all
      on_item_failure: collect_failures
      body:
        kind: fanout
        over: [p, q]
        as: y
        max_parallel: 2
        join: all
        on_item_failure: collect_failures
        body:
          kind: delegate
          role: developer
          prompt: p
          output: proposal
        collect: inner
      collect: outer_out
`
		p := writeSpec(t, "nested.yaml", src)
		m := mermaidOf(t, p)
		// The whole subtree is static, and the instance paths chain exactly as
		// exec does.
		for _, outer := range []int{0, 1} {
			for _, inner := range []int{0, 1} {
				path := fmt.Sprintf("steps.outer.items[%d].body[0].items[%d].body[0]", outer, inner)
				if !strings.Contains(m, path) {
					t.Errorf("nested copy %q missing:\n%s", path, m)
				}
			}
		}
		for _, item := range []string{"p", "q"} {
			if got := strings.Count(m, "item: "+item); got != 2 {
				t.Errorf("inner item %q appears %d times, want 2 (once per outer item):\n%s", item, got, m)
			}
		}
		if got := len(declaredIDs(t, m)); got != 4 {
			t.Errorf("declared copies = %d, want 4 (2 outer items × 2 inner items):\n%s", got, m)
		}
		// The nested container is itself copied: one subgraph per outer item.
		if got := strings.Count(m, "subgraph "); got != 3 {
			t.Errorf("subgraphs = %d, want 3 (the outer fan-out plus one inner copy per outer item):\n%s", got, m)
		}
	})

	t.Run("past the cap the template and width carry it", func(t *testing.T) {
		items := make([]string, 0, 13)
		for i := 0; i < 13; i++ {
			items = append(items, fmt.Sprintf("v%d", i))
		}
		src := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata:\n  name: wide\nspec:\n  steps:\n" +
			"    - id: f\n      kind: fanout\n      over: [" + strings.Join(items, ", ") + "]\n" +
			"      as: x\n      max_parallel: 13\n      join: all\n      on_item_failure: collect_failures\n" +
			"      body:\n        kind: delegate\n        role: developer\n        prompt: p\n        output: proposal\n" +
			"      collect: out\n"
		p := writeSpec(t, "wide.yaml", src)
		m := mermaidOf(t, p)
		if strings.Contains(m, "item: ") || strings.Contains(m, ".items[") {
			t.Errorf("a 13-item fan-out was expanded past the cap:\n%s", m)
		}
		if !strings.Contains(m, "steps.f.body[0]") {
			t.Errorf("single-template form not drawn:\n%s", m)
		}
		if !strings.Contains(m, "(fanout ×13)") {
			t.Errorf("width missing from the subgraph label:\n%s", m)
		}
	})
}

// ---------------------------------------------------------------- call nodes

// wfCallRoot writes a named subworkflow set into a temp directory and returns
// the root path. Each doc maps name -> file body; names ending .json keep
// their extension, everything else writes <name>.yaml.
func wfCallRoot(t *testing.T, docs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range docs {
		path := filepath.Join(root, name+".yaml")
		if strings.HasSuffix(name, ".json") {
			path = filepath.Join(root, name)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const wfCallParent = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: callparent}
spec:
  steps:
    - {id: plan, kind: delegate, role: developer, prompt: plan}
    - {id: sub, kind: call, workflow: helper, needs: [plan], params: {task: "{{ steps.plan.output }}"}}
    - {id: report, kind: emit, needs: [sub], value: "{{ steps.sub.output.summary }}"}
`

const wfCallHelper = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: helper}
spec:
  params:
    task: {type: string, required: true}
  steps:
    - {id: work, kind: delegate, role: developer, prompt: "do {{ params.task }}"}
    - {id: summary, kind: emit, needs: [work], value: "done"}
`

// TestWorkflowValidateCallsWithRegistry pins the CLI's --workflow-root: a
// document that calls a named subworkflow validates clean against a registry
// that resolves it, and the same document is refused fail-closed without the
// flag.
func TestWorkflowValidateCallsWithRegistry(t *testing.T) {
	root := wfCallRoot(t, map[string]string{"helper": wfCallHelper})
	spec := filepath.Join(root, "parent.yaml")
	if err := os.WriteFile(spec, []byte(wfCallParent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without the flag: fail closed, and the finding says what to pass.
	out, err := runWfValidate(t, spec)
	if err == nil {
		t.Fatalf("validate without --workflow-root exited clean:\n%s", out)
	}
	if !strings.Contains(out, "WF002") || !strings.Contains(out, "--workflow-root") {
		t.Errorf("the fail-closed finding does not name the flag:\n%s", out)
	}

	// With the flag: the named call resolves, the child validates like the
	// root, and the parent is valid.
	out, err = runWfValidate(t, "--workflow-root", root, spec)
	if err != nil {
		t.Fatalf("validate --workflow-root: %v\noutput:\n%s", err, out)
	}
	if !statusValidRE.MatchString(out) {
		t.Errorf("expected 'status valid' line, got:\n%s", out)
	}
}

// TestWorkflowValidateCallChildFindings pins child attribution: a subworkflow
// under the root that carries its own defect reports that defect through the
// parent's report, prefixed with the document that carries it — a call into
// a broken workflow is a finding in the caller's analysis.
func TestWorkflowValidateCallChildFindings(t *testing.T) {
	root := wfCallRoot(t, map[string]string{
		"helper": strings.Replace(wfCallHelper, "needs: [work]", "needs: ghoststep", 1),
	})
	spec := filepath.Join(root, "parent.yaml")
	if err := os.WriteFile(spec, []byte(wfCallParent), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runWfValidate(t, "--workflow-root", root, spec)
	if err == nil {
		t.Fatalf("a broken child validated clean:\n%s", out)
	}
	if !strings.Contains(out, "WF002") || !strings.Contains(out, "subworkflow helper: ") {
		t.Errorf("the child's finding is not attributed to the subworkflow:\n%s", out)
	}
}

// TestWorkflowValidateCallCycle pins the cross-document refusal through the
// CLI: two documents calling each other are one WF003 no registry can bless.
func TestWorkflowValidateCallCycle(t *testing.T) {
	root := wfCallRoot(t, map[string]string{
		"cya": "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: cya}\nspec:\n" +
			"  steps:\n    - {id: sub, kind: call, workflow: cyb}\n",
		"cyb": "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: cyb}\nspec:\n" +
			"  steps:\n    - {id: sub, kind: call, workflow: cya}\n",
	})
	spec := filepath.Join(root, "cya.yaml")
	out, err := runWfValidate(t, "--workflow-root", root, spec)
	if err == nil {
		t.Fatalf("a cross-document cycle validated clean:\n%s", out)
	}
	if !strings.Contains(out, "WF003") || !strings.Contains(out, "cya -> cyb -> cya") {
		t.Errorf("the cycle finding does not name the loop:\n%s", out)
	}
}

// TestWorkflowValidateCallTraversalName pins containment through the CLI: a
// call whose name is a path shape is refused as a name, registry or not.
func TestWorkflowValidateCallTraversalName(t *testing.T) {
	root := wfCallRoot(t, map[string]string{"helper": wfCallHelper})
	spec := filepath.Join(root, "parent.yaml")
	if err := os.WriteFile(spec, []byte(strings.Replace(wfCallParent, "workflow: helper", `workflow: "../secrets"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runWfValidate(t, "--workflow-root", root, spec)
	if err == nil {
		t.Fatalf("a traversal-shaped call name validated clean:\n%s", out)
	}
	if !strings.Contains(out, "WF002") || !strings.Contains(out, "not a subworkflow name") {
		t.Errorf("the traversal refusal is not the name-shape finding:\n%s", out)
	}
}
