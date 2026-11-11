package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/rolefile"
)

// input_shapes_test.go pins was9.6: the schema advertises the typed shape, the
// decoder still accepts the historical string form, and ambiguous or invalid
// shapes are errors rather than silently ignored.

func TestNormalizeTags(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		bad  bool
	}{
		{"array", `["a","b"]`, `["a","b"]`, false},
		{"legacy json string", `"[\"a\",\"b\"]"`, `["a","b"]`, false},
		{"absent", ``, "", false},
		{"null", `null`, "", false},
		{"empty string", `""`, "", false},
		{"invalid string", `"not json"`, "", true},
		{"wrong type", `42`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			got, err := normalizeTags(raw)
			if tc.bad {
				if err == nil {
					t.Fatalf("normalizeTags(%s) = %q, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTags(%s): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("normalizeTags(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestRoleTool_AcceptsTypedToolsAndContractFields(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}
	out, err := tool.Execute(context.Background(), `{
		"action":"create","name":"shaped","description":"d","body":"B",
		"tools":["read","grep"],
		"contract_heading":"## Result",
		"contract_fields":[{"name":"files","kind":"evidence"},{"name":"summary"}]
	}`)
	if err != nil {
		t.Fatalf("create: %v (output %s)", err, out)
	}
	data, rerr := os.ReadFile(dir + "/shaped.md")
	if rerr != nil {
		t.Fatal(rerr)
	}
	fm, _, err := rolefile.Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.Tools) != 2 || fm.Tools[0] != "read" || fm.Tools[1] != "grep" {
		t.Errorf("tools = %v, want [read grep]", fm.Tools)
	}
	if fm.Contract.Heading != "## Result" || len(fm.Contract.Fields) != 2 {
		t.Fatalf("contract = %+v", fm.Contract)
	}
	if fm.Contract.Fields[0].Kind != "evidence" || fm.Contract.Fields[1].Kind != "interpretation" {
		t.Errorf("contract kinds = %+v", fm.Contract.Fields)
	}
}

func TestRoleTool_StillAcceptsLegacyStrings(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}
	out, err := tool.Execute(context.Background(), `{
		"action":"create","name":"legacy","description":"d","body":"B",
		"tools":"read, grep",
		"contract_heading":"## Result",
		"contract_fields":"files:evidence, summary"
	}`)
	if err != nil {
		t.Fatalf("create: %v (output %s)", err, out)
	}
	legacy, lerr := os.ReadFile(dir + "/legacy.md")
	if lerr != nil {
		t.Fatal(lerr)
	}
	fm, _, err := rolefile.Parse(string(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.Tools) != 2 || fm.Tools[1] != "grep" {
		t.Errorf("tools = %v, want [read grep]", fm.Tools)
	}
	if len(fm.Contract.Fields) != 2 || fm.Contract.Fields[0].Kind != "evidence" {
		t.Errorf("contract = %+v", fm.Contract)
	}
}

func TestEdit_RejectsOldStringWithEdits(t *testing.T) {
	tool := &EditTool{}
	_, err := tool.Execute(context.Background(), `{
		"filePath":"x.go","oldString":"a","newString":"b",
		"edits":[{"oldString":"c","newString":"d"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both shapes = %v, want an explicit conflict error", err)
	}
}

func TestQuestion_RejectsOverlongHeader(t *testing.T) {
	tool := &QuestionTool{}
	header := strings.Repeat("x", 31)
	args, _ := json.Marshal(map[string]any{"questions": []map[string]any{{
		"question": "q", "header": header, "options": []map[string]string{{"label": "a"}},
	}}})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "maximum is 30") {
		t.Fatalf("31-char header = %v, want a length error", err)
	}
}

func TestShapeSchemas(t *testing.T) {
	var memory struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal((&MemoryAddTool{}).Schema(), &memory); err != nil {
		t.Fatal(err)
	}
	if memory.Properties["tags"].Type != "array" {
		t.Errorf("memory_add tags type = %q, want array", memory.Properties["tags"].Type)
	}

	var role struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal((&RoleTool{}).Schema(), &role); err != nil {
		t.Fatal(err)
	}
	if role.Properties["tools"].Type != "array" || role.Properties["contract_fields"].Type != "array" {
		t.Errorf("role tools/contract_fields must be arrays: %+v", role.Properties)
	}
	for _, field := range role.Required {
		if field == "action" {
			t.Error("role action is optional (defaults to list); it must not be required")
		}
	}

	var skill struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal((&SkillTool{}).Schema(), &skill); err != nil {
		t.Fatal(err)
	}
	if len(skill.Required) != 0 {
		t.Errorf("skill required = %v, want none (action defaults to load)", skill.Required)
	}

	var question struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal((&QuestionTool{}).Schema(), &question); err != nil {
		t.Fatal(err)
	}
	var questions struct {
		Items struct {
			Properties map[string]struct {
				MaxLength *int `json:"maxLength"`
			} `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(question.Properties["questions"], &questions); err != nil {
		t.Fatal(err)
	}
	if h := questions.Items.Properties["header"].MaxLength; h == nil || *h != 30 {
		t.Errorf("question header maxLength = %v, want 30", h)
	}
}
