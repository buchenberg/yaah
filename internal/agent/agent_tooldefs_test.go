package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
)

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
