package colors

import (
	"os"
	"testing"
)

func TestNewDarkTheme(t *testing.T) {
	th := NewDarkTheme()
	if th.Heading == "" {
		t.Error("dark theme should have Heading")
	}
	if th.User == "" {
		t.Error("dark theme should have User")
	}
	if th.Error == "" {
		t.Error("dark theme should have Error")
	}
	if th.NoColor {
		t.Error("dark theme NoColor should default to false")
	}
	if len(th.ToolColors) == 0 {
		t.Error("dark theme should have ToolColors")
	}
	if len(th.RoleColors) == 0 {
		t.Error("dark theme should have RoleColors")
	}
}

func TestNewLightTheme(t *testing.T) {
	th := NewLightTheme()
	if th.Heading == "" {
		t.Error("light theme should have Heading")
	}
	if th.NoColor {
		t.Error("light theme NoColor should default to false")
	}
}

func TestDetectTheme_DefaultDark(t *testing.T) {
	os.Unsetenv("YAARH_THEME")
	os.Unsetenv("NO_COLOR")
	th := DetectTheme()
	if th.NoColor {
		t.Error("default theme should not have NoColor set")
	}
}

func TestDetectTheme_Light(t *testing.T) {
	t.Setenv("YAARH_THEME", "light")
	th := DetectTheme()
	if th.Heading != "#005faf" {
		t.Errorf("light theme heading should be #005faf, got %s", th.Heading)
	}
}

func TestDetectTheme_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := DetectTheme()
	if !th.NoColor {
		t.Error("NO_COLOR=1 should set NoColor to true")
	}
}

func TestDetectTheme_DarkExplicit(t *testing.T) {
	t.Setenv("YAARH_THEME", "dark")
	th := DetectTheme()
	if th.NoColor {
		t.Error("explicit dark theme should not have NoColor")
	}
}

func TestDetectTheme_UnknownDefaultsDark(t *testing.T) {
	t.Setenv("YAARH_THEME", "unicorn")
	th := DetectTheme()
	if th.Heading != "#00ffff" {
		t.Errorf("unknown theme should default to dark, got heading %s", th.Heading)
	}
}

func TestToolHex_Known(t *testing.T) {
	th := NewDarkTheme()
	if th.ToolHex("read") != "#00ffff" {
		t.Errorf("read should be #00ffff, got %s", th.ToolHex("read"))
	}
	if th.ToolHex("bash") != "#00ff88" {
		t.Errorf("bash should be #00ff88, got %s", th.ToolHex("bash"))
	}
}

func TestToolHex_Unknown(t *testing.T) {
	th := NewDarkTheme()
	if th.ToolHex("nonexistent") != th.Dim {
		t.Errorf("unknown tool should return Dim (%s), got %s", th.Dim, th.ToolHex("nonexistent"))
	}
}

func TestRoleHex_Known(t *testing.T) {
	th := NewDarkTheme()
	if th.RoleHex("developer") != "#00ff88" {
		t.Errorf("developer should be #00ff88, got %s", th.RoleHex("developer"))
	}
	if th.RoleHex("checker") != "#ffffff" {
		t.Errorf("checker should be #ffffff, got %s", th.RoleHex("checker"))
	}
}

func TestRoleHex_Unknown(t *testing.T) {
	th := NewDarkTheme()
	if th.RoleHex("astronaut") != th.Heading {
		t.Errorf("unknown role should return Heading (%s), got %s", th.Heading, th.RoleHex("astronaut"))
	}
}

func TestTag_WithColor(t *testing.T) {
	th := NewDarkTheme()
	result := th.Tag("#ff0000", "hello")
	expected := "[#ff0000]hello[-]"
	if result != expected {
		t.Errorf("Tag: got %q, want %q", result, expected)
	}
}

func TestTag_NoColor(t *testing.T) {
	th := NewDarkTheme()
	th.NoColor = true
	result := th.Tag("#ff0000", "hello")
	if result != "hello" {
		t.Errorf("Tag with NoColor: got %q, want %q", result, "hello")
	}
}

func TestTagBold_WithColor(t *testing.T) {
	th := NewDarkTheme()
	result := th.TagBold("#ff0000", "bold")
	expected := "[#ff0000::b]bold[-:-:-]"
	if result != expected {
		t.Errorf("TagBold: got %q, want %q", result, expected)
	}
}

func TestTagBold_NoColor(t *testing.T) {
	th := NewDarkTheme()
	th.NoColor = true
	result := th.TagBold("#ff0000", "bold")
	if result != "bold" {
		t.Errorf("TagBold with NoColor: got %q, want %q", result, "bold")
	}
}

func TestDimTag(t *testing.T) {
	th := NewDarkTheme()
	result := th.DimTag()
	if result != "[#888888::d]" {
		t.Errorf("DimTag: got %q, want %q", result, "[#888888::d]")
	}
}

func TestDimTag_NoColor(t *testing.T) {
	th := NewDarkTheme()
	th.NoColor = true
	result := th.DimTag()
	if result != "" {
		t.Errorf("DimTag with NoColor should be empty, got %q", result)
	}
}

func TestSecondaryTag(t *testing.T) {
	th := NewDarkTheme()
	result := th.SecondaryTag()
	if result != "[#9988bb]" {
		t.Errorf("SecondaryTag: got %q, want %q", result, "[#9988bb]")
	}
}

func TestColorTag(t *testing.T) {
	th := NewDarkTheme()
	result := th.ColorTag("#abcdef")
	if result != "[#abcdef]" {
		t.Errorf("ColorTag: got %q, want %q", result, "[#abcdef]")
	}
}

func TestColorTag_NoColor(t *testing.T) {
	th := NewDarkTheme()
	th.NoColor = true
	result := th.ColorTag("#abcdef")
	if result != "" {
		t.Errorf("ColorTag with NoColor should be empty, got %q", result)
	}
}

func TestResetTag(t *testing.T) {
	th := NewDarkTheme()
	if th.ResetTag() != "[-]" {
		t.Errorf("ResetTag: got %q, want %q", th.ResetTag(), "[-]")
	}
}

func TestResetTag_NoColor(t *testing.T) {
	th := NewDarkTheme()
	th.NoColor = true
	if th.ResetTag() != "" {
		t.Errorf("ResetTag with NoColor should be empty, got %q", th.ResetTag())
	}
}

func TestTag_EmptyText(t *testing.T) {
	th := NewDarkTheme()
	result := th.Tag("#ff0000", "")
	expected := "[#ff0000][-]"
	if result != expected {
		t.Errorf("Tag with empty text: got %q, want %q", result, expected)
	}
}

func TestToolColors_AllReturnValidHex(t *testing.T) {
	th := NewDarkTheme()
	for name := range th.ToolColors {
		hex := th.ToolHex(name)
		if hex == "" {
			t.Errorf("ToolHex(%q) returned empty string", name)
		}
	}
}

func TestRoleColors_AllReturnValidHex(t *testing.T) {
	th := NewDarkTheme()
	for role := range th.RoleColors {
		hex := th.RoleHex(role)
		if hex == "" {
			t.Errorf("RoleHex(%q) returned empty string", role)
		}
	}
}
