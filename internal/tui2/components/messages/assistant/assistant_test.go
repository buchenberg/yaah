package assistant

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	msg := "Hello, world!"
	got := Render(msg)
	if !strings.Contains(got, "Hello, world!") {
		t.Errorf("Render() should contain message text, got %q", got)
	}
	if !strings.Contains(got, "Yaah:") {
		t.Errorf("Render() should contain 'Yaah:' prefix, got %q", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	got := Render("")
	if !strings.Contains(got, "Yaah:") {
		t.Errorf("Render() with empty string should still contain 'Yaah:' prefix, got %q", got)
	}
}

func TestRenderMarkdown(t *testing.T) {
	msg := "**bold** and `code`"
	got := Render(msg)
	if !strings.Contains(got, "**bold** and `code`") {
		t.Errorf("Render() should preserve markdown text, got %q", got)
	}
}
