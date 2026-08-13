package runner

import (
	"context"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/agent"
)

// ShepherdTurnCheckpointer adapts agent.TurnCheckpointer to the shared
// shepherd-kernel-go ScopeManager. It records git checkpoints (plus opaque
// snapshots) on a single scope.
type ShepherdTurnCheckpointer struct {
	mgr      *shepherd.ScopeManager
	scopeID  string
	repoPath string
}

// NewShepherdTurnCheckpointer returns a turn checkpointer that creates
// single-use checkpoints on scopeID for the repository at repoPath.
func NewShepherdTurnCheckpointer(mgr *shepherd.ScopeManager, scopeID, repoPath string) *ShepherdTurnCheckpointer {
	return &ShepherdTurnCheckpointer{mgr: mgr, scopeID: scopeID, repoPath: repoPath}
}

// Checkpoint creates a single-use git checkpoint carrying snapshot.
func (c *ShepherdTurnCheckpointer) Checkpoint(ctx context.Context, snapshot []byte) (string, error) {
	cp, err := c.mgr.CreateCheckpoint(c.scopeID, c.repoPath, snapshot)
	if err != nil {
		return "", err
	}
	return cp.ID, nil
}

// Restore rewinds the workspace and returns the stored snapshot. The
// checkpoint is consumed by this call.
func (c *ShepherdTurnCheckpointer) Restore(ctx context.Context, id string) ([]byte, error) {
	return c.mgr.RestoreCheckpoint(id)
}

var _ agent.TurnCheckpointer = (*ShepherdTurnCheckpointer)(nil)
