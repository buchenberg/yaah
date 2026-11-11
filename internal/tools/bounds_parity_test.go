package tools

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bounds_parity_test.go pins the schema maximum to the code constant that
// enforces it. A schema bound with no clamp is advice; a clamp with no schema
// bound is a surprise. Both must name the same number.

func schemaBounds(t *testing.T, tool Tool, field string) (min, max *int) {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Minimum *int `json:"minimum"`
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
	return prop.Minimum, prop.Maximum
}

func TestSchemaBoundsMatchCode(t *testing.T) {
	cases := []struct {
		tool  Tool
		field string
		min   int
		max   int
	}{
		// read.limit is the deliberate minimum-0 case: 0 means "the whole
		// file", while the other knobs below are meaningless at 0.
		{&MemorySearchTool{}, "top_k", 1, memoryMaxTopK},
		{&MemorySessionSearchTool{}, "top_k", 1, memoryMaxTopK},
		{&ReadTool{}, "limit", 0, readMaxLines},
		{&TailTool{}, "lines", 1, tailMaxLines},
		{&TailTool{}, "bytes", 1, tailMaxBytes},
		{&WaitForTool{}, "timeout_seconds", 1, waitMaxTimeoutSeconds},
		{&WaitForTool{}, "interval_seconds", 1, waitMaxIntervalSeconds},
		{&WatchTool{}, "expires_in_seconds", 1, watchMaxExpiresSeconds},
		{&WatchTool{}, "interval_seconds", 1, waitMaxIntervalSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.tool.Name()+"/"+tc.field, func(t *testing.T) {
			min, max := schemaBounds(t, tc.tool, tc.field)
			if max == nil {
				t.Fatalf("%s schema has no maximum for %s", tc.tool.Name(), tc.field)
			}
			if *max != tc.max {
				t.Errorf("schema maximum = %d, code constant = %d", *max, tc.max)
			}
			if min == nil {
				t.Fatalf("%s schema has no minimum for %s", tc.tool.Name(), tc.field)
			}
			if *min != tc.min {
				t.Errorf("schema minimum = %d, want %d", *min, tc.min)
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

// TestReadLimitZeroPreservesWholeFile is the regression guard for the review
// question on #1251: an absent/zero limit must keep meaning "the whole file",
// never fall back to a default line count.
func TestReadLimitZeroPreservesWholeFile(t *testing.T) {
	path := t.TempDir() + "/all.txt"
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for i, args := range []string{
		`{"filePath":` + strconv.Quote(path) + `}`,
		`{"filePath":` + strconv.Quote(path) + `,"limit":0}`,
	} {
		got, err := (&ReadTool{}).Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		out.Reset()
		out.WriteString(got)
		for _, want := range []string{"one", "two", "three"} {
			if !strings.Contains(got, want) {
				t.Errorf("call %d: result missing %q: %q", i, want, got)
			}
		}
	}
}
