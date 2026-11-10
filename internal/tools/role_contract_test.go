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

// TestRoleTool_createFillsDefaultContractAndBudgets pins the W7.1
// default: a role created without contract or budget columns comes out
// schema-complete — the worker-shaped contract and sane budgets copying
// the shipped golang-developer.md shape — instead of the zeroed budgets
// ("no cap") and missing contract a create once wrote. This supersedes
// the earlier pin that asserted the contract block was omitted: the
// blank-slate create was itself a bug the parity plan called out.
func TestRoleTool_createFillsDefaultContractAndBudgets(t *testing.T) {
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
	fm, _, err := rolefile.Parse(string(data))
	if err != nil {
		t.Fatalf("created file does not parse: %v", err)
	}
	if fm.Contract.Heading != "## Changes" {
		t.Errorf("default contract heading = %q; want \"## Changes\"", fm.Contract.Heading)
	}
	if len(fm.Contract.Fields) != len(defaultRoleContract.Fields) {
		t.Fatalf("default contract fields = %d; want %d", len(fm.Contract.Fields), len(defaultRoleContract.Fields))
	}
	for i, want := range defaultRoleContract.Fields {
		if fm.Contract.Fields[i] != want {
			t.Errorf("field[%d] = %+v; want %+v", i, fm.Contract.Fields[i], want)
		}
	}
	if fm.MaxLoopCycles != defaultRoleMaxIterations {
		t.Errorf("max_iterations = %d; want %d", fm.MaxLoopCycles, defaultRoleMaxIterations)
	}
	if fm.MaxToolTurns != defaultRoleMaxTurns {
		t.Errorf("max_turns = %d; want %d", fm.MaxToolTurns, defaultRoleMaxTurns)
	}
	if fm.Timeout != defaultRoleTimeout {
		t.Errorf("timeout = %d; want %d", fm.Timeout, defaultRoleTimeout)
	}
	// Floors stay at the no-floor default — those are per-role tuning,
	// not part of the shape.
	if fm.MinLoopCycles != 0 || fm.MinToolTurns != 0 {
		t.Errorf("min budgets = %d/%d; want 0/0", fm.MinLoopCycles, fm.MinToolTurns)
	}
}

// TestRoleTool_createWithContractOverride checks the explicit contract
// columns parse: typed and untyped field forms, and a fail-closed
// rejection of an unknown kind — a direct tool call, like mode.
func TestRoleTool_createWithContractOverride(t *testing.T) {
	dir := t.TempDir()
	tool := &RoleTool{Dirs: []string{dir}}

	out, err := tool.Execute(context.Background(),
		`{"action":"create","name":"audit","description":"audits","body":"Audit.","contract_heading":"## Audit","contract_fields":"severity:evidence, files_scanned, findings:interpretation"}`)
	if err != nil {
		t.Fatalf("create failed: %v (output: %s)", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.md"))
	if err != nil {
		t.Fatal(err)
	}
	fm, _, err := rolefile.Parse(string(data))
	if err != nil {
		t.Fatalf("created file does not parse: %v", err)
	}
	if fm.Contract.Heading != "## Audit" || len(fm.Contract.Fields) != 3 {
		t.Fatalf("contract = %+v; want 3 fields under ## Audit", fm.Contract)
	}
	if fm.Contract.Fields[0].Kind != "evidence" || fm.Contract.Fields[1].Kind != "" || fm.Contract.Fields[2].Kind != "interpretation" {
		t.Errorf("field kinds = %+v; want evidence/interpretation/interpretation", fm.Contract.Fields)
	}

	if _, err := tool.Execute(context.Background(),
		`{"action":"create","name":"bad","description":"x","body":"x","contract_heading":"## H","contract_fields":"a:conjecture"}`); err == nil {
		t.Fatal("unknown contract field kind was accepted; want rejection")
	}
}

const roleWithMode = `---
name: moded
description: carries a capability mode
mode: all
owner: platform-team
specialty: building
tools:
    - read
max_iterations: 10
---

Build things.
`

// TestRoleTool_editPreservesMode pins yaah-21uk D1: the mode capability
// field round-trips through the role tool's edit path (Parse→Marshal),
// and unmodeled extension keys around it survive. Since yaah-21uk.13 the
// tool also exposes a mode argument; this pins the preserve path — an
// edit that omits mode leaves the file's value untouched.
func TestRoleTool_editPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moded.md")
	if err := os.WriteFile(path, []byte(roleWithMode), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &RoleTool{Dirs: []string{dir}}
	out, err := tool.Execute(context.Background(),
		`{"action":"edit","name":"moded","description":"updated description"}`)
	if err != nil {
		t.Fatalf("edit failed: %v (output: %s)", err, out)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for _, want := range []string{
		"mode: all",
		"owner: platform-team",
		"updated description",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("edited role file lost %q\n--- file content ---\n%s", want, content)
		}
	}

	// The rewritten file must still parse, with the mode intact.
	fm, _, err := rolefile.Parse(content)
	if err != nil {
		t.Fatalf("edited file no longer parses: %v", err)
	}
	if fm.Mode != "all" {
		t.Errorf("mode = %q; want all", fm.Mode)
	}
}
