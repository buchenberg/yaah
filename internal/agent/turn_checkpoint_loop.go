package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// defaultMaxTurnRestores bounds turn-level restores per Run when
// LoopConfig.MaxTurnRestores is unset, so a deterministically failing
// turn cannot rewind forever.
const defaultMaxTurnRestores = 3

// Guidance appended as a user message after a restore so the model
// knows the rewind happened and why.
const (
	turnFailureGuidanceFormat = "SUPERVISOR: the previous turn failed while executing tools: %v. " +
		"The workspace and your conversation were rolled back to the state before that turn. " +
		"Take a different approach."

	exhaustionGuidance = "SUPERVISOR: you exhausted your iteration budget without finishing. " +
		"The workspace and your conversation were rolled back to the state before your last turn. " +
		"Wrap up with what you have, or take a more direct approach."
)

// turnCheckpointActive reports whether this loop records per-turn
// checkpoints: the checkpointer must be wired and explicitly enabled.
func (l *Loop) turnCheckpointActive() bool {
	return l.Config.TurnCheckpointEnabled && l.Config.TurnCheckpointer != nil
}

// checkpointTurn snapshots the workspace plus the current conversation
// before a model turn, registering the checkpoint ID for a later
// restore. Failures are logged and skipped — a turn without a
// checkpoint is valid, it just cannot be rewound.
//
// When TurnCheckpointMax is set and reached, all live checkpoints are
// pruned first: v1 only ever restores the most recent checkpoint, so
// older ones are dead weight that would otherwise accumulate git stash
// metadata for the whole run.
func (l *Loop) checkpointTurn(ctx context.Context, messages []types.Message) {
	if !l.turnCheckpointActive() {
		return
	}

	if max := l.Config.TurnCheckpointMax; max > 0 && len(l.State.TurnCheckpoints) >= max {
		if err := l.Config.TurnCheckpointer.Prune(ctx); err != nil {
			slog.Debug("turn_checkpoint: prune failed", "err", err)
		}
		l.State.TurnCheckpoints = nil
	}

	snap, err := json.Marshal(messages)
	if err != nil {
		slog.Debug("turn_checkpoint: snapshot marshal failed", "err", err)
		return
	}

	id, err := l.Config.TurnCheckpointer.Checkpoint(ctx, snap)
	if err != nil {
		slog.Debug("turn_checkpoint: checkpoint failed (continuing without)", "err", err)
		return
	}
	l.State.TurnCheckpoints = append(l.State.TurnCheckpoints, id)
}

// restoreLastTurnCheckpoint rewinds the workspace and conversation to
// the most recent turn checkpoint, then appends guidance as a user
// message so the model retries from the rewound state. It reports
// whether a restore happened.
//
// Restore is skipped (returning false) when checkpointing is inactive,
// no checkpoint exists, or the MaxTurnRestores budget is exhausted —
// the caller then falls through to its original failure behavior.
//
// Checkpoints are single-use: the restored ID and the whole live list
// are dropped. If the git rewind succeeds but the snapshot cannot be
// decoded, the conversation stays as-is (the workspace is already
// rewound at that point and the checkpoint is consumed either way).
func (l *Loop) restoreLastTurnCheckpoint(ctx context.Context, messages *[]types.Message, guidance string) bool {
	if !l.turnCheckpointActive() {
		return false
	}
	maxRestores := l.Config.MaxTurnRestores
	if maxRestores <= 0 {
		maxRestores = defaultMaxTurnRestores
	}
	if l.State.TurnRestores >= maxRestores {
		return false
	}
	ids := l.State.TurnCheckpoints
	if len(ids) == 0 {
		return false
	}
	id := ids[len(ids)-1]

	snap, err := l.Config.TurnCheckpointer.Restore(ctx, id)
	if err != nil {
		slog.Debug("turn_checkpoint: restore failed", "id", id, "err", err)
		return false
	}

	// The checkpoint is consumed — drop the live list regardless of how
	// the snapshot decodes below.
	l.State.TurnCheckpoints = nil
	l.State.TurnRestores++
	l.State.RestoredFrom = id
	jobs.RecordTurnRestore(ctx, id)

	if len(snap) > 0 {
		var restored []types.Message
		if err := json.Unmarshal(snap, &restored); err != nil {
			slog.Debug("turn_checkpoint: snapshot decode failed; keeping conversation", "id", id, "err", err)
		} else {
			*messages = restored
		}
	}

	*messages = append(*messages, types.UserMsg(guidance))
	l.State.Messages = *messages
	l.Persister.SetMsgIdx(len(*messages))
	return true
}
