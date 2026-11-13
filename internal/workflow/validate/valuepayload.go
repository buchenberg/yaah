package validate

import (
	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// payloadsMatchKind checks WF014: a payload field belongs to the node kind
// that can carry it.
//
// The v1 node surface is one flat struct (WD1: no user-defined node types),
// so the typed decoder accepts every known key on every node and
// ir.Resolve reads only the fields its kind understands: resolveGate never
// looks at `candidates`, resolveSelect never looks at `on_pass`,
// resolveSettle never looks at `policy`. A field on the wrong kind is
// therefore not a parse error and not an unknown field — it is silently
// dropped, which is the worst shape of mistake, because the author wrote a
// constraint that will never hold. The matrix below is exactly what the
// resolver populates, per kind, which makes "the graph cannot carry this"
// the test rather than a table of authoring opinions; the shared fields
// (id, kind, needs, when, retry, timeout_seconds, on_failure, output) belong
// to every kind and are never reported.
//
// Fields the resolver has no home for are invisible to this rule, which is
// the price of testing the graph instead of the document, and is recorded
// with the code rather than hidden: `policy` on a kind that has no policy at
// all — a gate, a fan-out, a loop — is legal in the document and dropped on
// the way into the IR, so nothing here can name it. What the two kinds that
// do own a policy write from that one shared key is now carried
// (yaah-0u0v.31): `policy` on a select, `settle_policy` on a settle, each on
// its own field, because ir.Node is one flat struct and encoding/json drops
// every field sharing a tag. A value from the other half of the vocabulary
// (`policy: judge` on a settle, `policy: path_disjoint` on a select) is
// refused by the resolver, which is why this rule never reports it twice.
func payloadsMatchKind(c *valueCtx) []Diagnostic {
	var out []Diagnostic
	for _, n := range c.order {
		if n == nil {
			continue
		}
		allowed, ok := payloadFields[n.Kind]
		if !ok {
			// An unknown kind is a decoder and resolver failure
			// (WF001); guessing which payload fields are misplaced on a
			// node whose kind the DSL does not define would only bury the
			// real finding.
			continue
		}
		for _, f := range populatedPayloads(n) {
			if !f.set || allowed[f.name] {
				continue
			}
			out = append(out, diag(CodeWF014, n.Path+"."+f.name,
				"%q is not a payload field of a %s node: the resolver drops it, so the declaration has no effect",
				f.name, n.Kind))
		}
	}
	return out
}

// populatedField is one payload field that carries a value.
type populatedField struct {
	name string
	set  bool
}

// payloadFields maps each node kind to the payload fields ir.Resolve can
// populate on it. Wire names, so a diagnostic reads like the document.
var payloadFields = map[ir.Kind]map[string]bool{
	ir.KindDelegate: names("role", "prompt", "lane", "seed_from", "continue_from",
		"fork_from", "provider", "model", "max_turns", "grants", "network", "world"),
	ir.KindFanout: names("over", "as", "max_parallel", "body", "collect",
		"join", "quorum", "on_item_failure"),
	ir.KindBranch:    names("on", "cases", "default"),
	ir.KindLoop:      names("until", "max_iterations", "continue_from", "body", "collect"),
	ir.KindGate:      names("assert", "judge", "on_pass", "on_fail"),
	ir.KindSettle:    names("proposals", "where", "action", "settle_policy", "order", "on_conflict", "on_empty"),
	ir.KindSelect:    names("candidates", "judge", "policy"),
	ir.KindAggregate: names("inputs", "strategy", "body"),
	ir.KindCall:      names("workflow", "params", "max_depth"),
	ir.KindEmit:      names("value", "artifact_path"),
	ir.KindHuman:     names("prompt", "on_timeout"),
}

func names(list ...string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, n := range list {
		m[n] = true
	}
	return m
}

// populatedPayloads lists the payload fields a node carries, in a fixed
// order so findings do not depend on struct order or on Go's map iteration.
// A field counts as carried when it holds a value at all: an author who
// wrote `role: ""` declared nothing, and that is the resolver's business.
func populatedPayloads(n *ir.Node) []populatedField {
	return []populatedField{
		// delegate, human
		{"role", n.Role != ""},
		{"prompt", n.Prompt != nil},
		{"lane", n.Lane != ""},
		{"seed_from", n.SeedFrom != nil},
		{"continue_from", n.ContinueFrom != nil},
		{"fork_from", n.ForkFrom != nil},
		{"provider", n.Provider != ""},
		{"model", n.Model != ""},
		{"max_turns", n.MaxTurns != nil},
		{"grants", len(n.Grants) > 0},
		{"network", n.Network != ""},
		{"world", n.World != ""},
		{"on_timeout", n.OnTimeout != ""},
		// fanout
		{"over", n.Over != nil},
		{"as", n.As != ""},
		{"max_parallel", n.MaxParallel != nil},
		{"join", n.Join != ""},
		{"quorum", n.Quorum != nil},
		{"on_item_failure", n.OnItemFailure != ""},
		// fanout, loop, aggregate
		{"body", len(n.Body) > 0},
		{"collect", n.Collect != ""},
		// branch
		{"on", n.On != nil},
		{"cases", len(n.Cases) > 0},
		{"default", n.Default != ""},
		// loop
		{"until", n.Until != nil},
		{"max_iterations", n.MaxIterations != nil},
		// gate, select
		{"assert", n.Assert != nil},
		{"judge", n.Judge != nil},
		{"candidates", n.Candidates != nil},
		{"on_pass", n.OnPass != ""},
		{"on_fail", n.OnFail != ""},
		{"policy", n.Policy != ""},
		// settle
		{"proposals", n.Proposals != nil},
		{"where", n.Where != nil},
		{"action", n.Action != ""},
		{"order", n.Order != ""},
		{"on_conflict", n.OnConflict != ""},
		{"on_empty", n.OnEmpty != ""},
		{"settle_policy", n.SettlePolicy != ""},
		// aggregate
		{"inputs", len(n.Inputs) > 0},
		{"strategy", n.Strategy != ""},
		// call
		{"workflow", n.Workflow != ""},
		{"max_depth", n.MaxDepth != nil},
		// emit
		{"value", n.Value != nil},
		{"artifact_path", n.ArtifactPath != ""},
	}
}

// params is the one payload field whose name is a map key rather than a
// field, so it reaches the walk through CallParams above and needs no
// separate entry.
