package validate

import (
	"testing"
)

// TestLinearitySmoke is the compile-and-behaves check the rest of the family's
// tests are built on: a leaked proposal, a settled one, and the run-exit
// residue WF025 reports alongside the leak.
func TestLinearitySmoke(t *testing.T) {
	leaky := `  steps:
    - id: build
      kind: delegate
      role: developer
      lane: proposal
      output: proposal
      prompt: do it
    - id: check
      kind: delegate
      role: tester
      lane: report
      prompt: check it
`
	g, diags := Validate(documentWith(leaky))
	if g == nil {
		t.Fatalf("document did not resolve:%s", render(diags))
	}
	t.Logf("leaky:%s", render(diags))
	if !hasCode(diags, CodeWF021) {
		t.Errorf("want a WF021 leak for the unsettled proposal, got:%s", render(diags))
	}
	if !hasCode(diags, CodeWF025) {
		t.Errorf("want WF025 exit residue alongside the leak, got:%s", render(diags))
	}

	clean := `  steps:
    - id: build
      kind: delegate
      role: developer
      lane: proposal
      output: proposal
      prompt: do it
    - id: land
      kind: settle
      needs: [build]
      proposals: { from: steps.build.output }
      action: apply
      on_empty: fail
`
	g, diags = Validate(documentWith(clean))
	if g == nil {
		t.Fatalf("document did not resolve:%s", render(diags))
	}
	if len(diags) != 0 {
		t.Errorf("want zero findings, got:%s", render(diags))
	}
	proof := Prove(g)
	if proof == nil || len(proof.Proposals) != 1 {
		t.Fatalf("proof = %+v, want one proposal", proof)
	}
	p := proof.Proposals[0]
	if p.Proposal != "steps.build" || !p.SettledOnEveryPath || !p.OnceOnEveryPath {
		t.Errorf("proof record = %+v, want steps.build settled once", p)
	}
	if len(p.Dispositions) != 1 || p.Dispositions[0] != "steps.land" {
		t.Errorf("dispositions = %v, want [steps.land]", p.Dispositions)
	}
	if len(proof.OpenAtExit) != 0 {
		t.Errorf("OpenAtExit = %v, want none", proof.OpenAtExit)
	}
}
