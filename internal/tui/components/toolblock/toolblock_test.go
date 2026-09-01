package toolblock

import (
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

func TestIcon_Known(t *testing.T) {
	tests := []struct{ name, want string }{
		{"read", "📖"},
		{"write", "✍️"},
		{"bash", "💻"},
		{"grep", "🔍"},
		{"todowrite", "✅"},
		{"task", "🔗"},
	}
	for _, tc := range tests {
		if got := Icon(tc.name); got != tc.want {
			t.Errorf("Icon(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIcon_Unknown(t *testing.T) {
	if got := Icon("nonexistent_tool"); got != "🔧" {
		t.Errorf("Icon(unknown) = %q, want 🔧", got)
	}
}

func TestIcon_EmptyString(t *testing.T) {
	if got := Icon(""); got != "🔧" {
		t.Errorf("Icon(\"\") = %q, want 🔧", got)
	}
}

func TestNew_RunningState(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "read", `{"path":"/foo"}`, &th)
	if b.ID() != "t1" {
		t.Errorf("ID = %q, want t1", b.ID())
	}
	if b.Name() != "read" {
		t.Errorf("Name = %q, want read", b.Name())
	}
	if b.S() != Running {
		t.Errorf("new block should be Running, got %v", b.S())
	}
	if b.IsExpanded() {
		t.Error("new block should be collapsed")
	}
}

func TestComplete(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"ls"}`, &th)
	b.Complete("ls completed", "file1\nfile2")
	if b.S() != Done {
		t.Errorf("state should be Done, got %v", b.S())
	}
}

func TestFail(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"rm -rf /"}`, &th)
	b.Fail("permission denied", "access denied")
	if b.S() != Error {
		t.Errorf("state should be Error, got %v", b.S())
	}
}

func TestToggle(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "read", `{"path":"/foo"}`, &th)
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

func TestRender_RunningCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "read", `{"path":"/foo"}`, &th)
	out := b.Render()
	if !strings.Contains(out, "▶") {
		t.Errorf("running collapsed should contain ▶, got %q", out)
	}
	if !strings.Contains(out, "read") {
		t.Errorf("should contain tool name, got %q", out)
	}
	if !strings.Contains(out, `{"path":"/foo"}`) {
		t.Errorf("should contain args, got %q", out)
	}
}

func TestRender_DoneCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"ls"}`, &th)
	b.Complete("ls completed", "file1")
	out := b.Render()
	if !strings.Contains(out, "✓") {
		t.Errorf("done collapsed should contain ✓, got %q", out)
	}
	if strings.Contains(out, "▶") {
		t.Error("done collapsed should not contain ▶")
	}
}

func TestRender_ErrorCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"bad"}`, &th)
	b.Fail("failed", "error message")
	out := b.Render()
	if !strings.Contains(out, "✗") {
		t.Errorf("error collapsed should contain ✗, got %q", out)
	}
}

func TestRender_Expanded(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"ls"}`, &th)
	b.Complete("done", "output_line")
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "output_line") {
		t.Errorf("expanded should contain result, got %q", out)
	}
	if !strings.Contains(out, "Args:") {
		t.Errorf("expanded should show args section, got %q", out)
	}
	if !strings.Contains(out, "Duration:") {
		t.Errorf("expanded should show duration, got %q", out)
	}
}

func TestRender_ExpandedRunning(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"sleep 10"}`, &th)
	b.Toggle()
	out := b.Render()
	if strings.Contains(out, "Duration:") {
		t.Error("running expanded should not show Duration")
	}
}

func TestRender_ExpandedError(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"bad"}`, &th)
	b.Fail("failed", "the error text")
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "the error text") {
		t.Errorf("expanded error should contain error text, got %q", out)
	}
}

func TestDurationStr_SubSecond(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{}`, &th)
	time.Sleep(10 * time.Millisecond)
	b.Complete("done", "")
	if !strings.Contains(b.durationStr(), "ms") {
		t.Errorf("sub-second duration should show ms, got %q", b.durationStr())
	}
}

func TestDurationStr_Second(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{}`, &th)
	b.startTime = time.Now().Add(-2 * time.Second)
	b.Complete("done", "")
	if !strings.Contains(b.durationStr(), "s") {
		t.Errorf("second duration should show s, got %q", b.durationStr())
	}
}

func TestRenderCtx_Width(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", `{"command":"ls"}`, &th)
	b.Complete("done", "output")
	b.Toggle()
	wide := b.RenderCtx(colors.RenderCtx{Width: 120, Theme: &th})
	narrow := b.RenderCtx(colors.RenderCtx{Width: 40, Theme: &th})
	if len(wide) <= len(narrow) {
		t.Error("wider context should produce more dashes")
	}
}

func TestRender_EmptyArgs(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("t1", "bash", "", &th)
	b.Complete("done", "result")
	b.Toggle()
	out := b.Render()
	if strings.Contains(out, "Args:") {
		t.Error("expanded should not show Args section when args empty")
	}
}

// TestRender_EndsWithFullReset pins the dim-attribute leak fix: the
// collapsed line opens a dim attribute via DimTag and must close with the
// full reset [-:-:-]. The short form [-] resets only the foreground color
// in tview, leaving dim active for all following text in the buffer.
func TestRender_EndsWithFullReset(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Block)
	}{
		{"running", func(b *Block) {}},
		{"done", func(b *Block) { b.Complete("ok", "") }},
		{"error", func(b *Block) { b.Fail("fail", "boom") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th := colors.NewDarkTheme()
			b := New("t1", "bash", `{"command":"ls"}`, &th)
			tc.mutate(b)
			out := b.Render()
			if !strings.HasSuffix(out, "[-:-:-]") {
				t.Errorf("collapsed render should end with full reset, got %q", out)
			}
		})
	}
}
