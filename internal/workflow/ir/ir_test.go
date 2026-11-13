package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/spec"
)

var update = flag.Bool("update", false, "rewrite golden files")

func readTestdata(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func mustParse(t *testing.T, data []byte) *spec.Document {
	t.Helper()
	doc, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("spec.Parse: %v", err)
	}
	return doc
}

func TestResolveGoldenSpecs(t *testing.T) {
	for _, name := range []string{"sequential", "fanout_settle", "bounded_repair", "speculative"} {
		t.Run(name, func(t *testing.T) {
			doc := mustParse(t, readTestdata(t, filepath.Join("specs", name+".yaml")))
			g, err := Resolve(doc)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			got, err := GraphJSON(g)
			if err != nil {
				t.Fatalf("GraphJSON: %v", err)
			}
			goldenPath := filepath.Join("testdata", "golden", name+".graph.json")
			if *update {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want := readTestdata(t, filepath.Join("golden", name+".graph.json"))
			if !bytes.Equal(got, want) {
				t.Fatalf("resolved graph drifted from golden\nwant:\n%s\ngot:\n%s", want, got)
			}
			// Stability: resolving again yields byte-identical JSON.
			g2, err := Resolve(mustParse(t, readTestdata(t, filepath.Join("specs", name+".yaml"))))
			if err != nil {
				t.Fatalf("re-resolve: %v", err)
			}
			got2, err := GraphJSON(g2)
			if err != nil {
				t.Fatalf("GraphJSON(2): %v", err)
			}
			if !bytes.Equal(got, got2) {
				t.Fatal("resolution is not deterministic")
			}
		})
	}
}

func TestResolveJSONTwinMatchesYAML(t *testing.T) {
	doc := mustParse(t, readTestdata(t, filepath.Join("..", "..", "spec", "testdata", "valid", "fanout_settle.json")))
	g, err := Resolve(doc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, err := GraphJSON(g)
	if err != nil {
		t.Fatalf("GraphJSON: %v", err)
	}
	want := readTestdata(t, filepath.Join("golden", "fanout_settle.graph.json"))
	if !bytes.Equal(got, want) {
		t.Fatal("JSON twin resolves to a different graph")
	}
}

func goldenDoc(t *testing.T, name string) *spec.Document {
	t.Helper()
	return mustParse(t, readTestdata(t, filepath.Join("specs", name+".yaml")))
}

func TestDefaultsFoldIntoDelegates(t *testing.T) {
	g, err := Resolve(goldenDoc(t, "fanout_settle"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var build, plan *Node
	for _, s := range g.Steps {
		switch s.ID {
		case "build":
			build = s
		case "plan":
			plan = s
		}
	}
	if build == nil || plan == nil {
		t.Fatalf("missing steps: %v", g.Steps)
	}
	// The build body delegates inherit defaults.lane/network/grants.
	if len(build.Body) != 1 {
		t.Fatalf("build body = %d nodes", len(build.Body))
	}
	d := build.Body[0]
	if d.Lane != LaneProposal {
		t.Errorf("body delegate lane = %q, want proposal (folded from defaults)", d.Lane)
	}
	if d.Network != NetworkDenyAll {
		t.Errorf("body delegate network = %q, want deny_all (folded from defaults)", d.Network)
	}
	if len(d.Grants) != 2 || d.Grants[0].Path != "src" || d.Grants[0].Access != AccessReadWrite {
		t.Errorf("body delegate grants = %+v, want defaults folded in", d.Grants)
	}
	// plan overrides lane explicitly, so it keeps report.
	if plan.Lane != LaneReport {
		t.Errorf("plan lane = %q, want report (explicit)", plan.Lane)
	}
}

func TestStableNodePaths(t *testing.T) {
	g, err := Resolve(goldenDoc(t, "fanout_settle"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if g.Steps[1].Path != "steps.build" {
		t.Errorf("build path = %q", g.Steps[1].Path)
	}
	if len(g.Steps[1].Body) != 1 || g.Steps[1].Body[0].Path != "steps.build.body[0]" {
		t.Errorf("body path = %q, want steps.build.body[0]", g.Steps[1].Body[0].Path)
	}
}

func TestTemplateScopeErrors(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name: "undeclared param",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ params.nope }}"`,
			wantSub: `names an undeclared param "nope"`,
		},
		{
			name: "unknown step",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ steps.ghost.output }}"`,
			wantSub: `out-of-scope step "ghost"`,
		},
		{
			name: "bad steps field",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ steps.a.input }}"`,
			wantSub: `must use output or collect`,
		},
		{
			name: "collect on non-container",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ steps.a.collect }}"
    - id: b
      kind: emit
      value: done`,
			wantSub: `is a delegate and has no collect`,
		},
		{
			name: "item outside body",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ ws.brief }}"`,
			wantSub: `"ws" is not a step in scope`,
		},
		{
			name: "index outside body",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ index }}"`,
			wantSub: "index is only in scope inside a fan-out or loop body",
		},
		{
			name: "bad run field",
			src: `
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: "{{ run.phase }}"`,
			wantSub: "must be run.id or run.attempt",
		},
		{
			name: "unterminated brace",
			src: `
  steps:
    - id: a
      kind: emit
      value: "hello {{ name"`,
			wantSub: "unterminated",
		},
		{
			name: "nested braces",
			src: `
  steps:
    - id: a
      kind: emit
      value: "{{ x {{ y }} }}"`,
			wantSub: "do not nest",
		},
		{
			name: "forward body-local id",
			src: `
  steps:
    - id: fan
      kind: fanout
      over: [one]
      body:
        - id: first
          kind: emit
          value: "{{ second.value }}"
        - id: second
          kind: emit
          value: x`,
			wantSub: `"second" is not a step in scope`,
		},
		{
			name: "body id shadows top-level id",
			src: `
  steps:
    - id: root
      kind: emit
      value: x
    - id: fan
      kind: fanout
      over: [one]
      body:
        - id: root
          kind: emit
          value: shadowed`,
			wantSub: `shadows a top-level step id`,
		},
		{
			name: "duplicate body-local id",
			src: `
  steps:
    - id: fan
      kind: fanout
      over: [one]
      body:
        - id: dup
          kind: emit
          value: one
        - id: dup
          kind: emit
          value: two`,
			wantSub: `duplicate body-local id "dup"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n" + tc.src
			doc, err := spec.Parse([]byte(src))
			if err != nil {
				t.Fatalf("spec.Parse: %v", err)
			}
			_, err = Resolve(doc)
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *ResolveError: %v", err, err)
			}
			if !strings.Contains(re.Message, tc.wantSub) {
				t.Errorf("message %q does not contain %q", re.Message, tc.wantSub)
			}
			if re.Path == "" {
				t.Error("error carries no node path")
			}
		})
	}
}

func TestSelectorCompileErrors(t *testing.T) {
	cases := []struct {
		name    string
		when    string
		wantSub string
	}{
		{"two predicates", "{from: steps.a.output, equals: x, exists: true}", "at most one"},
		{"equals a mapping", "{from: steps.a.output, equals: {a: 1}}", "requires a scalar literal"},
		{"not_equals a list", "{from: steps.a.output, not_equals: [a]}", "requires a scalar literal"},
		{"index root", "{from: index}", "cannot bind index"},
		{"unknown root", "{from: weather.today}", `unknown selector root "weather"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n  steps:\n    - id: a\n      kind: delegate\n      role: worker\n      prompt: hi\n    - id: b\n      kind: emit\n      when: %s\n      value: x\n", tc.when)
			doc, err := spec.Parse([]byte(src))
			if err != nil {
				t.Fatalf("spec.Parse: %v", err)
			}
			_, err = Resolve(doc)
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *ResolveError: %v", err, err)
			}
			if !strings.Contains(re.Message, tc.wantSub) {
				t.Errorf("message %q does not contain %q", re.Message, tc.wantSub)
			}
			if !strings.HasPrefix(re.Path, "steps.b.when") {
				t.Errorf("path = %q, want steps.b.when…", re.Path)
			}
		})
	}
}

func TestReferenceChecks(t *testing.T) {
	base := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n  steps:\n    - id: a\n      kind: delegate\n      role: worker\n      prompt: hi\n"
	cases := []struct {
		name    string
		step    string
		wantSub string
	}{
		{"unknown need", "    - id: b\n      kind: emit\n      needs: [ghost]\n      value: x\n", `unknown step id "ghost"`},
		{"unknown branch then", "    - id: b\n      kind: branch\n      on: {from: steps.a.output}\n      cases:\n        - {value: fast, then: ghost}\n      default: a\n", `unknown step id "ghost"`},
		{"unknown gate pass", "    - id: b\n      kind: gate\n      assert: {name: ok}\n      on_pass: ghost\n      on_fail: a\n", `unknown step id "ghost"`},
		{"unknown branch default", "    - id: b\n      kind: branch\n      on: {from: steps.a.output}\n      default: ghost\n", `unknown step id "ghost"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := spec.Parse([]byte(base + tc.step))
			if err != nil {
				t.Fatalf("spec.Parse: %v", err)
			}
			_, err = Resolve(doc)
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *ResolveError: %v", err, err)
			}
			if !strings.Contains(re.Message, tc.wantSub) {
				t.Errorf("message %q does not contain %q", re.Message, tc.wantSub)
			}
		})
	}
}

func TestDuplicateTopLevelID(t *testing.T) {
	src := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n  steps:\n    - id: a\n      kind: emit\n      value: one\n    - id: a\n      kind: emit\n      value: two\n"
	doc, err := spec.Parse([]byte(src))
	if err != nil {
		t.Fatalf("spec.Parse: %v", err)
	}
	_, err = Resolve(doc)
	var re *ResolveError
	if !errors.As(err, &re) || !strings.Contains(re.Message, `duplicate step id "a"`) {
		t.Fatalf("err = %v, want duplicate id rejection", err)
	}
}

func TestCompileTemplateSegments(t *testing.T) {
	lex := &lexscope{
		params: map[string]spec.ParamSpec{"task": {Type: spec.ParamString}},
		steps: map[string]*stepInfo{
			"plan":   {id: "plan", kind: spec.KindDelegate},
			"build":  {id: "build", kind: spec.KindFanout, hasCollect: true},
			"second": {id: "second", kind: spec.KindEmit},
		},
		// A fan-out body scope: the item binding and the iteration counter.
		items:   []string{"ws"},
		indexed: true,
	}
	tpl, err := compileTemplate("t", "Work on {{ ws.brief }} ({{ index }}) for {{ params.task }}; see {{ steps.plan.output.summary }} and {{ steps.build.collect }}.", lex)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	wantRefs := []string{"ws.brief", "index", "params.task", "steps.plan.output.summary", "steps.build.collect"}
	if len(tpl.Refs) != len(wantRefs) {
		t.Fatalf("refs = %v, want %v", tpl.Refs, wantRefs)
	}
	for i, r := range tpl.Refs {
		if r.String() != wantRefs[i] {
			t.Errorf("refs[%d] = %q, want %q", i, r.String(), wantRefs[i])
		}
	}
	// Literal passthrough.
	plain, err := compileTemplate("t", "no interpolation here", lex)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	if len(plain.Segments) != 1 || plain.Segments[0].Ref != nil {
		t.Errorf("plain template segments = %+v", plain.Segments)
	}
	// Empty string compiles to no template.
	if t2, err := compileTemplate("t", "", lex); t2 != nil || err != nil {
		t.Errorf("empty template = %v, %v; want nil, nil", t2, err)
	}
}

func TestRenderAndHandles(t *testing.T) {
	lex := &lexscope{
		params: map[string]spec.ParamSpec{"task": {Type: spec.ParamString}},
		steps: map[string]*stepInfo{
			"report": {id: "report", kind: spec.KindDelegate},
		},
		// A fan-out body scope, so both `ws` and `index` resolve.
		items:   []string{"ws"},
		indexed: true,
	}
	tpl, err := compileTemplate("t", "Goal: {{ params.task }}; item {{ ws.name }} #{{ index }}; run {{ run.id }}/{{ run.attempt }}; big: {{ steps.report.output.blob }}", lex)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	scope := &mapScope{
		params: map[string]any{"task": "ship it"},
		steps: map[string]map[string]any{
			"report": {"output": map[string]any{"blob": strings.Repeat("x", 5000)}},
		},
		items: map[string]any{"ws": map[string]any{"name": "alpha"}},
		index: 2,
		run:   map[string]any{"id": "r-1", "attempt": float64(1)},
	}
	res, err := tpl.Render(scope, RenderOptions{HandleThreshold: 1024, PreviewBytes: 16})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(res.Text, "Goal: ship it; item alpha #2; run r-1/1; big: ") {
		t.Errorf("text = %q", res.Text)
	}
	if strings.Contains(res.Text, strings.Repeat("x", 5000)) {
		t.Error("oversized value rendered inline; want handle")
	}
	if len(res.Handles) != 1 {
		t.Fatalf("handles = %d, want 1", len(res.Handles))
	}
	h := res.Handles[0]
	if h.Ref.String() != "steps.report.output.blob" {
		t.Errorf("handle ref = %q", h.Ref.String())
	}
	if h.Size != 5000 {
		t.Errorf("handle size = %d, want 5000", h.Size)
	}
	if len(h.Preview) != 16 {
		t.Errorf("preview len = %d, want 16", len(h.Preview))
	}
}

func TestRenderErrors(t *testing.T) {
	lex := &lexscope{
		params: map[string]spec.ParamSpec{"task": {Type: spec.ParamString}},
		steps: map[string]*stepInfo{
			"a": {id: "a", kind: spec.KindDelegate},
		},
	}
	tpl, err := compileTemplate("t", "{{ params.task.detail }}", lex)
	if err != nil {
		t.Fatalf("CompileTemplate: %v", err)
	}
	scope := &mapScope{params: map[string]any{"task": "scalar"}}
	_, err = tpl.Render(scope, RenderOptions{})
	var rerr *RenderError
	if !errors.As(err, &rerr) {
		t.Fatalf("error is %T, want *RenderError", err)
	}
	if rerr.Ref != "params.task.detail" {
		t.Errorf("ref = %q", rerr.Ref)
	}
}

func TestItemAliasAndShadowing(t *testing.T) {
	lex := &lexscope{
		steps: map[string]*stepInfo{"outer": {id: "outer", kind: spec.KindFanout, hasCollect: true}},
		items: []string{"ws", "proposal"},
	}
	// "item" resolves to the innermost binding.
	ref, err := compileRef("t", "item.id", lex)
	if err != nil {
		t.Fatalf("compileRef: %v", err)
	}
	if ref.Root != RootItem || ref.Name != "proposal" {
		t.Errorf("ref = %+v, want item/proposal", ref)
	}
	// Outer binding still reachable by name.
	ref, err = compileRef("t", "ws.brief", lex)
	if err != nil {
		t.Fatalf("compileRef: %v", err)
	}
	if ref.Name != "ws" {
		t.Errorf("ref = %+v, want ws", ref)
	}
}

// mapScope is a test Scope backed by plain maps.
type mapScope struct {
	params map[string]any
	steps  map[string]map[string]any
	items  map[string]any
	index  int
	run    map[string]any
}

func (m *mapScope) Param(name string) (any, bool) {
	v, ok := m.params[name]
	return v, ok
}

func (m *mapScope) StepField(id, field string) (any, error) {
	step, ok := m.steps[id]
	if !ok {
		return nil, fmt.Errorf("unknown step %q", id)
	}
	v, ok := step[field]
	if !ok {
		return nil, fmt.Errorf("step %q has no %s", id, field)
	}
	return v, nil
}

func (m *mapScope) Item(name string) (any, bool) {
	v, ok := m.items[name]
	return v, ok
}

func (m *mapScope) Index() int { return m.index }

func (m *mapScope) RunField(field string) (any, error) {
	v, ok := m.run[field]
	if !ok {
		return nil, fmt.Errorf("run has no %s", field)
	}
	return v, nil
}

func TestValueTextComposites(t *testing.T) {
	got, err := valueText(map[string]any{"b": int64(2), "a": []any{"x", true}})
	if err != nil {
		t.Fatalf("valueText: %v", err)
	}
	if got != `{"a":["x",true],"b":2}` {
		t.Errorf("valueText = %q", got)
	}
	got, err = valueText(float64(1.5))
	if err != nil || got != "1.5" {
		t.Errorf("valueText(1.5) = %q, %v", got, err)
	}
	var jsonNum json.Number = "42"
	got, err = valueText(jsonNum)
	if err != nil || got != "42" {
		t.Errorf("valueText(42) = %q, %v", got, err)
	}
}

// ------------------------------------------------------------ settle policy

// stepDoc is a fan-out followed by one node whose kind and payload vary, so
// a policy test can name the kind that declares the key.
func stepDoc(kind, policy string) []byte {
	head := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n  steps:\n    - id: fan\n      kind: fanout\n      over: [a]\n      as: ws\n      collect: results\n      body:\n        kind: delegate\n        role: developer\n        prompt: build it\n"
	id, payload := "land", "      proposals: { from: steps.fan.collect }\n      action: apply\n"
	if kind == "select" {
		id, payload = "pick", "      candidates: { from: steps.fan.collect }\n"
	}
	body := "    - id: " + id + "\n      kind: " + kind + "\n      needs: [fan]\n" + payload
	return []byte(head + body + "      policy: " + policy + "\n")
}

func nodeByID(t *testing.T, g *Graph, id string) *Node {
	t.Helper()
	for _, n := range g.Steps {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("graph has no top-level step %q", id)
	return nil
}

// TestSettlePolicyReachesTheGraph pins yaah-0u0v.31: spec.Node.Policy decodes
// the two settlement values, and resolveSettle used to read only on_conflict
// and on_empty, so `policy: path_disjoint` was dropped on the way into the
// graph while the document still said the settle had chosen one.
func TestSettlePolicyReachesTheGraph(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   SettlePolicy
	}{
		{"path_disjoint", SettlePathDisjoint},
		{"three_way", SettleThreeWay},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			g, err := Resolve(mustParse(t, stepDoc("settle", tc.policy)))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			land := nodeByID(t, g, "land")
			if land.SettlePolicy != tc.want {
				t.Errorf("settle SettlePolicy = %q, want %q", land.SettlePolicy, tc.want)
			}
			// The select half of the shared key stays unpopulated on a
			// settle, which is what keeps the two graph keys honest about
			// the one document key.
			if land.Policy != "" {
				t.Errorf("settle also populated Policy = %q, want it left zero", land.Policy)
			}
			// Wire shape: the settle writes settle_policy, never policy.
			raw, err := GraphJSON(g)
			if err != nil {
				t.Fatalf("GraphJSON: %v", err)
			}
			if !strings.Contains(string(raw), `"settle_policy":"`+tc.policy+`"`) {
				t.Errorf("graph json lacks the settle policy key:\n%s", raw)
			}
			if strings.Contains(string(raw), `"policy":"`) {
				t.Errorf("graph json wrote the select key for a settle:\n%s", raw)
			}
		})
	}

	// The two goldens that documented the defect carry it now.
	for _, tc := range []struct {
		name string
		want SettlePolicy
	}{
		{"fanout_settle", SettlePathDisjoint},
		{"bounded_repair", SettleThreeWay},
	} {
		t.Run("golden "+tc.name, func(t *testing.T) {
			g, err := Resolve(goldenDoc(t, tc.name))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := nodeByID(t, g, "land").SettlePolicy; got != tc.want {
				t.Errorf("%s land.SettlePolicy = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPolicyVocabularyIsPerKind pins the design decision behind the settle
// policy field: the resolver converts faithfully what the vocabulary allows
// for the kind it is resolving and refuses the other half, so a document
// cannot leave the pipeline claiming a disposition its node cannot honour.
// WF014 reads the graph and cannot see a dropped field, so a silent drop
// here would be unreported; the rejection is what keeps the two rules from
// double-reporting the same mistake.
func TestPolicyVocabularyIsPerKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		policy  string
		path    string
		wantSub string
	}{
		{"settle rejects a selection policy", "settle", "judge", "steps.land.policy", "unconvertible settle policy"},
		{"settle rejects first_success", "settle", "first_success", "steps.land.policy", "unconvertible settle policy"},
		{"settle rejects smallest_diff", "settle", "smallest_diff", "steps.land.policy", "unconvertible settle policy"},
		{"select rejects a settlement policy", "select", "path_disjoint", "steps.pick.policy", "unconvertible select policy"},
		{"select rejects three_way", "select", "three_way", "steps.pick.policy", "unconvertible select policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(mustParse(t, stepDoc(tc.kind, tc.policy)))
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *ResolveError: %v", err, err)
			}
			if re.Path != tc.path {
				t.Errorf("path = %q, want %q", re.Path, tc.path)
			}
			if !strings.Contains(re.Message, tc.wantSub) {
				t.Errorf("message %q does not contain %q", re.Message, tc.wantSub)
			}
		})
	}

	// The kind that owns neither half still drops the key: `policy` on a
	// gate has no compiled home, which is WF014's documented blind spot for
	// a field the resolver never reads at all.
	gateDoc := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n  steps:\n    - id: g\n      kind: gate\n      assert: {name: ok}\n      policy: path_disjoint\n"
	g, err := Resolve(mustParse(t, []byte(gateDoc)))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if n := nodeByID(t, g, "g"); n.Policy != "" || n.SettlePolicy != "" {
		t.Errorf("gate populated Policy = %q, SettlePolicy = %q; want both zero", n.Policy, n.SettlePolicy)
	}
}

// --------------------------------------------------------- selector field

const selectorFieldDoc = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata: {name: t}
spec:
  steps:
    - id: fan
      kind: fanout
      over: [a]
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
    - id: rep
      kind: loop
      needs: [fan]
      max_iterations: 2
      until: { from: steps.fan.collect, field: verdict, equals: pass }
      collect: outs
      body:
        kind: delegate
        role: developer
        prompt: repair it
    - id: land
      kind: settle
      needs: [rep]
      proposals: { from: steps.fan.collect }
      where: { from: steps.fan.collect, field: verdict, equals: pass }
      action: apply
    - id: bare
      kind: select
      needs: [land]
      candidates: { from: steps.fan.collect }
      policy: judge
    - id: proj
      kind: settle
      needs: [bare]
      proposals: { from: steps.fan.collect }
      where: { from: steps.fan.collect, field: verdict }
      action: apply
`

// TestSelectorFieldReachesThePredicate pins yaah-0u0v.32: spec.Selector.Field
// decoded but compileSelector never read it, so
// `where: {from: steps.x.collect, field: verdict, equals: pass}` compiled to a
// predicate comparing the whole element against "pass" — silently the wrong
// semantics, and the only place the author's field survived was the document.
func TestSelectorFieldReachesThePredicate(t *testing.T) {
	g, err := Resolve(mustParse(t, []byte(selectorFieldDoc)))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  Predicate
		want Predicate
	}{
		{
			name: "loop until keeps its field with its operator",
			got:  nodeByID(t, g, "rep").Until.Predicate,
			want: Predicate{Op: PredEquals, Arg: "pass", Field: "verdict"},
		},
		{
			name: "settle where keeps its field",
			got:  nodeByID(t, g, "land").Where.Predicate,
			want: Predicate{Op: PredEquals, Arg: "pass", Field: "verdict"},
		},
		{
			name: "a field with no operator is still carried",
			got:  nodeByID(t, g, "proj").Where.Predicate,
			want: Predicate{Field: "verdict"},
		},
		{
			name: "an undeclared field stays empty",
			got:  nodeByID(t, g, "land").Proposals.Predicate,
			want: Predicate{},
		},
		{
			name: "a selector without a predicate stays bare",
			got:  nodeByID(t, g, "bare").Candidates.Predicate,
			want: Predicate{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Field != tc.want.Field {
				t.Errorf("Predicate.Field = %q, want %q", tc.got.Field, tc.want.Field)
			}
			if tc.got.Op != tc.want.Op {
				t.Errorf("Predicate.Op = %q, want %q", tc.got.Op, tc.want.Op)
			}
			if !reflect.DeepEqual(tc.got.Arg, tc.want.Arg) {
				t.Errorf("Predicate.Arg = %#v, want %#v", tc.got.Arg, tc.want.Arg)
			}
		})
	}

	// Wire shape: filtered selectors carry field, unfiltered ones do not.
	raw, err := GraphJSON(g)
	if err != nil {
		t.Fatalf("GraphJSON: %v", err)
	}
	if got := strings.Count(string(raw), `"field":"verdict"`); got != 3 {
		t.Errorf("graph json has %d field keys, want 3:\n%s", got, raw)
	}

	// The goldens that depend on field projection agree.
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"bounded_repair", "land"},
		{"fanout_settle", "land"},
	} {
		t.Run("golden "+tc.name, func(t *testing.T) {
			gh, err := Resolve(goldenDoc(t, tc.name))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if f := nodeByID(t, gh, tc.id).Where.Predicate.Field; f != "verdict" {
				t.Errorf("%s %s.Where.Predicate.Field = %q, want verdict", tc.name, tc.id, f)
			}
		})
	}
}

// ---------------------------------------------------------- index scope

// TestIndexInLoopBodies pins yaah-0u0v.33: compileRef admitted `index` only
// where the lexical scope carried an item binding, so a loop body could not
// number its iterations — the very thing the error text promised it could
// do. The scope now keys on "this body iterates", not "this body binds an
// item": fan-out and loop bodies have an index, a fan-out keeps one when the
// author wrote no `as`, a container's own fields are still resolved outside
// its body, and an aggregate body combines inputs rather than iterating them
// so it stays out.
func TestIndexInLoopBodies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		steps    string
		wantSub  string
		wantPath string
	}{
		{
			name: "a loop body numbers its iterations",
			steps: `  steps:
    - id: rep
      kind: loop
      max_iterations: 3
      collect: outs
      body:
        kind: delegate
        role: developer
        prompt: attempt {{ index }} of 3
`,
		},
		{
			name: "a fan-out body with no as binding still has index",
			steps: `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: item {{ index }}
`,
		},
		{
			name: "a loop nested in a fan-out body sees both scopes",
			steps: `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      as: ws
      collect: results
      body:
        - id: rep
          kind: loop
          max_iterations: 2
          collect: outs
          body:
            kind: delegate
            role: developer
            prompt: '{{ ws }} attempt {{ index }}'
`,
		},
		{
			name: "a container's own fields are outside its body",
			steps: `  steps:
    - id: rep
      kind: loop
      max_iterations: 2
      continue_from: "resume at {{ index }}"
      collect: outs
      body:
        kind: delegate
        role: developer
        prompt: attempt {{ index }}
`,
			wantSub:  "index is only in scope inside a fan-out or loop body",
			wantPath: "steps.rep.continue_from",
		},
		{
			name: "an aggregate body combines and has no index",
			steps: `  steps:
    - id: fan
      kind: fanout
      over: [a]
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
    - id: agg
      kind: aggregate
      needs: [fan]
      strategy: concat
      inputs:
        - from: steps.fan.collect
      body:
        kind: emit
        value: "attempt {{ index }}"
`,
			wantSub: "index is only in scope inside a fan-out or loop body",
		},
		{
			name: "top level still has no index",
			steps: `  steps:
    - id: a
      kind: emit
      value: "{{ index }}"
`,
			wantSub: "index is only in scope inside a fan-out or loop body",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: t}\nspec:\n" + tc.steps
			doc, err := spec.Parse([]byte(src))
			if err != nil {
				t.Fatalf("spec.Parse: %v\n%s", err, src)
			}
			_, err = Resolve(doc)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				return
			}
			var re *ResolveError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *ResolveError: %v", err, err)
			}
			if !strings.Contains(re.Message, tc.wantSub) {
				t.Errorf("message %q does not contain %q", re.Message, tc.wantSub)
			}
			if tc.wantPath != "" && re.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", re.Path, tc.wantPath)
			}
		})
	}
}
