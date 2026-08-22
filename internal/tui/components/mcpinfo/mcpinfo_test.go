package mcpinfo

import (
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

func TestFormat_Empty(t *testing.T) {
	th := colors.NewDarkTheme()
	out := Format(nil, &th)
	if !strings.Contains(out, "no servers") {
		t.Errorf("empty should show 'no servers', got %q", out)
	}
}

func TestFormat_OneConnected(t *testing.T) {
	th := colors.NewDarkTheme()
	servers := []Server{{Name: "filesystem", Connected: true}}
	out := Format(servers, &th)
	if !strings.Contains(out, "filesystem") {
		t.Errorf("should contain server name, got %q", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("should contain dot indicator, got %q", out)
	}
}

func TestFormat_OneDisconnected(t *testing.T) {
	th := colors.NewDarkTheme()
	servers := []Server{{Name: "database", Connected: false}}
	out := Format(servers, &th)
	if !strings.Contains(out, "database") {
		t.Errorf("should contain server name, got %q", out)
	}
}

func TestFormat_Multiple(t *testing.T) {
	th := colors.NewDarkTheme()
	servers := []Server{
		{Name: "filesystem", Connected: true},
		{Name: "database", Connected: false},
		{Name: "api", Connected: true},
	}
	out := Format(servers, &th)
	if !strings.Contains(out, "filesystem") {
		t.Error("should contain first server")
	}
	if !strings.Contains(out, "database") {
		t.Error("should contain second server")
	}
	if !strings.Contains(out, "api") {
		t.Error("should contain third server")
	}
}

func TestFormat_ConnectedHasConnectedColor(t *testing.T) {
	th := colors.NewDarkTheme()
	servers := []Server{{Name: "filesystem", Connected: true}}
	out := Format(servers, &th)
	colorTag := "[" + th.Connected + "]"
	if !strings.Contains(out, colorTag) {
		t.Errorf("connected server should use Connected color %q, got %q", colorTag, out)
	}
}

func TestFormat_DisconnectedHasDimColor(t *testing.T) {
	th := colors.NewDarkTheme()
	servers := []Server{{Name: "database", Connected: false}}
	out := Format(servers, &th)
	colorTag := "[" + th.Dim + "]"
	if !strings.Contains(out, colorTag) {
		t.Errorf("disconnected server should use Dim color %q, got %q", colorTag, out)
	}
}
