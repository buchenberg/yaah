package agent

import (
	"sync"
	"sync/atomic"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	agentctx "github.com/buchenberg/yaah/internal/agent/context"
	"github.com/buchenberg/yaah/internal/agent/llm"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/jobs"
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

// ToolResultMaxLen is a deprecated alias for context.DefaultTruncateMaxBytes.
// Use Loop.truncateToolResult() in agent_truncation.go for the line/byte
// dual-limit truncation.
const ToolResultMaxLen = agentctx.DefaultTruncateMaxBytes

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

	// BeforeViewEvents, when set, runs on the BrokerView forwarder goroutine
	// immediately before it starts delivering this run's broker events. The
	// session uses it to replay events buffered while no broker was live, so
	// their order relative to live broker events is preserved without a
	// second goroutine calling View.HandleEvent.
	BeforeViewEvents func()

	broker     *pubsub.Broker[Event]
	brokerView *BrokerView
	// brokerClosed is true once publishDone closed the broker;
	// applyDefaults re-arms it on the next Run (finding A4). It is
	// unsynchronized BY DESIGN: Runs on one Loop must be sequential —
	// Run → publishDone sets it, the next Run's applyDefaults clears it.
	// Concurrent Runs on a single Loop are not supported.
	brokerClosed bool
	ctxMgrMu     sync.Mutex // guards lazy CtxMgr fills from concurrent tool goroutines

	LLM *llm.Client

	CompactProvider  Provider
	FallbackProvider Provider
	FallbackModel    string

	Persister *SessionPersister
	Hooks     *HookEmitter

	FollowUps <-chan string
	Steer     <-chan string

	// OnUsage, when set, fires after every LLM call with the loop's
	// running usage totals (prompt/completion/total/cache). It is the
	// session's incremental-persistence seam: a session killed mid-run
	// (timeout, crash) still reports what it spent so far.
	OnUsage func(types.Usage)
	// OnSubUsage, when set, fires when a blocking sub-agent dispatch's
	// usage is folded into the loop's totals — the sub-agent
	// attribution seam, so orchestrator and sub-agent spend can be
	// reported separately (the fold into the parent total is
	// unchanged; this is an additional, read-only observation).
	OnSubUsage func(types.Usage)

	// BackgroundJobs, when set, is wired with the loop's broker callbacks
	// (SubAgentStart/End events) at Run start so background sub-agents
	// dispatching through the shared DelegateTool emit live UI events while a
	// loop is active. Usage attribution is session-scoped (on the manager)
	// so it survives across Runs; only the event hooks are loop-scoped.
	BackgroundJobs *jobs.BackgroundJobs

	ApproveFn            func(name, args string) bool `json:"-"`
	ContinueAfterMaxIter func() bool                  `json:"-"`
	ConflictTracker      *tools.ConflictTracker
	CtxMgr               *ContextManager

	toolConcurrency *pipeline.ToolConcurrencyMiddleware
	subAgentSem     chan struct{}

	toolDefsCache []types.ToolDef
	toolDefsGen   int

	usageMu   sync.Mutex
	toolIDGen atomic.Int64

	// subAgentIDGen assigns each spawned sub-agent a unique identifier
	// ("sa-N"), independent of tool-execution IDs, so views can correlate
	// a sub-agent's start/end/result across events and with its tool call.
	subAgentIDGen atomic.Int64
}

// LoopConfig holds immutable configuration set once before Run.
type LoopConfig struct {
	Model string
	// ProviderName is the configured provider label (e.g. "zai") for
	// this loop. Used only for error attribution; empty means unknown.
	ProviderName string
	// FallbackProviderName labels the fallback provider in error
	// envelopes. Empty when no fallback is configured.
	FallbackProviderName   string
	MaxLoopCycles          int
	MaxToolTurns           int
	BudgetIterationsSource string // provenance of MaxLoopCycles ("call", "floor", ...); "" unknown
	BudgetTurnsSource      string // provenance of MaxToolTurns; "" unknown
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
	// MCPApproval gates tools served by MCP servers. Remote tools
	// cannot implement tools.DangerClassifier, so classifyDanger
	// applies this policy to them: "ask" (default), "allow", "deny".
	MCPApproval string
	// MCPToolNames lists tool names registered from MCP servers so
	// classifyDanger can apply MCPApproval to them.
	MCPToolNames map[string]bool
	// ToolSpillDir is the directory where oversized tool results are
	// spilled to disk. Injected by the composition root (the yaah config
	// dir); empty disables spilling and the truncation hint carries no
	// file path.
	ToolSpillDir string

	WrapUpThreshold int
	// Turn checkpointing for sub-agent loops only.
	TurnCheckpointer      TurnCheckpointer
	TurnCheckpointEnabled bool
	TurnCheckpointMax     int
	// MaxTurnRestores caps turn-level checkpoint restores per Run so a
	// deterministically failing turn cannot rewind forever. Values <= 0
	// fall back to defaultMaxTurnRestores.
	MaxTurnRestores        int
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

	// IsSubAgent marks loops created by NewSubAgentLoop. Sub-agent loops
	// build the curated sub-agent middleware pipeline instead of the
	// orchestrator default.
	IsSubAgent bool

	// DestructiveGuard is the supervision service's blocking
	// pre-dispatch check (shepherd-adoption plan B2), plumbed through
	// from SubAgentConfig: the sub-agent pipeline consults it before a
	// tool call executes; a non-empty return denies the call. Nil (the
	// default) means no guard — tracing-off sessions dispatch
	// unchanged.
	DestructiveGuard func(ownerID, toolName, argsJSON string) string

	// TraceStore is the session's Shepherd trace store (plan 07, H4),
	// plumbed through from SubAgentConfig: the sub-agent pipeline's
	// shepherd_trace middleware writes through it. Nil when tracing
	// is disabled.
	TraceStore *shepherdtrace.SQLiteTraceStore
}

// LoopState holds mutable runtime state modified during Run.
type LoopState struct {
	Messages    []types.Message
	TotalTokens types.Usage
	// SubAgentTokens is the portion of TotalTokens folded in from child
	// dispatches. It is tracked separately so the loop's own usage can be
	// reported without double-counting nested delegates, and so child
	// totals never masquerade as the parent's current context occupancy.
	SubAgentTokens             types.Usage
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

	// TurnCheckpoints holds the IDs of live turn checkpoints in creation
	// order. Only populated when turn checkpointing is active.
	TurnCheckpoints []string
	// TurnRestores counts turn-level checkpoint restores performed during
	// this Run (diagnostic for supervised-task envelopes).
	TurnRestores int
	// RestoredFrom is the checkpoint ID of the most recent turn restore.
	RestoredFrom string
}
