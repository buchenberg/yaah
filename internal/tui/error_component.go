package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const maxErrorLines = 12

type ErrorMessage struct {
	content string
	width   int
	maxH    int
}

func NewErrorMessage(content string, width, maxHeight int) ErrorMessage {
	return ErrorMessage{content: content, width: width, maxH: maxHeight}
}

func (c ErrorMessage) Render() string {
	lines := strings.Split(c.content, "\n")

	maxH := c.maxH
	if maxH <= 0 || maxH > maxErrorLines {
		maxH = maxErrorLines
	}

	var body string
	if len(lines) > maxH {
		visible := lines[:maxH-1]
		body = strings.Join(visible, "\n")
		hidden := len(lines) - (maxH - 1)
		body += fmt.Sprintf("\n… %d more lines", hidden)
	} else {
		body = c.content
	}

	innerWidth := c.width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	wrapped := lipgloss.NewStyle().Width(innerWidth).Render(body)

	icon := errorStyle.Bold(true).Render("✗")
	header := fmt.Sprintf("%s Error", icon)

	rendered := errorBoxStyle.
		Width(c.width - 2).
		Render(header + "\n" + wrapped)

	return rendered + "\n"
}
