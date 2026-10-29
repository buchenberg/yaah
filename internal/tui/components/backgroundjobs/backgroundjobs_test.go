package backgroundjobs

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/subagent"
)

func TestFormat_Empty(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(nil, &th)
	if out != "" {
		t.Errorf("empty blocks should return empty, got %q", out)
	}
}

func TestFormat_NoRunning(t *testing.T) {
	th := colors.NewDarkTheme()
	blocks := []*subagent.Block{
		func() *subagent.Block {
			b := subagent.New("sa-1", "developer", "task")
			b.Complete()
			return b
		}(),
	}
	out := Format(blocks, &th)
	if out != "" {
		t.Errorf("completed blocks should return empty, got %q", out)
	}
}

func TestFormat_OneRunning(t *testing.T) {
	th := colors.NewDarkTheme()
	b := subagent.New("sa-1", "developer", "fix the bug")
	out := Format([]*subagent.Block{b}, &th)
	if !strings.Contains(out, "fix the bug") {
		t.Errorf("should contain task, got %q", out)
	}
	if !strings.Contains(out, b.SpinnerFrame()) {
		t.Errorf("should contain the active spinner frame %q, got %q", b.SpinnerFrame(), out)
	}
}

func TestFormat_MultipleRunning(t *testing.T) {
	th := colors.NewDarkTheme()
	b1 := subagent.New("sa-1", "developer", "fix bug A")
	b2 := subagent.New("sa-2", "tester", "test feature")
	out := Format([]*subagent.Block{b1, b2}, &th)
	if !strings.Contains(out, "fix bug A") {
		t.Error("should contain first task")
	}
	if !strings.Contains(out, "test feature") {
		t.Error("should contain second task")
	}
}

func TestFormat_MixedStates(t *testing.T) {
	th := colors.NewDarkTheme()
	b1 := subagent.New("sa-1", "developer", "running task")
	b2 := subagent.New("sa-2", "tester", "done task")
	b2.Complete()
	b3 := subagent.New("sa-3", "checker", "error task")
	b3.Fail()
	out := Format([]*subagent.Block{b1, b2, b3}, &th)
	if !strings.Contains(out, "running task") {
		t.Error("should contain running task")
	}
	if strings.Contains(out, "done task") {
		t.Error("should not contain completed task")
	}
	if strings.Contains(out, "error task") {
		t.Error("should not contain errored task")
	}
}

func TestFormat_ContainsDisplayName(t *testing.T) {
	th := colors.NewDarkTheme()
	b := subagent.New("sa-1", "developer", "fix bug")
	out := Format([]*subagent.Block{b}, &th)
	name := b.DisplayName()
	if !strings.Contains(out, name) {
		t.Errorf("should contain DisplayName %q, got %q", name, out)
	}
}

func TestFormat_AdvancesWithSpinner(t *testing.T) {
	th := colors.NewDarkTheme()
	b := subagent.New("sa-1", "developer", "fix bug")
	first := Format([]*subagent.Block{b}, &th)
	b.AdvanceSpinner()
	second := Format([]*subagent.Block{b}, &th)
	if first == second {
		t.Errorf("pane output did not change after advancing the spinner: %q", first)
	}
}

// TestFormat_WorldLabel pins the per-dispatch world on the row: the label the
// start event carries ("worktree", a degraded form) renders beside the role.
func TestFormat_WorldLabel(t *testing.T) {
	th := colors.NewDarkTheme()
	b := subagent.New("sa-1", "developer", "fix the bug")
	b.SetWorld("worktree")
	out := Format([]*subagent.Block{b}, &th)
	if !strings.Contains(out, "worktree") {
		t.Fatalf("world label not rendered: %q", out)
	}
}
