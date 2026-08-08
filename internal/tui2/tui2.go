package tui2

import (
	"fmt"
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
//
// Concurrency: fields are grouped into three categories:
//   - Set before Run() — safe to read from any goroutine after setup
//   - QueueUpdateDraw only — MUST only be accessed inside QueueUpdateDraw/QueueUpdate callbacks
//   - Atomic / immutable — safe to access from any goroutine
type TUI2 struct {
	// --- tview infrastructure (set before Run, reads ok from any goroutine) ---
	App   *tview.Application
	Pages *tview.Pages
	Root  *tview.Flex

	// --- Immutable after New() ---
	Theme *colors.Theme

	// --- tview widgets (set before Run; must use QueueUpdateDraw for SetText etc.) ---
	Banner    *tview.TextView
	InfoBar   *tview.TextView
	Messages  *tview.TextView
	Input     *tview.TextArea
	InfoPane  *tview.TextView
	TodoPane  *tview.TextView
	StatusBar *tview.TextView
	Header    *tview.Grid

	// --- State (read-only after init, rare updates via QueueUpdateDraw) ---
	McpServers []mcpinfo.Server

	// ═══════════════════════════════════════════════════════════
	// QueueUpdateDraw ONLY — all reads and writes to fields below
	// this line must happen inside QueueUpdateDraw or QueueUpdate.
	// Exception: callbacks below are set before Run() and read-only
	// thereafter, so calling them from any goroutine is safe.
	// ═══════════════════════════════════════════════════════════

	// --- Conversation state (QueueUpdateDraw only) ---
	conversationLog []convItem
	plainMessages   []string
	reasoningBlocks []*reasoning.Block
	toolBlocks      []*toolblock.Block
	subagentBlocks  []*subagent.Block
	thinkingInd     *thinking.Indicator
	focus           focusState

	// --- Agent callbacks (set before Run(); calling is safe from any goroutine) ---
	OnSubmit   func(prompt string)
	OnAbort    func()
	OnCompact  func()
	OnClear    func()
	OnSteer    func(text string)
	OnFollowUp func(text string)
	OnStop     func()

	// --- Control channel (receive-only; goroutine-safe) ---
	ControlCh <-chan types.CtrlMsg

	// --- Streaming state (QueueUpdateDraw only, except isStreaming which is atomic) ---
	pendingTokens strings.Builder
	pendingThink  string
	pendingTool   string
	compacting    bool
	contextTokens int
	contextWindow int
	lastProvider  string
	lastModel     string
	thinkingLabel string
	todoItems     []itodo.Item
	verbose       bool
	showBanner    bool
	ephemeralMsg  string
	tokensRx      atomic.Int64
	charsWritten  atomic.Int64
	charsRendered atomic.Int64
	userScrolled  bool

	// --- Atomic fields (safe from any goroutine) ---
	isStreaming atomic.Bool

	// --- Model picker state (QueueUpdateDraw only) ---
	availableModels []string
	providerNames   map[string]string

	// --- Version (set before Run(), immutable) ---
	version string

	// --- Palettes & callbacks (set before Run()) ---
	CmdPalette    *command.Palette
	OnModelSelect func(model string)
}

// New creates a new TUI2 application.
func New(version string) *TUI2 {
	th := colors.DetectTheme()
	t := &TUI2{
		App:         tview.NewApplication(),
		Theme:       &th,
		thinkingInd: thinking.New("Thinking..."),
		version:     "yaah " + version,
		showBanner:  true,
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
			t.App.QueueUpdateDraw(func() {
				anyActive := t.thinkingInd.Visible() || t.isStreaming.Load()
				if !anyActive {
					for _, sb := range t.subagentBlocks {
						if sb.S() == subagent.Active {
							anyActive = true
							break
						}
					}
				}
				if !anyActive {
					return
				}
				if t.thinkingInd.Visible() {
					t.thinkingInd.Advance()
				}
				for _, sb := range t.subagentBlocks {
					sb.AdvanceSpinner()
				}
				// During streaming, content arrives via Write() — don't
				// rebuild the entire buffer with SetText() every 200ms.
				// Only refresh for spinner/subagent animation.
				if !t.isStreaming.Load() {
					t.refreshMessages()
					t.renderInfoPane()
				}
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
	t.Messages.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		switch action {
		case tview.MouseScrollUp:
			t.userScrolled = true
		case tview.MouseScrollDown:
			_, _, _, h := t.Messages.GetInnerRect()
			row, _ := t.Messages.GetScrollOffset()
			totalLines := len(strings.Split(t.Messages.GetText(true), "\n"))
			if row+h >= totalLines {
				t.userScrolled = false
			}
		}
		return action, event
	})
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
// Only one of the content fields is set.
type convItem struct {
	text           string           // raw text (markdown for assistant, plain for others)
	isMarkdown     bool             // true if text is markdown needing renderMarkdown()
	cached         string           // rendered output (lazy, invalidated on width change)
	cachedWidth    int              // width at which cached was produced
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

	for i := range t.conversationLog {
		item := &t.conversationLog[i]
		switch {
		case item.text != "":
			b.WriteString("\n")
			if item.isMarkdown {
				if item.cached == "" || item.cachedWidth != w {
					item.cached = renderMarkdown(item.text, w)
					item.cachedWidth = w
				}
				b.WriteString(item.cached)
			} else {
				b.WriteString(item.text)
			}
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

	// Spinner — inline at the bottom while thinking.
	if t.thinkingInd.Visible() {
		b.WriteString("\n")
		b.WriteString(t.thinkingInd.Render())
		b.WriteString("\n")
	}

	msg := b.String()
	t.charsRendered.Store(int64(len(msg)))
	t.Messages.SetText(msg)
	if !t.userScrolled {
		t.Messages.ScrollToEnd()
	}
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
	case ActionCommand:
		t.toggleCommandPalette()
		return nil
	case ActionClear:
		t.doClear()
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
		if t.isStreaming.Load() {
			t.submitFollowUp()
		} else {
			t.submitInput()
		}
		return nil
	case ActionCancel:
		if t.App.GetFocus() != t.Input {
			return ev
		}
		if t.OnAbort != nil {
			t.OnAbort()
		}
		return nil
	case ActionScrollUp, ActionPageUp, ActionTop:
		t.userScrolled = true
	case ActionScrollDown, ActionPageDown, ActionBottom:
		t.userScrolled = false
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
	modelpicker.Show(t.App, t.Pages, models, providerNames, onSelect, t.Input)
}

// HandleCommand dispatches a colon command.
func (t *TUI2) HandleCommand(cmd command.Cmd, arg string) {
	switch cmd {
	case command.CmdQuit:
		t.Stop()
	case command.CmdClear:
		t.doClear()
	case command.CmdHelp:
		t.ShowHelp()
	case command.CmdCompact:
		if t.OnCompact != nil {
			t.OnCompact()
		}
	case command.CmdStop:
		if t.OnStop != nil {
			t.OnStop()
		} else if t.OnAbort != nil {
			t.OnAbort()
		}
	case command.CmdSteer:
		if t.OnSteer != nil && arg != "" {
			t.OnSteer(arg)
		}
	case command.CmdFollowUp:
		if t.OnFollowUp != nil && arg != "" {
			t.OnFollowUp(arg)
		}
	case command.CmdVerbose:
		t.verbose = !t.verbose
		t.refreshMessages()
	case command.CmdTop:
		t.Messages.ScrollTo(0, 0)
	case command.CmdBottom:
		t.Messages.ScrollToEnd()
	case command.CmdBanner:
		t.toggleBanner()
	case command.CmdModel:
		t.ShowModelPicker(t.availableModels, t.providerNames, func(model string) {
			if t.OnModelSelect != nil {
				t.OnModelSelect(model)
			}
		})
	case command.CmdSearch:
		t.searchMessages(arg)
	default:
	}
}

func (t *TUI2) showCommandList() {
	const modalName = "cmdlist_modal"

	entries := []struct {
		label string
		desc  string
		cmd   command.Cmd
	}{
		{"help", "Show keybindings and commands", command.CmdHelp},
		{"clear", "Clear conversation", command.CmdClear},
		{"compact", "Compact context window", command.CmdCompact},
		{"stop", "Abort running agent", command.CmdStop},
		{"steer", "Inject steering text (requires arg)", command.CmdSteer},
		{"model", "Switch model", command.CmdModel},
		{"search", "Search messages (requires arg)", command.CmdSearch},
		{"verbose", "Toggle verbose mode", command.CmdVerbose},
		{"banner", "Toggle banner", command.CmdBanner},
		{"top", "Scroll to top", command.CmdTop},
		{"bottom", "Scroll to bottom", command.CmdBottom},
		{"quit", "Exit yaah", command.CmdQuit},
	}

	list := tview.NewList().
		ShowSecondaryText(true).
		SetHighlightFullLine(true).
		SetWrapAround(false)

	for _, e := range entries {
		list.AddItem(e.label, e.desc, 0, func() {
			t.Pages.RemovePage(modalName)
			t.App.SetFocus(t.Input)
			t.focus = focusNormal
			t.HandleCommand(e.cmd, "")
		})
	}

	list.SetBorder(true).
		SetTitle(" Commands ").
		SetTitleColor(tcell.ColorYellow)

	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			t.Pages.RemovePage(modalName)
			t.App.SetFocus(t.Input)
			t.focus = focusNormal
			return nil
		}
		return ev
	})

	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(list, 0, 3, false).
			AddItem(nil, 0, 1, false), 0, 3, true).
		AddItem(nil, 0, 1, false)

	t.Pages.AddPage(modalName, flex, true, true)
	t.App.SetFocus(list)
	t.focus = focusCommandPalette
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
		t.focus = focusNormal
	} else {
		t.showCommandList()
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

func (t *TUI2) submitFollowUp() {
	if t.OnFollowUp != nil {
		text := t.Input.GetText()
		if text != "" {
			t.Input.SetText("", false)
			t.OnFollowUp(text)
		}
	}
}

func (t *TUI2) clearConversation() {
	t.plainMessages = nil
	t.conversationLog = nil
	t.reasoningBlocks = nil
	t.toolBlocks = nil
	t.subagentBlocks = nil
	t.userScrolled = false
	t.refreshMessages()
}

func (t *TUI2) doClear() {
	if t.OnClear != nil {
		t.OnClear()
	}
	t.clearConversation()
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
	const helpModal = "help_modal"
	var lines []string
	lines = append(lines, "[yellow]Keyboard Shortcuts[-]\n\n")
	for _, b := range DefaultBindings() {
		lines = append(lines, fmt.Sprintf("  [white]%-12s[-] [dim]%s[-]", b.Label, b.HelpText))
	}
	lines = append(lines, "\n[yellow]Commands (Ctrl+P → :command)[-]\n")
	lines = append(lines, "  :help          Show this help")
	lines = append(lines, "  :clear         Clear conversation")
	lines = append(lines, "  :compact       Compact context")
	lines = append(lines, "  :stop          Stop running agent")
	lines = append(lines, "  :steer <text>  Inject steering text")
	lines = append(lines, "  :model <name>  Switch model")
	lines = append(lines, "  :search <q>    Search messages")
	lines = append(lines, "  :verbose       Toggle verbose mode")
	lines = append(lines, "  :banner        Toggle banner")
	lines = append(lines, "  :top/:bottom   Scroll to top/bottom")
	lines = append(lines, "  :quit          Exit")

	textView := tview.NewTextView().
		SetDynamicColors(true).
		SetText(strings.Join(lines, "\n"))
	textView.SetBorder(true).
		SetTitle(" Help ").
		SetTitleColor(tcell.ColorYellow)

	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(textView, 0, 3, false).
			AddItem(nil, 0, 1, false), 0, 3, true).
		AddItem(nil, 0, 1, false)

	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyEnter {
			t.Pages.RemovePage(helpModal)
			t.App.SetFocus(t.Input)
			t.focus = focusNormal
			return nil
		}
		return ev
	})

	t.Pages.AddPage(helpModal, flex, true, true)
	t.App.SetFocus(textView)
}

type focusState int

const (
	focusNormal focusState = iota
	focusCommandPalette
	focusModal
)

func (t *TUI2) toggleBanner() {
	t.showBanner = !t.showBanner
	if t.showBanner {
		t.Root.RemoveItem(t.Header)
		t.Root.AddItem(t.Header, t.headerHeight(), 0, false)
	} else {
		t.Root.RemoveItem(t.Header)
	}
}

func (t *TUI2) headerHeight() int {
	if !t.showBanner {
		return 0
	}
	_, _, _, h := t.Banner.GetInnerRect()
	if h <= 0 {
		return 8
	}
	return h
}

func (t *TUI2) searchMessages(query string) {
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

func (t *TUI2) SetEphemeral(msg string) {
	t.ephemeralMsg = msg
	t.renderInfoPane()
	go func() {
		time.Sleep(3 * time.Second)
		t.App.QueueUpdateDraw(func() {
			t.ephemeralMsg = ""
			t.renderInfoPane()
		})
	}()
}
