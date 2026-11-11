package tools

import (
	"encoding/json"
	"testing"
	"time"
)

// bounds_parity_test.go pins the schema maximum to the code constant that
// enforces it. A schema bound with no clamp is advice; a clamp with no schema
// bound is a surprise. Both must name the same number.

func schemaMaximum(t *testing.T, tool Tool, field string) (int, bool) {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Maximum *int `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema(), &doc); err != nil {
		t.Fatalf("%s: schema: %v", tool.Name(), err)
	}
	prop, ok := doc.Properties[field]
	if !ok {
		t.Fatalf("%s: schema has no %q property", tool.Name(), field)
	}
	if prop.Maximum == nil {
		return 0, false
	}
	return *prop.Maximum, true
}

func TestSchemaBoundsMatchCode(t *testing.T) {
	cases := []struct {
		tool  Tool
		field string
		max   int
	}{
		{&MemorySearchTool{}, "top_k", memoryMaxTopK},
		{&MemorySessionSearchTool{}, "top_k", memoryMaxTopK},
		{&ReadTool{}, "limit", readMaxLines},
		{&TailTool{}, "lines", tailMaxLines},
		{&TailTool{}, "bytes", tailMaxBytes},
		{&WaitForTool{}, "timeout_seconds", waitMaxTimeoutSeconds},
		{&WaitForTool{}, "interval_seconds", waitMaxIntervalSeconds},
		{&WatchTool{}, "expires_in_seconds", watchMaxExpiresSeconds},
		{&WatchTool{}, "interval_seconds", waitMaxIntervalSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.tool.Name()+"/"+tc.field, func(t *testing.T) {
			got, ok := schemaMaximum(t, tc.tool, tc.field)
			if !ok {
				t.Fatalf("%s schema has no maximum for %s", tc.tool.Name(), tc.field)
			}
			if got != tc.max {
				t.Errorf("schema maximum = %d, code constant = %d", got, tc.max)
			}
		})
	}
}

func TestClampSecondsHonoursMaximum(t *testing.T) {
	def := 30 * time.Second
	cases := []struct {
		secs int
		max  int
		want time.Duration
	}{
		{0, 60, def},
		{-5, 60, def},
		{10, 60, 10 * time.Second},
		{60, 60, 60 * time.Second},
		{600, 60, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := clampSeconds(tc.secs, def, tc.max); got != tc.want {
			t.Errorf("clampSeconds(%d, %v, %d) = %v, want %v", tc.secs, def, tc.max, got, tc.want)
		}
	}
}
