package validate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// ------------------------------------------------------------------ helpers

// readFixtureFile reads one document from disk, by a path relative to this
// package directory. Cross-package corpora are addressed by relative path
// rather than copied, so the goldens stay a single source of truth.
func readFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// render spells findings out for a failure message, one per line.
func render(diags []Diagnostic) string {
	if len(diags) == 0 {
		return "\n\t(none)"
	}
	var b strings.Builder
	for _, d := range diags {
		b.WriteString("\n\t")
		b.WriteString(d.String())
	}
	return b.String()
}

// codeList is the emission sequence, which Structural fixes by rule order:
// comparing it catches a rule that stopped running, not just a rule that
// started wrong.
func codeList(diags []Diagnostic) string {
	parts := make([]string, len(diags))
	for i, d := range diags {
		parts[i] = d.Code
	}
	return strings.Join(parts, " ")
}

func seq(codes ...string) string { return strings.Join(codes, " ") }

// findingAt pulls the diagnostic for one rule at one node path, the anchor
// an author navigates to. It fails the test when the rule stayed silent.
func findingAt(t *testing.T, diags []Diagnostic, code, path string) Diagnostic {
	t.Helper()
	for _, d := range diags {
		if d.Code == code && d.Path == path {
			return d
		}
	}
	t.Fatalf("no %s diagnostic at %s; got:%s", code, path, render(diags))
	return Diagnostic{}
}

// documentWith wraps a steps block in the v1 envelope, the shorthand
// internal/workflow/ir's tests use so a table row shows only what matters.
// The workflow name is fixed and known to the call-depth rule: a call node
// naming "table-driven" calls into its own workflow.
func documentWith(steps string) []byte {
	return []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: table-driven}\nspec:\n" + steps)
}

// findingsOf runs the real pipeline — spec decode, ir.Resolve, Structural —
// and fails when the document never becomes a graph. Rules whose malformation
// Resolve refuses earlier are covered on hand-built graphs instead, never by
// loosening this helper.
func findingsOf(t *testing.T, steps string) []Diagnostic {
	t.Helper()
	g, diags := Validate(documentWith(steps))
	if g == nil {
		t.Fatalf("document did not resolve, cannot exercise the graph rules:%s", render(diags))
	}
	return diags
}

// ---------------------------------------------------------------- goldens

// TestGoldenCorpusHasNoStructuralFindings is the P0 exit criterion: every
// accepted workflow document in the epic decodes, resolves, and validates
// with zero diagnostics.
//
// The corpora are enumerated dynamically. golden/ holds the canonical-JSON
// artifacts the spec package pins today and may hold .yaml documents later,
// and the corpus grows as siblings land goldens; a hardcoded name list would
// silently keep passing over a new document that the rules reject.
func TestGoldenCorpusHasNoStructuralFindings(t *testing.T) {
	corpora := []struct {
		name     string
		patterns []string
	}{
		{
			// Accepted artifacts. schema.golden.json is deliberately not in
			// either pattern: it is the document schema, not a workflow, and
			// the strict decoder is right to refuse it.
			name:     "spec goldens",
			patterns: []string{"golden/*.canonical.json", "golden/*.yaml", "golden/*.yml"},
		},
		{
			name:     "spec valid fixtures",
			patterns: []string{"valid/*.yaml", "valid/*.yml", "valid/*.json"},
		},
		{
			name:     "ir spec fixtures",
			patterns: []string{filepath.Join("..", "ir", "testdata", "specs", "*.yaml")},
		},
	}

	total := 0
	for _, corpus := range corpora {
		t.Run(corpus.name, func(t *testing.T) {
			var files []string
			for _, pattern := range corpus.patterns {
				rel := pattern
				if !filepath.IsAbs(pattern) && !strings.HasPrefix(pattern, "..") {
					rel = filepath.Join("..", "spec", "testdata", pattern)
				}
				matches, err := filepath.Glob(rel)
				if err != nil {
					t.Fatalf("glob %s: %v", rel, err)
				}
				files = append(files, matches...)
			}
			sort.Strings(files)
			if len(files) == 0 {
				t.Fatalf("no workflow documents matched corpus %q", corpus.name)
			}
			for _, file := range files {
				name := filepath.Base(file)
				t.Run(name, func(t *testing.T) {
					g, diags := Validate(readFixtureFile(t, file))
					if g == nil {
						t.Fatalf("%s: document did not resolve:%s", file, render(diags))
					}
					if len(g.Steps) == 0 {
						t.Errorf("%s: resolved to a graph with no steps", file)
					}
					if len(diags) != 0 {
						t.Errorf("%s: want zero structural findings, got:%s", file, render(diags))
					}
					total++
				})
			}
		})
	}
	if total < 9 {
		t.Errorf("only %d golden documents validated; the corpora look truncated", total)
	}
}

// ---------------------------------------------------------------- adversarial

// adversarialCase is one malformed document and the findings the pipeline
// must produce for it. wantCodes pins the whole emission sequence, so a rule
// that goes quiet fails even when the primary finding is still right.
type adversarialCase struct {
	file      string
	wantCodes string
	code      string
	path      string
	sub       string
}

var adversarialCases = []adversarialCase{
	{
		file:      "needs_self_cycle.yaml",
		wantCodes: seq(CodeWF003, CodeWF004),
		code:      CodeWF003,
		path:      "steps.loop_a.needs",
		sub:       "needs cycle: loop_a -> loop_a",
	},
	{
		file:      "needs_two_cycle.yaml",
		wantCodes: seq(CodeWF003, CodeWF004, CodeWF004),
		code:      CodeWF003,
		path:      "steps.a.needs",
		sub:       "needs cycle: a -> b -> a",
	},
	{
		file:      "needs_long_cycle.yaml",
		wantCodes: seq(CodeWF003, CodeWF004, CodeWF004, CodeWF004, CodeWF004, CodeWF004),
		code:      CodeWF003,
		path:      "steps.a.needs",
		sub:       "needs cycle: a -> b -> c -> d -> e -> a",
	},
	{
		// WF003 names the island's loop once; WF004 names every step the
		// loop strands, including the one that only follows the island.
		file:      "unreachable_island.yaml",
		wantCodes: seq(CodeWF003, CodeWF004, CodeWF004, CodeWF004),
		code:      CodeWF004,
		path:      "steps.tail",
		sub:       `need "lone_a" is not reachable`,
	},
	{
		file:      "duplicate_output_names.yaml",
		wantCodes: seq(CodeWF005),
		code:      CodeWF005,
		path:      "steps.second.output.name",
		sub:       `output name "result" is already bound at steps.first`,
	},
	{
		file:      "duplicate_body_output_names.yaml",
		wantCodes: seq(CodeWF005),
		code:      CodeWF005,
		path:      "steps.fan.body[1].output.name",
		sub:       "already bound at steps.fan.body[0]",
	},
	{
		file:      "malformed_output_schema.yaml",
		wantCodes: seq(CodeWF005),
		code:      CodeWF005,
		path:      "steps.plan.output.schema",
		sub:       `type "banana" is not a JSON type`,
	},
	{
		file:      "call_self_no_max_depth.yaml",
		wantCodes: seq(CodeWF003),
		code:      CodeWF003,
		path:      "steps.again.max_depth",
		sub:       `call names its own workflow "recursion-abuse" and must declare max_depth`,
	},
	{
		// A dangling needs never reaches WF002 through a document:
		// ir.Resolve refuses it, and the seam reports the stage failure as
		// WF001. The identical defect on a hand-built graph is WF002 — see
		// TestHandBuiltGraphReferences.
		file:      "dangling_needs.yaml",
		wantCodes: seq(CodeWF001),
		code:      CodeWF001,
		path:      "steps.b.needs",
		sub:       `unknown step id "ghost"`,
	},
}

// TestAdversarialFixtures runs every malformed document through the real
// pipeline and asserts the stable code, the node path it is anchored at, and
// a phrase from the message.
func TestAdversarialFixtures(t *testing.T) {
	for _, tc := range adversarialCases {
		t.Run(strings.TrimSuffix(tc.file, ".yaml"), func(t *testing.T) {
			data := readFixtureFile(t, filepath.Join("testdata", "adversarial", tc.file))
			g, diags := Validate(data)
			if got := codeList(diags); got != tc.wantCodes {
				t.Errorf("emission sequence = %q, want %q;%s", got, tc.wantCodes, render(diags))
			}
			if tc.code == CodeWF001 {
				if g != nil {
					t.Error("a WF001 document must not yield a graph")
				}
			} else if g == nil {
				t.Fatal("document did not resolve, so the graph rules never ran")
			}
			d := findingAt(t, diags, tc.code, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("%s message = %q, want it to contain %q", tc.code, d.Message, tc.sub)
			}
		})
	}
}

// TestAdversarialCorpusIsFullyCovered keeps the fixture directory and the
// expectation table from drifting apart in either direction: a fixture
// nobody asserts on is a rule that can regress silently, and a table row
// with no file behind it proves nothing.
func TestAdversarialCorpusIsFullyCovered(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "adversarial", "*.yaml"))
	if err != nil {
		t.Fatalf("glob adversarial fixtures: %v", err)
	}
	if len(files) < len(adversarialCases) {
		t.Errorf("fixture directory holds %d files, the table expects %d", len(files), len(adversarialCases))
	}
	covered := make(map[string]bool, len(adversarialCases))
	for _, tc := range adversarialCases {
		covered[tc.file] = true
	}
	for _, file := range files {
		base := filepath.Base(file)
		if !covered[base] {
			t.Errorf("fixture %s has no expectations in adversarialCases", base)
		}
	}
	for _, tc := range adversarialCases {
		if _, err := os.Stat(filepath.Join("testdata", "adversarial", tc.file)); err != nil {
			t.Errorf("expected fixture %s is missing: %v", tc.file, err)
		}
	}
}

// ------------------------------------------------------------------ WF001

// TestDecodeFailuresSurfaceAsWF001 walks the spec package's whole invalid
// corpus. Every one of those documents must fail before the graph exists,
// with exactly one WF001 finding carrying the decoder's own path — never a
// WF002–WF005 finding, which would mean a rule invented a graph out of a
// document that does not decode. The corpus is enumerated dynamically for
// the same reason the goldens are.
func TestDecodeFailuresSurfaceAsWF001(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "spec", "testdata", "invalid", "*"))
	if err != nil {
		t.Fatalf("glob invalid fixtures: %v", err)
	}
	if len(files) < 10 {
		t.Fatalf("only %d invalid fixtures found; the corpus looks truncated", len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			g, diags := Validate(readFixtureFile(t, file))
			if g != nil {
				t.Fatal("an invalid document yielded a graph")
			}
			if len(diags) != 1 {
				t.Fatalf("want exactly one finding, got:%s", render(diags))
			}
			if diags[0].Code != CodeWF001 {
				t.Errorf("code = %s, want %s", diags[0].Code, CodeWF001)
			}
			if diags[0].Message == "" {
				t.Error("WF001 finding carries no message")
			}
			if hasCode(diags, CodeWF002) || hasCode(diags, CodeWF003) ||
				hasCode(diags, CodeWF004) || hasCode(diags, CodeWF005) {
				t.Errorf("a document-level failure must not report graph rules:%s", render(diags))
			}
		})
	}
}

// TestWF001CarriesTheDecoderPath pins a few paths and messages verbatim, so
// the seam cannot quietly stop forwarding the decoder's location.
func TestWF001CarriesTheDecoderPath(t *testing.T) {
	cases := []struct {
		file string
		path string
		sub  string
	}{
		{"unknown_field.yaml", "spec.steps[0].kolect", `unknown field "kolect"`},
		{"unknown_field_root.json", "spec.stepz", `unknown field "stepz"`},
		{"unknown_kind.yaml", "spec.steps[0].kind", `unknown node kind "banana"`},
		{"malformed_output.yaml", "spec.steps[0].output", "got list"},
		{"bad_action.yaml", "spec.steps[1].action", `unknown SettleAction "merge"`},
		{"bad_param_type.yaml", "spec.params.task.type", `unknown ParamType "str"`},
		{"template_depth.yaml", "spec.steps[0].prompt", "template nesting depth"},
		{"wrong_apiversion.yaml", "apiVersion", "unknown APIVersion"},
		{"missing_apiversion.yaml", "apiVersion", `missing required field`},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data := readFixtureFile(t, filepath.Join("..", "spec", "testdata", "invalid", tc.file))
			_, diags := Validate(data)
			d := findingAt(t, diags, CodeWF001, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", d.Message, tc.sub)
			}
		})
	}
}

func TestDiagnoseClassifiesEachStage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Diagnostic
	}{
		{
			name: "decode error",
			err:  &spec.DecodeError{Path: "spec.steps[0].kolect", Message: `unknown field "kolect"`},
			want: Diagnostic{Code: CodeWF001, Path: "spec.steps[0].kolect", Message: `unknown field "kolect"`},
		},
		{
			// The seam must survive wrapping: a caller that adds context
			// with %w still gets the decoder's path, not the wrapper's text.
			name: "wrapped decode error",
			err:  fmt.Errorf("workflow kickoff.yaml: %w", &spec.DecodeError{Path: "apiVersion", Message: "missing required field \"apiVersion\""}),
			want: Diagnostic{Code: CodeWF001, Path: "apiVersion", Message: `missing required field "apiVersion"`},
		},
		{
			name: "resolve error",
			err:  &ir.ResolveError{Path: "steps.b.needs", Message: `unknown step id "ghost"`},
			want: Diagnostic{Code: CodeWF001, Path: "steps.b.needs", Message: `unknown step id "ghost"`},
		},
		{
			name: "wrapped resolve error",
			err:  fmt.Errorf("resolving: %w", &ir.ResolveError{Path: "steps.a", Message: "duplicate step id \"a\""}),
			want: Diagnostic{Code: CodeWF001, Path: "steps.a", Message: `duplicate step id "a"`},
		},
		{
			// An unclassified failure still becomes a WF001 rather than
			// disappearing: the document did not become a graph, whatever
			// the cause.
			name: "foreign error",
			err:  errors.New("open workflow.yaml: no such file"),
			want: Diagnostic{Code: CodeWF001, Message: "open workflow.yaml: no such file"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnose(tc.err)
			if len(got) != 1 {
				t.Fatalf("want one finding, got:%s", render(got))
			}
			if got[0] != tc.want {
				t.Errorf("finding = %+v, want %+v", got[0], tc.want)
			}
		})
	}
	if got := Diagnose(nil); len(got) != 0 {
		t.Errorf("Diagnose(nil) = %s, want no findings", render(got))
	}
}

// ------------------------------------------------------------------ WF003

func TestCallDepthBounds(t *testing.T) {
	cases := []struct {
		name    string
		steps   string
		code    string
		path    string
		sub     string
		clean   bool
		comment string
	}{
		{
			name:  "zero depth",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: other\n      max_depth: 0\n",
			code:  CodeWF003, path: "steps.again.max_depth", sub: "must be at least 1, got 0",
		},
		{
			name:  "negative depth",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: other\n      max_depth: -3\n",
			code:  CodeWF003, path: "steps.again.max_depth", sub: "must be at least 1, got -3",
		},
		{
			name:  "depth above the ceiling",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: other\n      max_depth: 99\n",
			code:  CodeWF003, path: "steps.again.max_depth", sub: "exceeds the limit 16",
		},
		{
			name:  "self call with no declared depth",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: table-driven\n",
			code:  CodeWF003, path: "steps.again.max_depth", sub: `names its own workflow "table-driven"`,
		},
		{
			name:  "depth exactly at the ceiling",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: other\n      max_depth: 16\n",
			clean: true,
		},
		{
			// A call into another workflow is unresolvable from one graph: the
			// registry bead owns that cycle, so no depth is demanded here.
			name:  "foreign call with no declared depth",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: other\n",
			clean: true,
		},
		{
			name:  "self call with a declared depth",
			steps: "  steps:\n    - id: again\n      kind: call\n      workflow: table-driven\n      max_depth: 3\n",
			clean: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := findingsOf(t, tc.steps)
			if tc.clean {
				if len(diags) != 0 {
					t.Fatalf("want zero findings, got:%s", render(diags))
				}
				return
			}
			d := findingAt(t, diags, tc.code, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", d.Message, tc.sub)
			}
		})
	}
}

// TestNeedsCycleIgnoresUnboundReference pins the division of labour: an
// unknown needs entry is WF002, and it must not also be reported as a cycle
// the reference cannot possibly form.
func TestNeedsCycleIgnoresUnboundReference(t *testing.T) {
	// Through a document the unbound entry is refused by ir.Resolve, so the
	// graph rules never see the defect at all.
	_, diags := Validate(documentWith(`  steps:
    - id: a
      kind: emit
      value: one
      needs: [ghost, a]
`))
	if got := codeList(diags); got != seq(CodeWF001) {
		t.Errorf("emission sequence = %q, want %q (Resolve refuses the reference first)%s", got, CodeWF001, render(diags))
	}

	// The same shape on a graph built by hand: one WF002 for the unbound
	// entry, one WF003 for the self-edge that survives beside it.
	g := &ir.Graph{
		Name: "hand-built",
		Steps: []*ir.Node{{
			Path:  "steps.a",
			ID:    "a",
			Kind:  ir.KindEmit,
			Needs: []string{"ghost", "a"},
		}},
	}
	got := Structural(g)
	if codeList(got) != seq(CodeWF002, CodeWF003, CodeWF004) {
		t.Errorf("hand-built sequence = %q, want %q;%s", codeList(got), seq(CodeWF002, CodeWF003, CodeWF004), render(got))
	}
	if d := findingAt(t, got, CodeWF002, "steps.a.needs[0]"); !strings.Contains(d.Message, `"ghost"`) {
		t.Errorf("WF002 message = %q, want it to name the unbound id", d.Message)
	}
	if d := findingAt(t, got, CodeWF003, "steps.a.needs"); !strings.Contains(d.Message, "a -> a") {
		t.Errorf("WF003 message = %q, want the self-edge chain", d.Message)
	}
}

// ------------------------------------------------------------------ WF004

// TestReachabilityCoversBodyNodes checks the two ways a body node can be
// stranded: its container is never entered, or its own needs chain leads
// only into a cycle.
func TestReachabilityCoversBodyNodes(t *testing.T) {
	cycle := `    - id: cyc_a
      kind: emit
      value: one
      needs: [cyc_b]
    - id: cyc_b
      kind: emit
      value: two
      needs: [cyc_a]
`
	cases := []struct {
		name  string
		steps string
		path  string
		sub   string
		codes string
	}{
		{
			name: "body node needed by a stranded step",
			steps: "  steps:\n" + cycle + `    - id: fan
      kind: fanout
      over: [x]
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: 'do {{ ws }}'
        needs: [cyc_a]
`,
			path:  "steps.fan.body[0]",
			sub:   `need "cyc_a" is not reachable`,
			codes: seq(CodeWF003, CodeWF004, CodeWF004, CodeWF004),
		},
		{
			name: "body node inside a stranded container",
			steps: "  steps:\n" + cycle + `    - id: fan
      kind: fanout
      over: [x]
      as: ws
      needs: [cyc_a]
      body:
        kind: delegate
        role: developer
        prompt: 'do {{ ws }}'
`,
			path:  "steps.fan.body[0]",
			sub:   "unreachable because its container steps.fan is",
			codes: seq(CodeWF003, CodeWF004, CodeWF004, CodeWF004, CodeWF004),
		},
		{
			name: "chained needs from an entry are reachable",
			steps: `  steps:
    - id: start
      kind: delegate
      role: planner
      prompt: plan it
    - id: middle
      kind: delegate
      role: developer
      prompt: build it
      needs: [start]
    - id: end
      kind: emit
      value: done
      needs: [middle]
`,
			codes: "",
		},
		{
			name: "a diamond joins back without a cycle",
			steps: `  steps:
    - id: root
      kind: delegate
      role: planner
      prompt: plan it
    - id: left
      kind: delegate
      role: developer
      prompt: build left
      needs: [root]
    - id: right
      kind: delegate
      role: developer
      prompt: build right
      needs: [root]
    - id: join
      kind: aggregate
      strategy: concat
      needs: [left, right]
`,
			codes: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := findingsOf(t, tc.steps)
			if got := codeList(diags); got != tc.codes {
				t.Errorf("emission sequence = %q, want %q;%s", got, tc.codes, render(diags))
			}
			if tc.path == "" {
				return
			}
			d := findingAt(t, diags, CodeWF004, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", d.Message, tc.sub)
			}
		})
	}
}

// ------------------------------------------------------------------ WF005

func TestOutputBindings(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		code  string
		path  string
		sub   string
		clean bool
	}{
		{
			name: "empty name",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: "", type: Report}
`,
			code: CodeWF005, path: "steps.a.output.name", sub: "must not be empty",
		},
		{
			name: "whitespace name",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: "   ", type: Report}
`,
			code: CodeWF005, path: "steps.a.output.name", sub: "must not be empty",
		},
		{
			name: "schema is not JSON",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, schema: '{"type": "object"', type: Report}
`,
			code: CodeWF005, path: "steps.a.output.schema", sub: "not valid JSON",
		},
		{
			name: "schema is a JSON list",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, schema: '[1, 2]', type: Report}
`,
			code: CodeWF005, path: "steps.a.output.schema", sub: "neither a schema name nor a JSON object",
		},
		{
			name: "schema is blank",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, schema: '   ', type: Report}
`,
			code: CodeWF005, path: "steps.a.output.schema", sub: "must not be blank",
		},
		{
			name: "schema has trailing content",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, schema: '{"type": "object"} {"type": "array"}', type: Report}
`,
			code: CodeWF005, path: "steps.a.output.schema", sub: "trailing content",
		},
		{
			name: "named schema reference",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, schema: report.v1, type: Report}
`,
			clean: true,
		},
		{
			name: "inline object schema",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: {name: Alpha, type: Report, schema: '{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}'}
`,
			clean: true,
		},
		{
			name: "distinct names across steps and bodies",
			steps: `  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: build it
      output: Alpha
    - id: fan
      kind: fanout
      over: [x]
      as: ws
      body:
        kind: delegate
        role: reviewer
        prompt: 'review {{ ws }}'
        output: Beta
`,
			clean: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := findingsOf(t, tc.steps)
			if tc.clean {
				if len(diags) != 0 {
					t.Fatalf("want zero findings, got:%s", render(diags))
				}
				return
			}
			d := findingAt(t, diags, tc.code, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", d.Message, tc.sub)
			}
		})
	}
}

// ------------------------------------------------------- hand-built graphs

// TestHandBuiltGraphReferences covers WF002 on graphs Resolve will not
// produce: a duplicate id and an unbound needs entry are refused during
// resolution, so the only way to exercise the graph-level authority is to
// hand the validator a graph built or edited outside the pipeline. That is
// also how a scheduler, a replay, or a later bead feeds it.
func TestHandBuiltGraphReferences(t *testing.T) {
	emit := func(path, id string, needs ...string) *ir.Node {
		return &ir.Node{Path: path, ID: id, Kind: ir.KindEmit, Needs: needs}
	}
	cases := []struct {
		name  string
		graph *ir.Graph
		code  string
		path  string
		sub   string
		codes string // full emission sequence, when the shape has several findings
	}{
		{
			name: "duplicate top-level ids",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.first", "a"),
				emit("steps.second", "a"),
			}},
			code: CodeWF002, path: "steps.second.id", sub: `duplicate step id "a" (first bound at steps.first)`,
			codes: seq(CodeWF002),
		},
		{
			name: "top-level step with no id",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps[0]", ""),
			}},
			code: CodeWF002, path: "steps[0].id", sub: "top-level step has no id",
			codes: seq(CodeWF002),
		},
		{
			name: "unbound needs entry",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.a", "a"),
				emit("steps.b", "b", "ghost"),
			}},
			code: CodeWF002, path: "steps.b.needs[0]", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002, CodeWF004),
		},
		{
			// WF004 joins on every need: one reachable need does not
			// rescue a node whose other need names no step.
			name: "mixed needs, one reachable and one dangling",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.a", "a"),
				emit("steps.b", "b", "a", "ghost"),
			}},
			code: CodeWF004, path: "steps.b", sub: `need "ghost" names no step`,
			codes: seq(CodeWF002, CodeWF004),
		},
		{
			name: "mixed needs, one reachable and one stranded in a cycle",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.entry", "entry"),
				emit("steps.x", "x", "y"),
				emit("steps.y", "y", "x"),
				emit("steps.tail", "tail", "entry", "x"),
			}},
			code: CodeWF004, path: "steps.tail", sub: `need "x" is not reachable`,
			codes: seq(CodeWF003, CodeWF004, CodeWF004, CodeWF004),
		},
		{
			name: "branch arm naming no step",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.a", "a"),
				{
					Path:    "steps.b",
					ID:      "b",
					Kind:    ir.KindBranch,
					Default: "a",
					Cases:   []ir.Case{{Value: "fast", Then: "ghost"}},
				},
			}},
			code: CodeWF002, path: "steps.b.cases[0].then", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002),
		},
		{
			name: "branch default naming no step",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				{Path: "steps.b", ID: "b", Kind: ir.KindBranch, Default: "ghost"},
			}},
			code: CodeWF002, path: "steps.b.default", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002),
		},
		{
			name: "gate dispositions naming no step",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				{Path: "steps.g", ID: "g", Kind: ir.KindGate, OnPass: "ghost", OnFail: "phantom"},
			}},
			code: CodeWF002, path: "steps.g.on_pass", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002, CodeWF002),
		},
		{
			name: "gate assert subject naming no step",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				{Path: "steps.g", ID: "g", Kind: ir.KindGate, Assert: &ir.Assert{Name: "all_succeeded", Args: map[string]any{"from": "ghost"}}},
			}},
			code: CodeWF002, path: "steps.g.assert.args.from", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002),
		},
		{
			name: "duplicate body-local ids",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout,
				Body: []*ir.Node{
					emit("steps.fan.body[0]", "dup"),
					emit("steps.fan.body[1]", "dup"),
				},
			}}},
			code: CodeWF002, path: "steps.fan.body[1].id", sub: `duplicate body-local id "dup"`,
			codes: seq(CodeWF002),
		},
		{
			name: "body-local id shadowing a step",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.root", "root"),
				{
					Path: "steps.fan", ID: "fan", Kind: ir.KindFanout,
					Body: []*ir.Node{emit("steps.fan.body[0]", "root")},
				},
			}},
			code: CodeWF002, path: "steps.fan.body[0].id", sub: `body-local id "root" shadows a top-level step id`,
			codes: seq(CodeWF002),
		},
		{
			name: "body node with an unbound needs entry",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				{
					Path: "steps.fan", ID: "fan", Kind: ir.KindFanout,
					Body: []*ir.Node{emit("steps.fan.body[0]", "", "ghost")},
				},
			}},
			code: CodeWF002, path: "steps.fan.body[0].needs[0]", sub: `unknown step id "ghost"`,
			codes: seq(CodeWF002, CodeWF004),
		},
		{
			name: "well-built graph reports nothing",
			graph: &ir.Graph{Name: "hand-built", Steps: []*ir.Node{
				emit("steps.a", "a"),
				emit("steps.b", "b", "a"),
				{
					Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, Needs: []string{"b"},
					Body: []*ir.Node{
						{
							Path: "steps.fan.body[0]", ID: "worker", Kind: ir.KindDelegate,
							Output: &ir.OutputSpec{Name: "Result", Schema: "result.v1"},
						},
					},
				},
			}},
			codes: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Structural(tc.graph)
			if got := codeList(diags); got != tc.codes {
				t.Errorf("emission sequence = %q, want %q;%s", got, tc.codes, render(diags))
			}
			if tc.path == "" {
				return
			}
			d := findingAt(t, diags, tc.code, tc.path)
			if !strings.Contains(d.Message, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", d.Message, tc.sub)
			}
		})
	}
}

// TestWalkIsBoundedByVisitedNodes covers the guard in graphNodes: a graph
// that aliases one node into two containers, or makes a body contain itself,
// must not send a validator into an endless walk. Resolve cannot build such
// a graph; an exported type can be edited into one.
func TestWalkIsBoundedByVisitedNodes(t *testing.T) {
	self := &ir.Node{Path: "steps.loop", ID: "loop", Kind: ir.KindFanout}
	self.Body = []*ir.Node{self}

	order, parent := graphNodes(&ir.Graph{Name: "aliased", Steps: []*ir.Node{self}})
	if len(order) != 1 {
		t.Fatalf("walk visited %d nodes, want the aliased node once", len(order))
	}
	if parent[order[0]] != nil {
		t.Error("a top-level step must have no container")
	}
	// Returning at all is the assertion; the findings themselves are not the
	// point of this test.
	Structural(&ir.Graph{Name: "aliased", Steps: []*ir.Node{self}})

	shared := &ir.Node{Path: "steps.shared", ID: "shared", Kind: ir.KindEmit}
	outer := &ir.Graph{Name: "aliased", Steps: []*ir.Node{
		{Path: "steps.one", ID: "one", Kind: ir.KindFanout, Body: []*ir.Node{shared}},
		{Path: "steps.two", ID: "two", Kind: ir.KindFanout, Body: []*ir.Node{shared}},
	}}
	order, _ = graphNodes(outer)
	if len(order) != 3 {
		t.Fatalf("walk visited %d nodes, want 3 (the shared body node once)", len(order))
	}
	if got := codeList(Structural(outer)); got != "" {
		t.Errorf("an aliased body node must not invent findings, got:%s", render(Structural(outer)))
	}
}

func TestNilGraphAndEmptyDocument(t *testing.T) {
	if got := Structural(nil); len(got) != 0 {
		t.Errorf("Structural(nil) = %s, want no findings", render(got))
	}
	if order, _ := graphNodes(nil); len(order) != 0 {
		t.Errorf("graphNodes(nil) visited %d nodes", len(order))
	}
	if len(stepsByID(nil)) != 0 {
		t.Error("stepsByID(nil) is not empty")
	}
	// A document with no steps is structurally vacuous: nothing to number,
	// nothing to reach, nothing to bind. Requiring at least one step is a
	// spec-package question (the decoder accepts an empty spec.steps) and is
	// recorded as such rather than invented under a structural code.
	_, diags := Validate([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: empty}\nspec:\n  steps: []\n"))
	if len(diags) != 0 {
		t.Errorf("an empty graph reported findings:%s", render(diags))
	}
}

// ------------------------------------------------------------- determinism

// TestStructuralIsDeterministic pins the ordering contract: the slice is a
// function of the graph alone, grouped by code and, inside a code, in
// document order. Callers diff and display findings without sorting.
func TestStructuralIsDeterministic(t *testing.T) {
	graph := &ir.Graph{
		Name: "everything-wrong",
		Steps: []*ir.Node{
			{Path: "steps.a", ID: "a", Kind: ir.KindEmit, Needs: []string{"b", "ghost"},
				Output: &ir.OutputSpec{Name: "result", Schema: "{oops"}},
			{Path: "steps.b", ID: "b", Kind: ir.KindEmit, Needs: []string{"a"},
				Output: &ir.OutputSpec{Name: "result"}},
			{Path: "steps.c", ID: "c", Kind: ir.KindCall, Workflow: "everything-wrong"},
		},
	}
	want := codeList(Structural(graph))
	for i := range 5 {
		if got := codeList(Structural(graph)); got != want {
			t.Fatalf("run %d differs: %q vs %q", i, got, want)
		}
	}
	diags := Structural(graph)
	for i := 1; i < len(diags); i++ {
		if diags[i-1].Code > diags[i].Code {
			t.Errorf("findings are not grouped by code: %s before %s in %q",
				diags[i-1].Code, diags[i].Code, codeList(diags))
		}
	}
	// The mixed graph above must land every structural code: WF002 for the
	// unbound needs entry, WF003 for the a/b cycle and the undeclared self
	// call, WF004 for both stranded steps, WF005 for the duplicate name and
	// the unparsable schema.
	for _, code := range []string{CodeWF002, CodeWF003, CodeWF004, CodeWF005} {
		if !hasCode(diags, code) {
			t.Errorf("no %s finding in a graph built to trigger every rule:%s", code, render(diags))
		}
	}
	if got := codeList(diags); got != seq(CodeWF002, CodeWF003, CodeWF003, CodeWF004, CodeWF004, CodeWF005, CodeWF005) {
		t.Errorf("emission sequence = %q;%s", got, render(diags))
	}
}

func TestDiagnosticString(t *testing.T) {
	cases := []struct {
		d    Diagnostic
		want string
	}{
		{Diagnostic{Code: CodeWF001, Message: "no path here"}, "WF001: no path here"},
		{Diagnostic{Code: CodeWF004, Path: "steps.tail", Message: "unreachable"}, "WF004 steps.tail: unreachable"},
	}
	for _, tc := range cases {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
