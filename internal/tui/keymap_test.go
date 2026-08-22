package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestDefaultBindings_NotEmpty(t *testing.T) {
	bindings := DefaultBindings()
	if len(bindings) == 0 {
		t.Error("DefaultBindings should not be empty")
	}
}

func TestDefaultBindings_AllHaveActions(t *testing.T) {
	for _, b := range DefaultBindings() {
		if b.Action == ActionNone {
			t.Errorf("binding %q should have a non-zero action", b.Label)
		}
	}
}

func TestDefaultBindings_UniqueActions(t *testing.T) {
	seen := make(map[Action]bool)
	for _, b := range DefaultBindings() {
		if seen[b.Action] {
			t.Errorf("duplicate action %v for binding %q", b.Action, b.Label)
		}
		seen[b.Action] = true
	}
}

func TestTranslate_ExactMatch(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionQuit {
		t.Errorf("Ctrl+C should map to Quit, got %v", got)
	}
}

func TestTranslate_Enter(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionSend {
		t.Errorf("Enter should map to Send, got %v", got)
	}
}

func TestTranslate_Escape(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionCancel {
		t.Errorf("Escape should map to Cancel, got %v", got)
	}
}

func TestTranslate_Up(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionScrollUp {
		t.Errorf("Up should map to ScrollUp, got %v", got)
	}
}

func TestTranslate_Down(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionScrollDown {
		t.Errorf("Down should map to ScrollDown, got %v", got)
	}
}

func TestTranslate_UnknownKey(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)
	bindings := DefaultBindings()
	if got := Translate(ev, bindings); got != ActionNone {
		t.Errorf("unknown key should map to None, got %v", got)
	}
}

func TestTranslate_EmptyBindings(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if got := Translate(ev, nil); got != ActionNone {
		t.Errorf("empty bindings should return None, got %v", got)
	}
}

func TestBinding_Match_Exact(t *testing.T) {
	b := Binding{Key: tcell.KeyEnter, Action: ActionSend}
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !b.Match(ev) {
		t.Error("Enter should match Enter binding")
	}
}

func TestBinding_Match_WithModifier(t *testing.T) {
	b := Binding{Key: tcell.KeyCtrlC, Mod: tcell.ModCtrl, Action: ActionQuit}
	ev := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	if !b.Match(ev) {
		t.Error("Ctrl+C with Ctrl mod should match")
	}
}

func TestBinding_Match_WrongKey(t *testing.T) {
	b := Binding{Key: tcell.KeyEnter, Action: ActionSend}
	ev := tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
	if b.Match(ev) {
		t.Error("Escape should not match Enter binding")
	}
}

func TestBinding_Match_KeyWithModWhenNoModSpecified(t *testing.T) {
	b := Binding{Key: tcell.KeyEnter, Action: ActionSend}
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModCtrl)
	if !b.Match(ev) {
		t.Error("Enter with Ctrl should match Enter binding with no mod specified")
	}
}

func TestBinding_Match_KeyWithZeroMod(t *testing.T) {
	b := Binding{Key: tcell.KeyEnter, Mod: 0, Action: ActionSend}
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if !b.Match(ev) {
		t.Error("Enter should match zero-mod binding")
	}
}

func TestTranslate_FirstMatchWins(t *testing.T) {
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	custom := []Binding{
		{Key: tcell.KeyEnter, Action: ActionSend, Label: "first"},
		{Key: tcell.KeyEnter, Action: ActionQuit, Label: "second"},
	}
	if got := Translate(ev, custom); got != ActionSend {
		t.Errorf("first matching binding should win, got %v", got)
	}
}
