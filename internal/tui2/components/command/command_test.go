package command

import (
	"testing"
)

func TestParse_Quit(t *testing.T) {
	tests := []string{"q", "quit", "exit"}
	for _, input := range tests {
		if got := Parse(input); got != CmdQuit {
			t.Errorf("Parse(%q) = %v, want CmdQuit", input, got)
		}
	}
}

func TestParse_Clear(t *testing.T) {
	if got := Parse("clear"); got != CmdClear {
		t.Errorf("Parse(clear) = %v, want CmdClear", got)
	}
}

func TestParse_Help(t *testing.T) {
	tests := []string{"h", "help"}
	for _, input := range tests {
		if got := Parse(input); got != CmdHelp {
			t.Errorf("Parse(%q) = %v, want CmdHelp", input, got)
		}
	}
}

func TestParse_Compact(t *testing.T) {
	if got := Parse("compact"); got != CmdCompact {
		t.Errorf("Parse(compact) = %v, want CmdCompact", got)
	}
}

func TestParse_Stop(t *testing.T) {
	if got := Parse("stop"); got != CmdStop {
		t.Errorf("Parse(stop) = %v, want CmdStop", got)
	}
}

func TestParse_Steer(t *testing.T) {
	if got := Parse("steer please do X"); got != CmdSteer {
		t.Errorf("Parse(steer ...) = %v, want CmdSteer", got)
	}
}

func TestParse_FollowUp(t *testing.T) {
	if got := Parse("followup what about Y"); got != CmdFollowUp {
		t.Errorf("Parse(followup ...) = %v, want CmdFollowUp", got)
	}
}

func TestParse_Verbose(t *testing.T) {
	if got := Parse("verbose"); got != CmdVerbose {
		t.Errorf("Parse(verbose) = %v, want CmdVerbose", got)
	}
}

func TestParse_Search(t *testing.T) {
	if got := Parse("search keyword"); got != CmdSearch {
		t.Errorf("Parse(search ...) = %v, want CmdSearch", got)
	}
}

func TestParse_Top(t *testing.T) {
	if got := Parse("top"); got != CmdTop {
		t.Errorf("Parse(top) = %v, want CmdTop", got)
	}
}

func TestParse_Bottom(t *testing.T) {
	if got := Parse("bottom"); got != CmdBottom {
		t.Errorf("Parse(bottom) = %v, want CmdBottom", got)
	}
}

func TestParse_Banner(t *testing.T) {
	if got := Parse("banner"); got != CmdBanner {
		t.Errorf("Parse(banner) = %v, want CmdBanner", got)
	}
}

func TestParse_Roles(t *testing.T) {
	if got := Parse("roles"); got != CmdReloadRoles {
		t.Errorf("Parse(roles) = %v, want CmdReloadRoles", got)
	}
}

func TestParse_Login(t *testing.T) {
	if got := Parse("login"); got != CmdLogin {
		t.Errorf("Parse(login) = %v, want CmdLogin", got)
	}
}

func TestParse_Logout(t *testing.T) {
	if got := Parse("logout"); got != CmdLogout {
		t.Errorf("Parse(logout) = %v, want CmdLogout", got)
	}
}

func TestParse_Session(t *testing.T) {
	if got := Parse("session"); got != CmdSession {
		t.Errorf("Parse(session) = %v, want CmdSession", got)
	}
}

func TestParse_MCP(t *testing.T) {
	if got := Parse("mcp"); got != CmdMCP {
		t.Errorf("Parse(mcp) = %v, want CmdMCP", got)
	}
}

func TestParse_Model(t *testing.T) {
	tests := []string{"model", "model gpt-4", "model claude"}
	for _, input := range tests {
		if got := Parse(input); got != CmdModel {
			t.Errorf("Parse(%q) = %v, want CmdModel", input, got)
		}
	}
}

func TestParse_Unknown(t *testing.T) {
	if got := Parse("nonexistent"); got != CmdNone {
		t.Errorf("Parse(nonexistent) = %v, want CmdNone", got)
	}
}

func TestParse_Empty(t *testing.T) {
	if got := Parse(""); got != CmdNone {
		t.Errorf("Parse(\"\") = %v, want CmdNone", got)
	}
}

func TestParse_CaseSensitive(t *testing.T) {
	if got := Parse("QUIT"); got != CmdNone {
		t.Errorf("Parse should be case-sensitive: Parse(QUIT) = %v, want CmdNone", got)
	}
}

func TestParse_LeadingWhitespace(t *testing.T) {
	if got := Parse("  clear"); got != CmdClear {
		t.Errorf("Parse should trim leading whitespace, got %v", got)
	}
}

func TestDefaultEntries_NotEmpty(t *testing.T) {
	entries := DefaultEntries()
	if len(entries) == 0 {
		t.Error("DefaultEntries should not be empty")
	}
}

func TestDefaultEntries_AllHaveLabels(t *testing.T) {
	for _, e := range DefaultEntries() {
		if e.Label == "" {
			t.Error("all entries should have a label")
		}
		if e.Desc == "" {
			t.Error("all entries should have a description")
		}
	}
}

func TestDefaultEntries_UniqueLabels(t *testing.T) {
	seen := make(map[string]bool)
	for _, e := range DefaultEntries() {
		if seen[e.Label] {
			t.Errorf("duplicate label %q", e.Label)
		}
		seen[e.Label] = true
	}
}
