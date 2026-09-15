package runner

import (
	"context"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/agent"
)

// ShepherdTurnCheckpointer adapts agent.TurnCheckpointer to the shared
// shepherd-kernel-go ScopeManager. It records workspace checkpoints (plus
// opaque snapshots) on a single scope, using whatever Sandbox that scope holds
// — an in-place repository, or the sub-agent's isolated worktree.
type ShepherdTurnCheckpointer struct {
	mgr     *shepherd.ScopeManager
	scopeID string
	sandbox shepherd.Sandbox
}

// NewShepherdTurnCheckpointer returns a turn checkpointer that creates
// single-use checkpoints on scopeID against sb.
func NewShepherdTurnCheckpointer(mgr *shepherd.ScopeManager, scopeID string, sb shepherd.Sandbox) *ShepherdTurnCheckpointer {
	return &ShepherdTurnCheckpointer{mgr: mgr, scopeID: scopeID, sandbox: sb}
}

// Checkpoint creates a single-use workspace checkpoint carrying snapshot.
func (c *ShepherdTurnCheckpointer) Checkpoint(ctx context.Context, snapshot []byte) (string, error) {
	cp, err := c.mgr.CreateCheckpoint(ctx, c.scopeID, snapshot)
	if err != nil {
		return "", err
	}
	return cp.ID, nil
}

// Restore rewinds the workspace and returns the stored snapshot. The
// checkpoint is consumed by this call.
func (c *ShepherdTurnCheckpointer) Restore(ctx context.Context, id string) ([]byte, error) {
	return c.mgr.RestoreCheckpoint(ctx, id)
}

// Prune discards every live checkpoint on the adapter's scope.
func (c *ShepherdTurnCheckpointer) Prune(ctx context.Context) error {
	c.mgr.PruneCheckpoints(c.scopeID)
	return nil
}

var _ agent.TurnCheckpointer = (*ShepherdTurnCheckpointer)(nil)
