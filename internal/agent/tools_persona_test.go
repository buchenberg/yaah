package agent

import (
	"slices"
	"testing"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// personaSeamRegistry builds a registry with a small write-capable
// surface so the advertised-set assertions have something to lose.
// tools.NewRegistry() installs yaah's full built-in set, so the surface
// is belt-limited to exactly these four instead.
func personaSeamRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistryWithBelt(tools.NewBelt([]string{"read", "grep", "bash", "write"}))
	return reg
}

// defNames extracts the advertised tool names from a defs list.
func defNames(defs []types.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

// TestPersonaToolsNarrowAdvertisedSet pins the yaah-21uk.12 per-run
// seam: a non-empty PersonaTools list intersects the level's advertised
// surface — narrow-only — and empty narrows nothing, the golden. The
// same intersection gates execution, so a synthesized call to a tool
// the persona does not name is refused even though the registry holds
// it.
func TestPersonaToolsNarrowAdvertisedSet(t *testing.T) {
	t.Run("persona list intersects the full surface", func(t *testing.T) {
		l := NewLoop(nil, personaSeamRegistry(t),
			WithToolsLevel(FullTools),
			WithPersonaTools([]string{"read", " grep "}),
		)
		got := defNames(l.buildToolsForLevel())
		if !slices.Equal(got, []string{"grep", "read"}) {
			t.Fatalf("advertised = %v; want the persona intersection [grep read] (sorted)", got)
		}
		if l.allowsTool("bash") {
			t.Error("allowsTool(bash) = true; the persona does not name it")
		}
		if l.allowsTool("write") {
			t.Error("allowsTool(write) = true; the persona does not name it")
		}
		if !l.allowsTool("grep") {
			t.Error("allowsTool(grep) = false; the persona names it")
		}
	})

	t.Run("empty persona list narrows nothing (golden)", func(t *testing.T) {
		l := NewLoop(nil, personaSeamRegistry(t), WithToolsLevel(FullTools))
		got := defNames(l.buildToolsForLevel())
		if !slices.Equal(got, []string{"bash", "grep", "read", "write"}) {
			t.Fatalf("advertised = %v; want the untouched full surface", got)
		}
		if !l.allowsTool("bash") {
			t.Error("golden narrowed: allowsTool(bash) = false without a persona list")
		}
	})

	t.Run("read-only level strips first, persona narrows second", func(t *testing.T) {
		l := NewLoop(nil, personaSeamRegistry(t),
			WithToolsLevel(ReadOnlyTools),
			WithPersonaTools([]string{"bash", "read"}),
		)
		got := defNames(l.buildToolsForLevel())
		// bash is not in the inspection set, so the level strips it
		// before the persona's list is applied.
		if !slices.Equal(got, []string{"read"}) {
			t.Fatalf("advertised = %v; want [read] (level strip ∩ persona list)", got)
		}
		if l.allowsTool("bash") {
			t.Error("allowsTool(bash) = true; neither the level nor the persona admits it")
		}
	})

	t.Run("narrowing can never widen a stripped level", func(t *testing.T) {
		l := NewLoop(nil, personaSeamRegistry(t),
			WithToolsLevel(ReadOnlyTools),
			WithPersonaTools([]string{"bash", "write"}),
		)
		if got := l.buildToolsForLevel(); got != nil {
			t.Fatalf("advertised = %v; want nil (persona names only tools the level strips)", got)
		}
	})
}
