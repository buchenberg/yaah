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
			b := subagent.New("sa-1", "developer", "", "task", "gpt-4", &th)
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
	b := subagent.New("sa-1", "developer", "", "fix the bug", "gpt-4", &th)
	out := Format([]*subagent.Block{b}, &th)
	if !strings.Contains(out, "fix the bug") {
		t.Errorf("should contain task, got %q", out)
	}
	if !strings.Contains(out, "🤖") {
		t.Errorf("should contain robot icon, got %q", out)
	}
}

func TestFormat_MultipleRunning(t *testing.T) {
	th := colors.NewDarkTheme()
	b1 := subagent.New("sa-1", "developer", "", "fix bug A", "gpt-4", &th)
	b2 := subagent.New("sa-2", "tester", "", "test feature", "gpt-4", &th)
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
	b1 := subagent.New("sa-1", "developer", "", "running task", "gpt-4", &th)
	b2 := subagent.New("sa-2", "tester", "", "done task", "gpt-4", &th)
	b2.Complete()
	b3 := subagent.New("sa-3", "checker", "", "error task", "gpt-4", &th)
	b3.Fail("oops")
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
	b := subagent.New("sa-1", "developer", "", "fix bug", "gpt-4", &th)
	out := Format([]*subagent.Block{b}, &th)
	name := b.DisplayName()
	if !strings.Contains(out, name) {
		t.Errorf("should contain DisplayName %q, got %q", name, out)
	}
}
