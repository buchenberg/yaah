package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/rivo/uniseg"
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

// TestRenderMarkdown_TableAlignsWithEmojiPresentation pins the width contract
// between yaah and tviewmd: a table column holding an emoji presentation
// sequence (base rune + U+FE0F) must render at the same width as a plain-text
// column of the same shape. U+270D U+FE0F is a single grapheme cluster that
// tview paints in two cells while go-runewidth measures it as one, so a
// renderer that measures per rune reserves one cell too few and every affected
// row overruns its separator (bead yaah-vdr, tviewmd v0.2.1).
func TestRenderMarkdown_TableAlignsWithEmojiPresentation(t *testing.T) {
	th := colors.NewDarkTheme()

	const (
		plain = "| mark | kind |\n|------|------|\n| note | note |\n| todo | todo |\n"
		emoji = "| mark | kind |\n|------|------|\n| \u270d\ufe0f | note |\n| \u270d\ufe0f | todo |\n"
	)

	wantOut := renderMarkdown(plain, 80, &th)
	gotOut := renderMarkdown(emoji, 80, &th)
	want := renderedRowWidths(wantOut)
	got := renderedRowWidths(gotOut)

	if len(want) != len(got) {
		t.Fatalf("emoji table rendered %d rows, plain table rendered %d\nemoji:\n%s",
			len(got), len(want), gotOut)
	}

	// Both tables are also held to one absolute width — two cell-widths and the
	// single space between columns, taken from the width oracle rather than
	// written out — so a regression that shifts every row equally cannot hide
	// behind the comparison below.
	wantCols := uniseg.StringWidth("mark") + 1 + uniseg.StringWidth("kind")
	for row := range want {
		if want[row] != wantCols {
			t.Errorf("plain table row %d is %d cells wide, want %d", row, want[row], wantCols)
		}
		if got[row] != want[row] {
			t.Errorf("emoji table row %d is %d cells wide, plain table row is %d (ragged borders)\nemoji:\n%s",
				row, got[row], want[row], gotOut)
		}
	}
}

// renderedRowWidths strips tview colour tags and measures each rendered line in
// display cells — the width the terminal actually paints. Blank lines (the empty
// tail after a trailing newline) are dropped so every entry is a real row.
//
// The tag pattern is deliberately naive: fixtures in this file contain no literal
// brackets, which tview would render as an escape such as ["["] and which this
// strip would then remove. Adding a bracketed literal to a fixture means teaching
// this helper about escapes first.
func renderedRowWidths(rendered string) []int {
	tag := regexp.MustCompile(`\[[^\]]*\]`)
	var widths []int
	for _, line := range strings.Split(rendered, "\n") {
		plain := tag.ReplaceAllString(line, "")
		if strings.TrimSpace(plain) == "" {
			continue
		}
		widths = append(widths, uniseg.StringWidth(plain))
	}
	return widths
}
