package dispatch

// envelope_doc_parity_test.go is the dispatch lane's half of the yaah-c3z3.4
// gate: every JSON field of every result envelope must be named in the doc the
// model actually receives. The harness lives in internal/envelopecheck and is
// shared with internal/tools; this file supplies only the lane's registry.

import (
	"reflect"
	"testing"

	"github.com/buchenberg/yaah/internal/envelopecheck"
)

// resultEnvelopeDocs is the registry of result envelopes and the prompt file
// that documents them. New result envelopes must be added here; the registry is
// explicit because a type can be platform-gated and therefore absent from
// reflection on another build. Once registered, a new JSON field fails the test
// until the doc names it.
var resultEnvelopeDocs = []envelopecheck.Entry{
	{Type: reflect.TypeOf(proposalEnvelope{}), Doc: "proposal"},
	{Type: reflect.TypeOf(proposalListEnvelope{}), Doc: "proposal"},
	{Type: reflect.TypeOf(proposalSummary{}), Doc: "proposal"},
}

// envelopeExemptions lists production *Envelope types that are not
// model-facing result envelopes. Keep this empty unless a real non-result
// envelope needs one; the value is the bead that removes it.
var envelopeExemptions = map[string]string{}

// TestResultEnvelopeRegistryCoversNamedEnvelopes catches a new production
// *Envelope that is never registered.
func TestResultEnvelopeRegistryCoversNamedEnvelopes(t *testing.T) {
	envelopecheck.RegistryCoversNamedEnvelopes(t, resultEnvelopeDocs, envelopeExemptions)
}

func TestResultEnvelopeDocs_NameEveryJSONField(t *testing.T) {
	envelopecheck.DocsNameEveryJSONField(t, resultEnvelopeDocs)
}
