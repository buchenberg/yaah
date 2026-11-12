package rolefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestSchemaJSONValid parses the published schema as JSON — a malformed
// schema would validate nothing and editors would ignore it silently.
func TestSchemaJSONValid(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &doc); err != nil {
		t.Fatalf("SchemaJSON is not valid JSON: %v", err)
	}
	if doc["$schema"] == nil {
		t.Fatal("SchemaJSON missing $schema declaration")
	}
}

// TestSchemaCoversFrontmatter pins the drift guarantee the schema exists
// to provide: the documented property set is exactly the Frontmatter
// struct's yaml keys — no missing field a port could drop (a naah cannot
// silently lose `headless` if the schema it was written against names
// it), no extra field the parser would ignore (documentation that lies).
func TestSchemaCoversFrontmatter(t *testing.T) {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(SchemaJSON), &doc); err != nil {
		t.Fatalf("SchemaJSON is not valid JSON: %v", err)
	}
	if len(doc.Properties) == 0 {
		t.Fatal("SchemaJSON has no properties")
	}
	if len(doc.Properties) != len(knownFrontmatterKeys) {
		t.Errorf("schema documents %d properties, Frontmatter has %d known keys", len(doc.Properties), len(knownFrontmatterKeys))
	}
	for key := range knownFrontmatterKeys {
		if _, ok := doc.Properties[key]; !ok {
			t.Errorf("Frontmatter key %q missing from SchemaJSON properties", key)
		}
	}
	for key := range doc.Properties {
		if !knownFrontmatterKeys[key] {
			t.Errorf("SchemaJSON property %q is not a Frontmatter field the parser reads", key)
		}
	}
}

// TestSchemaPermissionsVocabulary pins that the schema's effect enum is
// exactly the permissions package's vocabulary (allow, deny, ask):
// the schema describing a rule language the engine rejects is the same
// drift this file exists to prevent, one level down.
func TestSchemaPermissionsVocabulary(t *testing.T) {
	var doc struct {
		Defs struct {
			Effect struct {
				Enum []string `json:"enum"`
			} `json:"effect"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(SchemaJSON), &doc); err != nil {
		t.Fatalf("SchemaJSON is not valid JSON: %v", err)
	}
	got := append([]string(nil), doc.Defs.Effect.Enum...)
	sort.Strings(got)
	want := []string{"allow", "ask", "deny"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SchemaJSON effect enum = %v, want %v", got, want)
	}
}

// TestSchemaDocsInSync pins the published copy under docs/schemas/
// byte-for-byte against the schema the parser's tests validate — the
// artifact outside the binary and the constant inside it cannot drift.
// A broken path here means the schema is embedded in the tree it tests
// rather than the docs tree; run from package rolefile the relative
// path is docs/schemas from the repo root.
func TestSchemaDocsInSync(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "schemas", "rolefile-frontmatter.schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (regenerate the published copy from rolefile.SchemaJSON): %v", path, err)
	}
	if string(data) != SchemaJSON {
		t.Errorf("%s is out of sync with rolefile.SchemaJSON; regenerate the published copy", path)
	}
}
