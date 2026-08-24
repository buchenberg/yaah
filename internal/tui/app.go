package tui

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tui/colors"
	"github.com/buchenberg/yaah/internal/tui/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui/components/subagent"
	"github.com/buchenberg/yaah/internal/tui/components/thinking"
	"github.com/buchenberg/yaah/internal/tui/components/toolblock"
	"github.com/rivo/tview"
)

// App is the tview-based terminal UI prototype.
//
// Concurrency: fields are grouped into three categories:
//   - Set before Run() — safe to read from any goroutine after setup
//   - QueueUpdateDraw only — MUST only be accessed inside QueueUpdateDraw/QueueUpdate callbacks
//   - Atomic / immutable — safe to access from any goroutine
type App struct {
	App   *tview.Application
	Pages *tview.Pages
	Root  *tview.Flex

	Theme *colors.Theme

	Banner             *tview.TextView
	Messages           *tview.TextView
	Input              *tview.TextArea
	InfoPane           *tview.TextView
	TodoPane           *tview.TextView
	BackgroundJobsPane *tview.TextView
	rightPane          *tview.Flex
	Header             *tview.Grid

	McpServers []mcpinfo.Server

	// --- QueueUpdateDraw ONLY fields below ---

	conversationLog []convItem
	thinkingInd     *thinking.Indicator
	focus           focusState

	// Usage tracking
	cumulativeUsage cumulativeUsage

	// Protects pendingTokens during concurrent writes from the
	// forwarder goroutine (HandleEvent) and reads on the main
	// thread (flushPendingTokens).
	tokenMu       sync.Mutex
	pendingTokens strings.Builder

	OnSubmit   func(prompt string)
	OnAbort    func()
	OnCompact  func()
	OnClear    func()
	OnSteer    func(text string)
	OnFollowUp func(text string)
	OnStop     func()

	// ShowApprovalFn overrides the approval modal for testing.
	// If nil (default), the tview-based approval.Show modal is used.
	ShowApprovalFn func(name, args string, onAnswer func(bool))

	ControlCh <-chan control.Msg

	bgMu      sync.Mutex
	bgDone    chan struct{}
	uiEventCh chan uiEvent

	coalesceMu           sync.Mutex
	thinkingQueued       bool
	thinkingSeq          uint64
	pendingThinkingLabel string
	contextQueued        bool
	contextSeq           uint64
	pendingContextTokens int
	pendingContextWindow int

	pendingThink  string
	pendingTool   string
	compacting    bool
	contextTokens int
	contextWindow int
	lastProvider  string
	lastModel     string
	thinkingLabel string
	todoItems     []todo.Item
	verbose       bool
	showBanner    bool
	ephemeralMsg  string
	// ephemeralGen guards SetEphemeral: each call invalidates the
	// previous timer so a stale clear cannot stomp a newer message.
	ephemeralGen         atomic.Int64
	subAgentsEnabled     bool
	subAgentsProvider    string
	subAgentsConcurrency int
	subAgentsModel       string
	embeddingEnabled     bool
	embeddingModel       string
	middlewarePipeline   []string
	agentActive          bool
	tokensRx             atomic.Int64
	charsWritten         atomic.Int64
	charsRendered        atomic.Int64
	userScrolled         bool
	uiEventDrops         atomic.Int64
	uiEventFallbacks     atomic.Int64
	uiEventFallbackSat   atomic.Int64
	lastRefreshUnixNano  atomic.Int64

	isStreaming   atomic.Bool
	needsRefresh  atomic.Bool
	refreshQueued atomic.Bool

	// needsFullRender forces refreshMessages to rebuild the entire
	// conversation text instead of appending only new items. Set when an
	// existing item mutates (block transitions, toggles), on clear, or when
	// the thinking indicator enters/leaves the rendered buffer. Atomic
	// because it is also written from the event-forwarder goroutine.
	needsFullRender atomic.Bool

	fallbackSem chan struct{}

	availableModels []string
	providerNames   map[string]string

	// Render tracking for the incremental append fast path in
	// refreshMessages: how many convItems are already reflected in the
	// Messages text view, and at which width they were formatted.
	renderedItems int
	renderedWidth int

	version string

	OnModelSelect func(model string)
}

type convItem struct {
	text           string
	isMarkdown     bool
	cached         string
	cachedWidth    int
	toolBlock      *toolblock.Block
	subBlock       *subagent.Block
	reasoningBlock *reasoning.Block
}

func New(version string) *App {
	th := colors.DetectTheme()
	t := &App{
		App:         tview.NewApplication(),
		Theme:       &th,
		thinkingInd: thinking.New("Thinking..."),
		version:     version,
		showBanner:  true,
		fallbackSem: make(chan struct{}, uiMaxDirectFallbacks),
	}
	t.buildUI()
	return t
}

func (t *App) SetProvider(name string) {
	t.lastProvider = name
	t.renderInfoPane()
}

func (t *App) SetModel(name string) {
	t.lastModel = name
	t.renderInfoPane()
}

func (t *App) SetConfig(subAgentsEnabled bool, subAgentsProvider string, subAgentsConcurrency int, subAgentsModel string, embeddingEnabled bool, embeddingModel string, pipeline []string) {
	t.subAgentsEnabled = subAgentsEnabled
	t.subAgentsProvider = subAgentsProvider
	t.subAgentsConcurrency = subAgentsConcurrency
	t.subAgentsModel = subAgentsModel
	t.embeddingEnabled = embeddingEnabled
	t.embeddingModel = embeddingModel
	t.middlewarePipeline = pipeline
	t.renderInfoPane()
}
