package tools

import (
	"context"
	"testing"

	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// newSandboxWS mirrors the substrate test helper (test helpers do not cross
// packages): a sandbox workspace over a fresh fake sandbox, created with the
// workspace root as its workdir the way an isolated session provisions a
// container before dispatching tools.
func newSandboxWS(t *testing.T, sb *sandboxfake.Sandbox) *sandboxWorkspace {
	t.Helper()
	if sb == nil {
		sb = sandboxfake.New()
	}
	if !sb.Created() {
		if err := sb.Create(context.Background(), goatherdsandbox.SandboxSpec{Workdir: "/workspace"}); err != nil {
			t.Fatalf("create sandbox: %v", err)
		}
	}
	return newSandboxWorkspace(sb, "/workspace")
}
