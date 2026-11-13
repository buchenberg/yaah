package validate

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
	"github.com/buchenberg/yaah/internal/workflow/spec"
)

// ------------------------------------------------------------- value helpers

// valueFindingsOf runs the real pipeline and returns only this family's
// findings, so a table row shows what WF010–WF014 decided and never a
// structural finding another file's test already pins.
func valueFindingsOf(t *testing.T, steps string) []Diagnostic {
	t.Helper()
	data := documentWith(steps)
	doc, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("document did not decode: %v\n%s", err, data)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		t.Fatalf("document did not resolve: %v\n%s", err, data)
	}
	return Values(doc, g)
}

// graphFindings runs the family in graph-only mode: no authoring document,
// which is how a scheduler, a replay, or a test hands a validator a graph,
// and the only way to reach a reference the resolver never saw.
func graphFindings(g *ir.Graph) []Diagnostic {
	return Values(nil, g)
}

// mustFind asserts one code at one path saying one thing.
func mustFind(t *testing.T, diags []Diagnostic, code, path, sub string) {
	t.Helper()
	d := findingAt(t, diags, code, path)
	if !strings.Contains(d.Message, sub) {
		t.Errorf("%s %s message = %q, want it to contain %q", code, path, d.Message, sub)
	}
}

// mustBeClean fails loudly: shallow typing is only safe while it stays
// silent, so an unexpected value finding is the failure this family is most
// likely to have.
func mustBeClean(t *testing.T, diags []Diagnostic) {
	t.Helper()
	if len(diags) != 0 {
		t.Errorf("want zero value findings, got:%s", render(diags))
	}
}

// compiled builds a template whose segments and references agree, so a test
// of one rule does not trip WF013's integrity check on the way in.
func compiled(refs ...*ir.Ref) *ir.Template {
	t := &ir.Template{Raw: "v {{ x }}"}
	for _, r := range refs {
		if r == nil {
			t.Refs = append(t.Refs, nil)
			continue
		}
		t.Refs = append(t.Refs, r)
		t.Segments = append(t.Segments, ir.Segment{Ref: r})
	}
	return t
}

// emitAt is the smallest node a scope test needs: an emit step with one
// compiled value.
func emitAt(path, id string, refs ...*ir.Ref) *ir.Node {
	return &ir.Node{Path: path, ID: id, Kind: ir.KindEmit, Value: compiled(refs...)}
}

// ------------------------------------------------------------------ WF010

func TestValuesSkeleton(t *testing.T) {
	if got := Values(nil, nil); got != nil {
		t.Errorf("Values(nil, nil) = %v, want nil", got)
	}
	if got := Values(nil, &ir.Graph{Name: "empty"}); len(got) != 0 {
		t.Errorf("an empty graph reported findings: %s", render(got))
	}
}

type valueCase struct {
	name  string
	graph *ir.Graph
	doc   string
	code  string
	path  string
	sub   string
}

func runValueCases(t *testing.T, cases []valueCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags []Diagnostic
			if tc.graph != nil {
				diags = graphFindings(tc.graph)
			} else {
				diags = valueFindingsOf(t, tc.doc)
			}
			if tc.sub == "" {
				mustBeClean(t, diags)
				return
			}
			mustFind(t, diags, tc.code, tc.path, tc.sub)
		})
	}
}

// TestReferencesResolveHandBuilt is WF010's graph-level authority: every
// finding here is a compiled reference that no resolver ever checked, at the
// path an author navigates to.
func TestReferencesResolveHandBuilt(t *testing.T) {
	step := &ir.Node{Path: "steps.a", ID: "a", Kind: ir.KindEmit}
	declared := map[string]ir.Param{"task": {Name: "task", Type: ir.ParamString}}

	runValueCases(t, []valueCase{
		{
			name: "undeclared param",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootParams, Name: "task"}),
			}},
			code: CodeWF010, path: "steps.a.value[params.task]", sub: "names an undeclared param",
		},
		{
			name: "declared param resolves",
			graph: &ir.Graph{Name: "hand", Params: declared, Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootParams, Name: "task"}),
			}},
		},
		{
			name: "param reference with no name",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootParams}),
			}},
			code: CodeWF010, path: "steps.a.value[params.]", sub: "names no parameter",
		},
		{
			name: "step not in scope",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootSteps, StepID: "ghost", Field: "output"}),
			}},
			code: CodeWF010, path: "steps.a.value[steps.ghost.output]", sub: "which is not in scope",
		},
		{
			name: "step reference with no id",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootSteps, Field: "output"}),
			}},
			code: CodeWF010, path: "steps.a.value[steps..output]", sub: "names no step",
		},
		{
			name: "wrong step field",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{step,
				emitAt("steps.b", "b", &ir.Ref{Root: ir.RootSteps, StepID: "a", Field: "status"}),
			}},
			code: CodeWF010, path: "steps.b.value[steps.a.status]", sub: "must read output or collect",
		},
		{
			name: "collect from a node that produces none",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{step,
				emitAt("steps.c", "c", &ir.Ref{Root: ir.RootSteps, StepID: "a", Field: "collect"}),
			}},
			code: CodeWF010, path: "steps.c.value[steps.a.collect]", sub: "which produces none",
		},
		{
			name: "item binding outside any body",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootItem, Name: "ws"}),
			}},
			code: CodeWF010, path: "steps.a.value[ws]", sub: `names the item binding "ws", which is not in scope`,
		},
		{
			name: "index outside a body",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootIndex}),
			}},
			code: CodeWF010, path: "steps.a.value[index]", sub: "index is only in scope inside a fan-out or loop body",
		},
		{
			name: "index with a path",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over: &ir.Over{Literals: []any{"a"}},
				Body: []*ir.Node{{Path: "steps.fan.body[0]", Kind: ir.KindEmit,
					Value: compiled(&ir.Ref{Root: ir.RootIndex, Path: []string{"length"}})}},
			}}},
			code: CodeWF010, path: "steps.fan.body[0].value[index]", sub: "index is a number and has no fields",
		},
		{
			name: "unknown run field",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootRun, Field: "kind"}),
			}},
			code: CodeWF010, path: "steps.a.value[run.kind]", sub: "must be run.id or run.attempt",
		},
		{
			name: "run field with a deeper path",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootRun, Field: "id", Path: []string{"hex"}}),
			}},
			code: CodeWF010, path: "steps.a.value[run.id.hex]", sub: "run fields have no deeper path",
		},
		{
			name: "unknown root",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.Root("magic")}),
			}},
			code: CodeWF010, path: "steps.a.value[]", sub: "reference has an unknown root",
		},
		{
			name: "empty path segment",
			graph: &ir.Graph{Name: "hand", Params: declared, Steps: []*ir.Node{
				emitAt("steps.a", "a", &ir.Ref{Root: ir.RootParams, Name: "task", Path: []string{""}}),
			}},
			code: CodeWF010, path: "steps.a.value[params.task.]", sub: "reference path has an empty segment",
		},
		{
			name: "selector with no source",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.a", ID: "a", Kind: ir.KindEmit, When: &ir.Selector{}},
			}},
			code: CodeWF010, path: "steps.a.when.from", sub: "selector has no source",
		},
		{
			name: "nil reference slot",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.a", ID: "a", Kind: ir.KindEmit, Value: compiled(nil)},
			}},
			code: CodeWF010, path: "steps.a.value", sub: "reference slot that was never compiled",
		},
		{
			name: "selector source kinds resolve",
			graph: &ir.Graph{Name: "hand", Params: declared, Steps: []*ir.Node{
				{Path: "steps.sib", ID: "sib", Kind: ir.KindEmit},
				{Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
					Over: &ir.Over{Literals: []any{"a"}},
					Body: []*ir.Node{{Path: "steps.fan.body[0]", ID: "inner", Kind: ir.KindEmit,
						When: &ir.Selector{From: &ir.Source{Kind: ir.SourceItem, Name: "ws"}},
						Value: compiled(
							&ir.Ref{Root: ir.RootSteps, StepID: "sib", Field: "output"},
							&ir.Ref{Root: ir.RootParams, Name: "task"},
						)},
					}},
				{Path: "steps.b", ID: "b", Kind: ir.KindEmit,
					When:  &ir.Selector{From: &ir.Source{Kind: ir.SourceStepCollect, StepID: "fan"}},
					Needs: []string{"fan"}},
			}},
		},
		{
			name: "unknown selector source kind",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.a", ID: "a", Kind: ir.KindEmit,
					When: &ir.Selector{From: &ir.Source{Kind: ir.SourceKind("mystery")}}},
			}},
			code: CodeWF010, path: "steps.a.when.from", sub: "selector source has an unknown kind",
		},
		{
			name: "later body sibling is not in scope",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over: &ir.Over{Literals: []any{"a"}},
				Body: []*ir.Node{
					{Path: "steps.fan.body[0]", ID: "first", Kind: ir.KindEmit,
						Value: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "second", Field: "output"})},
					{Path: "steps.fan.body[1]", ID: "second", Kind: ir.KindEmit},
				},
			}}},
			code: CodeWF010, path: "steps.fan.body[0].value[steps.second.output]", sub: "which is not in scope",
		},
		{
			name: "earlier body sibling is in scope",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over: &ir.Over{Literals: []any{"a"}},
				Body: []*ir.Node{
					{Path: "steps.fan.body[0]", ID: "first", Kind: ir.KindDelegate, Role: "developer",
						Prompt: compiled(&ir.Ref{Root: ir.RootItem, Name: "ws"})},
					{Path: "steps.fan.body[1]", ID: "second", Kind: ir.KindEmit,
						Value: compiled(
							&ir.Ref{Root: ir.RootSteps, StepID: "first", Field: "output"},
							&ir.Ref{Root: ir.RootIndex},
						)},
				},
			}}},
		},
		{
			name: "loop body numbers its iterations",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.rep", ID: "rep", Kind: ir.KindLoop, Collect: "outs",
				Body: []*ir.Node{{Path: "steps.rep.body[0]", Kind: ir.KindEmit,
					Value: compiled(&ir.Ref{Root: ir.RootIndex})}}},
			}},
			// The scope this rule reconstructs is ir.resolveBody's, which
			// marks fan-out and loop bodies as iterating (yaah-0u0v.33). A
			// reference the resolver compiles must never come back here as
			// WF010, so the loop body is silent even though it binds no item.
		},
		{
			name: "aggregate body has no iteration to number",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.agg", ID: "agg", Kind: ir.KindAggregate, Strategy: ir.AggregateConcat,
				Body: []*ir.Node{{Path: "steps.agg.body[0]", Kind: ir.KindEmit,
					Value: compiled(&ir.Ref{Root: ir.RootIndex})}}},
			}},
			code: CodeWF010, path: "steps.agg.body[0].value[index]", sub: "index is only in scope inside a fan-out or loop body",
		},
		{
			name: "a loop nested in a fan-out body keeps the item scope",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over: &ir.Over{Literals: []any{"a"}},
				Body: []*ir.Node{{
					Path: "steps.fan.body[0]", ID: "rep", Kind: ir.KindLoop, Collect: "outs",
					Body: []*ir.Node{{Path: "steps.fan.body[0].body[0]", Kind: ir.KindEmit,
						Value: compiled(&ir.Ref{Root: ir.RootIndex}, &ir.Ref{Root: ir.RootItem, Name: "ws"})}}},
				}},
			}},
		},
		{
			name: "a container's own values are outside its body",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.rep", ID: "rep", Kind: ir.KindLoop, Collect: "outs",
				ContinueFrom: compiled(&ir.Ref{Root: ir.RootIndex}),
			}}},
			code: CodeWF010, path: "steps.rep.continue_from[index]", sub: "index is only in scope inside a fan-out or loop body",
		},
		{
			name: "item binding is invisible above its fan-out",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over:  &ir.Over{Literals: []any{"a"}},
				Value: compiled(&ir.Ref{Root: ir.RootItem, Name: "ws"}),
			}}},
			code: CodeWF010, path: "steps.fan.value[ws]", sub: "which is not in scope",
		},
	})
}

// -------------------------------------------------------------- WF011 (types)

const (
	fanDoc = `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
`
	typedFanDoc = `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      as: ws
      collect: results
      body:
        kind: delegate
        role: tester
        lane: report
        prompt: test it
        output: {name: verdict, type: Verdict}
`
)

func TestTypesCompatibleByDocument(t *testing.T) {
	runValueCases(t, []valueCase{
		{
			name: "fan-out over a scalar parameter",
			doc: `  params:
    task: {type: string}
  steps:
    - id: fan
      kind: fanout
      over: { from: params.task }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
`,
			code: CodeWF011, path: "steps.fan.over.from", sub: "requires a list",
		},
		{
			name: "fan-out over a scalar parameter wrapped with as_list",
			doc: `  params:
    task: {type: string}
  steps:
    - id: fan
      kind: fanout
      over: { from: params.task, as_list: true }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
`,
		},
		{
			name: "fan-out over a collect",
			doc: `  steps:
    - id: build
      kind: fanout
      over: [a, b]
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
        output: proposal
    - id: review
      kind: fanout
      over: { from: steps.build.collect }
      as: item
      collect: reviews
      body:
        kind: delegate
        role: reviewer
        lane: report
        prompt: review it
`,
		},
		{
			name: "fan-out over a named value type",
			doc: `  steps:
    - id: plan
      kind: delegate
      role: planner
      prompt: plan it
      output: {name: plan, type: Plan}
    - id: fan
      kind: fanout
      needs: [plan]
      over: { from: steps.plan.output }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: build it
`,
			code: CodeWF011, path: "steps.fan.over.from", sub: "requires a list",
		},
		{
			name: "settle over a list of verdicts",
			doc: typedFanDoc + `    - id: land
      kind: settle
      proposals: { from: steps.fan.collect }
      action: apply
`,
			code: CodeWF011, path: "steps.land.proposals.from", sub: "a settle acts on Proposal values",
		},
		{
			name: "settle over an untyped collect",
			doc: fanDoc + `    - id: land
      kind: settle
      proposals: { from: steps.fan.collect }
      action: apply
`,
		},
		{
			name: "where filter over a scalar parameter",
			doc: `  params:
    task: {type: string}
` + fanDoc + `    - id: land
      kind: settle
      proposals: { from: steps.fan.collect }
      where: { from: params.task, field: verdict, equals: pass }
      action: apply
`,
			code: CodeWF011, path: "steps.land.where.from", sub: "a where filter requires a list",
		},
		{
			name: "branch matching a list",
			doc: fanDoc + `    - id: pick
      kind: branch
      needs: [fan]
      on: { from: steps.fan.collect }
      cases:
        - value: done
          then: fan
`,
			code: CodeWF011, path: "steps.pick.on.from", sub: "needs one value to match",
		},
		{
			name: "continue_from naming a list",
			doc: fanDoc + `    - id: rep
      kind: loop
      needs: [fan]
      max_iterations: 3
      continue_from: "keep going on {{ steps.fan.collect }}"
      collect: outs
      body:
        kind: delegate
        role: developer
        prompt: repair it
`,
			code: CodeWF011, path: "steps.rep.continue_from[steps.fan.collect]",
			sub: "must name one prior run and reads list",
		},
		{
			name: "json_merge over a string input",
			doc: `  params:
    task: {type: string}
  steps:
    - id: agg
      kind: aggregate
      strategy: json_merge
      inputs:
        - from: params.task
`,
			code: CodeWF011, path: "steps.agg.inputs[0].from", sub: "json_merge merges objects",
		},
		{
			name: "json_merge over a map input",
			doc: `  params:
    config: {type: map}
  steps:
    - id: agg
      kind: aggregate
      strategy: json_merge
      inputs:
        - from: params.config
`,
		},
		{
			name: "output type contradicts its own schema",
			doc: `  steps:
    - id: rows
      kind: delegate
      role: developer
      prompt: build it
      output: {name: rows, type: Report, schema: '{"type":"array","items":{"type":"string"}}'}
`,
			code: CodeWF011, path: "steps.rows.output.type", sub: "but the declared schema describes",
		},
		{
			name: "output type agrees with its object schema",
			doc: `  steps:
    - id: rows
      kind: delegate
      role: developer
      prompt: build it
      output: {name: rows, type: Report, schema: '{"type":"object","properties":{"diff":{"type":"string"}}}'}
`,
		},
	})
}

// ------------------------------------------------------------ WF012 (shapes)

const planSchema = `'{"type":"object","properties":{"workstreams":{"type":"array","items":{"type":"object","properties":{"brief":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}}}}}}'`

const declaredPlanDoc = `  steps:
    - id: plan
      kind: delegate
      role: planner
      prompt: plan it
      output: {name: plan, schema: ` + planSchema + `}
    - id: fan
      kind: fanout
      needs: [plan]
      over: { from: steps.plan.output.workstreams }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: 'build {{ ws.brief }}'
`

const undeclaredPlanDoc = `  steps:
    - id: plan
      kind: delegate
      role: planner
      prompt: plan it
      output: {name: plan, schema: ` + planSchema + `}
    - id: fan
      kind: fanout
      needs: [plan]
      over: { from: steps.plan.output.workstreams }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: 'build {{ ws.scope }}'
`

// verdictBodyDoc collects Proposals whose elements declare a verdict field,
// so a settle's where filter has a shape to check against and the element
// type agrees with the verb.
const verdictBodyDoc = `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      as: ws
      collect: results
      body:
        kind: delegate
        role: reviewer
        lane: report
        prompt: review it
        output: {name: verdict, type: Proposal, schema: '{"type":"object","properties":{"verdict":{"type":"string"}}}'}
`

func TestFieldsDeclared(t *testing.T) {
	runValueCases(t, []valueCase{
		{
			name: "field access into a scalar parameter",
			doc: `  params:
    task: {type: string}
  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: 'uppercase {{ params.task.upper }}'
`,
			code: CodeWF012, path: "steps.a.prompt[params.task.upper]", sub: "of a string, which has no fields",
		},
		{
			name: "field access into an object parameter is undecided",
			doc: `  params:
    config: {type: map}
  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: 'read {{ params.config.retry.limit }}'
`,
		},
		{
			name: "declared element field resolves",
			doc:  declaredPlanDoc,
		},
		{
			name: "undeclared element field is reported",
			doc:  undeclaredPlanDoc,
			code: CodeWF012, path: "steps.fan.body[0].prompt[ws.scope]",
			sub: "does not declare (declared: brief, paths)",
		},
		{
			name: "named schema declares nothing and decides nothing",
			doc: `  steps:
    - id: plan
      kind: delegate
      role: planner
      prompt: plan it
      output: {name: plan, type: Plan, schema: plan.v1}
    - id: fan
      kind: fanout
      needs: [plan]
      over: { from: steps.plan.output.workstreams }
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: 'build {{ ws.whatever }}'
`,
		},
		{
			name: "an untyped collect keeps per-item field access silent",
			doc: `  steps:
    - id: build
      kind: fanout
      over: [a, b]
      as: ws
      collect: proposals
      body:
        kind: delegate
        role: developer
        prompt: build it
        output: proposal
    - id: review
      kind: fanout
      over: { from: steps.build.collect }
      as: item
      collect: reviews
      body:
        kind: delegate
        role: reviewer
        lane: report
        prompt: 'Proposal id: {{ item.id }} Diff: {{ item.diff }}'
`,
		},
		{
			name: "field access into a list path",
			doc: fanDoc + `    - id: twice
      kind: fanout
      needs: [fan]
      over: { from: steps.fan.collect.length }
      as: ws
      collect: more
      body:
        kind: delegate
        role: developer
        prompt: build it
`,
			code: CodeWF012, path: "steps.twice.over.from", sub: "of list; a list binds fields per item",
		},
		{
			name: "selector field declared by the element schema",
			doc: verdictBodyDoc + `    - id: land
      kind: settle
      needs: [fan]
      proposals: { from: steps.fan.collect }
      where: { from: steps.fan.collect, field: verdict, equals: pass }
      action: apply
`,
			code: "", path: "", sub: "",
		},
		{
			name: "selector field the element schema does not declare",
			doc: verdictBodyDoc + `    - id: land
      kind: settle
      needs: [fan]
      proposals: { from: steps.fan.collect }
      where: { from: steps.fan.collect, field: status, equals: pass }
      action: apply
`,
			code: CodeWF012, path: "steps.land.where.field", sub: `selector field "status" is not declared by Proposal`,
		},
	})
}

// TestSelectorFieldNeedsTheDocument pins the yaah-0u0v.32 seam: the compiled
// selector carries no Field, so WF012 can only see an author's field: by
// reading the document. Graph-only mode keeps every other check and loses
// exactly this one.
func TestSelectorFieldNeedsTheDocument(t *testing.T) {
	data := documentWith(verdictBodyDoc + `    - id: land
      kind: settle
      needs: [fan]
      proposals: { from: steps.fan.collect }
      where: { from: steps.fan.collect, field: status, equals: pass }
      action: apply
`)
	doc, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	mustFind(t, Values(doc, g), CodeWF012, "steps.land.where.field", "is not declared by Proposal")

	if diags := graphFindings(g); len(diags) != 0 {
		t.Errorf("graph-only mode reported findings that belong to the document:%s", render(diags))
	}
	// The tripwire for the day .32 lands: the compiled selector still has
	// the source and still has no field, so this rule must keep reading the
	// document until the IR carries it.
	if g.Steps[1].Where.From == nil {
		t.Error("resolved selector lost its source as well as its field")
	}
	if g.Steps[1].Where.Predicate.Op != ir.PredEquals {
		t.Errorf("resolved selector predicate = %q, want equals", g.Steps[1].Where.Predicate.Op)
	}
}

// TestFieldsDeclaredHandBuilt covers the two shape mistakes a resolver never
// lets a document reach: a field read off a literal list element and a
// declared param path that stops resolving.
func TestFieldsDeclaredHandBuilt(t *testing.T) {
	scalarParam := map[string]ir.Param{"task": {Name: "task", Type: ir.ParamString}}
	runValueCases(t, []valueCase{
		{
			name: "field read off a literal string element",
			graph: &ir.Graph{Name: "hand", Params: scalarParam, Steps: []*ir.Node{{
				Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Over: &ir.Over{Literals: []any{"a", "b"}},
				Body: []*ir.Node{{Path: "steps.fan.body[0]", Kind: ir.KindDelegate, Role: "developer",
					Prompt: compiled(&ir.Ref{Root: ir.RootItem, Name: "ws", Path: []string{"brief"}})}},
			}}},
			code: CodeWF012, path: "steps.fan.body[0].prompt[ws.brief]", sub: "of a string, which has no fields",
		},
		{
			name: "over selector reading an undeclared param source",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.x", ID: "x", Kind: ir.KindEmit},
				{Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws", Collect: "results",
					Over: &ir.Over{Selector: &ir.Selector{
						From: &ir.Source{Kind: ir.SourceParams, Name: "rows"}}},
					Body: []*ir.Node{{Path: "steps.fan.body[0]", Kind: ir.KindDelegate, Role: "developer",
						Prompt: compiled(&ir.Ref{Root: ir.RootItem, Name: "ws", Path: []string{"brief"}})}},
				},
			}},
			code: CodeWF010, path: "steps.fan.over.from", sub: "names an undeclared param",
		},
	})
}

// ---------------------------------------------------------- WF013 (recursion)

func TestTemplatesNotRecursive(t *testing.T) {
	self := &ir.Node{Path: "steps.a", ID: "a", Kind: ir.KindDelegate, Role: "developer",
		Prompt: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "a", Field: "output", Path: []string{"diff"}})}
	selfCollect := &ir.Node{Path: "steps.rep", ID: "rep", Kind: ir.KindLoop, Collect: "outs",
		Until: &ir.Selector{From: &ir.Source{Kind: ir.SourceStepCollect, StepID: "rep"}}}
	contained := &ir.Node{Path: "steps.fan", ID: "fan", Kind: ir.KindFanout, As: "ws",
		Collect: "results", Over: &ir.Over{Literals: []any{"a"}},
		Body: []*ir.Node{{Path: "steps.fan.body[0]", Kind: ir.KindDelegate, Role: "developer",
			Prompt: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "fan", Field: "collect"})}}}
	call := &ir.Node{Path: "steps.b", ID: "b", Kind: ir.KindEmit,
		Value: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "a", Field: "output", Path: []string{"upper(x)"}})}
	sibling := &ir.Node{Path: "steps.a", ID: "a", Kind: ir.KindEmit,
		Value: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "b", Field: "output", Path: []string{"diff"}})}

	runValueCases(t, []valueCase{
		{
			name:  "node reads its own output",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{self}},
			code:  CodeWF013, path: "steps.a.prompt[steps.a.output.diff]", sub: "reads its own output",
		},
		{
			name:  "loop terminator reads its own collect",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{selfCollect}},
		},
		{
			name:  "body reads the collect of the fan-out containing it",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{contained}},
			code:  CodeWF013, path: "steps.fan.body[0].prompt[steps.fan.collect]",
			sub: "reads the collect of the fan-out that contains it",
		},
		{
			name: "a call-shaped path segment is not a path",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.a", ID: "a", Kind: ir.KindDelegate, Role: "developer",
					Output: &ir.OutputSpec{Name: "out"}},
				call,
			}},
			code: CodeWF013, path: "steps.b.value[steps.a.output.upper(x)]", sub: "is not a plain name",
		},
		{
			name:  "a sibling output is not recursion",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{sibling, {Path: "steps.b", ID: "b", Kind: ir.KindEmit}}},
		},
		{
			name: "self-output read through the pipeline",
			doc: `  steps:
    - id: fix
      kind: delegate
      role: developer
      prompt: 'again: {{ steps.fix.output.diff }}'
`,
			code: CodeWF013, path: "steps.fix.prompt[steps.fix.output.diff]", sub: "reads its own output",
		},
		{
			name: "loop terminator through the pipeline",
			doc: `  steps:
    - id: rep
      kind: loop
      max_iterations: 3
      until: { from: steps.rep.collect, equals: pass }
      collect: outs
      body:
        kind: delegate
        role: developer
        prompt: repair it
`,
		},
	})
}

// TestTemplateIntegrityHandBuilt covers templates whose two views — the
// render program and the declared references — disagree, which only a graph
// assembled outside the resolver can express.
func TestTemplateIntegrityHandBuilt(t *testing.T) {
	ref := &ir.Ref{Root: ir.RootParams, Name: "task"}
	cases := []struct {
		name  string
		tmpl  *ir.Template
		path  string
		sub   string
		clean bool
	}{
		{
			name: "raw text never compiled",
			tmpl: &ir.Template{Raw: "{{ params.task }}"},
			path: "steps.a.value", sub: "raw text but no compiled segments",
		},
		{
			name: "literal still holds an opener",
			tmpl: &ir.Template{Raw: "a {{ b }}", Segments: []ir.Segment{{Literal: "a {{ b }} "}}},
			path: "steps.a.value.segment[0]", sub: "still holds a {{ opener",
		},
		{
			name: "segment is both literal and reference",
			tmpl: &ir.Template{Raw: "x", Segments: []ir.Segment{{Literal: "x", Ref: ref}}, Refs: []*ir.Ref{ref}},
			path: "steps.a.value.segment[0]", sub: "both literal text and a reference",
		},
		{
			name: "renders an undeclared reference",
			tmpl: &ir.Template{Raw: "x", Segments: []ir.Segment{{Ref: ref}}},
			path: "steps.a.value.segment[0]", sub: "without declaring it",
		},
		{
			name: "declares a reference it never renders",
			tmpl: &ir.Template{Raw: "x", Segments: []ir.Segment{{Literal: "x"}}, Refs: []*ir.Ref{ref}},
			path: "steps.a.value[params.task]", sub: "declares a reference it never renders",
		},
		{
			name: "compiled template agrees with itself",
			tmpl: compiled(ref), clean: true,
		},
		{
			name:  "literal-only template is well-formed",
			tmpl:  &ir.Template{Raw: "build it", Segments: []ir.Segment{{Literal: "build it"}}},
			clean: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &ir.Node{Path: "steps.a", ID: "a", Kind: ir.KindEmit, Value: tc.tmpl}
			graph := &ir.Graph{
				Name:   "hand",
				Params: map[string]ir.Param{"task": {Name: "task", Type: ir.ParamString}},
				Steps:  []*ir.Node{node},
			}
			diags := graphFindings(graph)
			if tc.clean {
				mustBeClean(t, diags)
				return
			}
			mustFind(t, diags, CodeWF013, tc.path, tc.sub)
		})
	}
}

// --------------------------------------------------------- WF014 (payloads)

// fanWithCollect is the source a clean settle or aggregate row can read
// without tripping WF010: a fan-out that really produces a collect.
func fanWithCollect() *ir.Node {
	return &ir.Node{Path: "steps.fan", ID: "fan", Kind: ir.KindFanout,
		As: "ws", Collect: "outs", Over: &ir.Over{Literals: []any{"a"}}}
}

func fanCollectSource() *ir.Source {
	return &ir.Source{Kind: ir.SourceStepCollect, StepID: "fan"}
}

func TestPayloadsMatchKind(t *testing.T) {
	runValueCases(t, []valueCase{
		{
			name: "cases on a gate",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.g", ID: "g", Kind: ir.KindGate,
				Cases: []ir.Case{{Value: "a", Then: "g"}}}}},
			code: CodeWF014, path: "steps.g.cases", sub: "not a payload field of a gate",
		},
		{
			name: "default on a loop",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.l", ID: "l", Kind: ir.KindLoop,
				Default: "l"}}},
			code: CodeWF014, path: "steps.l.default", sub: "not a payload field of a loop",
		},
		{
			name: "on_pass on a select",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.s", ID: "s", Kind: ir.KindSelect,
				OnPass: "s"}}},
			code: CodeWF014, path: "steps.s.on_pass", sub: "not a payload field of a select",
		},
		{
			name: "on_fail on a settle",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{fanWithCollect(),
				{Path: "steps.st", ID: "st", Kind: ir.KindSettle, OnFail: "fan"}}},
			code: CodeWF014, path: "steps.st.on_fail", sub: "not a payload field of a settle",
		},
		{
			name: "until on a fan-out",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{fanWithCollect(),
				{Path: "steps.f", ID: "f", Kind: ir.KindFanout, Collect: "more",
					Until: &ir.Selector{From: fanCollectSource()}}}},
			code: CodeWF014, path: "steps.f.until", sub: "not a payload field of a fanout",
		},
		{
			name: "max_iterations on a gate",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.g", ID: "g", Kind: ir.KindGate,
				MaxIterations: intPtr(3)}}},
			code: CodeWF014, path: "steps.g.max_iterations", sub: "not a payload field of a gate",
		},
		{
			name: "over and as on a loop",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.l", ID: "l", Kind: ir.KindLoop,
				Over: &ir.Over{Literals: []any{1}}, As: "ws"}}},
			code: CodeWF014, path: "steps.l.over", sub: "not a payload field of a loop",
		},
		{
			name: "join and max_parallel on a loop",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.l", ID: "l", Kind: ir.KindLoop,
				Join: ir.JoinAll, MaxParallel: intPtr(2)}}},
			code: CodeWF014, path: "steps.l.join", sub: "not a payload field of a loop",
		},
		{
			name: "join on a fan-out is legal, on a branch is not",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.b", ID: "b", Kind: ir.KindBranch,
				Join: ir.JoinQuorum, Quorum: intPtr(2)}}},
			code: CodeWF014, path: "steps.b.join", sub: "not a payload field of a branch",
		},
		{
			name: "role and prompt on an emit",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.e", ID: "e", Kind: ir.KindEmit,
				Role: "developer", Prompt: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"})}}},
			code: CodeWF014, path: "steps.e.role", sub: "not a payload field of a emit",
		},
		{
			name: "lane on a human",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.h", ID: "h", Kind: ir.KindHuman,
				Lane: ir.LaneReport}}},
			code: CodeWF014, path: "steps.h.lane", sub: "not a payload field of a human",
		},
		{
			name: "judge on a settle",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{fanWithCollect(),
				{Path: "steps.st", ID: "st", Kind: ir.KindSettle, Judge: &ir.Judge{Role: "r"},
					Proposals: &ir.Selector{From: fanCollectSource()}, Action: ir.SettleApply}}},
			code: CodeWF014, path: "steps.st.judge", sub: "not a payload field of a settle",
		},
		{
			name: "candidates on a gate",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.g", ID: "g", Kind: ir.KindGate,
				Candidates: &ir.Selector{From: &ir.Source{Kind: ir.SourceParams, Name: "rows"}}}}},
			code: CodeWF014, path: "steps.g.candidates", sub: "not a payload field of a gate",
		},
		{
			name: "assert on a select",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.s", ID: "s", Kind: ir.KindSelect,
				Assert: &ir.Assert{Name: "diff_within_limit"}}}},
			code: CodeWF014, path: "steps.s.assert", sub: "not a payload field of a select",
		},
		{
			name: "gate payload is complete",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.g", ID: "g", Kind: ir.KindGate,
				Assert: &ir.Assert{Name: "diff_within_limit"},
				Judge:  &ir.Judge{Role: "reviewer", Prompt: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"})},
				OnPass: "g", OnFail: "g"}}},
			code: "", path: "", sub: "",
		},
		{
			name: "select and branch payloads are complete",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{fanWithCollect(),
				{Path: "steps.s", ID: "s", Kind: ir.KindSelect,
					Candidates: &ir.Selector{From: fanCollectSource()}, Policy: ir.SelectFirstSuccess,
					Judge: &ir.Judge{Role: "reviewer"}},
				{Path: "steps.b", ID: "b", Kind: ir.KindBranch,
					On:      &ir.Selector{From: &ir.Source{Kind: ir.SourceStepOutput, StepID: "s"}},
					Cases:   []ir.Case{{Value: "x", Then: "s"}},
					Default: "s"}}},
			code: "", path: "", sub: "",
		},
		{
			name: "fan-out and loop payloads are complete",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{
				{Path: "steps.f", ID: "f", Kind: ir.KindFanout,
					Over: &ir.Over{Literals: []any{"a", "b"}}, As: "ws", MaxParallel: intPtr(2),
					Collect: "results", Join: ir.JoinQuorum, Quorum: intPtr(1),
					OnItemFailure: ir.OnItemCollectFailures,
					Body:          []*ir.Node{{Path: "steps.f.body[0]", Kind: ir.KindDelegate, Role: "developer"}}},
				{Path: "steps.l", ID: "l", Kind: ir.KindLoop, MaxIterations: intPtr(3),
					ContinueFrom: compiled(&ir.Ref{Root: ir.RootRun, Field: "attempt"}),
					Until:        &ir.Selector{From: &ir.Source{Kind: ir.SourceStepCollect, StepID: "l"}},
					Collect:      "outs",
					Body:         []*ir.Node{{Path: "steps.l.body[0]", Kind: ir.KindDelegate, Role: "developer"}}},
			}},
			code: "", path: "", sub: "",
		},
		{
			name: "delegate payload is complete",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.d", ID: "d", Kind: ir.KindDelegate,
				Role: "developer", Prompt: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"}),
				Lane:         ir.LaneProposal,
				SeedFrom:     compiled(&ir.Ref{Root: ir.RootRun, Field: "attempt"}),
				ContinueFrom: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"}),
				ForkFrom:     compiled(&ir.Ref{Root: ir.RootRun, Field: "id"}),
				Provider:     "p", Model: "m", MaxTurns: intPtr(3),
				Grants:  []ir.Grant{{Path: "src", Access: ir.AccessReadWrite}},
				Network: ir.NetworkDenyAll, World: "w"}}},
			code: "", path: "", sub: "",
		},
		{
			name: "settle, call, emit, aggregate and human payloads are complete",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{fanWithCollect(),
				{Path: "steps.st", ID: "st", Kind: ir.KindSettle,
					Proposals: &ir.Selector{From: fanCollectSource()},
					Where:     &ir.Selector{From: fanCollectSource()},
					Action:    ir.SettleApply, Order: ir.OrderSmallestDiffFirst,
					OnConflict: ir.OnConflictEscalate, OnEmpty: ir.OnEmptySkip},
				{Path: "steps.c", ID: "c", Kind: ir.KindCall, Workflow: "other",
					MaxDepth: intPtr(2), CallParams: map[string]ir.ParamValue{"k": {Const: 1}}},
				{Path: "steps.e", ID: "e", Kind: ir.KindEmit,
					Value: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"}), ArtifactPath: "a.md"},
				{Path: "steps.a", ID: "a", Kind: ir.KindAggregate, Strategy: ir.AggregateConcat,
					Inputs: []*ir.Selector{{From: fanCollectSource()}}},
				{Path: "steps.h", ID: "h", Kind: ir.KindHuman,
					Prompt: compiled(&ir.Ref{Root: ir.RootRun, Field: "id"}), OnTimeout: ir.OnFailureSkip},
			}},
			code: "", path: "", sub: "",
		},
		{
			name: "unknown kind is left to the decoder",
			graph: &ir.Graph{Name: "hand", Steps: []*ir.Node{{Path: "steps.u", ID: "u",
				Kind: ir.Kind("mystery"), Cases: []ir.Case{{Then: "u"}}}}},
			code: "", path: "", sub: "",
		},
	})
}

// TestPayloadFieldDroppedByResolution states the limit of WF014 from a
// document, which the graph-level matrix cannot cross: the resolver copies
// only the fields its kind understands, so a misplaced authoring key arrives
// as absence and there is nothing in the graph left to name. Reporting a
// declaration the resolver threw away needs this rule's spec-side half — the
// same seam WF012 uses for Selector.Field — and is recorded as a gap here
// rather than pretended about.
func TestPayloadFieldDroppedByResolution(t *testing.T) {
	runValueCases(t, []valueCase{{
		name: "a select's gate disposition is dropped before the graph exists",
		doc: fanDoc + `    - id: pick
      kind: select
      candidates: { from: steps.fan.collect }
      policy: judge
      on_pass: pick
`,
		code: "", path: "", sub: "",
	}, {
		name: "a settle's policy has no compiled home either",
		doc: fanDoc + `    - id: land
      kind: settle
      proposals: { from: steps.fan.collect }
      policy: path_disjoint
      action: apply
`,
		code: "", path: "", sub: "",
	}})
}

// ------------------------------------------------------- WF005 name scoping

// TestOutputNameScopeIsPerContainer pins the scope discipline the value
// family's reference scope implies: a body is its own namespace, a
// body-local name may not shadow a top-level binding, and the top-level
// namespace stays whole-graph unique.
func TestOutputNameScopeIsPerContainer(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		code  string
		path  string
		sub   string
	}{
		{
			name: "two containers may bind the same body-local name",
			steps: `  steps:
    - id: a
      kind: fanout
      over: [x]
      as: ws
      collect: first
      body:
        kind: delegate
        role: developer
        prompt: 'build {{ ws }}'
        output: proposal
    - id: b
      kind: fanout
      over: [y]
      as: ws
      collect: second
      body:
        kind: delegate
        role: reviewer
        prompt: 'review {{ ws }}'
        output: proposal
`,
		},
		{
			name: "siblings in one body may not",
			steps: `  steps:
    - id: fan
      kind: fanout
      over: [x]
      as: ws
      collect: results
      body:
        - kind: delegate
          role: developer
          prompt: 'build {{ ws }}'
          output: result
        - kind: delegate
          role: reviewer
          prompt: 'review {{ ws }}'
          output: result
`,
			code: CodeWF005, path: "steps.fan.body[1].output.name",
			sub: "already bound at steps.fan.body[0]",
		},
		{
			name: "a body name may not shadow a top-level binding",
			steps: `  steps:
    - id: top
      kind: delegate
      role: planner
      prompt: plan it
      output: Alpha
    - id: fan
      kind: fanout
      over: [x]
      as: ws
      collect: results
      body:
        kind: delegate
        role: developer
        prompt: 'build {{ ws }}'
        output: Alpha
`,
			code: CodeWF005, path: "steps.fan.body[0].output.name",
			sub: "shadows the top-level binding at steps.top",
		},
		{
			name: "top-level names stay unique",
			steps: `  steps:
    - id: first
      kind: delegate
      role: developer
      prompt: build it
      output: result
    - id: second
      kind: delegate
      role: reviewer
      prompt: review it
      output: result
`,
			code: CodeWF005, path: "steps.second.output.name", sub: "already bound at steps.first",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := findingsOf(t, tc.steps)
			if tc.code == "" {
				if len(diags) != 0 {
					t.Fatalf("want zero findings, got:%s", render(diags))
				}
				return
			}
			mustFind(t, diags, tc.code, tc.path, tc.sub)
		})
	}
}

// ------------------------------------------------------- corpus and contract

// TestValueCorpusHasNoValueFindings is this family's exit criterion,
// enumerated dynamically so a golden a sibling lands cannot sit unasserted.
// Both modes run: with the document, where the field: checks apply, and
// without it, where they must stay silent.
func TestValueCorpusHasNoValueFindings(t *testing.T) {
	patterns := []string{
		filepath.Join("..", "ir", "testdata", "specs", "*.yaml"),
		filepath.Join("..", "spec", "testdata", "golden", "*.canonical.json"),
		filepath.Join("..", "spec", "testdata", "valid", "*.yaml"),
		filepath.Join("..", "spec", "testdata", "valid", "*.json"),
	}
	seen := map[string]bool{}
	total := 0
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		sort.Strings(files)
		if len(files) == 0 {
			t.Fatalf("corpus %s matched no documents", pattern)
		}
		for _, file := range files {
			if seen[file] {
				continue
			}
			seen[file] = true
			total++
			t.Run(filepath.Base(file), func(t *testing.T) {
				doc, err := spec.Parse(readFixtureFile(t, file))
				if err != nil {
					t.Fatalf("%s: document did not decode: %v", file, err)
				}
				g, err := ir.Resolve(doc)
				if err != nil {
					t.Fatalf("%s: document did not resolve: %v", file, err)
				}
				if diags := Values(doc, g); len(diags) != 0 {
					t.Errorf("%s: want zero value findings, got:%s", file, render(diags))
				}
				if diags := graphFindings(g); len(diags) != 0 {
					t.Errorf("%s: graph-only mode reported findings:%s", file, render(diags))
				}
			})
		}
	}
	if total < 4 {
		t.Errorf("only %d golden documents checked; the corpora look truncated", total)
	}
}

// TestFanoutSettleItemBindingStaysSilent names the regression this family is
// most prone to: a fan-out iterating another fan-out's collect declares no
// element shape, so the body's per-item field access must decide nothing.
func TestFanoutSettleItemBindingStaysSilent(t *testing.T) {
	file := filepath.Join("..", "ir", "testdata", "specs", "fanout_settle.yaml")
	doc, err := spec.Parse(readFixtureFile(t, file))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	g, err := ir.Resolve(doc)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if diags := Values(doc, g); len(diags) != 0 {
		t.Errorf("fanout_settle reported value findings:%s", render(diags))
	}
}

func TestValuesNilGraphAndDeterminism(t *testing.T) {
	if got := Values(nil, nil); got != nil {
		t.Errorf("Values(nil, nil) = %v, want nil", got)
	}
	if got := Values(&spec.Document{}, nil); got != nil {
		t.Errorf("Values(doc, nil) = %v, want nil", got)
	}

	graph := &ir.Graph{
		Name:   "everything-wrong-values",
		Params: map[string]ir.Param{"task": {Name: "task", Type: ir.ParamString}},
		Steps: []*ir.Node{
			{Path: "steps.a", ID: "a", Kind: ir.KindDelegate, Role: "developer",
				Prompt: compiled(&ir.Ref{Root: ir.RootSteps, StepID: "a", Field: "output", Path: []string{"diff"}}),
				Output: &ir.OutputSpec{Name: "result", Type: ir.OutputVerdict,
					Schema: `{"type":"array","items":{"type":"string"}}`}},
			{Path: "steps.b", ID: "b", Kind: ir.KindFanout, As: "ws", Collect: "results",
				Needs: []string{"a"},
				Over: &ir.Over{Selector: &ir.Selector{
					From: &ir.Source{Kind: ir.SourceParams, Name: "task"}}},
				Body: []*ir.Node{{Path: "steps.b.body[0]", Kind: ir.KindGate,
					Cases: []ir.Case{{Value: "x", Then: "missing"}}}}},
			{Path: "steps.c", ID: "c", Kind: ir.KindSettle, Needs: []string{"b"},
				Proposals: &ir.Selector{From: &ir.Source{Kind: ir.SourceStepOutput, StepID: "a"}},
				Until:     &ir.Selector{From: &ir.Source{Kind: ir.SourceItem, Name: "ws"}}},
		},
	}

	first := codeList(Values(nil, graph))
	for range 5 {
		if got := codeList(Values(nil, graph)); got != first {
			t.Fatalf("Values is not deterministic: %q vs %q", got, first)
		}
	}
	diags := Values(nil, graph)
	for i := 1; i < len(diags); i++ {
		if diags[i-1].Code > diags[i].Code {
			t.Errorf("findings are not grouped by code: %s before %s in %q",
				diags[i-1].Code, diags[i].Code, codeList(diags))
		}
	}
	for _, code := range []string{CodeWF010, CodeWF011, CodeWF012, CodeWF013, CodeWF014} {
		if !hasCode(diags, code) {
			t.Errorf("no %s finding in a graph built to trigger every rule:%s", code, render(diags))
		}
	}
}

func intPtr(v int) *int { return &v }
