package jobs

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/types"
)

// TestSubAgentTranscriptRoundTrip: a dispatch body installs a sink, the runner
// writes its final conversation through it, and the body reads it back.
func TestSubAgentTranscriptRoundTrip(t *testing.T) {
	var got []types.Message
	ctx := WithSubAgentTranscript(context.Background(), &got)

	want := []types.Message{
		types.SystemMsg("sys"),
		types.UserMsg("do the work"),
	}
	CaptureSubAgentTranscript(ctx, want)

	if len(got) != len(want) {
		t.Fatalf("captured %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Content != want[i].Content {
			t.Errorf("captured[%d] = %q, want %q", i, got[i].Content, want[i].Content)
		}
	}
}

// TestCaptureSubAgentTranscriptNoSink: capture is a no-op, not a panic, when
// the caller installed no sink (report and shared lanes never do).
func TestCaptureSubAgentTranscriptNoSink(t *testing.T) {
	CaptureSubAgentTranscript(context.Background(), []types.Message{types.UserMsg("x")})
}
