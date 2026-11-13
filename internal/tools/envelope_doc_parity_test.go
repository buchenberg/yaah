package tools

// envelope_doc_parity_test.go closes the gap behind yaah-c3z3.4: the
// schema-property test covers tool arguments, but result envelopes have their
// own JSON fields, and a field can ship without ever appearing in the
// model-facing doc — which is how world/warning/notes went undocumented. The
// harness lives in internal/envelopecheck and is shared with internal/dispatch;
// this file supplies only the package's registry.

import (
	"reflect"
	"testing"

	"github.com/buchenberg/yaah/internal/envelopecheck"
)

// resultEnvelopeDocs is the registry of result envelopes and the prompt file
// that documents them. Model-facing result envelopes are named *Envelope;
// proposalSummary is the one summary entry and is covered by the Summary
// suffix. New result envelopes must be added here; the registry is explicit
// because a type can be platform-gated and therefore absent from reflection on
// another build. Once registered, a new JSON field fails the test until the doc
// names it.
var resultEnvelopeDocs = []envelopecheck.Entry{
	{Type: reflect.TypeOf(jobListEnvelope{}), Doc: "subagent_jobs"},
	{Type: reflect.TypeOf(cancelEnvelope{}), Doc: "subagent_jobs"},
	{Type: reflect.TypeOf(waitEnvelope{}), Doc: "subagent_jobs"},
	{Type: reflect.TypeOf(backgroundDispatchEnvelope{}), Doc: "delegate"},
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
