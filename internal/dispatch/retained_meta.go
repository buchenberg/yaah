package dispatch

// retained_meta.go persists the half of a worktree proposal the kernel's trace
// deliberately does not carry: the result text, the failure note, the captured
// conversation, and the dispatch params a continuation needs. The kernel
// recovers the scope and the sealed output (state, baseline, changes) from the
// trace; this sidecar sits beside the worktree and is read back at startup so a
// recovered proposal can still be shown and continued.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// worktreeProposalMetaSuffix names a worktree proposal's durable sidecar, which
// sits next to the worktree directory (and inside the worktree root excluded
// from git status).
const worktreeProposalMetaSuffix = ".proposal.json"

// maxDurableTranscriptBytes bounds the transcript embedded in the sidecar. A
// larger transcript is not truncated — a partial message sequence can orphan
// tool calls — it is omitted, leaving the proposal settleable but not
// continuable after a restart.
const maxDurableTranscriptBytes = 8 << 20 // 8 MiB

// worktreeProposalMeta is the on-disk sidecar for one worktree proposal.
type worktreeProposalMeta struct {
	ID            string `json:"id"`
	World         string `json:"world"` // "worktree"
	SessionID     string `json:"session_id,omitempty"`
	ParentScopeID string `json:"parent_scope_id,omitempty"`
	Result        string `json:"result,omitempty"`
	RunErr        string `json:"run_err,omitempty"`
	// Diff is the reviewable diff computed at seal. The kernel's sealed payload
	// omits it (see parity D15), so without this a recovered proposal would show
	// and release with no diff text.
	Diff string `json:"diff,omitempty"`
	// StopReason is the typed run outcome (completed, turn_budget, …), persisted
	// so show after a restart reports it too.
	StopReason string              `json:"stop_reason,omitempty"`
	Params     jobs.SubAgentParams `json:"params,omitempty"`
	TimeoutMS  int64               `json:"timeout_ms,omitempty"`
	Transcript []types.Message     `json:"transcript,omitempty"`
	// TranscriptOmitted records that the transcript exceeded the sidecar bound
	// and was dropped rather than truncated.
	TranscriptOmitted bool `json:"transcript_omitted,omitempty"`
}

// worktreeProposalMetaPath is the sidecar path for a worktree directory.
func worktreeProposalMetaPath(workdir string) string {
	return workdir + worktreeProposalMetaSuffix
}

// persistWorktreeProposalMeta writes the sidecar for a sealed worktree
// proposal. A failure is reported to the caller (as a note) but never fails the
// dispatch: the in-memory proposal still works; only a restart loses the
// result/transcript.
func persistWorktreeProposalMeta(rt RetainedRuntime, p *retainedProposal) error {
	if p == nil || p.workdir == "" {
		return nil
	}
	params := p.params
	// The seed is recomputed at continuation time; persisting it would double
	// the transcript and the workdir is per-run. A source link describes how
	// this proposal was originally threaded, not how to continue it.
	params.SeedMessages = nil
	params.Workdir = ""
	params.SeedFrom = ""
	params.SeedMode = ""

	meta := worktreeProposalMeta{
		ID:            p.id,
		World:         "worktree",
		SessionID:     rt.SessionID,
		ParentScopeID: p.parentID,
		Result:        p.result,
		RunErr:        p.runErr,
		Diff:          p.reviewDiff(),
		StopReason:    p.stopReason,
		Params:        params,
		TimeoutMS:     p.timeout.Milliseconds(),
		Transcript:    p.transcript,
	}
	if len(p.transcript) > 0 {
		if data, err := json.Marshal(p.transcript); err == nil && len(data) > maxDurableTranscriptBytes {
			meta.Transcript = nil
			meta.TranscriptOmitted = true
		}
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sidecar: %w", err)
	}
	// 0o600: the sidecar carries the transcript (worktree contents and tool
	// output) and the diff, so it is private to the owning user.
	if err := os.WriteFile(worktreeProposalMetaPath(p.workdir), data, 0o600); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}
	return nil
}

// removeWorktreeProposalMeta deletes a proposal's sidecar once it is settled.
// Best-effort: a leftover sidecar cannot resurrect a consumed output (the
// rehydration scan checks the kernel output state first).
func removeWorktreeProposalMeta(p *retainedProposal) {
	if p == nil || p.workdir == "" {
		return
	}
	_ = os.Remove(worktreeProposalMetaPath(p.workdir))
}

// readWorktreeProposalMeta reads and validates a sidecar.
func readWorktreeProposalMeta(workdir string) (*worktreeProposalMeta, error) {
	var meta worktreeProposalMeta
	if err := readJSON(worktreeProposalMetaPath(workdir), &meta); err != nil {
		return nil, err
	}
	if meta.ID == "" {
		return nil, errors.New("metadata has no proposal id")
	}
	if meta.World != "worktree" {
		return nil, fmt.Errorf("world %q is not a worktree proposal", meta.World)
	}
	return &meta, nil
}
