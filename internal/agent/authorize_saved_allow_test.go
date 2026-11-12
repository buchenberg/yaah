package agent

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/permissions"
	"github.com/buchenberg/yaah/internal/tools"
)

// TestAuthorizeSensitiveAskSkipsSavedAllow pins the carve-out half that the
// consult cannot enforce itself (yaah-x6vi.7). The saved-allow consultant
// receives ONE derived resource, but the row it matches against may be
// tool-wide or a glob and the call's real target may sit in another argument
// (replace and bulk_files select their files with pattern). So the decision
// is taken here, on the call: a credential-shaped ask never consults, and a
// store that would approve anything cannot lift it.
func TestAuthorizeSensitiveAskSkipsSavedAllow(t *testing.T) {
	consulted := 0
	newLoop := func(rules permissions.Rules) *Loop {
		consulted = 0
		return &Loop{
			Registry: tools.NewRegistry(),
			Config: LoopConfig{
				ToolsLevel:  FullTools,
				Permissions: rules,
				SavedAllow: func(name, resource string) bool {
					consulted++
					return true // a store row that would approve anything
				},
			},
		}
	}
	askEdit := permissions.Rules{{Tool: "edit", Effect: permissions.Ask}}

	t.Run("ordinary file consults and passes", func(t *testing.T) {
		l := newLoop(askEdit)
		d := l.Authorize("edit", `{"filePath":"src/main.go"}`)
		if d.Gate != pipeline.GatePass {
			t.Errorf("gate = %v, want pass (a saved allow lifts an ordinary ask)", d.Gate)
		}
		if consulted != 1 {
			t.Errorf("consulted %d times, want 1", consulted)
		}
	})

	t.Run("dotenv never consults", func(t *testing.T) {
		l := newLoop(askEdit)
		d := l.Authorize("edit", `{"filePath":".env"}`)
		if d.Gate != pipeline.GateAsk {
			t.Errorf("gate = %v, want ask (a saved allow may not lift a sensitive ask)", d.Gate)
		}
		if consulted != 0 {
			t.Errorf("consulted %d times for a sensitive target, want 0", consulted)
		}
	})

	t.Run("target hidden in pattern never consults", func(t *testing.T) {
		// bulk_files derives its resource from `path`, so the old
		// resource-shaped carve-out would have seen only "src".
		l := newLoop(permissions.Rules{{Tool: "bulk_files", Effect: permissions.Ask}})
		d := l.Authorize("bulk_files", `{"pattern":".env","path":"src"}`)
		if d.Gate != pipeline.GateAsk {
			t.Errorf("gate = %v, want ask", d.Gate)
		}
		if consulted != 0 {
			t.Errorf("consulted %d times, want 0", consulted)
		}
	})

	t.Run("patch naming only a diff body never consults", func(t *testing.T) {
		l := newLoop(permissions.Rules{{Tool: "patch", Effect: permissions.Ask}})
		d := l.Authorize("patch", `{"patch":"--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-a\n+b\n"}`)
		if d.Gate != pipeline.GateAsk {
			t.Errorf("gate = %v, want ask", d.Gate)
		}
		if consulted != 0 {
			t.Errorf("consulted %d times, want 0", consulted)
		}
	})
}
