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
	"charm.land/lipgloss/v2/list"
	"charm.land/lipgloss/v2/table"
	"charm.land/lipgloss/v2/tree"

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

	listBulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("99")).
			MarginRight(1)

	listItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	treeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	treeItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	commandPaletteStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("99")).
				Padding(0, 1)

	commandNameStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39")).
				Width(12)

	commandDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("243"))
)

// Message represents a chat message in the TUI.
type Message struct {
	Role     string
	Content  string // glamour-rendered for assistant, raw for others
	Raw      string // original markdown (for copy), same as Content for user/tool
	ToolName string // tool that produced this message (for tool result messages)
}

// AgentMsg is a message from the agent goroutine.
type AgentMsg struct {
	Token          string
	Thinking       string // reasoning/thinking content from models like DeepSeek
	ToolName       string
	ToolResult     string // tool result content
	ToolResultName string // tool name for the result
	Flush          string // streamed content to commit before a tool call
	Done           bool
	Response       string
	Err            error
	ContextTokens  int               // estimated tokens used, for status bar
	ContextWindow  int               // context window size, for status bar
	ModelList      []string          // models fetched from providers
	ProviderNames  map[string]string // provider key → display name
}

// Command represents a slash command available in the TUI.
type Command struct {
	Name        string
	Description string
}

// defaultCommands lists the built-in slash commands.
var defaultCommands = []Command{
	{Name: "/help", Description: "Show available commands"},
	{Name: "/clear", Description: "Clear chat history"},
	{Name: "/compact", Description: "Summarize old messages"},
	{Name: "/model", Description: "Switch model"},
	{Name: "/quit", Description: "Exit the TUI"},
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
	onCompact     func()
	onModel       func(string, string)
	commandMode   bool              // true when input starts with "/"
	commands      []Command         // registered slash commands
	modelMode     bool              // true when in model-selection sub-mode
	modelItems    []string          // available models in "provider/model" format
	modelSelected int               // highlighted index in filtered list
	providerNames map[string]string // provider key → display name
}

// clearCopyFlashMsg clears the copy flash indicator after a timeout.
type clearCopyFlashMsg struct{}

// New creates a new TUI model.
func New(provider, model string, contextWindow int, onSubmit func(string), onQuit func(), onCompact func(), onModel func(string, string)) *Model {
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
		onCompact:     onCompact,
		onModel:       onModel,
		commands:      defaultCommands,
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
	mdLinkRe   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	autoLinkRe = regexp.MustCompile(`<((?:https?|ftp)://[^>]+)>`)
	bareURLRe  = regexp.MustCompile(`(?m)(?:^|\s)((?:https?)://\S+)`)
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

// splitRow splits a pipe-delimited table row into trimmed columns.
func splitRow(line string) []string {
	line = strings.Trim(line, "| \t")
	cols := strings.Split(line, "|")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	return cols
}

// renderCompactTable renders a markdown table using lipgloss's table package.
func renderCompactTable(md string) string {
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) < 2 {
		return md
	}

	var headers []string
	var data [][]string
	var pastHeader bool

	for _, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cols := splitRow(line)
		if strings.Contains(line, "---") {
			pastHeader = true
			continue
		}
		if !pastHeader {
			headers = cols
			pastHeader = true
		} else {
			styled := make([]string, len(cols))
			for i, c := range cols {
				styled[i] = renderInlineMarkdown(c)
			}
			data = append(data, styled)
		}
	}

	if len(headers) == 0 {
		return md
	}

	styledHeaders := make([]string, len(headers))
	for i, h := range headers {
		styledHeaders[i] = renderInlineMarkdown(h)
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	cellStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		}).
		Headers(styledHeaders...).
		Rows(data...)

	return t.String()
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

// --- list and tree rendering ---

// renderToolResult renders tool result content, detecting lists and trees.
func (m *Model) renderToolResult(toolName, content string) string {
	if content == "" {
		return ""
	}
	if isTreeContent(content) {
		return m.renderTree(content)
	}
	if isListContent(content) {
		return m.renderList(content)
	}
	return toolStyle.Render(content)
}

// bulletPattern matches markdown bullet list items (* item, - item, + item).
var bulletPattern = regexp.MustCompile(`(?m)^[*\-+]\s`)

// isListContent detects if content contains bullet list items.
func isListContent(s string) bool {
	return strings.Contains(s, "\n") && bulletPattern.MatchString(s)
}

var treeLineRe = regexp.MustCompile(`[├└]──`)

// isTreeContent detects tree-like content with box-drawing characters.
func isTreeContent(s string) bool {
	return treeLineRe.MatchString(s)
}

// renderList renders bullet-list content using lipgloss's list package.
func (m *Model) renderList(md string) string {
	lines := strings.Split(md, "\n")
	var items []string
	var current strings.Builder

	flushCurrent := func() {
		if current.Len() > 0 {
			items = append(items, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if bulletPattern.MatchString(trimmed) {
			flushCurrent()
			content := bulletPattern.ReplaceAllString(trimmed, "")
			current.WriteString(content)
		} else if trimmed == "" {
			flushCurrent()
			items = append(items, "")
		} else if current.Len() > 0 {
			current.WriteString(" ")
			current.WriteString(trimmed)
		} else {
			flushCurrent()
			items = append(items, trimmed)
		}
	}
	flushCurrent()

	var nonEmpty []string
	for _, item := range items {
		if item != "" {
			nonEmpty = append(nonEmpty, item)
		}
	}
	if len(nonEmpty) == 0 {
		return toolStyle.Render(md)
	}

	l := list.New()
	for _, item := range nonEmpty {
		l.Item(item)
	}
	l.EnumeratorStyle(listBulletStyle).ItemStyle(listItemStyle)
	return l.String()
}

// renderTree renders tree-like content using lipgloss's tree package.
func (m *Model) renderTree(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")

	var rootName string
	var start int
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, rootName = splitTreePrefix(line)
		start = i + 1
		break
	}
	if rootName == "" {
		return toolStyle.Render(content)
	}

	t := tree.New().Root(rootName).
		Enumerator(tree.RoundedEnumerator).
		EnumeratorStyle(treeStyle).
		RootStyle(treeItemStyle).
		ItemStyle(treeItemStyle)
	stack := []*tree.Tree{t}

	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		prefix, name := splitTreePrefix(line)
		depth := treeDepth(prefix)

		for len(stack) > depth {
			stack = stack[:len(stack)-1]
		}

		parent := stack[len(stack)-1]
		node := tree.New().Root(name).
			Enumerator(tree.RoundedEnumerator).
			EnumeratorStyle(treeStyle).
			ItemStyle(treeItemStyle)
		parent.Child(node)
		stack = append(stack, node)
	}

	return t.String()
}

// splitTreePrefix separates tree-drawing characters from the node name.
func splitTreePrefix(line string) (prefix, name string) {
	treeChars := map[rune]bool{'│': true, ' ': true, '├': true, '└': true, '─': true, '┬': true}
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		r := runes[i]
		if !treeChars[r] {
			if r == '\\' && i+1 < len(runes) {
				i += 2
				continue
			}
			break
		}
		i++
	}
	return string(runes[:i]), strings.TrimSpace(string(runes[i:]))
}

// treeDepth computes the depth from tree-drawing prefix characters.
func treeDepth(prefix string) int {
	depth := 0
	for _, r := range prefix {
		if r == '│' || r == '├' || r == '└' {
			depth++
		}
	}
	return depth
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

// AddToolResult adds a tool result message. For todowrite, it renders the
// formatted todo list. For other tools, it shows the raw result.
func (m *Model) AddToolResult(toolName, content string) {
	m.messages = append(m.messages, Message{
		Role:     "tool",
		Content:  m.renderToolResult(toolName, content),
		Raw:      content,
		ToolName: toolName,
	})
	m.refreshViewport()
	m.scrollToBottom()
}

// executeCommand executes a slash command and adds the result to messages.
// /quit is handled by the caller (returns tea.Quit from Update).
func (m *Model) executeCommand(input string) {
	cmd := strings.TrimSpace(input)
	switch cmd {
	case "/help":
		var b strings.Builder
		b.WriteString("Available commands:\n")
		for _, c := range m.commands {
			b.WriteString(fmt.Sprintf("  %s  %s\n", c.Name, c.Description))
		}
		m.AddMessage("system", b.String())
	case "/clear":
		m.messages = nil
		m.refreshViewport()
	case "/compact":
		if m.onCompact != nil {
			m.onCompact()
		}
	case "/model":
		if len(m.modelItems) == 0 {
			m.AddMessage("system", "No models available. Configure providers or wait for model list to load.")
			return
		}
		m.modelMode = true
		m.modelSelected = 0
		m.input.SetValue("")
		m.input.Placeholder = "Search models..."
		m.clearCommandMode()
		m.adjustViewport()
		return
	default:
		m.AddMessage("system", fmt.Sprintf("Unknown command: %s", cmd))
	}
}

// updateCommandSuggestions updates the textinput suggestions for command mode.
func (m *Model) updateCommandSuggestions() {
	var names []string
	for _, c := range m.commands {
		names = append(names, c.Name)
	}
	m.input.SetSuggestions(names)
}

// selectModel applies the currently highlighted model and exits model mode.
func (m *Model) selectModel() {
	filtered := m.filteredModels()
	if m.modelSelected < len(filtered) {
		selected := filtered[m.modelSelected]
		parts := strings.SplitN(selected, "/", 2)
		providerName := parts[0]
		modelName := selected
		if len(parts) == 2 {
			modelName = parts[1]
		}
		m.provider = providerName
		m.modelName = modelName
		if m.onModel != nil {
			m.onModel(providerName, modelName)
		}
	}
	m.exitModelMode()
}

// filteredModels returns modelItems filtered by the current input value.
func (m *Model) filteredModels() []string {
	filter := strings.ToLower(m.input.Value())
	if filter == "" {
		return m.modelItems
	}
	var out []string
	for _, model := range m.modelItems {
		if strings.Contains(strings.ToLower(model), filter) {
			out = append(out, model)
		}
	}
	return out
}

// exitModelMode exits model-selection mode and resets the input.
func (m *Model) exitModelMode() {
	m.modelMode = false
	m.modelSelected = 0
	m.input.SetValue("")
	m.input.Placeholder = "Type a message..."
	m.adjustViewport()
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

	if msg.ToolResult != "" || msg.ToolResultName != "" {
		m.AddToolResult(msg.ToolResultName, msg.ToolResult)
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

	if len(msg.ModelList) > 0 {
		m.modelItems = msg.ModelList
		if msg.ProviderNames != nil {
			m.providerNames = msg.ProviderNames
		}
		m.refreshViewport()
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
		m.adjustViewport()
		return m, nil

	case tea.MouseMsg:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.onQuit != nil {
				m.onQuit()
			}
			return m, tea.Quit

		case "esc":
			if m.modelMode {
				m.exitModelMode()
				return m, nil
			}
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
			if m.modelMode {
				m.selectModel()
				return m, nil
			}
			if m.commandMode {
				value := m.input.Value()
				m.input.SetValue("")
				m.clearCommandMode()
				if strings.TrimSpace(value) == "/quit" {
					return m, tea.Quit
				}
				m.executeCommand(value)
				return m, nil
			}
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

		case "up":
			if m.modelMode {
				filtered := m.filteredModels()
				if m.modelSelected > 0 {
					m.modelSelected--
				}
				_ = filtered
				return m, nil
			}
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			return m, vpCmd

		case "down":
			if m.modelMode {
				filtered := m.filteredModels()
				if m.modelSelected < len(filtered)-1 {
					m.modelSelected++
				}
				_ = filtered
				return m, nil
			}
			var vpCmd2 tea.Cmd
			m.viewport, vpCmd2 = m.viewport.Update(msg)
			return m, vpCmd2

		case "pgup", "pgdown", "home", "end":
			var vpCmd3 tea.Cmd
			m.viewport, vpCmd3 = m.viewport.Update(msg)
			return m, vpCmd3

		}

	}

	m.input, cmd = m.input.Update(msg)
	m.detectCommandMode()
	return m, cmd
}

// detectCommandMode enables or disables command mode based on the input prefix.
func (m *Model) detectCommandMode() {
	if m.modelMode {
		return
	}
	if strings.HasPrefix(m.input.Value(), "/") {
		if !m.commandMode {
			m.commandMode = true
			m.input.ShowSuggestions = true
			m.updateCommandSuggestions()
			m.adjustViewport()
		}
	} else {
		if m.commandMode {
			m.clearCommandMode()
		}
	}
}

func (m *Model) clearCommandMode() {
	m.commandMode = false
	m.input.ShowSuggestions = false
	m.input.SetSuggestions(nil)
	m.adjustViewport()
}

// paletteLines returns the number of terminal rows the command palette
// occupies when visible. Includes the rounded border (2) and padding (2).
// maxModelLines returns the maximum number of model items that can fit
// in the terminal without pushing the input off-screen.
func (m *Model) maxModelLines() int {
	if m.height == 0 {
		return 10
	}
	available := m.height - m.headerHeight() - 4 // status, input, border/padding
	items := available - 4                       // border (2) + padding (2)
	if items < 1 {
		items = 1
	}
	return items
}

func (m *Model) paletteLines() int {
	if m.modelMode {
		filtered := m.filteredModels()
		if len(filtered) == 0 {
			return 4
		}
		// Count display rows: model items + provider heading rows
		rowCount := len(filtered)
		providers := make(map[string]bool)
		for _, model := range filtered {
			parts := strings.SplitN(model, "/", 2)
			providers[parts[0]] = true
		}
		rowCount += len(providers)
		if rowCount > m.maxModelLines() {
			rowCount = m.maxModelLines()
		}
		return 4 + rowCount
	}
	if !m.commandMode {
		return 0
	}
	filter := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), "/")
	filter = strings.ToLower(filter)
	count := 0
	for _, c := range m.commands {
		name := strings.TrimPrefix(c.Name, "/")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return 4 + count // border (2) + padding (2) + command lines
}

// adjustViewport recalculates and applies the viewport height based on
// current terminal dimensions and command mode state.
func (m *Model) adjustViewport() {
	if m.height == 0 {
		return
	}
	chatHeight := m.height - m.headerHeight() - 2
	if m.commandMode || m.modelMode {
		chatHeight -= m.paletteLines()
	}
	if chatHeight < 5 {
		chatHeight = 5
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(chatHeight)
	m.refreshViewport()
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
			if msg.ToolName != "" && (isListContent(msg.Raw) || isTreeContent(msg.Raw)) {
				b.WriteString(msg.Content)
				b.WriteString("\n")
			} else {
				rendered := toolStyle.Render(chatWrap("", msg.Content, m.width))
				b.WriteString(rendered)
				b.WriteString("\n")
			}

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

// renderModelPalette renders the model selection list above the input.
func (m *Model) renderModelPalette() string {
	filtered := m.filteredModels()
	if len(filtered) == 0 {
		return commandPaletteStyle.Render("No matching models")
	}

	// Build display rows: provider heading + model items
	type row struct {
		isHeading bool
		text      string
		modelIdx  int
	}
	var rows []row
	lastProvider := ""
	for i, model := range filtered {
		parts := strings.SplitN(model, "/", 2)
		providerKey := parts[0]
		name := model
		if len(parts) == 2 {
			name = parts[1]
		}
		if providerKey != lastProvider {
			displayName := providerKey
			if m.providerNames != nil {
				if dn, ok := m.providerNames[providerKey]; ok && dn != "" {
					displayName = dn
				}
			}
			rows = append(rows, row{isHeading: true, text: displayName})
			lastProvider = providerKey
		}
		rows = append(rows, row{text: name, modelIdx: i})
	}

	// Find the display row index for the selected model
	selectedRowIdx := 0
	for i, r := range rows {
		if r.modelIdx == m.modelSelected {
			selectedRowIdx = i
			break
		}
	}

	// Window calculation over display rows
	maxVisible := m.maxModelLines()
	start := selectedRowIdx - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(rows) {
		end = len(rows)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	current := m.provider + "/" + m.modelName
	var lines []string
	for i := start; i < end; i++ {
		r := rows[i]
		if r.isHeading {
			lines = append(lines, commandNameStyle.Render(r.text))
			continue
		}
		model := filtered[r.modelIdx]
		marker := "  "
		if r.modelIdx == m.modelSelected {
			marker = "> "
		}
		styled := listItemStyle.Render(r.text)
		if model == current {
			styled = commandNameStyle.Render(r.text + " (current)")
		}
		lines = append(lines, "  "+marker+styled)
	}

	return commandPaletteStyle.Render(strings.Join(lines, "\n"))
}

// renderCommandPalette renders the command suggestion list above the input.
func (m *Model) renderCommandPalette() string {
	filter := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), "/")
	filter = strings.ToLower(filter)

	var visible []Command
	for _, c := range m.commands {
		name := strings.TrimPrefix(c.Name, "/")
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			visible = append(visible, c)
		}
	}

	var lines []string
	for _, c := range visible {
		name := commandNameStyle.Render(c.Name)
		desc := commandDescStyle.Render(c.Description)
		lines = append(lines, name+" "+desc)
	}

	if len(lines) == 0 {
		return ""
	}

	return commandPaletteStyle.Render(strings.Join(lines, "\n"))
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

	// Palette (shown above input when in command or model mode)
	var palette string
	if m.modelMode {
		palette = m.renderModelPalette()
	} else if m.commandMode {
		palette = m.renderCommandPalette()
	}

	// Input (1 line)
	inputView := m.input.View()

	elements := []string{header, viewportView, status}
	if palette != "" {
		elements = append(elements, palette)
	}
	elements = append(elements, inputView)
	body := lipgloss.JoinVertical(lipgloss.Left, elements...)

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
