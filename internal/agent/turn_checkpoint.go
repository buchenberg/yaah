package agent

import "context"

// TurnCheckpointer captures and restores a sub-agent loop's state at a point
// in time.
//
// Snapshots are opaque byte blobs: the caller (the loop) owns encoding its
// conversation state, while the implementation owns durable storage (the git
// workspace plus the snapshot payload). Checkpoints are single-use — Restore
// consumes the checkpoint, so a restored checkpoint cannot be restored again.
// Branching/repeated exploration from the same point is a separate concern
// (e.g. fork semantics) and is intentionally out of scope for this interface.
type TurnCheckpointer interface {
	// Checkpoint records the current workspace and snapshot, returning an
	// identifier that can later be passed to Restore.
	Checkpoint(ctx context.Context, snapshot []byte) (id string, err error)
	// Restore rewinds the workspace to the checkpoint and returns the
	// snapshot stored at Checkpoint time. The checkpoint is consumed.
	Restore(ctx context.Context, id string) (snapshot []byte, err error)
}
