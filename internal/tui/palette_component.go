package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	zone "github.com/lrstanley/bubblezone/v2"
)

// CommandPalette renders the colon-command suggestion list shown above
// the input while in command mode.
type CommandPalette struct {
	commands []Command
	filter   string // input value including the leading ':'
	width    int
}

// NewCommandPalette creates a command palette component.
func NewCommandPalette(commands []Command, filter string, width int) CommandPalette {
	return CommandPalette{commands: commands, filter: filter, width: width}
}

// Render returns the styled palette, or "" when no commands match.
func (c CommandPalette) Render() string {
	filter := strings.TrimPrefix(strings.TrimSpace(c.filter), ":")
	filter = strings.ToLower(filter)

	var visible []Command
	for _, cmd := range c.commands {
		name := strings.TrimPrefix(cmd.Name, ":")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			visible = append(visible, cmd)
		}
	}

	var lines []string
	for _, cmd := range visible {
		name := commandNameStyle.Render(cmd.Name)
		desc := commandDescStyle.Render(cmd.Description)
		lines = append(lines, name+" "+desc)
	}

	if len(lines) == 0 {
		return ""
	}

	return commandPaletteStyle.Width(c.width).Render(strings.Join(lines, "\n"))
}

// ModelPalette renders the model selection list shown above the input
// in model mode: provider headings, scrollable model rows, current
// model marker, overflow indicator.
type ModelPalette struct {
	models        []string // filtered model list ("provider/model" or "model")
	providerNames map[string]string
	selected      int    // index into models
	current       string // "provider/model" of the active model
	maxVisible    int
	width         int
}

// NewModelPalette creates a model palette component.
func NewModelPalette(models []string, providerNames map[string]string, selected int, current string, maxVisible, width int) ModelPalette {
	return ModelPalette{
		models:        models,
		providerNames: providerNames,
		selected:      selected,
		current:       current,
		maxVisible:    maxVisible,
		width:         width,
	}
}

// Render returns the styled palette.
func (c ModelPalette) Render() string {
	if len(c.models) == 0 {
		return commandPaletteStyle.Width(c.width).Render("No matching models")
	}

	// Build display rows: provider heading + model items
	type row struct {
		isHeading bool
		text      string
		modelIdx  int
	}
	var rows []row
	lastProvider := ""
	for i, model := range c.models {
		parts := strings.SplitN(model, "/", 2)
		providerKey := parts[0]
		name := model
		if len(parts) == 2 {
			name = parts[1]
		}
		if providerKey != lastProvider {
			displayName := providerKey
			if c.providerNames != nil {
				if dn, ok := c.providerNames[providerKey]; ok && dn != "" {
					displayName = dn
				}
			}
			rows = append(rows, row{isHeading: true, text: displayName})
			lastProvider = providerKey
		}
		rows = append(rows, row{text: name, modelIdx: i})
	}

	selectedRowIdx := 0
	for i, r := range rows {
		if r.modelIdx == c.selected {
			selectedRowIdx = i
			break
		}
	}

	start, end := scrollWindow(selectedRowIdx, c.maxVisible, len(rows))

	var lines []string
	for i := start; i < end; i++ {
		r := rows[i]
		if r.isHeading {
			lines = append(lines, paletteTitleStyle.Render(r.text))
			continue
		}
		model := c.models[r.modelIdx]
		marker := "   "
		if r.modelIdx == c.selected {
			marker = " ▶ "
		}
		styled := listItemStyle.Render(r.text)
		if model == c.current {
			styled = commandNameStyle.Render(r.text + " (current)")
		}
		lines = append(lines, marker+styled)
	}

	if start > 0 || end < len(rows) {
		lines = append(lines, commandDescStyle.Render(fmt.Sprintf("  (%d-%d of %d)", start+1, end, len(rows))))
	}

	return commandPaletteStyle.Width(c.width).Render(strings.Join(lines, "\n"))
}

// QuestionPalette renders the interactive question dialog: header,
// question text, scrollable options with zone-marked rows, overflow
// indicator, and help line.
type QuestionPalette struct {
	modal      QuestionModal
	idx        int
	multi      []bool
	maxVisible int
	width      int
}

// NewQuestionPalette creates a question palette component.
func NewQuestionPalette(modal QuestionModal, idx int, multi []bool, maxVisible, width int) QuestionPalette {
	return QuestionPalette{
		modal:      modal,
		idx:        idx,
		multi:      multi,
		maxVisible: maxVisible,
		width:      width,
	}
}

// Render returns the styled question dialog.
func (c QuestionPalette) Render() string {
	contentWidth := c.width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	var lines []string
	lines = append(lines, paletteTitleStyle.Render(chatWrap("", c.modal.Header, contentWidth)))
	lines = append(lines, "")
	lines = append(lines, chatWrap("", c.modal.Question, contentWidth))
	lines = append(lines, "")

	start, end := scrollWindow(c.idx, c.maxVisible, len(c.modal.Options))

	for i := start; i < end; i++ {
		opt := c.modal.Options[i]
		prefix := "  "
		if c.modal.Multiple {
			if c.multi[i] {
				prefix = " ☑ "
			} else {
				prefix = " ☐ "
			}
		} else {
			if i == c.idx {
				prefix = " ▶ "
			}
		}
		fullLine := prefix + opt.Label
		if opt.Description != "" {
			fullLine += " — " + opt.Description
		}
		wrapped := chatWrap(prefix, fullLine[len(prefix):], contentWidth)
		lines = append(lines, zone.Mark(fmt.Sprintf("question-opt-%d", i), listItemStyle.Render(wrapped)))
	}

	if start > 0 || end < len(c.modal.Options) {
		lines = append(lines, commandDescStyle.Render(fmt.Sprintf("  (%d-%d of %d)", start+1, end, len(c.modal.Options))))
	}

	lines = append(lines, "")
	help := "↑↓ navigate · Enter select · Esc cancel"
	if c.modal.Multiple {
		help += " · Space toggle"
	}
	lines = append(lines, commandDescStyle.Render(help))

	return commandPaletteStyle.Width(c.width).Render(strings.Join(lines, "\n"))
}

// HelpOverlay renders the full keybinding help screen.
type HelpOverlay struct {
	width int
}

// NewHelpOverlay creates a help overlay component.
func NewHelpOverlay(width int) HelpOverlay {
	return HelpOverlay{width: width}
}

// Render returns the styled help screen.
func (h HelpOverlay) Render() string {
	contentWidth := h.width - 6
	if contentWidth < 30 {
		contentWidth = 30
	}

	var lines []string
	lines = append(lines, paletteTitleStyle.Render("Keybindings"))
	lines = append(lines, "")

	type group struct {
		title    string
		bindings []key.Binding
	}
	groups := []group{
		{"Navigation", []key.Binding{keys.Up, keys.Down, keys.PageUp, keys.PageDown, keys.Top, keys.Bottom}},
		{"Actions", []key.Binding{keys.Search, keys.Copy, keys.Reasoning, keys.Help}},
		{"Input", []key.Binding{keys.Submit, keys.Cancel}},
		{"Commands", []key.Binding{keys.Commands}},
		{"System", []key.Binding{keys.Quit}},
	}

	for _, g := range groups {
		lines = append(lines, paletteTitleStyle.Render(g.title))
		for _, b := range g.bindings {
			keys := strings.Join(b.Keys(), ", ")
			line := fmt.Sprintf("  %-18s %s", keys, b.Help().Desc)
			lines = append(lines, chatWrap("", line, contentWidth))
		}
		lines = append(lines, "")
	}

	lines = append(lines, commandDescStyle.Render("Press any key to close"))

	return commandPaletteStyle.Width(h.width).Render(strings.Join(lines, "\n"))
}
