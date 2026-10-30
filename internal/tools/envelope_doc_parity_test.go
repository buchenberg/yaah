package tools

// envelope_doc_parity_test.go closes the gap behind yaah-c3z3.4: the
// schema-property test covers tool arguments, but result envelopes have
// their own JSON fields. A field can ship without ever appearing in the
// model-facing doc, which is how world/warning/notes went undocumented.
// This gate names every JSON field of every result envelope in the doc
// the model actually receives.

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/prompts"
)

// resultEnvelopeDocs is the registry of result envelopes and the prompt
// file that documents them. Keep it in sync when adding an envelope; a
// new JSON field on a registered type fails the test until the doc names
// it.
var resultEnvelopeDocs = []struct {
	typ reflect.Type
	doc string
}{
	{reflect.TypeOf(proposalEnvelope{}), "proposal"},
	{reflect.TypeOf(proposalListEnvelope{}), "proposal"},
	{reflect.TypeOf(proposalSummary{}), "proposal"},
	{reflect.TypeOf(jobListEnvelope{}), "subagent_jobs"},
	{reflect.TypeOf(cancelEnvelope{}), "subagent_jobs"},
	{reflect.TypeOf(waitEnvelope{}), "subagent_jobs"},
	{reflect.TypeOf(backgroundDispatchResult{}), "delegate"},
	{reflect.TypeOf(dispatchFailureResult{}), "delegate"},
}

func TestResultEnvelopeDocs_NameEveryJSONField(t *testing.T) {
	for _, entry := range resultEnvelopeDocs {
		text := prompts.ToolDescription(entry.doc)
		for _, field := range jsonEnvelopeFields(entry.typ) {
			re, err := regexp.Compile(`\b` + regexp.QuoteMeta(field) + `\b`)
			if err != nil {
				t.Fatalf("compile pattern for %s.%s: %v", entry.typ, field, err)
			}
			if !re.MatchString(text) {
				t.Errorf("%s: %s.md never names result field %q; document it or stop emitting it", entry.typ, entry.doc, field)
			}
		}
	}
}

// jsonEnvelopeFields returns the JSON names of all fields in a result
// envelope, including embedded snapshots. Anonymous fields are followed
// so a wait/cancel envelope inherits the job snapshot's documented fields
// rather than hiding them behind the embedding.
func jsonEnvelopeFields(typ reflect.Type) []string {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			fields = append(fields, jsonEnvelopeFields(field.Type)...)
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
	}
	slices.Sort(fields)
	return slices.Compact(fields)
}
