package spec

// advspec_test.go is the adversarial half of the parser gate (yaah-0u0v.26,
// plan §9.1): attacks a hostile author would actually write — encoded size
// bombs, YAML alias expansion bombs, integer overflow dressed as limits —
// and pins that each is refused at the decode boundary, before anything
// downstream sees it. Where the library itself defuses an attack (yaml.v3's
// excessive-aliasing and depth limits), the refusal is pinned here too, so an
// upgrade that loses the defense fails the suite.

import (
	"fmt"
	"strings"
	"testing"
)

// advDoc wraps steps into a parse payload the decoder will accept when the
// attack is not the wrapping.
func advDoc(steps string) []byte {
	return []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: adv}\nspec:\n" + steps)
}

func TestAdvSpecSizeBomb(t *testing.T) {
	value := strings.Repeat("A", MaxSpecBytes)
	if _, err := ParseYAML(advDoc("  extra:\n    v: " + value)); err == nil {
		t.Fatal("oversized spec decoded, want a size refusal")
	}
}

func TestAdvSpecNodeCountBomb(t *testing.T) {
	var b strings.Builder
	b.WriteString("  steps:\n")
	for i := 0; i < MaxNodes+1; i++ {
		fmt.Fprintf(&b, "    - {id: n%d, kind: emit, value: x}\n", i)
	}
	if _, err := ParseYAML(advDoc(b.String())); err == nil {
		t.Fatalf("node-count bomb decoded (limits allow %d)", MaxNodes)
	}
	// The boundary itself must hold: exactly MaxNodes emit nodes is a
	// document, not a bomb. Body nesting counts as nodes too, so the
	// fanout-embedded form is the one worth pinning here.
	bounded := strings.Builder{}
	bounded.WriteString("  steps:\n")
	for i := 0; i < MaxNodes; i++ {
		fmt.Fprintf(&bounded, "    - {id: n%d, kind: emit, value: x}\n", i)
	}
	if _, err := ParseYAML(advDoc(bounded.String())); err != nil {
		t.Fatalf("document of exactly MaxNodes nodes refused: %v", err)
	}
}

func TestAdvSpecPromptSizeBomb(t *testing.T) {
	prompt := strings.Repeat("A", MaxPromptBytes+1)
	if _, err := ParseYAML(advDoc("  steps:\n    - {id: a, kind: delegate, role: developer, prompt: " + prompt + "\n}")); err == nil {
		t.Fatal("prompt-size bomb decoded")
	}
}

func TestAdvSpecTemplateDepthBomb(t *testing.T) {
	legal := strings.Repeat("{{", MaxTemplateDepth) + strings.Repeat("}}", MaxTemplateDepth)
	attack := strings.Repeat("{{", MaxTemplateDepth+1) + strings.Repeat("}}", MaxTemplateDepth+1)
	if _, err := ParseYAML(advDoc("  steps:\n    - {id: a, kind: emit, value: '" + attack + "'}\n")); err == nil {
		t.Fatalf("template depth %d decoded, limit is %d", MaxTemplateDepth+1, MaxTemplateDepth)
	}
	if _, err := ParseYAML(advDoc("  steps:\n    - {id: a, kind: emit, value: '" + legal + "'}\n")); err != nil {
		t.Fatalf("MaxTemplateDepth-deep value refused: %v", err)
	}
}

func TestAdvSpecAliasExpansionBomb(t *testing.T) {
	// A well-formed billion-laughs: every anchor defined before use, the
	// expansion factor per level nine. The library detects the aliasing
	// before the tree exists; the pin is that ParseYAML refuses the
	// payload rather than materializing it.
	var b strings.Builder
	b.WriteString("apiVersion: yaah.workflow/v1\nkind: Workflow\nmetadata: {name: bomb}\n")
	b.WriteString("a: &a [\"x\"]\n")
	prev := "a"
	for n := 0; n < 8; n++ {
		name := "b" + string(rune('a'+n))
		b.WriteString(name + ": &" + name + " [")
		for i := 0; i < 9; i++ {
			b.WriteString("*" + prev)
			if i < 8 {
				b.WriteString(", ")
			}
		}
		b.WriteString("]\n")
	}
	prev = "bh"
	b.WriteString("spec:\n  steps: *" + prev + "\n")
	if doc, err := ParseYAML([]byte(b.String())); err == nil {
		if len(doc.Spec.Steps) > MaxNodes {
			t.Fatalf("alias expansion materialized %d steps past the node cap", len(doc.Spec.Steps))
		}
		t.Fatalf("alias bomb decoded cleanly into %d steps", len(doc.Spec.Steps))
	}
}

func TestAdvSpecStructuralNestingBomb(t *testing.T) {
	// Deep flow nesting either way: an unbalanced cliff that is not a key,
	// and a balanced cliff the library's depth limit must refuse before
	// decoding begins.
	data := []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps: " + strings.Repeat("[", 20000))
	if _, err := ParseYAML(data); err == nil {
		t.Fatal("nesting cliff decoded without a refusal")
	}
	data = []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps: " +
		strings.Repeat("[", 20000) + strings.Repeat("]", 20000))
	if _, err := ParseYAML(data); err == nil {
		t.Fatal("balanced nesting bomb decoded past the library depth limit")
	}
}

// TestAdvIntegerOverflowIsRefused pins the 2026-11 fix in intValue: an
// integer larger than int64 used to decode to implementation-dependent
// garbage (int64 of an out-of-range float), handing an attacker a bonus
// value for max_iterations-shaped control fields. Both parse surfaces must
// refuse instead.
func TestAdvIntegerOverflowIsRefused(t *testing.T) {
	overflow := "9223372036854775808" // MaxInt64 + 1
	cases := []struct {
		name string
		data []byte
	}{
		{"json number", []byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":` + overflow + `}]}}`)},
		{"json exponent", []byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":1e30}]}}`)},
		{"yaml big int", []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps:\n    - {id: a, kind: loop, max_iterations: " + overflow + "}\n")},
		{"yaml infinity", []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps:\n    - {id: a, kind: loop, max_iterations: .inf}\n")},
		{"yaml huge", []byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps:\n    - {id: a, kind: loop, max_iterations: 99999999999999999999}\n")},
	}
	for _, tc := range cases {
		if doc, err := Parse(tc.data); err == nil {
			t.Errorf("%s: decoded, want a range refusal", tc.name)
			_ = doc
		}
	}
	// The values a document legitimately writes must keep decoding:
	// exactly MaxInt64, an exponent-form integer inside range, and a
	// negative number (cost rules own its semantics, the decoder only
	// guarantees it arrives as written).
	legal := []struct {
		name string
		data []byte
		want int64
	}{
		{"max int64", []byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":9223372036854775807}]}}`), 9223372036854775807},
		{"exponent form", []byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":1e3}]}}`), 1000},
		{"negative", []byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":-5}]}}`), -5},
	}
	for _, tc := range legal {
		doc, err := Parse(tc.data)
		if err != nil {
			t.Errorf("%s: refused, want %d: %v", tc.name, tc.want, err)
			continue
		}
		if got := doc.Spec.Steps[0].MaxIterations; got == nil || int64(*got) != tc.want {
			t.Errorf("%s: max_iterations = %v, want %d", tc.name, got, tc.want)
		}
	}
}
