package dispatch

// The sealed output is the durable record for a worktree proposal. Its
// Snapshot carries the captured conversation, its Metadata carries the rest of
// what a restart needs to show, continue, and settle the proposal, and its
// retained diff is the review text. There is no sidecar: the trace plus the
// named-blob store are the source of truth.

import (
	"encoding/json"
	"fmt"

	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/types"
)

// maxDurableTranscriptBytes bounds the transcript carried in the sealed
// output's execution snapshot. A larger transcript is not truncated — a
// partial message sequence can orphan tool calls — it is omitted, leaving the
// proposal settleable but not conversation-continuable after a restart.
const maxDurableTranscriptBytes = 8 << 20 // 8 MiB

// retainedProposalMeta is the proposal state carried in SealOptions.Metadata.
// The conversation rides separately in SealOptions.Snapshot and the diff is
// retained by the seal itself, so neither is duplicated here.
type retainedProposalMeta struct {
	ID            string              `json:"id,omitempty"`
	SessionID     string              `json:"session_id,omitempty"`
	ParentScopeID string              `json:"parent_scope_id,omitempty"`
	Result        string              `json:"result,omitempty"`
	RunErr        string              `json:"run_err,omitempty"`
	StopReason    string              `json:"stop_reason,omitempty"`
	Params        jobs.SubAgentParams `json:"params,omitempty"`
	TimeoutMS     int64               `json:"timeout_ms,omitempty"`
	// TranscriptOmitted records that the conversation exceeded the snapshot
	// bound and was dropped rather than truncated.
	TranscriptOmitted bool `json:"transcript_omitted,omitempty"`
}

// proposalSealOptions builds the seal payload for a worktree proposal. The
// conversation becomes the execution snapshot; everything else becomes opaque
// metadata. A transcript over the bound is omitted and marked.
func proposalSealOptions(meta retainedProposalMeta, transcript []types.Message) (goatherd.SealOptions, error) {
	// The seed is recomputed at continuation time; persisting it would double
	// the transcript and the workdir is per-run. A source link describes how
	// this proposal was originally threaded, not how to continue it.
	params := meta.Params
	params.SeedMessages = nil
	params.Workdir = ""
	params.SeedFrom = ""
	params.SeedMode = ""
	meta.Params = params

	opts := goatherd.SealOptions{}
	if len(transcript) > 0 {
		data, err := json.Marshal(transcript)
		if err != nil {
			return goatherd.SealOptions{}, fmt.Errorf("marshal transcript: %w", err)
		}
		if len(data) <= maxDurableTranscriptBytes {
			opts.Snapshot = data
		} else {
			meta.TranscriptOmitted = true
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return goatherd.SealOptions{}, fmt.Errorf("marshal proposal metadata: %w", err)
	}
	opts.Metadata = data
	return opts, nil
}

// proposalMetaFromOutput reads what proposalSealOptions wrote. Missing
// metadata means the output was sealed before durable proposal state existed;
// that is reported, not guessed.
func proposalMetaFromOutput(out *goatherd.RetainedOutput) (retainedProposalMeta, []types.Message, error) {
	var meta retainedProposalMeta
	raw := out.Metadata()
	if len(raw) == 0 {
		return meta, nil, fmt.Errorf("sealed output has no proposal metadata (sealed before durable proposal state)")
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, nil, fmt.Errorf("decode proposal metadata: %w", err)
	}
	var transcript []types.Message
	if snapshot := out.Snapshot(); len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &transcript); err != nil {
			return meta, nil, fmt.Errorf("decode transcript snapshot: %w", err)
		}
	}
	return meta, transcript, nil
}
