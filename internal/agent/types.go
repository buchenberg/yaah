package agent

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/pubsub"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// Provider is the interface for model backends.
type Provider = llm.Provider

// StreamProvider is a provider that supports streaming responses.
type StreamProvider = llm.StreamProvider

// ToolInfo is a legacy type preserved for external reference.
// New code should consume ToolStartEvent / ToolEndEvent via the Broker.
type ToolInfo struct {
	Name     string        // tool name
	Args     string        // abbreviated arguments
	Duration time.Duration // how long the tool took
	Result   string        // truncated tool result (only on second call)
	Error    string        // error message if the tool failed
}

// SubAgentInfo is a legacy type preserved for external reference.
// New code should consume SubAgentStartEvent / SubAgentEndEvent via the Broker.
type SubAgentInfo struct {
	Role     string        // worker, reviewer, planner, or custom
	Model    string        // model used by the sub-agent
	Prompt   string        // abbreviated task prompt
	Duration time.Duration // how long the sub-agent ran (0 on start)
	Error    string        // error or status message on completion
}

// ToolsLevel controls which tools the agent sees.
type ToolsLevel int

const (
	// FullTools is the default — all tools from the registry.
	FullTools ToolsLevel = iota
	// SubAgentsOnly gives only the task tool — the agent coordinates through sub-agents.
	SubAgentsOnly
)

// ToolResultMaxLen is a deprecated alias for defaultTruncateMaxBytes. Use
// Loop.truncateToolResult() in agent_truncation.go for the line/byte dual-limit
// truncation.
const ToolResultMaxLen = defaultTruncateMaxBytes

// pruneMessageMaxLen is the threshold above which old messages are pruned
// before being sent to the LLM summarizer during compaction.
const pruneMessageMaxLen = 2000

// minContextFloor is the minimum trigger threshold for compaction, preventing
// over-aggressive compaction on small-window models.
const minContextFloor = 64000

// Loop runs the agent conversation loop.
type Loop struct {
	Config LoopConfig
	State  LoopState

	Provider Provider
	Registry *tools.Registry
	// View receives agent events (tokens, thinking, flush, tool starts/ends,
	// sub-agent starts/ends, done). When set, the loop internally creates a
	// pubsub.Broker and BrokerView adapter; callers should NOT set Broker.
	View View

	broker     *pubsub.Broker[Event]
	brokerView *BrokerView

	Middleware []pipeline.Middleware
	LLM        *llm.Client

	CompactProvider  Provider
	FallbackProvider Provider
	FallbackModel    string

	Persister *SessionPersister
	Hooks     *HookEmitter

	FollowUps <-chan string
	Steer     <-chan string

	// BackgroundJobs, when set, is wired with the loop's broker callbacks
	// (SubAgentStart/End events) at Run start so background sub-agents
	// dispatching through the shared TaskTool emit live UI events while a
	// loop is active. Usage attribution is session-scoped (on the manager)
	// so it survives across Runs; only the event hooks are loop-scoped.
	BackgroundJobs *tools.BackgroundJobs

	ApproveFn       func(name, args string) bool `json:"-"`
	ConflictTracker *tools.ConflictTracker
	CtxMgr          *ContextManager

	toolConcurrency *pipeline.ToolConcurrencyMiddleware
	subAgentSem     chan struct{}

	toolDefsCache []types.ToolDef
	toolDefsGen   int

	usageMu   sync.Mutex
	toolIDGen atomic.Int64
}

// LoopConfig holds immutable configuration set once before Run.
type LoopConfig struct {
	Model                  string
	MaxLoopCycles          int
	MaxToolTurns           int
	JSONMode               bool
	ToolsLevel             ToolsLevel
	ContextWindow          int
	CompactionThreshold    float64
	RawCompactionThreshold float64
	CompactMaxMessages     int
	EstimateFactor         float64
	QualityGates           map[string][]string
	MaxRetries             int
	RetryBackoff           time.Duration
	LoopDetectCount        int
	LoopDetectWindow       int
	ApprovalMode           string
	WrapUpThreshold        int
	MaxInlineToolsPerTurn  int
	MaxToolConcurrency     int
	MaxSubAgentConcurrency int
	StuckChildTimeout      time.Duration
	StuckChildTimeouts     map[string]time.Duration
	PromptCaching          bool
	CompactModel           string
	SessionID              string
	PipelineNames          []string
	PipelineDisabled       []string
	PermissionRules        []pipeline.PermissionRule
	OtelEnabled            bool
	OtelVerbose            bool
	SystemPrompt           string
	SystemPromptOverride   string
}

// LoopState holds mutable runtime state modified during Run.
type LoopState struct {
	Messages                   []types.Message
	TotalTokens                types.Usage
	LastPromptTokens           int
	LastCachedPromptTokens     int
	TotalReasoningTokens       int
	TotalCachedPromptTokens    int
	LastFinishReason           string
	LastResponseModel          string
	PreviousSummary            string
	LastCompactionTokens       int
	IneffectiveCompactions     int
	CompactionForcedByOverflow bool
	CompactionBudgetMultiplier float64
	CompactionSavingsHistory   []float64
}
