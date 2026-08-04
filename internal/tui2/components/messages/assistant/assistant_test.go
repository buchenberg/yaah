package assistant

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	msg := "Hello, world!"
	got := Render(msg)
	if !strings.Contains(got, "Yaah:") {
		t.Errorf("Render() should contain 'Yaah:' prefix, got %q", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("Render() should contain message text, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Render() should end with newline, got %q", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	got := Render("")
	if !strings.Contains(got, "Yaah:") {
		t.Errorf("Render() with empty string should still contain 'Yaah:' prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Render() should end with newline, got %q", got)
	}
}

func TestRenderMarkdown(t *testing.T) {
	msg := "**bold** and `code`"
	got := Render(msg)
	if !strings.Contains(got, "bold") {
		t.Errorf("Render() should contain markdown text, got %q", got)
	}
	if !strings.Contains(got, "code") {
		t.Errorf("Render() should contain markdown text, got %q", got)
	}
}
