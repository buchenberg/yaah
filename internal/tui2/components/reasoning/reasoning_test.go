package reasoning

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/lolcat"
)

func TestNew(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "some reasoning", 0, &th)
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.ID() != "r1" {
		t.Errorf("ID = %q, want %q", b.ID(), "r1")
	}
	if b.IsExpanded() {
		t.Error("new block should be collapsed")
	}
}

func TestToggle(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "reasoning", 0, &th)
	if b.IsExpanded() {
		t.Error("should start collapsed")
	}
	b.Toggle()
	if !b.IsExpanded() {
		t.Error("should be expanded after Toggle")
	}
	b.Toggle()
	if b.IsExpanded() {
		t.Error("should be collapsed after second Toggle")
	}
}

func TestSetSeed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "reasoning", 0, &th)
	b.SetSeed(5)
	if b.seed != 5 {
		t.Errorf("seed = %v, want 5", b.seed)
	}
}

func TestRenderCollapsed(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "some reasoning", 0, &th)
	out := b.Render()
	if !strings.Contains(out, "▶") {
		t.Errorf("collapsed render should contain ▶, got %q", out)
	}
	plain := lolcat.StripTags(out)
	if !strings.Contains(plain, "Reasoning") {
		t.Errorf("collapsed render should contain 'Reasoning', got %q", plain)
	}
	if strings.Contains(plain, "some reasoning") {
		t.Error("collapsed render should not contain content")
	}
}

func TestRenderExpanded(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "the model thought about this", 0, &th)
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "▼") {
		t.Errorf("expanded render should contain ▼, got %q", out)
	}
	if !strings.Contains(out, "the model thought about this") {
		t.Errorf("expanded render should contain content, got %q", out)
	}
}

func TestRenderCtx_WithWidth(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "content", 0, &th)
	b.Toggle()
	out := b.RenderCtx(colors.RenderCtx{Width: 120, Theme: &th})
	if len(out) == 0 {
		t.Error("RenderCtx with width should produce output")
	}
}

func TestRenderCtx_ZeroWidth(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "content", 0, &th)
	b.Toggle()
	out := b.RenderCtx(colors.RenderCtx{Width: 0, Theme: &th})
	if len(out) == 0 {
		t.Error("RenderCtx with zero width should fall back to default")
	}
}

func TestRenderCtx_NegativeWidth(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "content", 0, &th)
	b.Toggle()
	out := b.RenderCtx(colors.RenderCtx{Width: -10, Theme: &th})
	if len(out) == 0 {
		t.Error("RenderCtx with negative width should fall back to default")
	}
}

func TestNoColor_ReasoningLabel(t *testing.T) {
	th := colors.NewDarkTheme()
	th.NoColor = true
	b := New("r1", "reasoning", 0, &th)
	out := b.Render()
	if strings.Contains(out, "[#") {
		t.Errorf("NoColor render should not contain hex tags, got %q", out)
	}
}

func TestExpandedContent_Multiline(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "line1\nline2\nline3", 0, &th)
	b.Toggle()
	out := b.Render()
	if !strings.Contains(out, "line1") {
		t.Error("should contain line1")
	}
	if !strings.Contains(out, "line2") {
		t.Error("should contain line2")
	}
	if !strings.Contains(out, "line3") {
		t.Error("should contain line3")
	}
}

func TestRender_RoundTripsCollapsedToExpanded(t *testing.T) {
	th := colors.NewDarkTheme()
	b := New("r1", "content", 0, &th)
	collapsed := b.Render()
	b.Toggle()
	expanded := b.Render()
	b.Toggle()
	collapsed2 := b.Render()
	if collapsed != collapsed2 {
		t.Error("collapsed render should be idempotent after toggle round-trip")
	}
	if collapsed == expanded {
		t.Error("collapsed and expanded renders should differ")
	}
}
