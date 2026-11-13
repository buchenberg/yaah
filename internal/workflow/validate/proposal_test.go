package validate

import (
	"path/filepath"
	"sort"
	"testing"
)

// TestSettlementProofProperty is the family's exit criterion: every accepted
// workflow document in the epic — zero findings from the whole Validate
// pipeline — must carry a settlement proof in which each proposal has exactly
// one disposition: settled on every reachable path, never settled twice, and
// nothing left in the run-end residue. The proof is computed from the same
// fixed point the rules read, so this is not a restatement of the diagnostics;
// it is the artifact `yaah workflow explain` prints, asserted to be consistent
// with them.
func TestSettlementProofProperty(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		filepath.Join("..", "spec", "testdata", "valid", "*.yaml"),
		filepath.Join("..", "spec", "testdata", "golden", "*.canonical.json"),
		filepath.Join("..", "ir", "testdata", "specs", "*.yaml"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	if len(files) < 4 {
		t.Fatalf("only %d corpus documents found; the corpus looks truncated", len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			g, diags := Validate(readFixtureFile(t, file))
			if g == nil {
				t.Fatalf("%s did not resolve:%s", file, render(diags))
			}
			if len(diags) != 0 {
				t.Fatalf("%s is not an accepted document:%s", file, render(diags))
			}
			proof := Prove(g)
			if len(proof.OpenAtExit) != 0 {
				t.Errorf("%s: accepted document still has %d proposal(s) open at the run end:%v",
					file, len(proof.OpenAtExit), proof.OpenAtExit)
			}
			for _, rec := range proof.Proposals {
				if len(rec.Dispositions) == 0 {
					t.Errorf("%s: proposal %s has no disposition step in the proof", file, rec.Proposal)
				}
				if !rec.SettledOnEveryPath {
					t.Errorf("%s: proposal %s is not settled on every reachable path", file, rec.Proposal)
				}
				if !rec.OnceOnEveryPath {
					t.Errorf("%s: proposal %s is settled twice on some path", file, rec.Proposal)
				}
				if rec.Stranded {
					t.Errorf("%s: proposal %s is stranded in a body with no collect", file, rec.Proposal)
				}
			}
		})
	}
}

// TestLinearityAdversarial exercises the five defects the bead names — a
// leaked proposal, a double settle, a skip with no catch, a fork that consumes
// its source, and a select with an undeclared judge — through the real
// pipeline, and asserts on this family's findings only so an incidental
// shortage in another family's view cannot mask or invent one.
func TestLinearityAdversarial(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		want  []string
		clean bool
	}{
		{
			name: "leaked proposal",
			steps: `  steps:
    - id: build
      kind: delegate
      role: developer
      lane: proposal
      prompt: build it
    - id: check
      kind: delegate
      role: tester
      lane: report
      prompt: check it
`,
			want: []string{CodeWF021, CodeWF025},
		},
		{
			name: "double settle",
			steps: `  steps:
    - id: build
      kind: delegate
      role: developer
      lane: proposal
      prompt: build it
    - id: land
      kind: settle
      needs: [build]
      proposals: { from: steps.build.output }
      action: apply
      on_empty: fail
    - id: cleanup
      kind: settle
      needs: [land]
      proposals: { from: steps.build.output }
      action: discard
      on_empty: fail
`,
			// A discard after an apply is a double settle — WF021. WF022
			// is the opposite verb direction (a select/apply whose target
			// was released or discarded earlier) and is pinned below.
			want: []string{CodeWF021},
		},
		{
			name: "skip with no catch",
			steps: `  steps:
    - id: build
      kind: fanout
      over: [alpha, beta]
      as: ws
      max_parallel: 2
      collect: proposals
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: 'build {{ ws }}'
    - id: land
      kind: settle
      needs: [build]
      proposals: { from: steps.build.collect }
      action: apply
      policy: path_disjoint
      on_conflict: skip
`,
			want: []string{CodeWF024},
		},
		{
			name: "fork consumes its source",
			steps: `  steps:
    - id: implement
      kind: delegate
      role: developer
      lane: proposal
      prompt: implement it
    - id: alt
      kind: delegate
      role: developer
      lane: proposal
      needs: [implement]
      fork_from: "{{ steps.implement.output.proposal_id }}"
      prompt: a second way
    - id: land
      kind: settle
      needs: [alt]
      proposals: { from: steps.alt.output }
      action: apply
      on_empty: fail
`,
			want: []string{CodeWF027},
		},
		{
			name: "select with an undeclared judge",
			steps: `  steps:
    - id: build
      kind: fanout
      over: [one, two, three]
      as: style
      max_parallel: 3
      collect: candidates
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: 'try {{ style }}'
    - id: pick
      kind: select
      needs: [build]
      candidates: { from: steps.build.collect }
      policy: judge
      output: winner
    - id: land
      kind: settle
      needs: [pick]
      proposals: { from: steps.pick.output, as_list: true }
      action: apply
      on_empty: fail
`,
			want: []string{CodeWF028},
		},
		{
			name: "release then apply",
			steps: `  steps:
    - id: build
      kind: delegate
      role: developer
      lane: proposal
      prompt: build it
    - id: drop
      kind: settle
      needs: [build]
      proposals: { from: steps.build.output }
      action: discard
      on_empty: fail
    - id: land
      kind: settle
      needs: [drop]
      proposals: { from: steps.build.output }
      action: apply
      on_empty: fail
`,
			want: []string{CodeWF022},
		},
		{
			name: "parallel three_way applies",
			steps: `  steps:
    - id: one
      kind: delegate
      role: developer
      lane: proposal
      prompt: first
    - id: two
      kind: delegate
      role: developer
      lane: proposal
      prompt: second
    - id: land_a
      kind: settle
      needs: [one]
      proposals: { from: steps.one.output }
      action: apply
      policy: three_way
      on_empty: fail
    - id: land_b
      kind: settle
      needs: [two]
      proposals: { from: steps.two.output }
      action: apply
      policy: three_way
      on_empty: fail
`,
			want: []string{CodeWF023},
		},
		{
			name: "settle names a report",
			steps: `  steps:
    - id: review
      kind: delegate
      role: reviewer
      lane: report
      prompt: review it
    - id: land
      kind: settle
      needs: [review]
      proposals: { from: steps.review.output, as_list: true }
      action: apply
      on_empty: fail
`,
			want: []string{CodeWF020},
		},
		{
			name: "un-collected body proposal",
			steps: `  steps:
    - id: fan
      kind: fanout
      over: [a, b]
      as: ws
      max_parallel: 2
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: 'build {{ ws }}'
    - id: land
      kind: settle
      needs: [fan]
      proposals: { from: steps.fan.output, as_list: true }
      action: apply
      on_empty: fail
`,
			want: []string{CodeWF021},
		},
		{
			// The shape bounded repair resolves into: a continuation that
			// supersedes its parent, a body whose proposals the collect
			// publishes, and one settle that lands them.
			name: "continuation chain settles once",
			steps: `  steps:
    - id: implement
      kind: delegate
      role: developer
      lane: proposal
      prompt: implement it
    - id: repair
      kind: loop
      needs: [implement]
      max_iterations: 3
      continue_from: "{{ steps.implement.output.proposal_id }}"
      collect: outcomes
      body:
        kind: delegate
        role: developer
        lane: proposal
        prompt: repair it
    - id: land
      kind: settle
      needs: [repair]
      proposals: { from: steps.repair.collect }
      action: apply
      policy: three_way
      on_empty: fail
`,
			clean: true,
		},
		{
			name: "nothing claims a proposal",
			steps: `  steps:
    - id: plan
      kind: delegate
      role: planner
      lane: report
      prompt: plan it
    - id: build
      kind: delegate
      role: developer
      prompt: build it
    - id: announce
      kind: emit
      needs: [plan]
      value: done
`,
			clean: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, diags := Validate(documentWith(tc.steps))
			if g == nil {
				t.Fatalf("document did not resolve:%s", render(diags))
			}
			out := Linearity(g)
			if tc.clean {
				if len(out) != 0 {
					t.Errorf("want zero linearity findings, got:%s", render(out))
				}
				return
			}
			for _, code := range tc.want {
				if !hasCode(out, code) {
					t.Errorf("want a %s finding, got:%s", code, render(out))
				}
			}
		})
	}
}

// TestLinearityNilGraph pins the aggregate's nil contract, which is what
// keeps Validate's return rule intact: only a WF001 has no graph.
func TestLinearityNilGraph(t *testing.T) {
	if got := Linearity(nil); len(got) != 0 {
		t.Errorf("Linearity(nil) = %s, want no findings", render(got))
	}
	if Prove(nil) != nil {
		t.Error("Prove(nil) must be nil")
	}
}

// TestLinearityIsDeterministic runs the corpus twice and compares the
// emission sequence, because the family's state is maps and a rule whose order
// came from map iteration would print a different report each run.
func TestLinearityIsDeterministic(t *testing.T) {
	file := filepath.Join("..", "ir", "testdata", "specs", "bounded_repair.yaml")
	data := readFixtureFile(t, file)
	g, diags := Validate(data)
	if g == nil {
		t.Fatalf("document did not resolve:%s", render(diags))
	}
	first := codeList(Linearity(g))
	for i := 0; i < 5; i++ {
		g2, d2 := Validate(data)
		if g2 == nil {
			t.Fatalf("round %d did not resolve:%s", i, render(d2))
		}
		if got := codeList(Linearity(g2)); got != first {
			t.Fatalf("round %d emission = %q, want %q", i, got, first)
		}
	}
}
