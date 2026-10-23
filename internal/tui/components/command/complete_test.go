package command

import (
	"strings"
	"testing"
)

func TestComplete_PrefixesCommandNames(t *testing.T) {
	got := Complete(":s", DefaultEntries())
	if len(got) == 0 {
		t.Fatal("no suggestions for :s")
	}
	for _, c := range got {
		if !strings.HasPrefix(c.Label, "s") {
			t.Errorf("suggestion %q does not match prefix %q", c.Label, "s")
		}
	}
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	// "session" is deliberately absent: the palette and typeahead only offer
	// commands that have a handler (see TestDefaultEntries_ExcludesUnwired).
	for _, want := range []string{"steer", "stop", "search"} {
		if !contains(labels, want) {
			t.Errorf("missing suggestion %q in %v", want, labels)
		}
	}
}

func TestComplete_LoneColonOffersCommands(t *testing.T) {
	if got := Complete(":", DefaultEntries()); len(got) == 0 {
		t.Fatal("a lone ':' should offer the command list")
	}
}

// An argument in progress is not completed: ":steer keep" has nothing to offer.
func TestComplete_NoSuggestionsOnceArgumentStarted(t *testing.T) {
	for _, text := range []string{":steer keep", ":steer ", ": search"} {
		if got := Complete(text, DefaultEntries()); len(got) != 0 {
			t.Errorf("Complete(%q) = %v, want none", text, got)
		}
	}
}

func TestComplete_IgnoresPlainText(t *testing.T) {
	for _, text := range []string{"", "hello", "steer", ":)"} {
		if got := Complete(text, DefaultEntries()); len(got) != 0 {
			t.Errorf("Complete(%q) = %v, want none", text, got)
		}
	}
}

// Tab should land the cursor where the argument begins.
func TestComplete_InsertsTrailingSpaceForArgCommands(t *testing.T) {
	got := Complete(":ste", DefaultEntries())
	if len(got) != 1 || got[0].Label != "steer" {
		t.Fatalf("Complete(:ste) = %v", got)
	}
	if got[0].Insert != ":steer " {
		t.Errorf("Insert = %q, want %q", got[0].Insert, ":steer ")
	}
}

func TestComplete_CapsResultCount(t *testing.T) {
	got := Complete(":", DefaultEntries())
	if len(got) > maxCompletions {
		t.Errorf("got %d suggestions, cap is %d", len(got), maxCompletions)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// The palette and typeahead must not advertise commands that have no handler:
// suggesting ":session" and then silently doing nothing is the bug class this
// whole change set is about.
func TestDefaultEntries_ExcludesUnwired(t *testing.T) {
	unwired := map[Cmd]bool{
		CmdReloadRoles: true, CmdLogin: true, CmdLogout: true,
		CmdSession: true, CmdMCP: true,
	}
	for _, e := range DefaultEntries() {
		if unwired[e.Cmd] {
			t.Errorf("palette advertises %q but HandleCommand has no case for it", e.Label)
		}
	}
}

func TestName_RoundTripsSplit(t *testing.T) {
	for _, cmd := range []Cmd{CmdQuit, CmdClear, CmdHelp, CmdSteer, CmdSearch, CmdSession, CmdMCP} {
		name := Name(cmd)
		if name == "" {
			t.Fatalf("Name(%v) is empty", cmd)
		}
		if got, _ := Split(name); got != cmd || got == CmdNone {
			t.Errorf("Split(Name(%v)) = %v", cmd, got)
		}
	}
	if got := Name(CmdNone); got != "" {
		t.Errorf("Name(CmdNone) = %q, want empty", got)
	}
}
