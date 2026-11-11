package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
)

type namedTestTool string

func (n namedTestTool) Name() string                                  { return string(n) }
func (namedTestTool) Description() string                             { return "test tool" }
func (namedTestTool) Schema() json.RawMessage                         { return json.RawMessage(`{"type":"object"}`) }
func (namedTestTool) Execute(context.Context, string) (string, error) { return "", nil }

func TestBuildToolDefs_cachedAcrossCalls(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(tools.NewLeafTool("read"))

	loop := &Loop{Registry: reg}

	first := loop.buildToolDefs()
	second := loop.buildToolDefs()

	if len(first) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(first))
	}
	// Same backing array means a cache hit with no rebuild.
	if &first[0] != &second[0] {
		t.Error("buildToolDefs should return the cached slice when the registry is unchanged")
	}
}

func TestBuildToolDefs_invalidatedOnRegister(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(tools.NewLeafTool("read"))

	loop := &Loop{Registry: reg}

	before := loop.buildToolDefs()
	if len(before) != 1 {
		t.Fatalf("expected 1 tool def before, got %d", len(before))
	}

	reg.Register(tools.NewLeafTool("grep"))
	after := loop.buildToolDefs()

	if len(after) != 2 {
		t.Errorf("expected 2 tool defs after registering a new tool, got %d", len(after))
	}
	if &before[0] == &after[0] {
		t.Error("buildToolDefs should rebuild after a registry mutation")
	}
}

func TestRegistryGeneration_incrementsOnRegister(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	g0 := reg.Generation()
	reg.Register(tools.NewLeafTool("read"))
	g1 := reg.Generation()
	reg.Register(tools.NewLeafTool("grep"))
	g2 := reg.Generation()

	if !(g0 < g1 && g1 < g2) {
		t.Errorf("generation should increase on each Register: g0=%d g1=%d g2=%d", g0, g1, g2)
	}
}

func TestBuildToolDefs_nilRegistry(t *testing.T) {
	loop := &Loop{}
	if got := loop.buildToolDefs(); got != nil {
		t.Errorf("buildToolDefs with nil registry = %#v, want nil", got)
	}
}

func TestReadOnlyTools_ExposesInspectionSet(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	for _, name := range []string{"read", "grep", "write", "edit", "bash"} {
		if tool := tools.NewLeafTool(name); tool != nil {
			reg.Register(tool)
		}
	}
	reg.Register(namedTestTool("delegate"))
	reg.Register(&tools.GitTool{})
	reg.Register(&tools.StaticcheckTool{})
	reg.Register(&tools.PlanTool{})
	loop := &Loop{Registry: reg}
	loop.Config.ToolsLevel = ReadOnlyTools
	defs := loop.buildToolsForLevel()
	got := make(map[string]bool, len(defs))
	for _, def := range defs {
		got[def.Function.Name] = true
	}
	for _, want := range []string{"read", "grep", "git", "staticcheck", "plan"} {
		if !got[want] {
			t.Errorf("plan-mode tool set missing %q: %v", want, got)
		}
	}
	for _, forbidden := range []string{"write", "edit", "bash", "delegate"} {
		if got[forbidden] {
			t.Errorf("plan-mode tool set exposes %q", forbidden)
		}
	}
}

// TestReadOnlyToolNames_InspectionSet pins the membership of the inspection
// set that backs plan mode. Adding "git" is what makes the per-call danger
// gate load-bearing: the set is matched on names, and git mutates by
// argument.
func TestReadOnlyToolNames_InspectionSet(t *testing.T) {
	want := []string{
		"read", "grep", "glob", "ls", "file_info", "go_outline", "calculate",
		"diff", "question", "git", "staticcheck", "plan",
	}
	for _, name := range want {
		if !readOnlyToolNames[name] {
			t.Errorf("inspection set is missing %q", name)
		}
	}
	if len(readOnlyToolNames) != len(want) {
		t.Errorf("inspection set has %d entries, want %d: %v", len(readOnlyToolNames), len(want), readOnlyToolNames)
	}
}

// TestAllowsTool_ReadOnlyLevel pins the execution gate for plan mode: the
// inspection set passes, everything that writes source or runs commands stays
// out.
func TestAllowsTool_ReadOnlyLevel(t *testing.T) {
	loop := &Loop{}
	loop.Config.ToolsLevel = ReadOnlyTools
	for _, name := range []string{"read", "grep", "git", "staticcheck", "plan"} {
		if !loop.allowsTool(name) {
			t.Errorf("plan-mode execution gate refused %q", name)
		}
	}
	for _, name := range []string{"bash", "edit", "write"} {
		if loop.allowsTool(name) {
			t.Errorf("plan-mode execution gate allowed %q", name)
		}
	}
}

type openSchemaTool struct{}

func (openSchemaTool) Name() string        { return "remote_tool" }
func (openSchemaTool) Description() string { return "a server-owned schema" }
func (openSchemaTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{}},"additionalProperties":true}`)
}
func (openSchemaTool) Execute(context.Context, string) (string, error) { return "", nil }

// AllowAdditionalProperties opts out of the local closing: an MCP server
// owns its schema and may accept keys we cannot see.
func (openSchemaTool) AllowAdditionalProperties() bool { return true }

func TestAdvertisedSchema_ClosesBuiltinsAndLeavesRemoteSchemasOpen(t *testing.T) {
	builtin := tools.CloseSchema(json.RawMessage(`{"type":"object","properties":{"a":{}}}`))
	if !strings.Contains(string(builtin), `"additionalProperties":false`) {
		t.Errorf("built-in schema not closed: %s", builtin)
	}

	remote := advertisedSchema(openSchemaTool{})
	if !strings.Contains(string(remote), `"additionalProperties":true`) {
		t.Errorf("remote schema was rewritten: %s", remote)
	}
}
