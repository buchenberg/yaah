// state.go — focus state and query helpers.
package tui

import (
	"fmt"
	"strings"
)

type focusState int

const (
	focusNormal focusState = iota
	focusCommandPalette
	focusModal
)

func (t *App) searchMessages(query string) {
	if query == "" {
		return
	}
	text := t.Messages.GetText(true)
	idx := strings.Index(strings.ToLower(text), strings.ToLower(query))
	if idx >= 0 {
		line := strings.Count(text[:idx], "\n")
		t.Messages.ScrollTo(line, 0)
		t.SetEphemeral(fmt.Sprintf("Found at line %d", line+1))
	} else {
		t.SetEphemeral("No matches found")
	}
}
