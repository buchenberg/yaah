package contextinfo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

func TestFormat_WithWindow(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(50000, 128000, &th)
	if !strings.Contains(out, "128000") {
		t.Errorf("should contain window size, got %q", out)
	}
	if !strings.Contains(out, "50000") {
		t.Errorf("should contain token count, got %q", out)
	}
}

func TestFormat_NoWindow(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(50000, 0, &th)
	if !strings.Contains(out, "─") {
		t.Errorf("should show dash when window unknown, got %q", out)
	}
	if strings.Contains(out, "128000") {
		t.Error("should not show window size when zero")
	}
}

func TestFormat_PercentageCapped(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(200000, 100000, &th)
	if strings.Contains(out, "200.0%") {
		t.Error("percentage should be capped at 100")
	}
}

func TestFormat_Percentage(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(50000, 100000, &th)
	if !strings.Contains(out, "50.0%") {
		t.Errorf("should show 50.0%%, got %q", out)
	}
}

func TestFormat_Context(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(0, 100000, &th)
	if !strings.Contains(out, "Context") {
		t.Errorf("should contain Context header, got %q", out)
	}
}
