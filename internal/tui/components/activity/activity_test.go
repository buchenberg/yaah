package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

func newTestRow() *Row {
	th := colors.NewDarkTheme()
	return NewRow(&th)
}

func TestRow_InitialStateIsIdle(t *testing.T) {
	r := newTestRow()
	if r.State() != Idle {
		t.Errorf("initial state = %v; want Idle", r.State())
	}
	if r.Busy() {
		t.Error("initial row should not be busy")
	}
}

func TestRow_SetStateThinking(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	if r.State() != Thinking || !r.Busy() {
		t.Errorf("state = %v, busy = %v; want Thinking, true", r.State(), r.Busy())
	}
}

func TestRow_SetStateIdleClearsStack(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.SetState(Tool, "bash")
	r.SetState(Idle, "")
	if r.State() != Idle {
		t.Errorf("state = %v; want Idle", r.State())
	}
	// Restore after Idle should be a no-op.
	r.Restore()
	if r.State() != Idle {
		t.Errorf("state after Restore = %v; want Idle (stack cleared)", r.State())
	}
}

func TestRow_OverlayRestoresBase(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.SetState(Tool, "bash")
	if r.State() != Tool {
		t.Fatalf("state = %v; want Tool", r.State())
	}
	r.Restore()
	if r.State() != Thinking {
		t.Errorf("state after Restore = %v; want Thinking", r.State())
	}
}

func TestRow_OverlayReplacesOverlay(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.SetState(Tool, "bash")
	r.SetState(SubAgent, "analyst")
	// prev should still be Thinking (base), not Tool
	r.Restore()
	if r.State() != Thinking {
		t.Errorf("state after Restore = %v; want Thinking (base preserved)", r.State())
	}
}

func TestRow_BaseReplacesBase(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.SetState(Responding, "")
	if r.State() != Responding {
		t.Errorf("state = %v; want Responding", r.State())
	}
	// prev should be Thinking
	r.SetState(Tool, "read")
	r.Restore()
	if r.State() != Responding {
		t.Errorf("state after Restore = %v; want Responding", r.State())
	}
}

func TestRow_RestoreOnNonOverlayIsNoop(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.Restore()
	if r.State() != Thinking {
		t.Errorf("state = %v; want Thinking (no-op restore)", r.State())
	}
}

func TestRow_PreviewClipping(t *testing.T) {
	r := newTestRow()
	r.SetState(Reasoning, "")
	long := strings.Repeat("a", 200)
	r.SetPreview(long)
	if len(r.preview) > previewLen {
		t.Errorf("preview len = %d; want <= %d", len(r.preview), previewLen)
	}
	// Should be the last previewLen runes
	if !strings.HasSuffix(long, r.preview) {
		t.Error("preview should be the trailing portion of the text")
	}
}

func TestRow_PreviewNewlinesReplaced(t *testing.T) {
	r := newTestRow()
	r.SetState(Reasoning, "")
	r.SetPreview("line1\nline2\nline3")
	if strings.Contains(r.preview, "\n") {
		t.Errorf("preview contains newline: %q", r.preview)
	}
	if !strings.Contains(r.preview, "line1 line2 line3") {
		t.Errorf("preview = %q; want newlines replaced with spaces", r.preview)
	}
}

func TestRow_PreviewNotClearedOnSameBaseState(t *testing.T) {
	r := newTestRow()
	r.SetState(Reasoning, "")
	r.SetPreview("some reasoning")
	r.SetState(Reasoning, "updated")
	// Preview should survive because we only clear on non-Reasoning base states
	if r.preview == "" {
		t.Error("preview cleared on same-state transition")
	}
}

func TestRow_PreviewClearedOnDifferentBaseState(t *testing.T) {
	r := newTestRow()
	r.SetState(Reasoning, "")
	r.SetPreview("some reasoning")
	r.SetState(Responding, "")
	if r.preview != "" {
		t.Errorf("preview = %q; want empty after state change", r.preview)
	}
}

func TestRow_SubAgentDetail(t *testing.T) {
	if got := FormatSubAgentDetail("analyst", 1); got != "analyst" {
		t.Errorf("single agent = %q; want %q", got, "analyst")
	}
	if got := FormatSubAgentDetail("analyst", 3); got != "analyst ×3" {
		t.Errorf("multi agent = %q; want %q", got, "analyst ×3")
	}
}

func TestRow_PulseReturnsFalseWhenIdle(t *testing.T) {
	r := newTestRow()
	if r.Pulse() {
		t.Error("Pulse() should return false when Idle")
	}
}

func TestRow_PulseReturnsTrueWhenBusy(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	if !r.Pulse() {
		t.Error("Pulse() should return true when busy")
	}
}

func TestRow_LabelTextThinking(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.renderLabel()
	if !strings.Contains(r.lastLabel, "Thinking") {
		t.Errorf("label = %q; want Thinking", r.lastLabel)
	}
}

func TestRow_LabelTextTool(t *testing.T) {
	r := newTestRow()
	r.SetState(Tool, "bash")
	r.renderLabel()
	if !strings.Contains(r.lastLabel, "Running bash") {
		t.Errorf("label = %q; want Running bash", r.lastLabel)
	}
}

func TestRow_LabelTextCompacting(t *testing.T) {
	r := newTestRow()
	r.SetState(Compacting, "12.3K→4.0K")
	r.renderLabel()
	if !strings.Contains(r.lastLabel, "Compacting 12.3K→4.0K") {
		t.Errorf("label = %q; want Compacting detail", r.lastLabel)
	}
}

func TestRow_LabelTextIdleBlank(t *testing.T) {
	r := newTestRow()
	r.SetState(Idle, "")
	r.renderLabel()
	if r.lastLabel != "" {
		t.Errorf("label = %q; want empty when Idle", r.lastLabel)
	}
}

func TestRow_LabelTextIdleEphemeral(t *testing.T) {
	r := newTestRow()
	r.SetState(Idle, "")
	r.SetEphemeral("Found at line 42")
	r.renderLabel()
	if !strings.Contains(r.lastLabel, "Found at line 42") {
		t.Errorf("label = %q; want ephemeral message", r.lastLabel)
	}
}

func TestRow_EphemeralClearedOnBusy(t *testing.T) {
	r := newTestRow()
	r.SetState(Idle, "")
	r.SetEphemeral("toast")
	r.SetState(Thinking, "")
	// Ephemeral is cleared when entering a non-Idle state
	r.renderLabel()
	if strings.Contains(r.lastLabel, "toast") {
		t.Error("ephemeral should be cleared when leaving Idle")
	}
}

func TestRow_ElapsedShown(t *testing.T) {
	r := newTestRow()
	r.SetState(Thinking, "")
	r.enteredAt = time.Now().Add(-5 * time.Second)
	r.renderLabel()
	if !strings.Contains(r.lastLabel, "5s") {
		t.Errorf("label = %q; want elapsed 5s", r.lastLabel)
	}
}

func TestRow_StateLabels(t *testing.T) {
	tests := []struct {
		state  State
		detail string
		want   string
	}{
		{Thinking, "", "Thinking…"},
		{Reasoning, "", "Reasoning"},
		{Responding, "", "Responding"},
		{Tool, "bash", "Running bash…"},
		{Tool, "", "Running tool…"},
		{SubAgent, "analyst", "Sub-agent analyst"},
		{SubAgent, "", "Sub-agent"},
		{Compacting, "12K→4K", "Compacting 12K→4K"},
		{Compacting, "", "Compacting…"},
		{Approving, "", "Awaiting approval…"},
		{Asking, "", "Awaiting input…"},
	}
	for _, tt := range tests {
		r := newTestRow()
		r.state = tt.state
		r.detail = tt.detail
		got := r.stateLabel()
		if got != tt.want {
			t.Errorf("stateLabel(%v, %q) = %q; want %q", tt.state, tt.detail, got, tt.want)
		}
	}
}
