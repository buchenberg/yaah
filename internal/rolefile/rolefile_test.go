package rolefile

import (
	"reflect"
	"strings"
	"testing"
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
	if !reflect.DeepEqual(fm2, fm) {
		t.Errorf("frontmatter changed on round trip:\n got %+v\nwant %+v", fm2, fm)
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
