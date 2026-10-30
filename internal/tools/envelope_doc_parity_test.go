package tools

// envelope_doc_parity_test.go closes the gap behind yaah-c3z3.4: the
// schema-property test covers tool arguments, but result envelopes have
// their own JSON fields. A field can ship without ever appearing in the
// model-facing doc, which is how world/warning/notes went undocumented.
// This gate names every JSON field of every result envelope in the doc
// the model actually receives.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// envelopeExemptions lists production *Envelope types that are not
// model-facing result envelopes. Keep this empty unless a real
// non-result envelope needs one; the value is the bead that removes it.
var envelopeExemptions = map[string]string{}

func TestResultEnvelopeDocs_NameEveryJSONField(t *testing.T) {
	for _, entry := range resultEnvelopeDocs {
		text := prompts.ToolDescription(entry.doc)
		for _, field := range jsonEnvelopeFields(entry.typ) {
			// Require the field in code formatting. A bare word in prose is
			// too weak: "an error" would satisfy an `error` field without
			// documenting it as a field.
			re, err := regexp.Compile("`" + regexp.QuoteMeta(field) + "`")
			if err != nil {
				t.Fatalf("compile pattern for %s.%s: %v", entry.typ, field, err)
			}
			if !re.MatchString(text) {
				t.Errorf("%s: %s.md never names result field `%s`; document it or stop emitting it", entry.typ, entry.doc, field)
			}
		}
	}
}

// TestResultEnvelopeRegistryCoversNamedEnvelopes catches the obvious new
// envelope a manual registry would otherwise miss. Tool result envelopes are
// declared in this package, so the scan is package-local; result types that
// do not end in "Envelope" (the delegate payloads) still need an explicit
// registry entry, and their JSON field names are covered once registered.
func TestResultEnvelopeRegistryCoversNamedEnvelopes(t *testing.T) {
	registered := make(map[string]bool, len(resultEnvelopeDocs))
	for _, entry := range resultEnvelopeDocs {
		registered[entry.typ.Name()] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		// Parse every production file, including tag-gated ones. Coverage
		// must not depend on which platform runs the test; an envelope that
		// exists only on one platform still needs its doc.
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(ts.Name.Name, "Envelope") {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				seen[ts.Name.Name] = true
				if bead, ok := envelopeExemptions[ts.Name.Name]; ok {
					t.Logf("%s: exempt — %s", ts.Name.Name, bead)
					continue
				}
				if !registered[ts.Name.Name] {
					t.Errorf("type %s is a production envelope but is not in resultEnvelopeDocs; add it and its model-facing doc, or record an envelopeExemptions entry citing a bead", ts.Name.Name)
				}
			}
		}
	}
	for name, bead := range envelopeExemptions {
		if strings.TrimSpace(bead) == "" {
			t.Errorf("envelopeExemptions[%s] must cite the bead that removes the exemption", name)
		}
		if !seen[name] {
			t.Errorf("envelopeExemptions names %s, but no production *Envelope type exists", name)
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
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Anonymous {
			if name == "-" {
				continue
			}
			if name != "" {
				// An explicit tag on an embedded field makes it a named
				// field; encoding/json does not flatten it.
				fields = append(fields, name)
				continue
			}
			if embedded := indirectStruct(field.Type); embedded != nil {
				fields = append(fields, jsonEnvelopeFields(embedded)...)
			} else {
				// An embedded non-struct is emitted under its type name.
				fields = append(fields, field.Name)
			}
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
	}
	slices.Sort(fields)
	return slices.Compact(fields)
}

// indirectStruct follows pointers to an embedded struct type, returning nil
// for embedded non-structs and for named non-struct types.
func indirectStruct(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct {
		return typ
	}
	return nil
}
