package tui

import (
	"strings"
	"unicode"

	"github.com/buchenberg/yaah/internal/tui/components/command"
)

// handleColonInput dispatches text entered with a leading ":" as a colon
// command instead of sending it to the model. It reports whether the text was
// consumed; when it returns false the caller should treat the text as a prompt.
//
// Text that starts with ":" but whose first word is not command-like — ":)" or
// ":3", say — is left to the caller so it can be sent as an ordinary message.
// Anything consumed leaves the input box empty, except for an unknown command,
// where the text is kept so it can be corrected.
func (t *App) handleColonInput(text string) bool {
	body, ok := strings.CutPrefix(text, ":")
	if !ok {
		return false
	}
	cmd, arg := command.Split(body)
	if cmd == command.CmdNone {
		name, _, _ := strings.Cut(strings.TrimSpace(body), " ")
		if !commandWord(name) {
			return false
		}
		t.SetEphemeral("unknown command :" + name + " — Ctrl+P lists commands")
		return true
	}
	if command.RequiresArg(cmd) && arg == "" {
		t.SetEphemeral("usage: " + command.Usage(cmd))
		return true
	}
	t.flushRefresh()
	t.Input.SetText("", false)
	t.HandleCommand(cmd, arg)
	return true
}

// commandWord reports whether s looks like a command name rather than prose or
// an emoticon: it holds at least one letter and nothing but letters, digits,
// '-' or '_'.
func commandWord(s string) bool {
	if s == "" {
		return false
	}
	var letter bool
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r), r == '-', r == '_':
		default:
			return false
		}
	}
	return letter
}
