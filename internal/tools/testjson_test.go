package tools

import (
	"encoding/json"
	"testing"
)

// mustJSON builds tool arguments so host paths containing backslashes stay
// valid JSON on Windows.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	return string(data)
}
