package host

// envelope_parity_test.go is the pin internal/workflow/exec/doc.go names. The
// scheduler may not import internal/dispatch (dispatch links
// internal/tools/substrate, and exec must carry no internal/tools dependency at
// all), so it reads the completion through the mirrored exec.Completion type.
// These tests are what keep the mirror honest: they render a REAL envelope with
// dispatch's own result type and marshaller, decode it into exec.Completion, and
// fail if a scheduler-visible fact moved, renamed, or disappeared.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/workflow/exec"
)

func decodeCompletion(t *testing.T, env dispatch.ProposalEnvelope) exec.Completion {
	t.Helper()
	var c exec.Completion
	raw := dispatch.MarshalProposalEnvelope(env)
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("decoding the rendered envelope into exec.Completion: %v\n%s", err, raw)
	}
	return c
}

func TestEnvelopeParityReportLane(t *testing.T) {
	res := dispatch.DispatchResult{
		Status: dispatch.DispatchCompleted,
		Result: "the report",
		Meta: dispatch.EnvelopeMeta{
			DispatchID: "steps.plan#1",
			Role:       "planner",
			Lane:       "report",
			World:      "shared",
			Model:      "glm-4",
			Duration:   1500 * time.Millisecond,
		},
	}
	c := decodeCompletion(t, res.Envelope())

	if c.Status != exec.StatusReport || !c.Completed() {
		t.Errorf("status = %q (completed=%v), want report and a completion", c.Status, c.Completed())
	}
	if c.DispatchID != "steps.plan#1" || c.Role != "planner" || c.Lane != "report" {
		t.Errorf("identity did not survive the mirror: %+v", c)
	}
	if c.World != "shared" || c.Model != "glm-4" {
		t.Errorf("runner facts did not survive the mirror: %+v", c)
	}
	if c.DurationMs != 1500 || c.Duration() != 1500*time.Millisecond {
		t.Errorf("duration_ms = %d, want 1500", c.DurationMs)
	}
	if c.StopReason != "completed" {
		t.Errorf("stop_reason = %q, want completed", c.StopReason)
	}
	if c.Result != "the report" {
		t.Errorf("result = %q, want the delivered text", c.Result)
	}
	// The dispatcher seals every envelope through SetRecovery, which derives
	// a recovery block from stop_reason when the lane asserted none: a
	// completed report carries recovery{retryable:false}. The mirror keeps
	// that verbatim — asserted, and not retryable.
	if c.Recovery == nil || c.Recovery.Retryable {
		t.Errorf("recovery = %+v, want the sealed assertion retryable=false on a completed report", c.Recovery)
	}
	// The contract block is reserved (yaah-was9.2): a real lane asserts
	// nothing today, and the mirror must read that as nil rather than as a
	// pass.
	if c.Contract != nil || c.ContractStatus() != "" {
		t.Errorf("contract = %+v, want absent on a real envelope", c.Contract)
	}
}

func TestEnvelopeParityTimeoutIsRetryable(t *testing.T) {
	// The retry decision the scheduler makes comes from this block and from
	// nothing else: a timeout classifies as status=error with
	// stop_reason=timeout and recovery.retryable=true.
	res := dispatch.DispatchResult{
		Status:  dispatch.DispatchTimedOut,
		Partial: "partial body",
		Timeout: 5 * time.Second,
		Meta: dispatch.EnvelopeMeta{
			DispatchID: "steps.a#1",
			Role:       "developer",
			Lane:       "report",
			Duration:   5 * time.Second,
		},
	}
	c := decodeCompletion(t, res.Envelope())

	if c.Status != exec.StatusError || c.Completed() {
		t.Errorf("status = %q, want error and not-completed", c.Status)
	}
	if c.StopReason != "timeout" {
		t.Errorf("stop_reason = %q, want timeout", c.StopReason)
	}
	if c.Recovery == nil || !c.Retryable() {
		t.Fatalf("recovery = %+v, want the envelope's retryable assertion to survive", c.Recovery)
	}
	if c.Recovery.Recommended == "" {
		t.Error("recovery.recommended did not survive the mirror")
	}
	if c.Result != "partial body" || !c.Partial {
		t.Errorf("partial body did not survive: result=%q partial=%v", c.Result, c.Partial)
	}
	if c.DurationMs == 0 {
		t.Error("duration_ms did not survive the mirror")
	}
}

func TestEnvelopeParityProposalAndContract(t *testing.T) {
	// A sealed proposal carries the flat settlement fields plus the nested
	// artifact view; the workflow run records the proposal id and disposes of
	// nothing (the verbs are yaah-0u0v.13's).
	res := dispatch.DispatchResult{
		Status: dispatch.DispatchProposal,
		Role:   "developer",
		Proposal: &dispatch.ProposalEnvelope{
			Status:     "proposal",
			ProposalID: "proposal-1",
			Contract:   &dispatch.ResultContract{Name: "developer", Status: "pass"},
		},
		Meta: dispatch.EnvelopeMeta{DispatchID: "steps.a#1", Lane: "proposal"},
	}
	c := decodeCompletion(t, res.Envelope())

	if c.Status != exec.StatusProposal || !c.Completed() {
		t.Errorf("status = %q, want proposal", c.Status)
	}
	if c.ProposalID != "proposal-1" {
		t.Errorf("proposal_id = %q, want the sealed id", c.ProposalID)
	}
	if c.Contract == nil || c.Contract.Name != "developer" || c.ContractStatus() != "pass" {
		t.Fatalf("contract = %+v, want the was9.2 verdict mirrored verbatim", c.Contract)
	}
}

// TestCompletionJSONTagsAreTheEnvelopesOwn pins the field names the mirror is
// spelled with, so a rename on either side fails here rather than silently
// dropping a fact the scheduler reads.
func TestCompletionJSONTagsAreTheEnvelopesOwn(t *testing.T) {
	raw, err := json.Marshal(exec.Completion{
		Status:     exec.StatusReport,
		DispatchID: "d",
		Role:       "r",
		Lane:       "report",
		World:      "shared",
		Model:      "m",
		DurationMs: 1,
		StopReason: "completed",
		ProposalID: "p",
		Result:     "x",
		Error:      "e",
		Recovery:   &exec.Recovery{Retryable: true, Recommended: "again"},
		Contract:   &exec.Contract{Name: "n", Status: "fail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"status", "dispatch_id", "role", "lane", "world", "model", "duration_ms", "stop_reason", "proposal_id", "result", "error", "recovery", "contract"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("exec.Completion does not emit %q; the envelope field name moved", name)
		}
	}
}
