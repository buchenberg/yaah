package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/rolefile"
)

const roleWithContract = `---
name: tester
description: runs the test suite
specialty: testing
contract:
    heading: Test Report
    fields:
        - command
        - {name: exit_code, kind: evidence}
        - {name: assessment, kind: interpretation}
tools:
    - read
    - bash
max_iterations: 50
timeout: 960
---

Run the tests and report back.
`

// TestRoleTool_editPreservesContract pins that rewriting a role file via
// the role tool keeps the response contract block. The tool's
// frontmatter struct once omitted the contract field, so 'edit' silently
// dropped it from user role files (data loss, review finding B3).
func TestRoleTool_editPreservesContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tester.md")
	if err := os.WriteFile(path, []byte(roleWithContract), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &RoleTool{Dirs: []string{dir}}
	out, err := tool.Execute(context.Background(),
		`{"action":"edit","name":"tester","description":"updated description"}`)
	if err != nil {
		t.Fatalf("edit failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for _, want := range []string{
		"contract:",
		"heading: Test Report",
		"exit_code",
		"kind: evidence",
		"assessment",
		"kind: interpretation",
		"updated description",
		"max_iterations: 50",
		"timeout: 960",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("edited role file lost %q\n--- file content ---\n%s", want, content)
		}
	}

	// The rewritten file must still parse, with the contract intact.
	fm, body, err := rolefile.Parse(content)
	if err != nil {
		t.Fatalf("edited file no longer parses: %v", err)
	}
	if fm.Contract.Heading != "Test Report" {
		t.Errorf("contract heading = %q; want %q", fm.Contract.Heading, "Test Report")
	}
	if len(fm.Contract.Fields) != 3 {
		t.Fatalf("contract fields = %d; want 3", len(fm.Contract.Fields))
	}
	if fm.Contract.Fields[0].Name != "command" {
		t.Errorf("string-form field lost: %+v", fm.Contract.Fields[0])
	}
	if fm.Contract.Fields[1].Kind != "evidence" {
		t.Errorf("map-form field kind lost: %+v", fm.Contract.Fields[1])
	}
	if !strings.Contains(body, "Run the tests and report back.") {
		t.Errorf("body lost: %q", body)
	}
}

// TestRoleTool_createWithoutContractOmitsBlock ensures roles created
// without a contract do not grow an empty contract: block (omitempty).
func TestRoleTool_createWithoutContractOmitsBlock(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}

	out, err := tool.Execute(context.Background(),
		`{"action":"create","name":"writer","description":"writes docs","body":"Write the docs."}`)
	if err != nil {
		t.Fatalf("create failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(filepath.Join(dir, "writer.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "contract:") {
		t.Errorf("created role without contract should not contain a contract block:\n%s", data)
	}
}
