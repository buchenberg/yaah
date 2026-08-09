package tui2

import (
	"strings"
	"sync/atomic"

	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tui2/colors"
	"github.com/buchenberg/yaah/internal/tui2/components/mcpinfo"
	"github.com/buchenberg/yaah/internal/tui2/components/reasoning"
	"github.com/buchenberg/yaah/internal/tui2/components/subagent"
	"github.com/buchenberg/yaah/internal/tui2/components/thinking"
	"github.com/buchenberg/yaah/internal/tui2/components/toolblock"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/rivo/tview"
)

// TUI2 is the tview-based terminal UI prototype.
//
// Concurrency: fields are grouped into three categories:
//   - Set before Run() — safe to read from any goroutine after setup
//   - QueueUpdateDraw only — MUST only be accessed inside QueueUpdateDraw/QueueUpdate callbacks
//   - Atomic / immutable — safe to access from any goroutine
type TUI2 struct {
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
	reasoningBlocks []*reasoning.Block
	toolBlocks      []*toolblock.Block
	subagentBlocks  []*subagent.Block
	thinkingInd     *thinking.Indicator
	focus           focusState

	OnSubmit   func(prompt string)
	OnAbort    func()
	OnCompact  func()
	OnClear    func()
	OnSteer    func(text string)
	OnFollowUp func(text string)
	OnStop     func()

	ControlCh <-chan types.CtrlMsg

	pendingTokens        strings.Builder
	pendingThink         string
	pendingTool          string
	compacting           bool
	contextTokens        int
	contextWindow        int
	lastProvider         string
	lastModel            string
	thinkingLabel        string
	todoItems            []todo.Item
	verbose              bool
	showBanner           bool
	ephemeralMsg         string
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

	isStreaming  atomic.Bool
	needsRefresh atomic.Bool

	availableModels []string
	providerNames   map[string]string

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

func New(version string) *TUI2 {
	th := colors.DetectTheme()
	t := &TUI2{
		App:         tview.NewApplication(),
		Theme:       &th,
		thinkingInd: thinking.New("Thinking..."),
		version:     version,
		showBanner:  true,
	}
	t.buildUI()
	return t
}

func (t *TUI2) SetProvider(name string) {
	t.lastProvider = name
	t.renderInfoPane()
}

func (t *TUI2) SetModel(name string) {
	t.lastModel = name
	t.renderInfoPane()
}

func (t *TUI2) SetConfig(subAgentsEnabled bool, subAgentsProvider string, subAgentsConcurrency int, subAgentsModel string, embeddingEnabled bool, embeddingModel string, pipeline []string) {
	t.subAgentsEnabled = subAgentsEnabled
	t.subAgentsProvider = subAgentsProvider
	t.subAgentsConcurrency = subAgentsConcurrency
	t.subAgentsModel = subAgentsModel
	t.embeddingEnabled = embeddingEnabled
	t.embeddingModel = embeddingModel
	t.middlewarePipeline = pipeline
	t.renderInfoPane()
}
