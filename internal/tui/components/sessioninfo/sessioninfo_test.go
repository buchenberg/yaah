package sessioninfo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui/colors"
)

func TestFormat(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(Info{Provider: "openai", Model: "gpt-4", Status: "idle"}, &th)
	if !strings.Contains(out, "openai") {
		t.Errorf("should contain provider, got %q", out)
	}
	if !strings.Contains(out, "gpt-4") {
		t.Errorf("should contain model, got %q", out)
	}
	if strings.Contains(out, "Version:") {
		t.Errorf("should not contain a Version row (promoted to header), got %q", out)
	}
}

func TestFormat_EmptyFields(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(Info{}, &th)
	if out == "" {
		t.Error("should still produce output with empty fields")
	}
}

func TestHeader_Release(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Header("v1.2.3-9-gc9023a91", &th)
	if !strings.Contains(out, "Yaah v1.2.3") {
		t.Errorf("header = %q, want Yaah v1.2.3", out)
	}
	if strings.Contains(out, "dev") {
		t.Errorf("release header should not say dev, got %q", out)
	}
}

func TestHeader_NoPrefix(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Header("1.2.3", &th)
	if !strings.Contains(out, "Yaah v1.2.3") {
		t.Errorf("header = %q, want Yaah v1.2.3", out)
	}
}

func TestHeader_Dev(t *testing.T) {
	th := colors.NewDarkTheme()
	for _, v := range []string{"", "0.0.0", "dev", "(devel)"} {
		out := Header(v, &th)
		if !strings.Contains(out, "Yaah (dev)") {
			t.Errorf("Header(%q) = %q, want Yaah (dev)", v, out)
		}
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
