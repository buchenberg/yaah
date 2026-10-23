package tui

import (
	"strings"

	"github.com/buchenberg/yaah/internal/tui/components/command"
)

// suggestionMaxRows caps how tall the typeahead strip may grow.
const suggestionMaxRows = 6

// updateSuggestions recomputes the typeahead strip from the composer text and
// shows, resizes or hides it. It is a no-op when the match set is unchanged so
// that typing an argument does not churn the layout.
func (t *App) updateSuggestions() {
	matches := command.Complete(t.Input.GetText(), command.DefaultEntries())
	if len(matches) > suggestionMaxRows {
		matches = matches[:suggestionMaxRows]
	}
	if sameSuggestions(matches, t.suggestions) {
		return
	}
	t.suggestions = matches
	t.suggestionIdx = 0
	t.renderSuggestions()
}

// renderSuggestions draws the strip and sizes its row: the highlighted match in
// reverse video, the rest dim, each with its description.
func (t *App) renderSuggestions() {
	if len(t.suggestions) == 0 {
		t.Root.ResizeItem(t.suggestionsView, 0, 0)
		t.suggestionsView.SetText("")
		return
	}
	var b strings.Builder
	for i, s := range t.suggestions {
		b.WriteString(t.suggestionLine(s, i == t.suggestionIdx))
		b.WriteByte('\n')
	}
	t.suggestionsView.SetText(b.String())
	t.Root.ResizeItem(t.suggestionsView, len(t.suggestions), 0)
}

// suggestionLine renders one match. The highlighted row uses reverse video
// rather than fixed colours, so it follows the terminal's palette; with
// NO_COLOR set it degrades to a plain "> " marker.
func (t *App) suggestionLine(s command.Completion, selected bool) string {
	label := ":" + s.Label
	if t.Theme.NoColor {
		if selected {
			return "> " + label + "  " + s.Desc
		}
		return "  " + label + "  " + s.Desc
	}
	if selected {
		return "[::r] " + label + " " + t.Theme.Tag(t.Theme.Dim, s.Desc) + t.Theme.ResetTag()
	}
	return " " + t.Theme.Tag(t.Theme.Dim, label) + t.Theme.Tag(t.Theme.Dim, " "+s.Desc)
}

// hasSuggestions reports whether the typeahead strip is currently shown.
func (t *App) hasSuggestions() bool { return len(t.suggestions) > 0 }

// cycleSuggestion moves the highlight by delta, wrapping around the matches.
func (t *App) cycleSuggestion(delta int) {
	if !t.hasSuggestions() {
		return
	}
	n := len(t.suggestions)
	t.suggestionIdx = ((t.suggestionIdx+delta)%n + n) % n
	t.renderSuggestions()
}

// acceptSuggestion completes the composer with the highlighted match and hides
// the strip.
func (t *App) acceptSuggestion() bool {
	if !t.hasSuggestions() {
		return false
	}
	t.Input.SetText(t.suggestions[t.suggestionIdx].Insert, true)
	t.hideSuggestions()
	return true
}

// dismissSuggestions hides the strip. It reports whether anything was hidden, so
// Escape can dismiss typeahead without also stopping the turn.
func (t *App) dismissSuggestions() bool {
	if !t.hasSuggestions() {
		return false
	}
	t.hideSuggestions()
	return true
}

func (t *App) hideSuggestions() {
	t.suggestions = nil
	t.suggestionIdx = 0
	t.renderSuggestions()
}

// sameSuggestions reports whether a and b hold the same matches in the same
// order, so a redraw can be skipped.
func sameSuggestions(a, b []command.Completion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
