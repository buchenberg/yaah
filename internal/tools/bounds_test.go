package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestClampPositiveInt(t *testing.T) {
	cases := []struct{ in, max, want int }{
		{-5, 600, 0},
		{0, 600, 0},
		{1, 600, 1},
		{600, 600, 600},
		{601, 600, 600},
	}
	for _, tc := range cases {
		if got := clampPositiveInt(tc.in, tc.max); got != tc.want {
			t.Errorf("clampPositiveInt(%d, %d) = %d, want %d", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestExecSchemasBoundTimeouts(t *testing.T) {
	cases := []struct {
		name  string
		tool  Tool
		field string
		max   int
	}{
		{"bash", &BashTool{}, "timeout", 600},
		{"http", &HTTPTool{}, "timeout", 300},
		{"git", &GitTool{}, "timeout", 300},
		{"go_test", NewGoTestTool(), "timeout_seconds", 1800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc struct {
				Properties map[string]struct {
					Minimum *int `json:"minimum"`
					Maximum *int `json:"maximum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(tc.tool.Schema(), &doc); err != nil {
				t.Fatal(err)
			}
			prop, ok := doc.Properties[tc.field]
			if !ok {
				t.Fatalf("schema has no %q property", tc.field)
			}
			if prop.Maximum == nil || *prop.Maximum != tc.max {
				t.Errorf("%s maximum = %v, want %d", tc.field, prop.Maximum, tc.max)
			}
			if prop.Minimum == nil || *prop.Minimum != 1 {
				t.Errorf("%s minimum = %v, want 1", tc.field, prop.Minimum)
			}
		})
	}

	var diff struct {
		Properties map[string]struct {
			Minimum *int `json:"minimum"`
			Maximum *int `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewDiffTool().Schema(), &diff); err != nil {
		t.Fatal(err)
	}
	prop := diff.Properties["context_lines"]
	if prop.Maximum == nil || *prop.Maximum != diffMaxContextLines {
		t.Errorf("context_lines maximum = %v, want %d", prop.Maximum, diffMaxContextLines)
	}
	if prop.Minimum == nil || *prop.Minimum != 0 {
		t.Errorf("context_lines minimum = %v, want 0", prop.Minimum)
	}
}

func TestGoTest_RefusesExecFlags(t *testing.T) {
	tool := NewGoTestTool()
	for _, flag := range []string{"-exec=/bin/true", "-exec", "--toolexec=x", "-toolexec"} {
		args, _ := json.Marshal(map[string]any{"packages": "./...", "flags": []string{flag}})
		_, err := tool.Execute(context.Background(), string(args))
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("go_test flag %q = %v, want a refusal", flag, err)
		}
	}
}

func TestGoMod_RefusesDirectoryAndModfileFlags(t *testing.T) {
	tool := NewGoModTool()
	for _, flag := range []string{"-C", "-C=/tmp", "-modfile=other.mod"} {
		args, _ := json.Marshal(map[string]any{"action": "list", "flags": []string{flag}})
		_, err := tool.Execute(context.Background(), string(args))
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("go_mod flag %q = %v, want a refusal", flag, err)
		}
	}
}
