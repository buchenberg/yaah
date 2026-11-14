package yaah

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/validate"
	"github.com/spf13/cobra"
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
		{"call_self_no_max_depth.yaml", "invalid", []string{"WF003"}},
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
