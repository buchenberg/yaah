package agent

import (
	"context"
	"encoding/json"
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

func TestReadOnlyTools_WithholdsMutatingTools(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	for _, name := range []string{"read", "grep", "write", "edit", "bash"} {
		if tool := tools.NewLeafTool(name); tool != nil {
			reg.Register(tool)
		}
	}
	reg.Register(namedTestTool("delegate"))
	reg.Register(&tools.PlanTool{})
	loop := &Loop{Registry: reg}
	loop.Config.ToolsLevel = ReadOnlyTools
	defs := loop.buildToolsForLevel()
	got := make(map[string]bool, len(defs))
	for _, def := range defs {
		got[def.Function.Name] = true
	}
	for _, want := range []string{"read", "grep"} {
		if !got[want] {
			t.Errorf("read-only tool set missing %q: %v", want, got)
		}
	}
	for _, forbidden := range []string{"write", "edit", "bash", "delegate", "plan"} {
		if got[forbidden] {
			t.Errorf("read-only tool set exposes %q", forbidden)
		}
	}
	if loop.allowsTool("write") {
		t.Error("read-only execution gate allowed write")
	}
	if !loop.allowsTool("read") {
		t.Error("read-only execution gate refused read")
	}
}

func TestSubAgentsOnlyGate_WithholdsDirectTools(t *testing.T) {
	loop := &Loop{}
	loop.Config.ToolsLevel = SubAgentsOnly
	if loop.allowsTool("write") {
		t.Error("sub-agents-only execution gate allowed write")
	}
	if !loop.allowsTool("delegate") {
		t.Error("sub-agents-only execution gate refused delegate")
	}
}
