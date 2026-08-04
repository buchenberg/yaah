package tui2

import (
	"strings"

	itodo "github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tui2/components/approval"
	"github.com/buchenberg/yaah/internal/tui2/components/banner"
	"github.com/buchenberg/yaah/internal/tui2/components/command"
	"github.com/buchenberg/yaah/internal/tui2/components/help"
	"github.com/buchenberg/yaah/internal/tui2/components/infobar"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/input"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
	"github.com/buchenberg/yaah/internal/tui2/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui2/components/provider"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/separator"
	"github.com/buchenberg/yaah/internal/tui2/components/statusbar"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/thinking"
	"github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI2 is the tview-based terminal UI prototype.
type TUI2 struct {
	App   *tview.Application
	Pages *tview.Pages // root for modal overlays
	Root  *tview.Flex  // main content flex (a page within Pages)

	// Header sub-components
	Banner       *tview.TextView
	BannerSep    *tview.TextView // dim separator rule below the tagline
	ProviderInfo *tview.TextView

	// Body components
	InfoBar  *tview.TextView
	Messages *tview.TextView
	Input    *tview.TextArea
	InfoPane *tview.TextView // right-side info panel

	// Footer
	StatusBar *tview.TextView

	// Layout containers
	Header *tview.Grid

	// State
	McpServers []mcpinfo.Server
	PlanInfo   string
	StatusInfo string

	// Message blocks
	reasoningBlocks []*reasoning.Block
	toolBlocks      []*toolblock.Block
	subagentBlocks  []*subagent.Block
	thinkingInd     *thinking.Indicator

	// Plain text messages (non-block content)
	plainMessages []string
}

// New creates a new TUI2 application.
func New() *TUI2 {
	t := &TUI2{
		App:         tview.NewApplication(),
		thinkingInd: thinking.New("Reasoning..."),
	}
	t.buildUI()

	// Populate sample data for visual testing
	t.populateSampleData()

	return t
}

// Run starts the tview event loop.
func (t *TUI2) Run() error {
	return t.App.Run()
}

// Stop gracefully shuts down the TUI.
func (t *TUI2) Stop() {
	t.App.Stop()
}

// buildUI wires the component tree and layout.
func (t *TUI2) buildUI() {
	// --- Leaf primitives (from sub-packages) ---
	var bannerLines int
	bannerLines, t.Banner = banner.Build()
	t.ProviderInfo = provider.Build()
	t.InfoBar, t.PlanInfo = infobar.Build()
	t.Messages = messages.Build()
	t.Input = input.Build()
	t.InfoPane = infopane.Build()
	t.StatusBar, t.StatusInfo = statusbar.Build()

	// --- Header: two-column grid (banner left | provider right) ---
	headerRows := bannerLines
	if headerRows < 10 {
		headerRows = 10
	}

	// One extra row for the bottom separator
	totalRows := headerRows + 1
	rows := make([]int, totalRows)
	for i := range rows {
		rows[i] = 1
	}

	t.Header = tview.NewGrid().
		SetRows(rows...).
		SetColumns(-2, -1, -1). // -X = proportional (2:1:1 → left 2/3, right 1/3)
		SetBorders(false)
	t.Header.SetBorder(true).
		SetBorderColor(tcell.ColorGray).
		SetTitle(" Header ")

	// Banner spans all content rows in col 0 (left portion of width)
	t.Header.AddItem(t.Banner, 0, 0, headerRows, 1, 0, 0, false)

	// Provider info: cols 1-2, all rows (MCP moved to InfoPane)
	t.Header.AddItem(t.ProviderInfo, 0, 1, headerRows, 2, 0, 0, false)

	// Dim separator rule below the tagline (own grid row, no newlines)
	t.BannerSep = separator.Build()
	t.Header.AddItem(t.BannerSep, headerRows, 0, 1, 3, 0, 0, false)

	// --- Body: horizontal split — messages+input (3/4) | infopane (1/4) ---
	bodyLeft := tview.NewFlex().
		SetDirection(tview.FlexRow)
	bodyLeft.AddItem(t.Messages, 0, 1, false) // messages fills (no focus)
	bodyLeft.AddItem(t.Input, 3, 0, true)     // input fixed 3 (focused)

	body := tview.NewFlex().
		SetDirection(tview.FlexColumn)
	body.AddItem(bodyLeft, 0, 5, true)    // ~85% width, focus → bodyLeft → Input
	body.AddItem(t.InfoPane, 0, 1, false) // ~15% width

	// --- Overall layout: header-sticky-top / body-fills / footer-sticky-bottom ---
	t.Root = tview.NewFlex().
		SetDirection(tview.FlexRow).
		SetFullScreen(true)

	t.Root.AddItem(t.Header, totalRows, 0, false) // header: dynamic, no grow
	t.Root.AddItem(t.InfoBar, 1, 0, false)        // infobar: fixed 1
	t.Root.AddItem(body, 0, 1, true)              // body: fills remaining, focus → Input
	t.Root.AddItem(t.StatusBar, 1, 0, false)      // footer: fixed 1

	// --- Application setup ---
	t.Pages = tview.NewPages()
	t.Pages.AddPage("main", t.Root, true, true)
	t.App.SetRoot(t.Pages, true).
		EnableMouse(true).
		EnablePaste(true)

	// Global keybindings
	t.App.SetInputCapture(t.globalInputCapture)

	// Style the application
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorYellow
}

// refreshMessages rebuilds the messages TextView content from all
// blocks and plain messages. Call after any state change.
func (t *TUI2) refreshMessages() {
	var b strings.Builder

	// Plain messages first.
	for _, m := range t.plainMessages {
		b.WriteString(m)
		b.WriteString("\n")
	}

	// Thinking indicator.
	if t.thinkingInd.Visible() {
		b.WriteString(t.thinkingInd.Render())
		b.WriteString("\n")
	}

	// Reasoning blocks.
	for _, rb := range t.reasoningBlocks {
		b.WriteString(rb.Render())
		b.WriteString("\n")
	}

	// Tool blocks.
	for _, tb := range t.toolBlocks {
		b.WriteString(tb.Render())
		b.WriteString("\n")
	}

	// Sub-agent blocks.
	for _, sb := range t.subagentBlocks {
		b.WriteString(sb.Render())
		b.WriteString("\n")
	}

	t.Messages.SetText(b.String())
}

// AddReasoningBlock creates a reasoning block.
func (t *TUI2) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0)
	t.reasoningBlocks = append(t.reasoningBlocks, rb)
	t.refreshMessages()
}

// AddToolError transitions a tool block to Error.
func (t *TUI2) AddToolError(id, summary, err string) {
	for _, tb := range t.toolBlocks {
		if tb.ID() == id {
			tb.Fail(summary, err)
			t.refreshMessages()
			return
		}
	}
}

// ToggleBlockByIndex finds the nth block (of any type) and toggles it.
func (t *TUI2) ToggleBlockByIndex(n int) {
	// Flatten all blocks in render order.
	type block interface {
		Toggle()
		IsExpanded() bool
	}

	blocks := []block{}
	for _, rb := range t.reasoningBlocks {
		blocks = append(blocks, rb)
	}
	for _, tb := range t.toolBlocks {
		blocks = append(blocks, tb)
	}
	for _, sb := range t.subagentBlocks {
		blocks = append(blocks, sb)
	}

	if n >= 0 && n < len(blocks) {
		blocks[n].Toggle()
		t.refreshMessages()
	}
}

// CollapseAll collapses all expandable blocks.
func (t *TUI2) CollapseAll() {
	for _, rb := range t.reasoningBlocks {
		for rb.IsExpanded() {
			rb.Toggle()
		}
	}
	for _, tb := range t.toolBlocks {
		for tb.IsExpanded() {
			tb.Toggle()
		}
	}
	for _, sb := range t.subagentBlocks {
		for sb.IsExpanded() {
			sb.Toggle()
		}
	}
	t.refreshMessages()
}

// ShowThinking shows the animated thinking indicator.
func (t *TUI2) ShowThinking() {
	t.thinkingInd.Show()
	t.refreshMessages()
}

// HideThinking hides the animated thinking indicator.
func (t *TUI2) HideThinking() {
	t.thinkingInd.Hide()
	t.refreshMessages()
}

// AdvanceThinking advances the thinking spinner and lolcat seed.
func (t *TUI2) AdvanceThinking() {
	if t.thinkingInd.Visible() {
		t.thinkingInd.Advance()
		t.refreshMessages()
	}
}

// AdvanceReasoningSeeds advances lolcat seeds for all reasoning blocks.
func (t *TUI2) AdvanceReasoningSeeds(seed float64) {
	for _, rb := range t.reasoningBlocks {
		rb.SetSeed(seed)
	}
	t.refreshMessages()
}

// BlinkSubAgents toggles blink visibility for all active sub-agent blocks.
func (t *TUI2) BlinkSubAgents() {
	needsRefresh := false
	for _, sb := range t.subagentBlocks {
		if sb.S() == subagent.Active {
			sb.ToggleBlink()
			needsRefresh = true
		}
	}
	if needsRefresh {
		t.refreshMessages()
	}
}

// globalInputCapture handles global keybindings (before tview routing).
func (t *TUI2) globalInputCapture(ev *tcell.EventKey) *tcell.EventKey {
	action := Translate(ev, DefaultBindings())
	switch action {
	case ActionQuit:
		t.Stop()
		return nil
	case ActionHelp:
		help.Show(t.App, t.Pages, bindingsToHelpBindings(DefaultBindings()))
		return nil
	case ActionCommand:
		// Show command palette component when wired.
		return nil
	case ActionClear:
		t.plainMessages = nil
		t.reasoningBlocks = nil
		t.toolBlocks = nil
		t.subagentBlocks = nil
		t.refreshMessages()
		return nil
	case ActionToggleReasoning:
		for _, rb := range t.reasoningBlocks {
			rb.Toggle()
		}
		t.refreshMessages()
		return nil
	case ActionToggleTools:
		for _, tb := range t.toolBlocks {
			tb.Toggle()
		}
		t.refreshMessages()
		return nil
	case ActionToggleSubAgents:
		for _, sb := range t.subagentBlocks {
			sb.Toggle()
		}
		t.refreshMessages()
		return nil
	}
	return ev
}

// --- Modal dispatchers (called from the agent frame) ---

// ShowQuestion displays a question modal and returns answers via channel.
func (t *TUI2) ShowQuestion(header, questionText string, opts []struct{ Label, Description string }, multiple bool, onAnswer func(question.Answer)) {
	question.Show(t.App, t.Pages, header, questionText, opts, multiple, onAnswer)
}

// ShowApproval displays an approval modal and returns via callback.
func (t *TUI2) ShowApproval(name, args string, onAnswer func(bool)) {
	approval.Show(t.App, t.Pages, name, args, onAnswer)
}

// ShowModelPicker displays a model picker modal.
func (t *TUI2) ShowModelPicker(models []string, providerNames map[string]string, onSelect func(string)) {
	modelpicker.Show(t.App, t.Pages, models, providerNames, onSelect)
}

// HandleCommand dispatches a colon command.
func (t *TUI2) HandleCommand(cmd command.Cmd, arg string) {
	switch cmd {
	case command.CmdQuit:
		t.Stop()
	case command.CmdClear:
		t.plainMessages = nil
		t.reasoningBlocks = nil
		t.toolBlocks = nil
		t.subagentBlocks = nil
		t.refreshMessages()
	case command.CmdHelp:
		help.Show(t.App, t.Pages, bindingsToHelpBindings(DefaultBindings()))
	case command.CmdCompact:
		t.CollapseAll()
	default:
		// Other commands handled by the agent frame.
	}
}

// UpdateTodos updates the TODO list in the infopane.
func (t *TUI2) UpdateTodos(items []itodo.Item) {
	t.InfoPane.SetText(todo.FormatList(items))
}

// UpdateInfopane sets a specific infopane tab content.
// tab is one of: "subagent", "todos", "context", "mcp".
func (t *TUI2) UpdateInfopane(tab, content string) {
	_ = tab // TODO: wire infopane tabs
	t.InfoPane.SetText(content)
}

// bindingsToHelpBindings converts internal bindings to the help package's format.
func bindingsToHelpBindings(bindings []Binding) []help.Binding {
	var out []help.Binding
	for _, b := range bindings {
		out = append(out, help.Binding{Label: b.Label, HelpText: b.HelpText})
	}
	return out
}
