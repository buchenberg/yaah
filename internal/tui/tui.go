// Package tui implements the terminal UI for yaah using bubbletea.
package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/buchenberg/yaah/internal/banner"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("14"))

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	copyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	codeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	boldStyle = lipgloss.NewStyle().
			Bold(true)

	italicStyle = lipgloss.NewStyle().
			Italic(true)

	thinkingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Italic(true)
)

// Message represents a chat message in the TUI.
type Message struct {
	Role    string
	Content string // glamour-rendered for assistant, raw for others
	Raw     string // original markdown (for copy), same as Content for user/tool
}

// AgentMsg is a message from the agent goroutine.
type AgentMsg struct {
	Token         string
	Thinking      string // reasoning/thinking content from models like DeepSeek
	ToolName      string
	Flush         string // streamed content to commit before a tool call
	Done          bool
	Response      string
	Err           error
	ContextTokens int // estimated tokens used, for status bar
	ContextWindow int // context window size, for status bar
}

// Model is the bubbletea model for the yaah TUI.
type Model struct {
	messages      []Message
	viewport      viewport.Model
	input         textinput.Model
	spinner       spinner.Model
	mdRenderer    *glamour.TermRenderer
	banner        string // pre-rendered figlet + lolcat ASCII art
	provider      string
	modelName     string
	width         int
	height        int
	thinking      bool
	toolCall      string
	streaming     bool   // currently streaming a response
	streamContent string // accumulated streaming content
	thinkContent  string // accumulated thinking/reasoning content
	copyFlash     string // brief "Copied!" indicator
	contextPct    int    // context window fill percentage (0-100)
	contextTokens int    // estimated token count
	contextWindow int    // context window size
	onSubmit      func(string)
	onQuit        func()
}

// clearCopyFlashMsg clears the copy flash indicator after a timeout.
type clearCopyFlashMsg struct{}

// New creates a new TUI model.
func New(provider, model string, contextWindow int, onSubmit func(string), onQuit func()) *Model {
	input := textinput.New()
	input.Placeholder = "Type a message..."
	input.Focus()
	input.CharLimit = 0
	input.SetWidth(80)

	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(spinnerStyle),
	)

	vp := viewport.New()

	bn, _ := banner.Generate()

	return &Model{
		input:         input,
		spinner:       sp,
		viewport:      vp,
		banner:        bn,
		provider:      provider,
		modelName:     model,
		contextWindow: contextWindow,
		onSubmit:      onSubmit,
		onQuit:        onQuit,
	}
}

// createRenderer (re)creates the glamour markdown renderer.
func (m *Model) createRenderer() {
	width := m.width - 2
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
		glamour.WithChromaFormatter("terminal256"),
		glamour.WithPreservedNewLines(),
	)
	if err == nil {
		m.mdRenderer = r
	}
}

var (
	mdLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	autoLinkRe = regexp.MustCompile(`<((?:https?|ftp)://[^>]+)>`)
	bareURLRe = regexp.MustCompile(`(?m)(?:^|\s)((?:https?)://\S+)`)
)

// osc8Link wraps text in an OSC 8 hyperlink for clickable terminal links.
func osc8Link(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// injectHyperlinks converts markdown links, autolinks, and bare URLs into
// OSC 8 hyperlink sequences before glamour renders them.
func injectHyperlinks(md string) string {
	md = mdLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := mdLinkRe.FindStringSubmatch(match)
		if len(parts) == 3 {
			return osc8Link(parts[1], parts[2])
		}
		return match
	})
	md = autoLinkRe.ReplaceAllStringFunc(md, func(match string) string {
		url := strings.Trim(match, "<>")
		return osc8Link(url, url)
	})
	return md
}

// glamourRender renders markdown through the reusable renderer.
func (m *Model) glamourRender(content string) string {
	if m.mdRenderer == nil {
		return content
	}
	content = injectHyperlinks(content)
	out, err := m.mdRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(out)
}

// tuiCompactStyle returns a glamour override style JSON. It strips heading
// markers (###), removes document margins, and keeps everything else default.
func tuiCompactStyle() []byte {
	return []byte(`{
  "document": {
    "margin": 0
  },
  "h1": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "h2": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "h3": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "h4": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "h5": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "h6": {
    "prefix": "",
    "suffix": "",
    "block_prefix": "\n\n\n",
    "block_suffix": "\n"
  },
  "codeblock": {
    "block_prefix": "\n",
    "block_suffix": "\n",
    "prefix": "  ",
    "style_prefix": "\033[48;5;236m",
    "style_suffix": "\033[0m"
  }
}`)
}

// renderMarkdown renders raw markdown through glamour. Tables are extracted
// and rendered as plain compact text before passing the rest to glamour.
func (m *Model) renderMarkdown(content string) string {
	segments := parseAndRenderTables(content)
	var result strings.Builder
	for i, seg := range segments {
		if seg.isTable {
			if i > 0 {
				result.WriteString("\n\n")
			}
			result.WriteString(renderCompactTable(seg.content))
		} else if seg.content != "" {
			result.WriteString(m.glamourRender(seg.content))
		}
	}
	return strings.TrimSpace(result.String())
}

type textSegment struct {
	content string
	isTable bool
}

// parseAndRenderTables splits markdown into table and non-table segments.
// A table is: one or more lines starting with "|", where the first or second
// line contains "---" (the separator row).
func parseAndRenderTables(md string) []textSegment {
	lines := strings.Split(md, "\n")
	var segments []textSegment

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Detect table start: current line starts with | and next line is a separator
		if strings.HasPrefix(line, "|") && i+1 < len(lines) {
			next := lines[i+1]
			if strings.HasPrefix(next, "|") && strings.Contains(next, "---") {
				var buf strings.Builder
				// Collect header + separator + all continuation rows
				for i < len(lines) && strings.HasPrefix(lines[i], "|") {
					buf.WriteString(lines[i])
					buf.WriteString("\n")
					i++
					// After separator, collect remaining data rows
					if i-1 >= 0 && strings.Contains(lines[i-1], "---") {
						for i < len(lines) && strings.HasPrefix(lines[i], "|") {
							buf.WriteString(lines[i])
							buf.WriteString("\n")
							i++
						}
						break
					}
				}
				// Also check: line before separator IS the header, separator IS second
				// Handles case where separator is first line (unusual but possible)
				segments = append(segments, textSegment{content: buf.String(), isTable: true})
				continue
			}
		}

		// Non-table line: accumulate into a text segment
		var buf strings.Builder
		for i < len(lines) {
			line := lines[i]
			// Stop if we hit a table start
			if strings.HasPrefix(line, "|") && i+1 < len(lines) {
				next := lines[i+1]
				if strings.HasPrefix(next, "|") && strings.Contains(next, "---") {
					break
				}
			}
			buf.WriteString(line)
			buf.WriteString("\n")
			i++
		}
		s := strings.TrimSpace(buf.String())
		if s != "" {
			segments = append(segments, textSegment{content: s, isTable: false})
		}
	}
	return segments
}

// renderCompactTable renders a markdown table as compact aligned text.
func renderCompactTable(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) < 2 {
		return md
	}

	type cell struct {
		raw  string
		rendered string
	}

	var rows [][]cell
	var colWidths []int
	var sepIndex int

	for i, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		rawCols := splitTableRow(line)
		if strings.Contains(line, "---") {
			sepIndex = i
			for j, c := range rawCols {
				w := len(c)
				for len(colWidths) <= j {
					colWidths = append(colWidths, 0)
				}
				if w > colWidths[j] {
					colWidths[j] = w
				}
			}
			continue
		}
		cells := make([]cell, len(rawCols))
		for j, c := range rawCols {
			rendered := renderInlineMarkdown(c)
			cells[j] = cell{raw: c, rendered: rendered}
			w := visibleWidth(rendered)
			for len(colWidths) <= j {
				colWidths = append(colWidths, 0)
			}
			if w > colWidths[j] {
				colWidths[j] = w
			}
		}
		rows = append(rows, cells)
	}

	if len(rows) == 0 || len(colWidths) == 0 {
		return md
	}

	var out strings.Builder

	for i, row := range rows {
		for j, c := range row {
			if j < len(colWidths) {
				dw := visibleWidth(c.rendered)
				out.WriteString(c.rendered)
				if dw < colWidths[j] {
					out.WriteString(strings.Repeat(" ", colWidths[j]-dw))
				}
			} else {
				out.WriteString(c.rendered)
			}
			if j < len(row)-1 {
				out.WriteString("  ")
			}
		}
		out.WriteString("\n")
		if i == 0 && sepIndex > 0 {
			for j := range colWidths {
				out.WriteString(strings.Repeat("─", colWidths[j]))
				if j < len(colWidths)-1 {
					out.WriteString("  ")
				}
			}
			out.WriteString("\n")
		}
	}

	return out.String() + "\n"
}

// renderInlineMarkdown renders basic inline markdown in a table cell:
// backtick code spans, bold, and italic.
func renderInlineMarkdown(s string) string {
	code := func(t string) string { return codeStyle.Render(t) }
	bold := func(t string) string { return boldStyle.Render(t) }
	italic := func(t string) string { return italicStyle.Render(t) }
	s = replacePattern(s, "`", "`", code)
	s = replacePattern(s, "**", "**", bold)
	s = replacePattern(s, "*", "*", italic)
	return s
}

func replacePattern(s, open, close string, style func(string) string) string {
	for {
		start := strings.Index(s, open)
		if start == -1 {
			break
		}
		end := strings.Index(s[start+len(open):], close)
		if end == -1 {
			break
		}
		end += start + len(open)
		inner := s[start+len(open) : end]
		styled := style(inner)
		s = s[:start] + styled + s[end+len(close):]
	}
	return s
}

// visibleWidth returns the display width of a string, stripping ANSI codes.
func visibleWidth(s string) int {
	w := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r <= 0x7F {
			w++
		} else if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func isWideRune(r rune) bool {
	return r >= 0x1100 && r <= 0x115F ||
		r >= 0x2E80 && r <= 0xA4CF ||
		r >= 0xAC00 && r <= 0xD7A3 ||
		r >= 0xF900 && r <= 0xFAFF ||
		r >= 0xFE30 && r <= 0xFE6F ||
		r >= 0xFF01 && r <= 0xFF60 ||
		r >= 0xFFE0 && r <= 0xFFE6 ||
		r >= 0x1B000 && r <= 0x1B2FF ||
		r >= 0x1F004 && r <= 0x1F251 ||
		r >= 0x20000 && r <= 0x3FFFD
}

func splitTableRow(line string) []string {
	line = strings.Trim(line, "| \t")
	cols := strings.Split(line, "|")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return cols
}

func updateWidths(widths *[]int, cols []string) {
	for len(*widths) < len(cols) {
		*widths = append(*widths, 0)
	}
	for i, col := range cols {
		w := displayWidth(col)
		if w > (*widths)[i] {
			(*widths)[i] = w
		}
	}
}

func padRight(s string, width int) string {
	dw := displayWidth(s)
	if dw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-dw)
}

// displayWidth returns the approximate terminal display width of a string.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r <= 0x7F {
			w++
		} else if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// AddAssistantMessage renders markdown through glamour and stores both
// the rendered output (Content) and the raw markdown (Raw).
func (m *Model) AddAssistantMessage(raw string) {
	m.messages = append(m.messages, Message{
		Role:    "assistant",
		Content: m.renderMarkdown(raw),
		Raw:     raw,
	})
	m.refreshViewport()
	m.scrollToBottom()
}

// reRenderMessages re-renders all assistant messages through the current
// glamour renderer (used on window resize when word-wrap width changes).
func (m *Model) reRenderMessages() {
	for i := range m.messages {
		if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
			m.messages[i].Content = m.renderMarkdown(m.messages[i].Raw)
		}
	}
}

// headerHeight returns the number of lines the banner + provider header
// occupies. Used to size the viewport.
func (m *Model) headerHeight() int {
	header := m.banner + "\n\n" +
		titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"
	return len(strings.Split(header, "\n"))
}

// refreshViewport rebuilds the viewport content from the current message state.
func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.renderMessages())
}

// scrollToBottom programmatically scrolls the viewport to the end.
func (m *Model) scrollToBottom() {
	m.viewport.GotoBottom()
}

// AddMessage adds a message to the chat history.
func (m *Model) AddMessage(role, content string) {
	m.messages = append(m.messages, Message{Role: role, Content: content, Raw: content})
	m.refreshViewport()
	m.scrollToBottom()
}

// SetThinking sets the thinking state.
func (m *Model) SetThinking(thinking bool) {
	m.thinking = thinking
	m.refreshViewport()
}

// SetToolCall sets the current tool call display.
func (m *Model) SetToolCall(name string) {
	m.toolCall = name
	m.refreshViewport()
}

// ClearToolCall clears the tool call display.
func (m *Model) ClearToolCall() {
	m.toolCall = ""
	m.refreshViewport()
}

// AppendToken appends a streaming token to the current response.
func (m *Model) AppendToken(token string) {
	if !m.streaming {
		m.streaming = true
		m.streamContent = ""
	}
	m.streamContent += token
	m.refreshViewport()
	m.scrollToBottom()
}

// HandleAgentMsg processes messages from the agent goroutine.
func (m *Model) HandleAgentMsg(msg AgentMsg) {
	if msg.Err != nil {
		m.AddMessage("assistant", fmt.Sprintf("Error: %v", msg.Err))
		m.SetThinking(false)
		m.streaming = false
		m.streamContent = ""
		return
	}

	if msg.Flush != "" {
		// Commit the accumulated streaming content as a message so the
		// next segment (after a tool call) starts on a fresh line.
		m.streaming = false
		m.streamContent = ""
		m.AddAssistantMessage(msg.Flush)
		return
	}

	if msg.Token != "" {
		m.AppendToken(msg.Token)
		return
	}

	if msg.Thinking != "" {
		m.thinkContent += msg.Thinking
		m.refreshViewport()
		m.scrollToBottom()
		return
	}

	if msg.ToolName != "" {
		m.SetToolCall(msg.ToolName)
		return
	}

	if msg.Done {
		m.SetThinking(false)
		m.ClearToolCall()
		m.thinkContent = ""
		if m.streaming && m.streamContent != "" {
			content := m.streamContent
			m.streaming = false
			m.streamContent = ""
			m.AddAssistantMessage(content)
		} else if msg.Response != "" {
			m.AddAssistantMessage(msg.Response)
		}
	}

	if msg.ContextWindow > 0 {
		m.HandleContextInfo(msg.ContextTokens, msg.ContextWindow)
	}
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case AgentMsg:
		m.HandleAgentMsg(msg)
		return m, nil

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		if m.thinking {
			m.refreshViewport()
		}
		return m, spinCmd

	case clearCopyFlashMsg:
		m.copyFlash = ""
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.createRenderer()
		m.reRenderMessages()
		chatHeight := msg.Height - m.headerHeight() - 2
		if chatHeight < 5 {
			chatHeight = 5
		}
		m.viewport.SetWidth(msg.Width)
		m.viewport.SetHeight(chatHeight)
		m.refreshViewport()
		return m, nil

	case tea.MouseMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.onQuit != nil {
				m.onQuit()
			}
			return m, tea.Quit

		case "ctrl+y":
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].Role == "assistant" && m.messages[i].Raw != "" {
					m.copyFlash = "Copied markdown to clipboard!"
					return m, tea.Batch(
						tea.SetClipboard(m.messages[i].Raw),
						tea.Tick(2*time.Second, func(time.Time) tea.Msg {
							return clearCopyFlashMsg{}
						}),
					)
				}
			}
			return m, nil

		case "enter":
			if m.thinking {
				return m, nil
			}
			value := m.input.Value()
			if strings.TrimSpace(value) == "" {
				return m, nil
			}
			m.AddMessage("user", value)
			m.SetThinking(true)
			m.input.SetValue("")
			if m.onSubmit != nil {
				m.onSubmit(value)
			}
			return m, nil

		case "up", "down", "pgup", "pgdown", "home", "end":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			return m, vpCmd

		}

	}

	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// chatWrap wraps text to fit within the terminal width, accounting for a
// prefix label (e.g. "yaah: "). It returns the wrapped text with the prefix
// applied only to the first line.
func chatWrap(prefix, content string, width int) string {
	maxWidth := width - len(prefix)
	if maxWidth < 10 {
		maxWidth = 10
	}
	wrapped := wrapText(content, maxWidth)
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = strings.Repeat(" ", len(prefix)) + line
		}
	}
	return strings.Join(lines, "\n")
}

// wrapText performs simple word-wrapping at the given width, preserving
// explicit newlines in the source text.
func wrapText(text string, width int) string {
	var result strings.Builder
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			result.WriteString("\n")
			continue
		}
		wrapped := wrapParagraph(paragraph, width)
		result.WriteString(wrapped)
		result.WriteString("\n")
	}
	out := result.String()
	// Remove the trailing newline added by the last iteration
	return strings.TrimSuffix(out, "\n")
}

// wrapParagraph wraps a single line (no embedded newlines) to the given width.
func wrapParagraph(line string, width int) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var result strings.Builder
	lineLen := 0
	for i, word := range words {
		wLen := displayWidth(word)
		if i == 0 {
			result.WriteString(word)
			lineLen = wLen
		} else if lineLen+1+wLen > width {
			result.WriteString("\n")
			result.WriteString(word)
			lineLen = wLen
		} else {
			result.WriteString(" ")
			result.WriteString(word)
			lineLen += 1 + wLen
		}
	}
	return result.String()
}

// renderMessages produces the full chat content (messages, streaming,
// thinking indicator, tool call) as a single string suitable for
// handing to the viewport. Width is m.width; callers must ensure
// m.width is set.
func (m *Model) renderMessages() string {
	var b strings.Builder

	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			rendered := userStyle.Render(chatWrap("", msg.Content, m.width))
			b.WriteString(rendered)
			b.WriteString("\n\n")

		case "assistant":
			b.WriteString("\n")
			b.WriteString(assistantStyle.Render(msg.Content))
			copyText := copyStyle.Render("\n\n📋 ctrl+y to copy as md")
			b.WriteString(copyText)
			b.WriteString("\n\n")

		case "tool":
			rendered := toolStyle.Render(chatWrap("", msg.Content, m.width))
			b.WriteString(rendered)
			b.WriteString("\n")

		default:
			rendered := chatWrap("", msg.Content, m.width)
			b.WriteString(rendered)
			b.WriteString("\n")
		}
	}

	// Thinking/reasoning content (from models like DeepSeek)
	if m.thinkContent != "" {
		b.WriteString("\n")
		b.WriteString(thinkingStyle.Render(m.thinkContent))
		b.WriteString("\n")
	}

	// Streaming content
	if m.streaming && m.streamContent != "" {
		rendered := assistantStyle.Render(chatWrap("", m.streamContent, m.width))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Thinking indicator
	if m.thinking && !m.streaming {
		rendered := spinnerStyle.Render(fmt.Sprintf("  %s Thinking...", m.spinner.View()))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	// Tool call display
	if m.toolCall != "" {
		rendered := toolStyle.Render(fmt.Sprintf("  tool: %s", m.toolCall))
		b.WriteString(rendered)
		b.WriteString("\n")
	}

	return b.String()
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Initializing...")
	}

	// Header: figlet banner + blank line + provider/model line + trailing newline.
	header := m.banner + "\n\n" +
		titleStyle.Render(fmt.Sprintf("%s/%s", m.provider, m.modelName)) + "\n"

	// Status bar (1 line). No trailing \n — JoinVertical adds the separator.
	var statusText string
	if m.copyFlash != "" {
		statusText = " " + m.copyFlash
	} else {
		ctxBar := ""
		if m.contextWindow > 0 {
			ctxBar = " " + contextBar(m.contextPct)
		}
		statusText = fmt.Sprintf(" %s/%s │ messages: %d │ ctrl+y copy as md │ ctrl+c quit%s",
			m.provider, m.modelName, len(m.messages), ctxBar)
	}
	status := statusStyle.Width(m.width).Render(statusText)

	// Viewport holds the scrollable chat history
	viewportView := m.viewport.View()

	// Input (1 line)
	inputView := m.input.View()

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		viewportView,
		status,
		inputView,
	)

	v := tea.NewView(body)
	v.AltScreen = true
	// Position the terminal cursor at the textinput's location.
	// textinput.Cursor() returns Y=0 (relative to the widget), so we
	// offset it to the input line, which is the last line of the view.
	if !m.input.VirtualCursor() {
		if c := m.input.Cursor(); c != nil {
			c.Y = m.height - 1
			v.Cursor = c
		}
	}
	return v
}

// contextBar returns a 10-segment bar showing fill percentage.
func contextBar(pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	segments := 10
	filled := (pct*segments + 50) / 100 // round to nearest
	if filled == 0 && pct > 0 {
		filled = 1 // show at least one segment for non-zero
	}
	if filled > segments {
		filled = segments
	}
	empty := segments - filled
	if filled >= 8 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("█", filled), strings.Repeat("░", empty), pct)
	}
	if filled >= 5 {
		return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("▓", filled), strings.Repeat("░", empty), pct)
	}
	return fmt.Sprintf("[%s%s %d%%]", strings.Repeat("░", empty), strings.Repeat("█", filled), pct)
}

// HandleContextInfo updates the context window display.
func (m *Model) HandleContextInfo(tokens, window int) {
	m.contextTokens = tokens
	m.contextWindow = window
	if window > 0 {
		m.contextPct = tokens * 100 / window
		if m.contextPct > 100 {
			m.contextPct = 100
		}
	}
}
