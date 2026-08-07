package tui2

import (
	"strings"
	"sync/atomic"
	"time"

	itodo "github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/approval"
	"github.com/buchenberg/yaah/internal/tui2/components/banner"
	"github.com/buchenberg/yaah/internal/tui2/components/command"
	"github.com/buchenberg/yaah/internal/tui2/components/infobar"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/input"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
	"github.com/buchenberg/yaah/internal/tui2/components/modelpicker"
	"github.com/buchenberg/yaah/internal/tui2/components/question"
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/statusbar"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/thinking"
	"github.com/buchenberg/yaah/internal/tui2/components/todo"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI2 is the tview-based terminal UI prototype.
type TUI2 struct {
	App   *tview.Application
	Pages *tview.Pages // root for modal overlays
	Root  *tview.Flex  // main content flex (a page within Pages)

	// Theme controls all visual styling.
	Theme *colors.Theme

	// Header sub-components
	Banner *tview.TextView

	// Body components
	InfoBar  *tview.TextView
	Messages *tview.TextView
	Input    *tview.TextArea
	InfoPane *tview.TextView // right-side info panel
	TodoPane *tview.TextView // right-side todo list

	// Footer
	StatusBar *tview.TextView

	// Layout containers
	Header *tview.Grid

	// State
	McpServers []mcpinfo.Server

	// Conversation log — a single ordered list of renderable items so
	// tool calls, sub-agent blocks, and text render chronologically
	// (interleaved) instead of grouped by type.
	conversationLog []convItem

	// Message blocks (used for toggle/collapse operations)
	reasoningBlocks []*reasoning.Block
	toolBlocks      []*toolblock.Block
	subagentBlocks  []*subagent.Block
	thinkingInd     *thinking.Indicator

	// Plain text messages (non-block content)
	plainMessages []string

	focus focusState

	// --- Agent callbacks (set by cmd/yaah before Run) ---
	OnSubmit  func(prompt string)
	OnAbort   func()
	OnCompact func()
	OnClear   func()

	// --- Control channel (fed by agent frame) ---
	ControlCh <-chan types.CtrlMsg

	// --- Streaming / agent state ---
	pendingTokens string
	pendingThink  string
	pendingTool   string
	isStreaming   atomic.Bool
	compacting    bool
	contextTokens int
	contextWindow int
	lastProvider  string
	lastModel     string
	version       string
	thinkingLabel string
	todoItems     []itodo.Item

	// Model picker state
	availableModels []string
	providerNames   map[string]string

	// CmdPalette is the vim-style ":" command input.
	CmdPalette *command.Palette

	// OnModelSelect is called when a model is chosen from the picker.
	OnModelSelect func(model string)
}

// New creates a new TUI2 application.
func New() *TUI2 {
	th := colors.DetectTheme()
	t := &TUI2{
		App:         tview.NewApplication(),
		Theme:       &th,
		thinkingInd: thinking.New("Reasoning..."),
		version:     "yaah",
	}
	t.buildUI()
	return t
}

// Run starts the tview event loop.
func (t *TUI2) Run() error {
	t.startControlLoop()
	t.startSpinnerTicker()
	t.App.SetFocus(t.Input)
	t.renderInfoPane()
	t.renderTodoPane()
	return t.App.Run()
}

// Stop gracefully shuts down the TUI.
func (t *TUI2) Stop() {
	t.App.Stop()
}

// SetProvider sets the provider display name and refreshes the info pane.
func (t *TUI2) SetProvider(name string) {
	t.lastProvider = name
	t.renderInfoPane()
}

// SetModel sets the model display name and refreshes the info pane.
func (t *TUI2) SetModel(name string) {
	t.lastModel = name
	t.renderInfoPane()
}

func (t *TUI2) startControlLoop() {
	go func() {
		for msg := range t.ControlCh {
			t.App.QueueUpdateDraw(func() {
				t.handleControlMsg(msg)
			})
		}
	}()
}

func (t *TUI2) startSpinnerTicker() {
	go func() {
		for range time.Tick(200 * time.Millisecond) {
			// Run the ticker when the thinking spinner is visible OR any
			// sub-agent block is active.
			anyActive := t.thinkingInd.Visible()
			if !anyActive {
				for _, sb := range t.subagentBlocks {
					if sb.S() == subagent.Active {
						anyActive = true
						break
					}
				}
			}
			if !anyActive {
				continue
			}
			t.App.QueueUpdateDraw(func() {
				if t.thinkingInd.Visible() {
					t.thinkingInd.Advance()
				}
				for _, sb := range t.subagentBlocks {
					sb.AdvanceSpinner()
				}
				t.refreshMessages()
			})
		}
	}()
}

// buildUI wires the component tree and layout.
func (t *TUI2) buildUI() {
	// --- Leaf primitives (from sub-packages) ---
	var bannerLines int
	bannerLines, t.Banner = banner.Build()
	t.InfoBar, _ = infobar.Build()
	t.Messages = messages.Build()
	t.Input = input.Build(t.Theme)
	t.InfoPane = infopane.Build()
	t.StatusBar, _ = statusbar.Build()

	// Command palette (vim-style ":" input, shown as a Pages overlay).
	cmdModalName := "cmdpalette_modal"
	t.CmdPalette = command.Build(func(cmd command.Cmd, arg string) {
		t.HandleCommand(cmd, arg)
		t.App.QueueUpdateDraw(func() {
			t.Pages.RemovePage(cmdModalName)
			t.App.SetFocus(t.Input)
		})
	})
	t.TodoPane = todo.Build(nil)

	// --- Header: two-column grid (banner left | provider right) ---
	// Size the header to exactly the banner height — no minimum floor.
	headerRows := bannerLines

	totalRows := headerRows
	rows := make([]int, totalRows)
	for i := range rows {
		rows[i] = 1
	}

	t.Header = tview.NewGrid().
		SetRows(rows...).
		SetColumns(-1). // full-width banner
		SetBorders(false)

	// Banner spans all content rows
	t.Header.AddItem(t.Banner, 0, 0, headerRows, 1, 0, 0, false)

	// --- Body: horizontal split — messages (4/5) | infopane+todos (1/5) ---
	messagesCol := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.Messages, 0, 1, false)

	rightPane := tview.NewFlex().
		SetDirection(tview.FlexRow)
	rightPane.AddItem(t.InfoPane, 0, 3, false)
	rightPane.AddItem(t.TodoPane, 0, 1, false)

	body := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(messagesCol, 0, 4, true). // ~80% width
		AddItem(rightPane, 0, 1, false)   // ~20% width

	// --- Overall layout: header / infobar / body / input / footer ---
	t.Root = tview.NewFlex().
		SetDirection(tview.FlexRow).
		SetFullScreen(true)

	t.Root.AddItem(t.Header, totalRows, 0, false) // header: dynamic, no grow
	t.Root.AddItem(t.InfoBar, 1, 0, false)        // infobar: fixed 1
	t.Root.AddItem(body, 0, 1, true)              // body: fills remaining
	t.Root.AddItem(t.Input, 3, 0, false)          // input: full-width footer
	t.Root.AddItem(t.StatusBar, 1, 0, false)      // footer: fixed 1

	// --- Application setup ---
	t.Pages = tview.NewPages()
	t.Pages.AddPage("main", t.Root, true, true)
	t.App.SetRoot(t.Pages, true).EnableMouse(true)

	// Global keybindings
	t.App.SetInputCapture(t.globalInputCapture)

	// Style the application
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorYellow
}

// convItem is a single entry in the chronological conversation log.
// Only one of the fields is set.
type convItem struct {
	text           string           // plain text (user messages, system notices)
	rawMarkdown    string           // raw markdown (assistant responses)
	toolBlock      *toolblock.Block // tool call block
	subBlock       *subagent.Block  // sub-agent block
	reasoningBlock *reasoning.Block // reasoning block (persistent)
}

// refreshMessages rebuilds the messages TextView content from all
// conversation items in chronological order. Call after any state change.
func (t *TUI2) refreshMessages() {
	var b strings.Builder
	w := messageWidth(t.Messages)
	ctx := colors.RenderCtx{Width: w, Theme: t.Theme}

	for _, item := range t.conversationLog {
		switch {
		case item.text != "":
			b.WriteString("\n")
			b.WriteString(item.text)
			b.WriteString("\n\n")
		case item.rawMarkdown != "":
			b.WriteString("\n")
			b.WriteString(renderMarkdown(item.rawMarkdown, w))
			b.WriteString("\n\n")
		case item.toolBlock != nil:
			b.WriteString(item.toolBlock.RenderCtx(ctx))
			b.WriteString("\n")
		case item.subBlock != nil:
			b.WriteString(item.subBlock.RenderCtx(ctx))
			b.WriteString("\n")
		case item.reasoningBlock != nil:
			b.WriteString("\n")
			b.WriteString(item.reasoningBlock.RenderCtx(ctx))
			b.WriteString("\n\n")
		}
	}

	// Streaming text (accumulated tokens, not yet flushed).
	if t.isStreaming.Load() && t.pendingTokens != "" {
		w := messageWidth(t.Messages)
		b.WriteString(renderMarkdown(t.pendingTokens, w))
		b.WriteString("\n")
	}

	// Spinner — inline at the bottom while thinking.
	if t.thinkingInd.Visible() {
		b.WriteString("\n")
		b.WriteString(t.thinkingInd.Render())
		b.WriteString("\n")
	}

	t.Messages.SetText(b.String())
	t.Messages.ScrollToEnd()
}

// AddReasoningBlock creates a reasoning block.
func (t *TUI2) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0, t.Theme)
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
		t.ShowHelp()
		return nil
	case ActionCommand:
		t.toggleCommandPalette()
		return nil
	case ActionClear:
		if t.OnClear != nil {
			t.OnClear()
		}
		t.clearConversation()
		return nil
	case ActionToggleReasoning:
		t.toggleAllReasoning()
		return nil
	case ActionToggleTools:
		t.toggleAllTools()
		return nil
	case ActionToggleSubAgents:
		t.toggleAllSubAgents()
		return nil
	case ActionSend:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		t.submitInput()
		return nil
	case ActionCancel:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		if t.OnAbort != nil {
			t.OnAbort()
		}
		return nil
	}
	return ev
}

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
		t.clearConversation()
	case command.CmdHelp:
		t.ShowHelp()
	case command.CmdCompact:
		t.CollapseAll()
	case command.CmdModel:
		t.ShowModelPicker(t.availableModels, t.providerNames, func(model string) {
			if t.OnModelSelect != nil {
				t.OnModelSelect(model)
			}
		})
	default:
	}
}

// UpdateTodos updates the TODO list in the right panel.
func (t *TUI2) UpdateTodos(items []itodo.Item) {
	t.TodoPane.SetText(todo.FormatList(items))
}

// UpdateInfopane sets a specific infopane tab content.
// tab is one of: "subagent", "todos", "context", "mcp".
func (t *TUI2) UpdateInfopane(tab, content string) {
	t.InfoPane.SetText(content)
}

func (t *TUI2) toggleCommandPalette() {
	const cmdModal = "cmdpalette_modal"
	if t.Pages.HasPage(cmdModal) {
		t.Pages.RemovePage(cmdModal)
		t.App.SetFocus(t.Input)
		t.focus = focusCommandPalette
	} else {
		t.CmdPalette.SetText("")
		t.Pages.AddPage(cmdModal, t.CmdPalette, true, true)
		t.App.SetFocus(t.CmdPalette)
		t.focus = focusCommandPalette
	}
}

func (t *TUI2) submitInput() {
	if t.OnSubmit != nil {
		text := t.Input.GetText()
		if text != "" {
			t.Input.SetText("", false)
			t.OnSubmit(text)
		}
	}
}

func (t *TUI2) clearConversation() {
	t.plainMessages = nil
	t.conversationLog = nil
	t.reasoningBlocks = nil
	t.toolBlocks = nil
	t.subagentBlocks = nil
	t.refreshMessages()
}

func (t *TUI2) toggleAllReasoning() {
	for _, rb := range t.reasoningBlocks {
		rb.Toggle()
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllTools() {
	for _, tb := range t.toolBlocks {
		tb.Toggle()
	}
	t.refreshMessages()
}

func (t *TUI2) toggleAllSubAgents() {
	for _, sb := range t.subagentBlocks {
		sb.Toggle()
	}
	t.refreshMessages()
}

func (t *TUI2) ShowHelp() {
	p := tview.NewPages()
	m := tview.NewModal().
		SetText("Help: TUI2 keybindings").
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(_ int, _ string) {
			t.Pages.RemovePage("help_modal")
			t.App.SetFocus(t.Input)
		})
	p.AddPage("inner", m, true, true)
	t.Pages.AddPage("help_modal", p, true, true)
}

type focusState int

const (
	focusNormal focusState = iota
	focusCommandPalette
	focusModal
)
