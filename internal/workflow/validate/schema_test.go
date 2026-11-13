package validate

import (
	"strings"
	"testing"
)

// TestSchemaProblem is the unit table behind WF005's schema half. The
// strings here are the shapes an author actually writes into
// `output.schema`, which the typed surface carries as a plain string and
// the decoder therefore cannot check at all.
func TestSchemaProblem(t *testing.T) {
	deep := strings.Repeat(`{"properties":{"p":`, 20) + `{"type":"string"}` + strings.Repeat("}", 40)

	cases := []struct {
		name string
		in   string
		want string // "" means the schema is usable
		sub  string
	}{
		// Accepted: the two forms the field can hold.
		{name: "bare name", in: "report.v1"},
		{name: "pathlike name", in: "schemas/output/report.v1.json"},
		{name: "underscored name", in: "report_v1:latest"},
		{name: "empty object", in: `{}`},
		{name: "object type", in: `{"type":"object"}`},
		{name: "type union", in: `{"type":["string","null"]}`},
		{name: "properties and required", in: `{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"boolean"}}}`},
		{name: "nested items", in: `{"type":"array","items":{"type":"integer"}}`},
		{name: "tuple items", in: `{"type":"array","items":[{"type":"string"},{"type":"null"}]}`},
		{name: "additionalProperties boolean", in: `{"additionalProperties":false}`},
		{name: "additionalProperties schema", in: `{"additionalProperties":{"type":"string"}}`},
		{name: "not", in: `{"not":{"type":"null"}}`},
		{name: "anyOf", in: `{"anyOf":[{"type":"string"},{"type":"integer"}]}`},
		{name: "enum", in: `{"enum":["pass","fail"]}`},

		// Rejected: nothing here is a full conformance check, only the
		// malformations a hand-written schema produces.
		{name: "blank", in: "   ", sub: "must not be blank"},
		{name: "name with a space", in: "report one", sub: "neither a schema name nor a JSON object"},
		{name: "over-long name", in: strings.Repeat("s", 200), sub: "neither a schema name nor a JSON object"},
		{name: "not JSON", in: `{"type":"object"`, sub: "not valid JSON"},
		{name: "trailing content", in: `{"type":"object"} {"type":"array"}`, sub: "trailing content"},
		{name: "unknown type", in: `{"type":"banana"}`, sub: `type "banana" is not a JSON type`},
		{name: "type wrong kind", in: `{"type":5}`, sub: "must be a JSON type name or a list of them"},
		{name: "type empty list", in: `{"type":[]}`, sub: "list is empty"},
		{name: "type list duplicate", in: `{"type":["string","string"]}`, sub: "is listed twice"},
		{name: "type list non-string", in: `{"type":[1]}`, sub: "must contain JSON type names"},
		{name: "required not a list", in: `{"required":"id"}`, sub: `"required" must be a list of property names`},
		{name: "required empty name", in: `{"required":[""]}`, sub: "must be a non-empty property name"},
		{name: "required wrong element", in: `{"required":["id",2]}`, sub: "must be a non-empty property name"},
		{name: "enum not a list", in: `{"enum":"x"}`, sub: "must be a list of literals"},
		{name: "enum empty", in: `{"enum":[]}`, sub: "can match no value"},
		{name: "properties not an object", in: `{"properties":[]}`, sub: `"properties" must be an object of property schemas`},
		{name: "nested property type", in: `{"properties":{"a":{"type":"nope"}}}`, sub: `property "a"`},
		{name: "items wrong kind", in: `{"items":3}`, sub: `"items" must be a schema or a list of schemas`},
		{name: "items tuple element", in: `{"items":[{"type":"string"},7]}`, sub: "items[1] must be a schema object"},
		{name: "additionalProperties wrong kind", in: `{"additionalProperties":"yes"}`, sub: "must be a boolean or a schema"},
		{name: "not wrong kind", in: `{"not":"nope"}`, sub: `"not" must be a schema object`},
		{name: "allOf not a list", in: `{"allOf":{"type":"object"}}`, sub: `"allOf" must be a list of schemas`},
		{name: "anyOf empty", in: `{"anyOf":[]}`, sub: "constrains nothing"},
		{name: "oneOf element", in: `{"oneOf":[3]}`, sub: "oneOf[0] must be a schema object"},
		{name: "nests too deep", in: deep, sub: "nests deeper than 16 levels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schemaProblem(tc.in)
			if tc.sub == "" {
				if got != "" {
					t.Errorf("schema %s rejected: %s", abbreviate(tc.in), got)
				}
				return
			}
			if got == "" {
				t.Fatalf("schema %s accepted, want it rejected for %q", abbreviate(tc.in), tc.sub)
			}
			if !strings.Contains(got, tc.sub) {
				t.Errorf("message = %q, want it to contain %q", got, tc.sub)
			}
		})
	}
}

// TestSchemaProblemIsDeterministic: a schema object is decoded into a Go
// map, whose iteration order is randomised, so the first complaint must be
// chosen by sorted property name and not by whichever key Go visited first.
func TestSchemaProblemIsDeterministic(t *testing.T) {
	in := `{"properties": {"zulu": {"type": "nope"}, "alpha": {"type": "bad"}, "mike": {"type": "wrong"}}}`
	first := schemaProblem(in)
	for range 20 {
		if got := schemaProblem(in); got != first {
			t.Fatalf("message changed across runs: %q vs %q", first, got)
		}
	}
	if !strings.Contains(first, `property "alpha"`) {
		t.Errorf("message = %q, want the alphabetically first bad property", first)
	}
}

// TestIsSchemaName bounds the reference form: a single token of identifier
// bytes, at most 128 of them.
func TestIsSchemaName(t *testing.T) {
	for _, good := range []string{"report.v1", "Report", "a/b/c.json", "urn:report", "x-y_z", strings.Repeat("s", 128)} {
		if !isSchemaName(good) {
			t.Errorf("isSchemaName(%q) = false, want true", abbreviate(good))
		}
	}
	for _, bad := range []string{"", " ", "two words", "trailing\nnewline", "ünicode", strings.Repeat("s", 129), "[1,2]"} {
		if isSchemaName(bad) {
			t.Errorf("isSchemaName(%q) = true, want false", abbreviate(bad))
		}
	}
}

func TestTruncated(t *testing.T) {
	if got := truncated("short", 60); got != "short" {
		t.Errorf("truncated(\"short\") = %q", got)
	}
	if got := truncated("héllo wörld", 3); got != "hél…" {
		t.Errorf("truncated over 3 runes = %q, want the cut on a rune boundary", got)
	}
	if got := truncated(strings.Repeat("x", 100), 60); len([]rune(got)) != 61 {
		t.Errorf("truncated length = %d, want 60 bytes plus the ellipsis", len(got))
	}
}

// abbreviate keeps a long schema out of a test failure message.
func abbreviate(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
