package tui2

import (
	"sync/atomic"
	"time"

	itodo "github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tui2/components/banner"
	"github.com/buchenberg/yaah/internal/tui2/components/command"
	"github.com/buchenberg/yaah/internal/tui2/components/infobar"
	"github.com/buchenberg/yaah/internal/tui2/components/infopane"
	"github.com/buchenberg/yaah/internal/tui2/components/input"
	"github.com/buchenberg/yaah/internal/tui2/components/messages"
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
	Pages *tview.Pages
	Root  *tview.Flex

	Banner *tview.TextView

	InfoBar  *tview.TextView
	Messages *tview.TextView
	Input    *tview.TextArea
	InfoPane *tview.TextView
	TodoPane *tview.TextView

	StatusBar *tview.TextView

	Header *tview.Grid

	conversation *Conversation

	reasoningBlocks []*reasoning.Block
	toolBlocks      []*toolblock.Block
	subagentBlocks  []*subagent.Block
	thinkingInd     *thinking.Indicator

	theme *Theme

	OnSubmit  func(prompt string)
	OnAbort   func()
	OnCompact func()
	OnClear   func()

	ControlCh <-chan types.CtrlMsg

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

	availableModels []string
	providerNames   map[string]string

	CmdPalette *command.Palette

	OnModelSelect func(model string)
}

// New creates a new TUI2 application.
func New() *TUI2 {
	th := DetectTheme()
	t := &TUI2{
		App:          tview.NewApplication(),
		thinkingInd:  thinking.New("Reasoning..."),
		conversation: &Conversation{},
		theme:        &th,
		version:      "yaah",
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

func (t *TUI2) Stop() { t.App.Stop() }

func (t *TUI2) SetProvider(name string) {
	t.lastProvider = name
	t.renderInfoPane()
}

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
	var bannerLines int
	bannerLines, t.Banner = banner.Build()
	t.InfoBar, _ = infobar.Build()
	t.Messages = messages.Build()
	t.Input = input.Build()
	t.InfoPane = infopane.Build()
	t.StatusBar, _ = statusbar.Build()

	cmdModalName := "cmdpalette_modal"
	t.CmdPalette = command.Build(func(cmd command.Cmd, arg string) {
		t.HandleCommand(cmd, arg)
		t.App.QueueUpdateDraw(func() {
			t.Pages.RemovePage(cmdModalName)
			t.App.SetFocus(t.Input)
		})
	})
	t.TodoPane = todo.Build(nil)

	headerRows := bannerLines
	totalRows := headerRows
	rows := make([]int, totalRows)
	for i := range rows {
		rows[i] = 1
	}

	t.Header = tview.NewGrid().
		SetRows(rows...).
		SetColumns(-1).
		SetBorders(false)

	t.Header.AddItem(t.Banner, 0, 0, headerRows, 1, 0, 0, false)

	messagesCol := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(t.Messages, 0, 1, false)

	rightPane := tview.NewFlex().
		SetDirection(tview.FlexRow)
	rightPane.AddItem(t.InfoPane, 0, 3, false)
	rightPane.AddItem(t.TodoPane, 0, 1, false)

	body := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(messagesCol, 0, 4, true).
		AddItem(rightPane, 0, 1, false)

	t.Root = tview.NewFlex().
		SetDirection(tview.FlexRow).
		SetFullScreen(true)

	t.Root.AddItem(t.Header, totalRows, 0, false)
	t.Root.AddItem(t.InfoBar, 1, 0, false)
	t.Root.AddItem(body, 0, 1, true)
	t.Root.AddItem(t.Input, 3, 0, false)
	t.Root.AddItem(t.StatusBar, 1, 0, false)

	t.Pages = tview.NewPages()
	t.Pages.AddPage("main", t.Root, true, true)
	t.App.SetRoot(t.Pages, true).EnableMouse(true)

	t.App.SetInputCapture(t.globalInputCapture)

	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorYellow
}

// refreshMessages rebuilds the messages TextView from the conversation viewmodel.
func (t *TUI2) refreshMessages() {
	ctx := newRenderCtx(messageWidth(t.Messages), 0, t.theme)
	var spinner string
	if t.thinkingInd.Visible() {
		spinner = t.thinkingInd.Render()
	}
	t.Messages.SetText(t.conversation.Render(ctx, t.pendingTokens, spinner, t.thinkingInd.Visible()))
	t.Messages.ScrollToEnd()
}

// AddReasoningBlock creates a reasoning block.
func (t *TUI2) AddReasoningBlock(id, content string) {
	rb := reasoning.New(id, content, 0)
	t.reasoningBlocks = append(t.reasoningBlocks, rb)
	t.conversation.AppendReasoning(rb)
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

// ToggleBlockByIndex finds the nth block and toggles it.
func (t *TUI2) ToggleBlockByIndex(n int) {
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

func (t *TUI2) ShowThinking() { t.thinkingInd.Show(); t.refreshMessages() }
func (t *TUI2) HideThinking() { t.thinkingInd.Hide(); t.refreshMessages() }

func (t *TUI2) AdvanceThinking() {
	if t.thinkingInd.Visible() {
		t.thinkingInd.Advance()
		t.refreshMessages()
	}
}

func (t *TUI2) AdvanceReasoningSeeds(seed float64) {
	for _, rb := range t.reasoningBlocks {
		rb.SetSeed(seed)
	}
	t.refreshMessages()
}

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

// AddUserMessage appends a styled user message to the conversation.
func (t *TUI2) AddUserMessage(text string) {
	accent := "#00afff"
	if t.theme != nil && t.theme.Accent != "" {
		accent = t.theme.Accent
	}
	t.conversation.AppendText("[" + accent + "]You: [-]" + text + "\n")
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}

// addAssistantResponse appends a markdown-rendered assistant response.
func (t *TUI2) addAssistantResponse(text string, width int) {
	w := width
	if w <= 0 {
		w = messageWidth(t.Messages)
	}
	t.conversation.AppendText(renderMarkdown(text, w))
	t.refreshMessages()
	t.App.SetFocus(t.Input)
}
