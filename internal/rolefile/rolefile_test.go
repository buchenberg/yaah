package rolefile

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const sampleRole = `---
name: tester
description: runs the test suite
specialty: testing
contract:
    heading: Test Report
    fields:
        - command
        - {name: exit_code, kind: evidence}
tools:
    - read
    - bash
max_iterations: 50
min_iterations: 10
max_turns: 20
min_turns: 4
timeout: 960
---

Run the tests and report back.
`

func TestParse(t *testing.T) {
	fm, body, err := Parse(sampleRole)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Name != "tester" || fm.Specialty != "testing" {
		t.Errorf("name/specialty = %q/%q", fm.Name, fm.Specialty)
	}
	if fm.MaxLoopCycles != 50 || fm.Timeout != 960 {
		t.Errorf("limits = %d/%d", fm.MaxLoopCycles, fm.Timeout)
	}
	// Plan subagent-turn-budget-floors §4.5: the floor fields must
	// survive Parse — a missing Frontmatter field is silently dropped
	// when a role file is rewritten.
	if fm.MinLoopCycles != 10 || fm.MinToolTurns != 4 {
		t.Errorf("floors = %d/%d, want 10/4", fm.MinLoopCycles, fm.MinToolTurns)
	}
	if fm.MaxToolTurns != 20 {
		t.Errorf("max_turns = %d, want 20", fm.MaxToolTurns)
	}
	if len(fm.Tools) != 2 || fm.Tools[1] != "bash" {
		t.Errorf("tools = %v", fm.Tools)
	}
	if fm.Contract.Heading != "Test Report" {
		t.Errorf("contract heading = %q", fm.Contract.Heading)
	}
	if len(fm.Contract.Fields) != 2 {
		t.Fatalf("contract fields = %d; want 2", len(fm.Contract.Fields))
	}
	if fm.Contract.Fields[0].Name != "command" || fm.Contract.Fields[0].Kind != "" {
		t.Errorf("string-form field = %+v", fm.Contract.Fields[0])
	}
	if fm.Contract.Fields[1].Name != "exit_code" || fm.Contract.Fields[1].Kind != "evidence" {
		t.Errorf("map-form field = %+v", fm.Contract.Fields[1])
	}
	if body != "Run the tests and report back." {
		t.Errorf("body = %q", body)
	}
}

// TestRoundTrip pins that Marshal(Parse(x)) preserves every field — the
// regression that motivated this package (review B3) was a parse/marshal
// asymmetry that dropped the contract block on edit.
func TestRoundTrip(t *testing.T) {
	fm, body, err := Parse(sampleRole)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	fm2, body2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse after Marshal: %v\n--- output ---\n%s", err, out)
	}
	// Marshal emits modeled fields absent from the source (e.g. json_mode),
	// so the recorded key order gains entries. Field values are what this
	// test pins.
	fm2.order = fm.order
	if !reflect.DeepEqual(fm2, fm) {
		t.Errorf("frontmatter changed on round trip:\n got %+v\nwant %+v", fm2, fm)
	}
	if body2 != body {
		t.Errorf("body changed on round trip: %q vs %q", body2, body)
	}
}

// TestRoundTripOutputField pins the modeled output field (yaah-7yf): a
// role file that declares `output: report` keeps the value through
// parse→marshal, and the field joins the recorded key order so rewrites
// do not move it relative to extension keys.
func TestRoundTripOutputField(t *testing.T) {
	input := `---
name: reviewer
output: report
tools: [read, grep]
---
body
`
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if fm.Output != "report" {
		t.Fatalf("Output = %q, want report", fm.Output)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	fm2, body2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse after Marshal: %v\n--- output ---\n%s", err, out)
	}
	if fm2.Output != "report" {
		t.Errorf("output changed on round trip: %q vs %q", fm2.Output, fm.Output)
	}
	if !slices.Contains(fm2.order, "output") {
		t.Errorf("key order lost the output field: %v", fm2.order)
	}
	if body2 != body {
		t.Errorf("body changed on round trip: %q vs %q", body2, body)
	}
}

func TestParseCRLF(t *testing.T) {
	crlf := strings.ReplaceAll(sampleRole, "\n", "\r\n")
	fm, body, err := Parse(crlf)
	if err != nil {
		t.Fatalf("Parse(CRLF): %v", err)
	}
	if fm.Name != "tester" {
		t.Errorf("name = %q", fm.Name)
	}
	if body != "Run the tests and report back." {
		t.Errorf("body = %q", body)
	}
}

func TestParseErrors(t *testing.T) {
	if _, _, err := Parse("no frontmatter"); err == nil {
		t.Error("missing frontmatter accepted")
	}
	if _, _, err := Parse("---\nname: x\nbody without close"); err == nil {
		t.Error("unterminated frontmatter accepted")
	}
	if _, _, err := Parse("---\n: :\n---\n"); err == nil {
		t.Error("invalid YAML accepted")
	}
}

func TestMarshalEmptyBody(t *testing.T) {
	out, err := Marshal(Frontmatter{Name: "bare"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "---\n") || strings.Count(out, "---") != 2 {
		t.Errorf("frontmatter-only file malformed: %q", out)
	}
}

// TestMarshalPreservesUnknownFrontmatter: role edit parses and rewrites the
// file, so keys the struct does not model must survive verbatim.
func TestMarshalPreservesUnknownFrontmatter(t *testing.T) {
	input := `---
name: tester
description: runs checks
specialty: testing
owner: platform-team
metadata:
  tier: 2
  labels: [ci, review]
tools: [bash]
---
`

	fm, body, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		"owner: platform-team",
		"metadata:",
		"tier: 2",
		"labels:",
		"ci",
		"review",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("marshaled role lost %q:\n%s", want, out)
		}
	}

	fm2, body2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	out2, err := Marshal(fm2, body2)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if out2 != out {
		t.Errorf("round trip changed the file:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
}

func topLevelKeys(t *testing.T, content string) []string {
	t.Helper()
	block := strings.TrimSpace(content)
	if !strings.HasPrefix(block, "---") {
		t.Fatalf("no frontmatter: %q", content)
	}
	rest := block[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		t.Fatalf("unterminated frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(rest[:idx]), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("frontmatter is not a mapping")
	}
	var keys []string
	for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
		keys = append(keys, doc.Content[0].Content[i].Value)
	}
	return keys
}

func TestMarshalPreservesUnknownKeyOrder(t *testing.T) {
	input := `---
owner: platform-team
name: tester
metadata:
  tier: 2
description: runs checks
tools: [bash]
---
`
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	keys := topLevelKeys(t, out)
	pos := func(name string) int {
		for i, key := range keys {
			if key == name {
				return i
			}
		}
		t.Fatalf("missing key %q in %v", name, keys)
		return -1
	}
	if !(pos("owner") < pos("name") && pos("name") < pos("metadata") &&
		pos("metadata") < pos("description") && pos("description") < pos("tools")) {
		t.Fatalf("key order changed: %v", keys)
	}
	if !strings.Contains(out, "owner: platform-team") ||
		!strings.Contains(out, "tier: 2") {
		t.Fatalf("unknown values lost:\n%s", out)
	}
}

func TestMarshalExpandsDanglingAliases(t *testing.T) {
	input := `---
name: &n tester
owner: *n
---
`
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "*n") {
		t.Fatalf("dangling alias in output:\n%s", out)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(strings.SplitN(out, "\n---", 2)[0][3:]), &parsed); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if parsed["owner"] != "tester" {
		t.Fatalf("owner = %v, want tester", parsed["owner"])
	}
}

func TestMarshalPreservesMergeKeys(t *testing.T) {
	input := `---
base: &b
  owner: platform
<<: *b
name: tester
---
`
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	front := strings.SplitN(out, "\n---", 2)[0][3:]
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if parsed["owner"] != "platform" {
		t.Fatalf("merge key lost: owner = %v\n%s", parsed["owner"], out)
	}
}

func TestMarshalPreservesKeepChompScalar(t *testing.T) {
	input := "---\nname: tester\nnotes: |+\n  hello\n\ntools: [bash]\n---\n"
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	front := strings.SplitN(out, "\n---", 2)[0][3:]
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if !strings.HasSuffix(parsed["notes"].(string), "\n\n") {
		t.Fatalf("keep-chomp trailing newlines lost: %q\n%s", parsed["notes"], out)
	}
}

func TestMarshalHandlesSelfReferentialAnchor(t *testing.T) {
	input := `---
name: tester
metadata: &m
  self: *m
---
`
	fm, body, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(fm, body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, _, err := Parse(out); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
}

func TestParseRejectsComplexKeys(t *testing.T) {
	input := "---\n? [a, b]\n: value\nname: tester\n---\n"
	if _, _, err := Parse(input); err == nil {
		t.Fatal("Parse accepted a non-scalar frontmatter key")
	}
}
