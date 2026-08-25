package messages

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui/components/subagent"
	"github.com/buchenberg/yaah/internal/tui/components/toolblock"
	"github.com/buchenberg/yaah/internal/tui/lolcat"
)

func TestFormat_Empty(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(nil, Content{Width: 80, Theme: &th})
	if out != "" {
		t.Errorf("empty items should produce empty string, got %q", out)
	}
}

func TestFormat_TextOnly(t *testing.T) {
	th := colors.NewDarkTheme()
	items := []Item{
		{Text: "Hello, world"},
		{Text: "Second message"},
	}
	out := Format(items, Content{Width: 80, Theme: &th})
	if !strings.Contains(out, "Hello, world") {
		t.Errorf("should contain first text, got %q", out)
	}
	if !strings.Contains(out, "Second message") {
		t.Errorf("should contain second text, got %q", out)
	}
}

func TestFormat_ToolBlock(t *testing.T) {
	th := colors.NewDarkTheme()
	tb := toolblock.New("t1", "bash", `{"command":"ls"}`, &th)
	tb.Complete("done", "output")
	items := []Item{{ToolBlock: tb}}
	out := Format(items, Content{Width: 80, Theme: &th})
	if !strings.Contains(out, "bash") {
		t.Errorf("should contain tool name, got %q", out)
	}
}

func TestFormat_SubAgentBlock(t *testing.T) {
	th := colors.NewDarkTheme()
	sb := subagent.New("sa-1", "developer", "", "fix the bug", "gpt-4", &th)
	items := []Item{{SubBlock: sb}}
	out := Format(items, Content{Width: 80, Theme: &th})
	if !strings.Contains(out, "fix the bug") {
		t.Errorf("should contain sub-agent task, got %q", out)
	}
}

func TestFormat_ReasoningBlock(t *testing.T) {
	th := colors.NewDarkTheme()
	rb := reasoning.New("r1", "model thoughts", 0, &th)
	items := []Item{{ReasBlock: rb}}
	out := Format(items, Content{Width: 80, Theme: &th})
	plain := lolcat.StripTags(out)
	if !strings.Contains(plain, "Reasoning") {
		t.Errorf("should contain reasoning label, got %q", plain)
	}
}

func TestFormat_MixedBlocks(t *testing.T) {
	th := colors.NewDarkTheme()
	tb := toolblock.New("t1", "read", `{"path":"/foo"}`, &th)
	tb.Complete("done", "content")
	sb := subagent.New("sa-1", "developer", "", "fix bug", "gpt-4", &th)
	rb := reasoning.New("r1", "thoughts", 0, &th)

	items := []Item{
		{Text: "User: do something"},
		{ToolBlock: tb},
		{SubBlock: sb},
		{ReasBlock: rb},
	}
	out := Format(items, Content{Width: 80, Theme: &th})
	plain := lolcat.StripTags(out)
	if !strings.Contains(out, "User: do something") {
		t.Error("should contain user text")
	}
	if !strings.Contains(out, "read") {
		t.Error("should contain tool")
	}
	if !strings.Contains(out, "fix bug") {
		t.Error("should contain sub-agent")
	}
	if !strings.Contains(plain, "Reasoning") {
		t.Error("should contain reasoning")
	}
}

func TestFormat_WidthPropagation(t *testing.T) {
	th := colors.NewDarkTheme()
	tb := toolblock.New("t1", "bash", `{"command":"ls"}`, &th)
	tb.Complete("done", "out")
	tb.Toggle()
	wide := Format([]Item{{ToolBlock: tb}}, Content{Width: 120, Theme: &th})
	narrow := Format([]Item{{ToolBlock: tb}}, Content{Width: 40, Theme: &th})
	if len(wide) <= len(narrow) {
		t.Error("wider context should produce longer output")
	}
}
