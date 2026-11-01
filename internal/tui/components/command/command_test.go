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

func TestSplit_CommandAndArg(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd Cmd
		wantArg string
	}{
		{"steer keep it short", CmdSteer, "keep it short"},
		{"steer  padded arg ", CmdSteer, "padded arg"},
		{"followup and then?", CmdFollowUp, "and then?"},
		{"search keyword", CmdSearch, "keyword"},
		{"model gpt-4", CmdModel, "gpt-4"},
		{"model", CmdModel, ""},
		{"clear", CmdClear, ""},
		{"  quit  ", CmdQuit, ""},
		{"steer", CmdSteer, ""},
		{"followup", CmdFollowUp, ""},
		{"search", CmdSearch, ""},
		{"nope", CmdNone, ""},
		{"steerx", CmdNone, ""},
		{"", CmdNone, ""},
	}
	for _, tt := range tests {
		cmd, arg := Split(tt.input)
		if cmd != tt.wantCmd || arg != tt.wantArg {
			t.Errorf("Split(%q) = (%v, %q), want (%v, %q)", tt.input, cmd, arg, tt.wantCmd, tt.wantArg)
		}
	}
}

// A bare command name must parse to its command so callers can report a missing
// argument rather than an unknown command.
func TestParse_BareArgCommand(t *testing.T) {
	if got := Parse("steer"); got != CmdSteer {
		t.Errorf("Parse(\"steer\") = %v, want CmdSteer", got)
	}
	if got := Parse("followup"); got != CmdFollowUp {
		t.Errorf("Parse(\"followup\") = %v, want CmdFollowUp", got)
	}
}

func TestRequiresArg(t *testing.T) {
	for _, cmd := range []Cmd{CmdSteer, CmdFollowUp, CmdSearch} {
		if !RequiresArg(cmd) {
			t.Errorf("RequiresArg(%v) = false, want true", cmd)
		}
		if Usage(cmd) == "" {
			t.Errorf("Usage(%v) is empty for an arg-requiring command", cmd)
		}
	}
	for _, cmd := range []Cmd{CmdQuit, CmdClear, CmdHelp, CmdCompact, CmdStop, CmdNone} {
		if RequiresArg(cmd) {
			t.Errorf("RequiresArg(%v) = true, want false", cmd)
		}
	}
}

// Every command that requires an argument should be reachable from the palette.
func TestDefaultEntries_CoversArgCommands(t *testing.T) {
	have := make(map[Cmd]bool)
	for _, e := range DefaultEntries() {
		have[e.Cmd] = true
	}
	for _, cmd := range []Cmd{CmdSteer, CmdFollowUp, CmdSearch} {
		if !have[cmd] {
			t.Errorf("palette is missing an entry for %v", cmd)
		}
	}
}

// Split must treat every whitespace separator the way Complete does, or text the
// typeahead accepts would parse differently when submitted.
func TestSplit_WhitespaceSeparators(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd Cmd
		wantArg string
	}{
		{"steer\ttabbed", CmdSteer, "tabbed"},
		{"model  two spaces", CmdModel, "two spaces"},
		{"steer \t mixed", CmdSteer, "mixed"},
		{"clear\t", CmdClear, ""},
	}
	for _, tt := range tests {
		cmd, arg := Split(tt.input)
		if cmd != tt.wantCmd || arg != tt.wantArg {
			t.Errorf("Split(%q) = (%v, %q), want (%v, %q)", tt.input, cmd, arg, tt.wantCmd, tt.wantArg)
		}
	}
}

// Name prints the canonical spelling, not whichever alias happens to come first.
func TestName_PrefersCanonicalSpelling(t *testing.T) {
	if got := Name(CmdQuit); got != "quit" {
		t.Errorf(`Name(CmdQuit) = %q, want "quit"`, got)
	}
	if got := Name(CmdHelp); got != "help" {
		t.Errorf(`Name(CmdHelp) = %q, want "help"`, got)
	}
}

func TestParse_Modes(t *testing.T) {
	if got := Parse("plan"); got != CmdPlan {
		t.Errorf("Parse(plan) = %v, want CmdPlan", got)
	}
	if got := Parse("build"); got != CmdBuild {
		t.Errorf("Parse(build) = %v, want CmdBuild", got)
	}
}
