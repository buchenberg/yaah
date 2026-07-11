package repl

import (
	"os"
	"strings"
	"testing"
)

func TestColorRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	InitNoColor()
	// When NO_COLOR is set, all color functions should return plain strings
	if got := Bold("hello"); got != "hello" {
		t.Errorf("Bold with NO_COLOR = %q, want %q", got, "hello")
	}
	if got := Dim("world"); got != "world" {
		t.Errorf("Dim with NO_COLOR = %q, want %q", got, "world")
	}
}

func TestColorNoEnvReturnsANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	InitNoColor()
	// When NO_COLOR is unset, color functions should include ANSI codes
	got := Bold("hello")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("Bold without NO_COLOR = %q, expected ANSI escape", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("Bold output lost the text: %q", got)
	}
}

func TestBannerContainsVersion(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	banner := Banner("1.2.3")
	if !strings.Contains(banner, "1.2.3") {
		t.Errorf("banner missing version: %q", banner)
	}
	if !strings.Contains(banner, "yaah") {
		t.Errorf("banner missing 'yaah': %q", banner)
	}
}

func TestPromptReturnsPromptString(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	p := Prompt()
	if !strings.Contains(p, "yaah") {
		t.Errorf("prompt missing 'yaah': %q", p)
	}
}

func TestInitNoColorFromEnv(t *testing.T) {
	// When NO_COLOR is unset → useColor should be true
	os.Unsetenv("NO_COLOR")
	InitNoColor()
	if !useColor {
		t.Errorf("useColor should be true when NO_COLOR is unset")
	}

	// When NO_COLOR=1 → useColor should be false
	t.Setenv("NO_COLOR", "1")
	InitNoColor()
	if useColor {
		t.Errorf("useColor should be false when NO_COLOR is set")
	}

	// When NO_COLOR="" (empty) → useColor should still be true
	t.Setenv("NO_COLOR", "")
	InitNoColor()
	if !useColor {
		t.Errorf("useColor should be true when NO_COLOR is empty")
	}
}
