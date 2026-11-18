package spec

// fuzz_test.go is the decoder's standing fuzz harness (yaah-0u0v.26, plan
// §9.9): any byte sequence must either decode to a document that satisfies
// every decode limit and hashes, or be refused with a decode error — never
// a panic, a hang, or a document that violates its own caps. The seeds are
// the adversarial suite's payloads plus a valid document, so a regression
// in the attacks is reachable from the corpus immediately.

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	valid := advDoc("  steps:\n    - {id: a, kind: emit, value: hello}\n    - {id: b, kind: delegate, role: developer, prompt: hi, needs: [a]}\n")
	f.Add(valid)
	// Attack seeds from the adversarial suite.
	f.Add([]byte("a: &a [\"x\"]\nb: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a]\nc: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b]\nd: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c]\n"))
	f.Add([]byte("apiVersion: yaah.workflow/v1\nkind: Workflow\nspec:\n  steps: " + strings.Repeat("[", 20000)))
	f.Add([]byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[{"id":"a","kind":"loop","max_iterations":99999999999999999999}]}}`))
	f.Add([]byte(`{"apiVersion":"yaah.workflow/v1","kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[` +
		`{"id":"a","kind":"delegate","role":"developer","prompt":"` + strings.Repeat("A", 4096) + `"}]}}`))
	f.Add([]byte("\xff\xfe\x00\x01bad utf8"))
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Parse(data)
		if err != nil {
			return
		}
		// A document the decoder accepted must satisfy the decode limits
		// and canonicalize: checkLimits and SpecHash walk every field
		// and both must succeed on any accepted input.
		if lerr := checkLimits(doc); lerr != nil {
			t.Errorf("accepted document violates its own limits: %v", lerr)
		}
		if _, herr := SpecHash(doc); herr != nil {
			t.Errorf("accepted document does not canonicalize: %v", herr)
		}
	})
}
