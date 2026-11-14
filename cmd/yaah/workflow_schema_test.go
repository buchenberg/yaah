package yaah

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWorkflowSchemasParse pins that both published schema constants are
// well-formed JSON with the draft declaration — a malformed schema would
// validate nothing and consumers would drift silently.
func TestWorkflowSchemasParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"validate", WorkflowValidateSchemaJSON},
		{"explain", WorkflowExplainSchemaJSON},
		{"run", WorkflowRunSchemaJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(tc.body), &doc); err != nil {
				t.Fatalf("schema constant is not valid JSON: %v", err)
			}
			if doc["$schema"] == nil {
				t.Error("schema missing $schema declaration")
			}
			if doc["$id"] == nil {
				t.Error("schema missing $id")
			}
		})
	}
}

// TestWorkflowSchemaDocsInSync pins the published copies under docs/schemas/
// byte-for-byte against the constants — the artifact outside the binary and
// the contract inside it cannot drift. On mismatch, regenerate the docs copy
// from the constant.
func TestWorkflowSchemaDocsInSync(t *testing.T) {
	for _, tc := range []struct {
		file string
		body string
	}{
		{"workflow-validate.schema.json", WorkflowValidateSchemaJSON},
		{"workflow-explain.schema.json", WorkflowExplainSchemaJSON},
		{"workflow-run.schema.json", WorkflowRunSchemaJSON},
	} {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "docs", "schemas", tc.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if string(data) != tc.body {
				t.Errorf("%s is out of sync with its Go constant; regenerate the published copy", path)
			}
		})
	}
}
