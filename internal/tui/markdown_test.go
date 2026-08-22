package tui

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

func TestRenderMarkdown_Empty(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("", 80, &th)
	if out != "" {
		t.Errorf("empty markdown should return empty, got %q", out)
	}
}

func TestRenderMarkdown_PlainText(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("hello world", 80, &th)
	if !strings.Contains(out, "hello world") {
		t.Errorf("should contain plain text, got %q", out)
	}
}

func TestRenderMarkdown_Width(t *testing.T) {
	th := colors.NewDarkTheme()
	narrow := renderMarkdown("hello world this is a long line of text", 20, &th)
	wide := renderMarkdown("hello world this is a long line of text", 80, &th)
	if len(narrow) == 0 || len(wide) == 0 {
		t.Fatal("both should produce output")
	}
}

func TestRenderMarkdown_ZeroWidth(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("hello", 0, &th)
	if len(out) == 0 {
		t.Error("zero width should fall back to default")
	}
}

func TestRenderMarkdown_NegativeWidth(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("hello", -10, &th)
	if len(out) == 0 {
		t.Error("negative width should produce output")
	}
}

func TestRenderMarkdown_CodeBlock(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("```go\nfmt.Println(\"hi\")\n```", 80, &th)
	if !strings.Contains(out, "fmt.Println") {
		t.Errorf("should contain code, got %q", out)
	}
}

func TestRenderMarkdown_Heading(t *testing.T) {
	th := colors.NewDarkTheme()
	out := renderMarkdown("# Title", 80, &th)
	if !strings.Contains(out, "Title") {
		t.Errorf("should contain heading text, got %q", out)
	}
}

func TestMessageWidth_Nil(t *testing.T) {
	if got := messageWidth(nil); got != 80 {
		t.Errorf("messageWidth(nil) = %d, want 80", got)
	}
}
