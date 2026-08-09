package sessioninfo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

func TestFormat(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(Info{Provider: "openai", Model: "gpt-4", Version: "1.2.3"}, &th)
	if !strings.Contains(out, "openai") {
		t.Errorf("should contain provider, got %q", out)
	}
	if !strings.Contains(out, "gpt-4") {
		t.Errorf("should contain model, got %q", out)
	}
}

func TestFormat_Version(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(Info{Provider: "openai", Model: "gpt-4", Version: "1.2.3"}, &th)
	if !strings.Contains(out, "Agent:") {
		t.Errorf("should contain Agent label, got %q", out)
	}
}

func TestFormat_EmptyFields(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(Info{}, &th)
	if out == "" {
		t.Error("should still produce output with empty fields")
	}
}

func TestShortVersion_Plain(t *testing.T) {
	if got := shortVersion("1.2.3"); got != "1.2.3" {
		t.Errorf("shortVersion(1.2.3) = %q, want 1.2.3", got)
	}
}

func TestShortVersion_WithCommit(t *testing.T) {
	if got := shortVersion("1.2.3-abc1234"); got != "1.2.3" {
		t.Errorf("shortVersion should strip commit suffix, got %q", got)
	}
}

func TestShortVersion_Empty(t *testing.T) {
	if got := shortVersion(""); got != "" {
		t.Errorf("shortVersion(\"\") = %q, want empty", got)
	}
}

func TestShortVersion_MultipleDashes(t *testing.T) {
	if got := shortVersion("1.2.3-beta-1-abc"); got != "1.2.3" {
		t.Errorf("shortVersion should strip at first dash, got %q", got)
	}
}
