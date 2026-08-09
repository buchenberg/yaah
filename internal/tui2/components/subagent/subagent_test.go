package subagent

import (
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

func TestNew_ActiveState(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "Go expert", "fix the bug", "gpt-4", &th)
	if b.ID() != "sa-1" {
		t.Errorf("ID = %q, want sa-1", b.ID())
	}
	if b.Role() != "developer" {
		t.Errorf("Role = %q, want developer", b.Role())
	}
	if b.S() != Active {
		t.Errorf("new block should be Active, got %v", b.S())
	}
	if b.Task() != "fix the bug" {
		t.Errorf("Task = %q, want 'fix the bug'", b.Task())
	}
	if b.DisplayName() == "" {
		t.Error("DisplayName should not be empty")
	}
	if b.IsExpanded() {
		t.Error("new block should be collapsed")
	}
}

func TestNew_DisplayName(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	name := b.DisplayName()
	if !strings.Contains(name, "developer") && !strings.Contains(name, "Developer") {
		t.Errorf("DisplayName should contain role, got %q", name)
	}
}

func TestComplete(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.Complete()
	if b.S() != Done {
		t.Errorf("should be Done after Complete, got %v", b.S())
	}
}

func TestFail(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.Fail("context canceled")
	if b.S() != Error {
		t.Errorf("should be Error after Fail, got %v", b.S())
	}
}

func TestToggle(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	if b.IsExpanded() {
		t.Error("should start collapsed")
	}
	b.Toggle()
	if !b.IsExpanded() {
		t.Error("should be expanded after toggle")
	}
	b.Toggle()
	if b.IsExpanded() {
		t.Error("should be collapsed after second toggle")
	}
}

func TestToggleBlink(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	if !b.blinkVisible {
		t.Error("should start with blink visible")
	}
	b.ToggleBlink()
	if b.blinkVisible {
		t.Error("should not be visible after toggle")
	}
	b.ToggleBlink()
	if !b.blinkVisible {
		t.Error("should be visible after second toggle")
	}
}

func TestAdvanceSpinner_Active(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	initial := b.spinnerFrame
	b.AdvanceSpinner()
	if b.spinnerFrame == initial {
		t.Error("spinner frame should change on active block")
	}
}

func TestAdvanceSpinner_Done(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.Complete()
	initial := b.spinnerFrame
	b.AdvanceSpinner()
	if b.spinnerFrame != initial {
		t.Error("spinner frame should not change on done block")
	}
}

func TestElapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.startTime = time.Now().Add(-1 * time.Second)
	elapsed := b.Elapsed()
	if elapsed < 900*time.Millisecond || elapsed > 1100*time.Millisecond {
		t.Errorf("Elapsed ~1s, got %v", elapsed)
	}
}

func TestRender_ActiveCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "fix the bug", "gpt-4", &th)
	out := b.Render()
	if !strings.Contains(out, "fix the bug") {
		t.Errorf("active collapsed should contain task, got %q", out)
	}
	if !strings.Contains(out, "🤖") || !strings.Contains(out, " ") {
		// should have robot icon (blink visible)
	}
}

func TestRender_DoneCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "fix the bug", "gpt-4", &th)
	b.Complete()
	out := b.Render()
	if !strings.Contains(out, "✓") {
		t.Errorf("done collapsed should contain ✓, got %q", out)
	}
}

func TestRender_ErrorCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "fix the bug", "gpt-4", &th)
	b.Fail("context canceled")
	out := b.Render()
	if !strings.Contains(out, "✗") {
		t.Errorf("error collapsed should contain ✗, got %q", out)
	}
}

func TestRender_Expanded(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "Go systems", "fix the bug", "gpt-4", &th)
	b.Complete()
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "Go systems") {
		t.Errorf("expanded should show specialty, got %q", out)
	}
	if !strings.Contains(out, "fix the bug") {
		t.Errorf("expanded should show task, got %q", out)
	}
	if !strings.Contains(out, "gpt-4") {
		t.Errorf("expanded should show model, got %q", out)
	}
	if !strings.Contains(out, "Duration:") {
		t.Errorf("expanded should show duration, got %q", out)
	}
}

func TestRender_ExpandedError(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.Fail("context canceled")
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "context canceled") {
		t.Errorf("expanded error should show error text, got %q", out)
	}
}

func TestDurationStr(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.startTime = time.Now().Add(-1500 * time.Millisecond)
	b.Complete()
	if !strings.Contains(b.durationStr(), "s") {
		t.Errorf("duration should be in seconds, got %q", b.durationStr())
	}
}

func TestRobot_BlinkOff(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.ToggleBlink()
	if b.robot() != " " {
		t.Errorf("robot with blink off should be space, got %q", b.robot())
	}
}

func TestRobot_Done(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("sa-1", "developer", "", "task", "gpt-4", &th)
	b.Complete()
	if b.robot() != "🤖" {
		t.Errorf("robot when done should be 🤖, got %q", b.robot())
	}
}
