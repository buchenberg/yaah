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

var fuzzCatalog = map[string]bool{
	CodeWF001: true, CodeWF002: true, CodeWF003: true, CodeWF004: true, CodeWF005: true,
	CodeWF010: true, CodeWF011: true, CodeWF012: true, CodeWF013: true, CodeWF014: true,
	CodeWF020: true, CodeWF021: true, CodeWF022: true, CodeWF023: true, CodeWF024: true,
	CodeWF025: true, CodeWF026: true, CodeWF027: true, CodeWF028: true, CodeWF029: true,
	CodeWF030: true, CodeWF031: true, CodeWF032: true, CodeWF033: true, CodeWF034: true,
	CodeWF035: true, CodeWF036: true, CodeWF037: true,
}

func checkCatalog(t *testing.T, diags []Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if !fuzzCatalog[d.Code] {
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
