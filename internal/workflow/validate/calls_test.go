package validate

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// callSet is the registry stand-in the validate tests resolve names with: it
// maps workflow names to resolved graphs, the shape Env.Calls reads, with no
// filesystem in the way.
type callSet map[string]*ir.Graph

func (c callSet) ResolveCall(name string) (CallDocument, bool) {
	g, ok := c[name]
	if !ok || g == nil {
		return CallDocument{}, false
	}
	return CallDocument{Name: name, Graph: g}, true
}

// callDocGraphs resolves documents whose names and steps the walker threads,
// failing the test when any of them is not a graph.
func callDocGraphs(t *testing.T, docs map[string]string) callSet {
	t.Helper()
	out := callSet{}
	for name, steps := range docs {
		g, diags := Validate([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: " + name + "}\nspec:\n" + steps))
		if g == nil {
			t.Fatalf("document %s did not resolve: %v", name, render(diags))
		}
		out[name] = g
	}
	return out
}

// callStep is one call node, the shape every document here threads.
const callStep = "    - {id: c, kind: call, workflow: %s}\n"

// TestCallReferenceFindings pins WF002's call half: a call that names nothing,
// a name that cannot be a registry name, no registry at all, and a name the
// registry does not hold — each its own message, all fail-closed.
func TestCallReferenceFindings(t *testing.T) {
	g := costGraph(t, "  steps:\n    - {id: c, kind: call, workflow: helper}\n")
	names := map[string]string{
		"empty workflow": "  steps:\n    - {id: c, kind: call}\n",
	}
	for name, steps := range names {
		dg := costGraph(t, steps)
		findings := Calls(dg, Env{})
		if len(findings) != 1 || findings[0].Code != CodeWF002 {
			t.Errorf("%s: findings = %v, want one WF002", name, render(findings))
			continue
		}
		if findings[0].Path != "steps.c.workflow" {
			t.Errorf("%s: path = %s, want steps.c.workflow", name, findings[0].Path)
		}
	}

	// Without a registry, a resolvable-shaped name is reported as
	// unresolvable: fail closed, and say why.
	findings := Calls(g, Env{})
	if len(findings) != 1 || findings[0].Code != CodeWF002 {
		t.Fatalf("no registry: findings = %v, want one WF002", render(findings))
	}
	if !strings.Contains(findings[0].Message, "--workflow-root") {
		t.Errorf("no-registry message does not name the flag: %s", findings[0].Message)
	}

	// With a registry that does not hold the name.
	findings = Calls(g, Env{Calls: callSet{}})
	if len(findings) != 1 || findings[0].Code != CodeWF002 {
		t.Fatalf("unknown name: findings = %v, want one WF002", render(findings))
	}
	if !strings.Contains(findings[0].Message, "helper") {
		t.Errorf("unknown-name message does not name the workflow: %s", findings[0].Message)
	}

	// A traversal-shaped name is refused as a name, whatever the registry
	// holds: containment's first half.
	traversal := costGraph(t, "  steps:\n    - {id: c, kind: call, workflow: \"../secrets\"}\n")
	findings = Calls(traversal, Env{Calls: callSet{}})
	if len(findings) != 1 || findings[0].Code != CodeWF002 {
		t.Fatalf("traversal name: findings = %v, want one WF002", render(findings))
	}
	if !strings.Contains(findings[0].Message, "not a subworkflow name") {
		t.Errorf("traversal message does not refuse it as a name: %s", findings[0].Message)
	}
}

// TestCallResolvedNameSilent pins that a call the registry resolves is not a
// finding, and that the cross-document walk agrees: no cycle, no depth.
func TestCallResolvedNameSilent(t *testing.T) {
	docs := callDocGraphs(t, map[string]string{
		"table-driven": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "helper"),
		"helper":       "  steps:\n    - {id: work, kind: emit, value: done}\n",
	})
	findings := Calls(docs["table-driven"], Env{Calls: docs})
	if len(findings) != 0 {
		t.Fatalf("a resolved call produced findings: %v", render(findings))
	}
}

// TestCallCycleAcrossDocuments pins WF003's cross-document half: a calls b,
// b calls a is refused — the message names the loop, and the traversal
// budget never enters the cycle.
func TestCallCycleAcrossDocuments(t *testing.T) {
	docs := callDocGraphs(t, map[string]string{
		"entry": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "a"),
		"a":     "  steps:\n" + strings.ReplaceAll(callStep, "%s", "b"),
		"b":     "  steps:\n" + strings.ReplaceAll(callStep, "%s", "a"),
	})
	findings := Calls(docs["entry"], Env{Calls: docs})
	if len(findings) != 1 {
		t.Fatalf("cross-document cycle findings = %v, want exactly one", render(findings))
	}
	if findings[0].Code != CodeWF003 {
		t.Errorf("code = %s, want WF003", findings[0].Code)
	}
	if !strings.Contains(findings[0].Message, "a -> b -> a") {
		t.Errorf("cycle message does not name the loop: %s", findings[0].Message)
	}
}

// TestCallSelfRecursionBound pins the one legal recursion: a document that
// calls itself under a declared bound walks silently up to the bound, and
// the depth it may compose is that bound — max_depth: 2 means the document's
// own pass plus two entered copies, never a third.
func TestCallSelfRecursionBound(t *testing.T) {
	self := "  steps:\n" + strings.ReplaceAll(callStep, "%s", "rec") +
		"    - {id: work, kind: delegate, role: developer, prompt: hi}\n"
	docs := callDocGraphs(t, map[string]string{
		"entry": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "rec"),
		"rec":   strings.ReplaceAll(self, "kind: call, workflow: rec}", "kind: call, workflow: rec, max_depth: 2}"),
	})
	findings := Calls(docs["entry"], Env{Calls: docs})
	if len(findings) != 0 {
		t.Fatalf("a bounded self-call produced findings: %v", render(findings))
	}

	// max_depth: 2 admits two entered copies; a document chain that re-enters
	// the third time is over the bound. Three self edges deep is the smallest
	// witness: entry -> rec (copy 1) -> rec again (copy 2, admitted by the
	// bound) -> rec a third time is silently beyond the budget, which the
	// walk enforces by not following the edge again.
	deep := callDocGraphs(t, map[string]string{
		"entry": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "rec"),
		"rec":   strings.ReplaceAll(self, "kind: call, workflow: rec}", "kind: call, workflow: rec, max_depth: 1}"),
	})
	// max_depth: 1 admits exactly one re-entry; the second is beyond the
	// declaration but legal in shape — the runtime guard refuses the run, and
	// the walk stays silent because the declared bound bound its path.
	if findings := Calls(deep["entry"], Env{Calls: deep}); len(findings) != 0 {
		t.Errorf("a self-call at the edge of its bound produced findings: %v", render(findings))
	}
}

// TestCallDepthCeiling pins WF003's excessive-depth half: a chain of distinct
// documents, each calling the next, is refused once its depth passes
// MaxCallDepth — the cost bomb no document value bounds.
func TestCallDepthCeiling(t *testing.T) {
	docs := map[string]string{}
	total := MaxCallDepth + 2
	for i := 0; i < total; i++ {
		if i == total-1 {
			docs["d"+itoa(i)] = "  steps:\n    - {id: work, kind: emit, value: done}\n"
		} else {
			docs["d"+itoa(i)] = "  steps:\n" + strings.ReplaceAll(callStep, "%s", "d"+itoa(i+1))
		}
	}
	set := callDocGraphs(t, docs)
	findings := Calls(set["d0"], Env{Calls: set})
	if len(findings) != 1 {
		t.Fatalf("depth chain findings = %v, want exactly one", render(findings))
	}
	if findings[0].Code != CodeWF003 {
		t.Errorf("code = %s, want WF003", findings[0].Code)
	}
	if !strings.Contains(findings[0].Message, "exceeds the limit") {
		t.Errorf("depth message does not state the ceiling: %s", findings[0].Message)
	}

	// One shorter stays silent: the ceiling is the limit, not a cliff below it.
	docs["d"+itoa(total-3)] = "  steps:\n    - {id: work, kind: emit, value: done}\n"
	set = callDocGraphs(t, docs)
	if findings := Calls(set["d0"], Env{Calls: set}); len(findings) != 0 {
		t.Errorf("a chain inside the ceiling produced findings: %v", render(findings))
	}
}

// TestCallCostExpansion pins WF033's registry-precise mode: a call's price is
// the child's own worst case, the child's unbounded repetition travels to the
// parent's Unbound list prefixed with the child's name, and a self-recursive
// child is priced by the copies its declared bound admits.
func TestCallCostExpansion(t *testing.T) {
	docs := callDocGraphs(t, map[string]string{
		"table-driven": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "helper"),
		"helper": "  steps:\n" +
			"    - {id: w1, kind: delegate, role: developer, prompt: hi}\n" +
			"    - {id: w2, kind: delegate, role: developer, prompt: hi}\n",
	})
	parent := docs["table-driven"]

	est := Explain(parent, Env{Calls: docs}).Cost
	if est.Dispatches != 2 {
		t.Errorf("dispatches = %d, want the child's 2", est.Dispatches)
	}
	if !est.Complete {
		t.Errorf("estimate is a floor: %v", est.Unbound)
	}

	// A child whose repetition nobody bounded marks the parent's estimate as
	// a floor, with the child's own path prefixed by its name.
	unbounded := callDocGraphs(t, map[string]string{
		"helper": "  steps:\n" +
			"    - {id: w1, kind: delegate, role: developer, prompt: hi}\n" +
			"    - id: f\n      kind: fanout\n      over: {from: steps.w1.output.workstreams}\n" +
			"      max_parallel: 2\n      body: {kind: delegate, role: developer, prompt: hi}\n",
	})
	ubParent := callDocGraphs(t, map[string]string{
		"table-driven": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "helper"),
	})
	est = Explain(ubParent["table-driven"], Env{Calls: unbounded}).Cost
	if est.Complete {
		t.Error("an unbounded child left the estimate complete")
	}
	if len(est.Unbound) != 1 || est.Unbound[0] != "helper/steps.f" {
		t.Errorf("unbound = %v, want [helper/steps.f]", est.Unbound)
	}

	// No registry: the old floor semantics stand.
	est = Explain(parent, Env{}).Cost
	if est.Complete {
		t.Error("an unresolvable child left the estimate complete")
	}

	// A self-recursive child: onePass prices its own dispatches, the self
	// edge's repetition is the copies its bound admits, and the parent pays
	// for all of them. max_depth: 2 means one pass plus two entered copies.
	self := callDocGraphs(t, map[string]string{
		"selfhelper": "  steps:\n" +
			"    - {id: c, kind: call, workflow: selfhelper, max_depth: 2}\n" +
			"    - {id: w1, kind: delegate, role: developer, prompt: hi}\n",
	})
	selfParent := callDocGraphs(t, map[string]string{
		"table-driven": "  steps:\n" + strings.ReplaceAll(callStep, "%s", "selfhelper"),
	})
	est = Explain(selfParent["table-driven"], Env{Calls: self}).Cost
	if est.Dispatches != 3 {
		t.Errorf("dispatches = %d, want 3 (one pass plus two bound copies)", est.Dispatches)
	}
	if !est.Complete {
		t.Errorf("a bounded self-recursion left a floor: %v", est.Unbound)
	}
}

func TestCallCostRootSelfFactor(t *testing.T) {
	steps := "  steps:\n" +
		strings.ReplaceAll(strings.ReplaceAll(callStep, "%s", "table-driven"), "workflow: table-driven}", "workflow: table-driven, max_depth: 2}") +
		"    - {id: w1, kind: delegate, role: developer, prompt: hi}\n"
	g := costGraph(t, steps)
	est := Explain(g, Env{Calls: callSet{"table-driven": g}}).Cost
	if est.Dispatches != 3 {
		t.Errorf("dispatches = %d, want 3 (the root pass plus two bound copies)", est.Dispatches)
	}
	if !est.Complete {
		t.Errorf("a bounded root recursion left a floor: %v", est.Unbound)
	}
}

// TestCallBudgetIncludesExpansion pins WF033 end to end: a parent whose
// budget the child's expansion breaks is refused, with the worst case
// carrying the child's dispatches.
func TestCallBudgetIncludesExpansion(t *testing.T) {
	docs := callDocGraphs(t, map[string]string{
		"table-driven": "  budgets:\n    max_dispatches: 1\n  steps:\n" + strings.ReplaceAll(callStep, "%s", "helper"),
		"helper": "  steps:\n" +
			"    - {id: w1, kind: delegate, role: developer, prompt: hi}\n" +
			"    - {id: w2, kind: delegate, role: developer, prompt: hi}\n",
	})
	findings := Cost(docs["table-driven"], Env{Calls: docs})
	if len(findings) != 1 || findings[0].Code != CodeWF033 {
		t.Fatalf("findings = %v, want one WF033", render(findings))
	}
	if !strings.Contains(findings[0].Message, "2 dispatches") {
		t.Errorf("finding does not price the child's dispatches: %s", findings[0].Message)
	}
}

// TestCallIdentityIsRegistryNameNotMetadata pins the documented edge the
// single-document and cross-document identities disagree on: callDepth
// matches a self-call against metadata.name, while the cross-document walk
// and the runtime frame guard identify documents by registry name. A
// document whose metadata.name differs from its --workflow-root file name
// (entry.yaml whose metadata.name is "inner") is invisible to callDepth's
// "must declare max_depth" rule when it calls its own registry name, and
// the registry-name identities are what hold it. This test exists so the two
// identities cannot drift silently: the mismatched self-call is silent along
// callDepth's axis and terminates on the walk's, while the matching sibling
// is refused statically.
func TestCallIdentityIsRegistryNameNotMetadata(t *testing.T) {
	// The registry entry: metadata.name "inner", calling its own registry
	// name, undeclared. callDepth cannot see this self-call (it matches on
	// metadata.name); the walk reads it as the one legal recursion by the
	// name the registry holds.
	entryDiags := []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: inner}\nspec:\n  steps:\n" +
		"    - {id: c, kind: call, workflow: entry}\n")
	entryGraph, diags := Validate(entryDiags)
	if entryGraph == nil {
		t.Fatalf("the registry document did not resolve: %v", render(diags))
	}

	// The root: a document that calls entry under its registry name while
	// carrying some other metadata.name of its own.
	root, diags := Validate([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: caller}\nspec:\n  steps:\n" +
		strings.ReplaceAll(callStep, "%s", "entry")))
	if root == nil {
		t.Fatalf("the root did not resolve: %v", render(diags))
	}
	if findings := Calls(root, Env{Calls: callSet{"entry": entryGraph}}); len(findings) != 0 {
		t.Errorf("the mismatched self-call is silent statically and must carry no findings: %v", render(findings))
	}

	// The matching sibling — metadata.name equal to the registry name — is
	// refused statically by callDepth, the single document's half of the
	// same rule. The two identities disagree exactly here, deliberately:
	// see the walk's doc and the runtime frame guard for the enforced half.
	same, diags := Validate([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: entry}\nspec:\n  steps:\n" +
		"    - {id: c, kind: call, workflow: entry}\n"))
	if same == nil {
		t.Fatalf("the matching document did not resolve: %v", render(diags))
	}
	if findings := Structural(same); len(findings) != 1 || findings[0].Code != CodeWF003 {
		t.Errorf("the matching undeclared self-call is not refused by callDepth: %v", render(findings))
	}
}
