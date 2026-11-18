package validate

// fuzz_test.go is the validator's standing fuzz harness (yaah-0u0v.26, plan
// §9.9). The decoder's own invariants are pinned in the spec package; this
// harness drives the graph families — Structural, Values, Linearity,
// Prove — plus Cost and Authority through Explain, over arbitrary byte
// sequences, with a synthetic host Env. Contracts: no panic, no hang; every
// finding carries a code the catalog defines; a graph that resolves survives
// every family; a document that does not resolve yields a nil graph and a
// WF001, never a silent partial report.

import (
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// fuzzEnv is a deliberately tight synthetic host — small ceilings, one
// role, one world — so mutations that widen beyond the host are exercised
// against real bounds, not unlimited ones.
func fuzzEnv() Env {
	return Env{
		Roles: map[string]Role{
			"developer": {Delegatable: true},
		},
		Worlds:              []string{"host"},
		DefaultWorld:        "host",
		HostNetwork:         ir.NetworkDenyAll,
		MaxDispatches:       8,
		MaxTurnsPerDispatch: 4,
		MaxPromptBytes:      512,
		SecondsPerDispatch:  30,
	}
}

// fuzzCatalog is derived from allCodes, the code constants' single
// enumeration, so a new rule code joins it in the same edit that declares
// the constant — no second catalog to keep in sync.
func fuzzCatalog() map[string]bool {
	out := make(map[string]bool, 29)
	for _, c := range allCodes() {
		out[c] = true
	}
	return out
}

func checkCatalog(t *testing.T, diags []Diagnostic) {
	t.Helper()
	catalog := fuzzCatalog()
	for _, d := range diags {
		if !catalog[d.Code] {
			t.Errorf("finding outside the catalog: %+v", d)
		}
		if d.Message == "" {
			t.Errorf("finding without a message: %+v", d)
		}
	}
}

func FuzzValidate(f *testing.F) {
	f.Add([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: f}\nspec:\n" +
		"  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n"))
	f.Add([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: f}\nspec:\n" +
		"  budgets:\n    max_dispatches: 1000\n  steps:\n" +
		"    - {id: a, kind: judge, role: ghost, prompt: hi}\n"))
	f.Add([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: f}\nspec:\n" +
		"  steps:\n    - {id: a, kind: emit, value: \"sticky fingers use akiaABCDEFGHIJKLMNOP\"}\n"))
	f.Add([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: f}\nspec:\n" +
		"  steps:\n    - {id: a, kind: loop, body: {kind: loop, body: {kind: loop}}}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		g, diags := Validate(data)
		if g == nil {
			// A document that never became a graph reports exactly one
			// WF001 — never an empty report, never two.
			if len(diags) == 0 || len(diags) > 1 || diags[0].Code != CodeWF001 {
				t.Errorf("nil graph with malformed report: %s", render(diags))
			}
			checkCatalog(t, diags)
			return
		}
		env := fuzzEnv()
		ex := Explain(g, env)
		diags = append(diags, ex.Diagnostics...)
		diags = append(diags, Calls(g, env)...)
		_ = Prove(g)
		checkCatalog(t, diags)
		// The cost estimate must stay finite on any graph: saturation is
		// the honest answer, a negative or NaN worst-case total is a bug.
		if ex.Cost.Dispatches < 0 {
			t.Errorf("negative dispatch estimate: %+v", ex.Cost)
		}
	})
}
