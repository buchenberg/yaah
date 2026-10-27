package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider holds connection details for a model provider.
type Provider struct {
	API            string            `yaml:"api,omitempty"`  // "openai" (default) or "anthropic"
	Auth           string            `yaml:"auth,omitempty"` // "api_key" (default) or "oauth"
	BaseURL        string            `yaml:"base_url"`
	APIKey         string            `yaml:"api_key"`
	Name           string            `yaml:"name,omitempty"`
	Models         []ModelEntry      `yaml:"models,omitempty"`
	TimeoutSeconds int               `yaml:"timeout,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"` // extra headers sent on every request

	// ReasoningReplay selects how assistant thinking that lacks a
	// provider signature is replayed: "auto" (default) re-sends it as a
	// plain text block so the model keeps its chain-of-thought, "omit"
	// drops it, and "text" re-sends even signed reasoning as text for
	// endpoints that reject valid thinking replay.
	ReasoningReplay string `yaml:"reasoning_replay,omitempty"`

	// MaxTokens caps the output tokens for every request this provider
	// serves when the request does not set its own cap. Anthropic-
	// compatible reasoning endpoints count thinking tokens against this
	// budget, so the default (16384) can still be exhausted before any
	// content is emitted; the client retries once with a doubled cap, and
	// this field raises that starting point for reasoning-heavy models.
	MaxTokens int `yaml:"max_tokens,omitempty"`

	// OAuth fields (used when auth: oauth).
	OAuthClientID string `yaml:"oauth_client_id,omitempty"`
	OAuthScope    string `yaml:"oauth_scope,omitempty"`
	OAuthDomain   string `yaml:"oauth_domain,omitempty"` // authorization server domain
}

// ModelEntry is a single model in a provider's model list. It supports two
// YAML forms for backward compatibility:
//
//	models:
//	  - gpt-4o                          # plain string
//	  - name: deepseek-r1              # structured entry
//	    thinking: true
type ModelEntry struct {
	Name     string `yaml:"name"`
	Thinking *bool  `yaml:"thinking,omitempty"` // override auto-detection; nil = use registry
}

// UnmarshalYAML accepts either a plain string or a structured mapping.
func (m *ModelEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		m.Name = value.Value
		return nil
	}
	type raw ModelEntry
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*m = ModelEntry(r)
	return nil
}

// ModelNames returns the list of model name strings.
func (p Provider) ModelNames() []string {
	names := make([]string, len(p.Models))
	for i, m := range p.Models {
		names[i] = m.Name
	}
	return names
}

// ThinkingOverride returns the per-model thinking override for the given
// model name, or nil if no override is configured.
func (p Provider) ThinkingOverride(modelName string) *bool {
	for _, m := range p.Models {
		if m.Name == modelName {
			return m.Thinking
		}
	}
	return nil
}

// Defaults hold the default agent model and loop settings.
type Defaults struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	// CompactionModel summarizes old context when compaction runs. An
	// unset value means compaction runs on the main model, which is both
	// slower and worse at summarizing; a warning fires at resolution.
	CompactionModel string `yaml:"compaction_model"`
	// IntentSteerAfter / IntentHaltAfter tune the intent-repetition
	// detector: steer after N consecutive same-plan turns, halt at N.
	// Zero uses 4 / 8.
	IntentSteerAfter int    `yaml:"intent_steer_after"`
	IntentHaltAfter  int    `yaml:"intent_halt_after"`
	MaxLoopCycles    int    `yaml:"max_iterations"`
	MaxToolTurns     int    `yaml:"max_turns"`      // soft cap on tool-using turns; 0 = off
	ContextWindow    int    `yaml:"context_window"` // max context window; resolved window from model is capped by this value
	Approval         string `yaml:"approval"`
	MCPApproval      string `yaml:"mcp_approval"`  // approval policy for MCP-served tools: ask (default), allow, deny
	WorkspaceAsk     bool   `yaml:"workspace_ask"` // prompt before denying out-of-workspace access (with --workspace)
	// WorkspaceDenyPatterns are globs denied even inside the workspace
	// (with --workspace). When unset, a built-in secret-file default
	// applies (.env, *.pem, private keys). Set explicitly to override,
	// or to an empty list []... to disable.
	WorkspaceDenyPatterns []string `yaml:"workspace_deny_patterns"`
	MaxInlineToolsPerTurn int      `yaml:"max_inline_tools_per_turn"` // 0 = unlimited
	EstimateFactor        float64  `yaml:"estimate_factor"`           // 0 = default (1.3)

	// Tools is the main session's tool belt: when set, only these tools
	// are registered, and every other tool is left out of the request's
	// schema list. Empty means every registered tool (the default). The
	// belt is a per-call prompt cost — an unused tool still ships its
	// schema on every request — so a lane that only needs a few tools can
	// name them (yaah-w8x). The session is told which tools are absent so
	// it does not call them.
	Tools []string `yaml:"tools"`

	// Compaction controls context summarisation behaviour.
	CompactionThreshold    float64 `yaml:"compaction_threshold"`     // fraction of ContextWindow; 0 = 0.5
	RawCompactionThreshold float64 `yaml:"raw_compaction_threshold"` // fraction ignoring cache; 0 = 0.5
	CompactMaxMessages     int     `yaml:"compact_max_messages"`     // force compaction above N messages; 0 = off

	// Loop detection governs when the agent halts on repeated tool calls.
	LoopDetectCount  int `yaml:"loop_detect_count"`  // identical calls to trigger halt; 0 = default (5)
	LoopDetectWindow int `yaml:"loop_detect_window"` // sliding window size; 0 = default (10)

	// Evidence guard steers or halts when exploration stops producing new
	// evidence (repeated calls, unchanged re-reads). Nudge 0 = default (6),
	// negative = disabled; halt 0 = disabled.
	EvidenceGuardNudge int `yaml:"evidence_guard_nudge"`
	EvidenceGuardHalt  int `yaml:"evidence_guard_halt"`

	// Provider resilience: retry on transient errors with backoff.
	MaxRetries       int `yaml:"max_retries"`        // 0 = no retries (default)
	RetryBackoffSecs int `yaml:"retry_backoff_secs"` // seconds; 0 = default (1)

	// Concurrency and caching toggles.
	MaxToolConcurrency int  `yaml:"max_tool_concurrency"`    // concurrent tool goroutines; 0 = unlimited
	PromptCaching      bool `yaml:"prompt_caching"`          // inject Anthropic cache-control breakpoints
	ReasoningProtect   int  `yaml:"reasoning_protect_turns"` // preserve reasoning in recent N turns; 0 = default (2)
	WrapUpThreshold    int  `yaml:"wrap_up_turns"`           // inject a wrap-up notice this many turns before the cap; 0 = default (1), negative = off

	// Tool result truncation caps.
	ToolResultMaxLines int `yaml:"tool_result_max_lines"` // 0 = default (500)
	ToolResultMaxBytes int `yaml:"tool_result_max_bytes"` // 0 = default (20480)

	// Soft-prune tuning.
	PruneProtectTokens int `yaml:"prune_protect_tokens"` // recent tool tokens shielded; 0 = default (2000)
	PruneMinReclaim    int `yaml:"prune_min_reclaim"`    // min tokens to commit a prune; 0 = default (400)
	PruneMinTurns      int `yaml:"prune_min_turns"`      // recent turns always kept; 0 = default (1)

	// Consolidation "nudge" (docs/compaction-as-sleep.md §7): fire the
	// consolidate middleware every N turns or every N tool results so the
	// loop consolidates durable state to long-term memory out-of-band.
	// Both 0 = disabled (default). Requires the "consolidate" middleware
	// to be enabled in middleware.enabled.
	ConsolidateTurnsInterval int `yaml:"consolidate_turns_interval"`
	ConsolidateItersInterval int `yaml:"consolidate_iters_interval"`

	// ConsolidateDeepEvery throttles the expensive deep-sleep fork: run the
	// consolidator sub-agent on every Nth nudge, and take the cheap "light
	// sleep" path (skip the fork; conversation pruning/compaction already
	// reclaim cheaply elsewhere) on the nudges in between. 0 or 1 = every
	// nudge is deep (the default).
	ConsolidateDeepEvery int `yaml:"consolidate_deep_every"`

	// Directives are session-level policy statements injected into the
	// top-level agent's system prompt, immediately after the identity
	// block. Sub-agents do NOT inherit these; they receive per-role
	// directives via SubAgentConfig.Roles[name].Directives instead.
	Directives []string `yaml:"directives"`

	// ShepherdTracePath is the file path of the Shepherd trace store.
	// Tracing is integral: sub-agent tool calls are always recorded, the
	// supervisor tool is registered, and per-turn checkpointing is
	// available to roles that opt in (`turn_checkpoints`). Empty resolves to the
	// default trace store under the yaah home — see TracePath.
	ShepherdTracePath string `yaml:"shepherd_trace_path"`

	// Supervisor configures the advisory supervision rules engine
	// (adoption plan B1). It is not gated on tracing: tracing is always
	// on.
	Supervisor SupervisorConfig `yaml:"supervisor"`

	// SupervisorHaltRoot is the DEPRECATED flat form of
	// Supervisor.HaltRoot (it shipped with the A3 halt bridge before
	// the plan §7 stanza existed). HaltRootEnabled resolves it: the
	// stanza's explicit value wins, unset falls back to this key, so
	// existing configurations keep gating the same switch. New
	// configuration uses supervisor.halt_root.
	SupervisorHaltRoot bool `yaml:"supervisor_halt_root"`

	// RetainedRepoPath is the git repository sub-agent worktrees are cut from,
	// and the repository per-turn checkpoints operate on. Empty (unset)
	// defaults to the working directory at execution time.
	RetainedRepoPath string `yaml:"retained_repo_path"`

	// RetainedWorktreeRoot is the parent directory for sub-agent worktrees.
	// Empty (unset) uses a ".yaah/shepherd-worktrees" directory inside the
	// repository working tree (project-local).
	RetainedWorktreeRoot string `yaml:"retained_worktree_root"`

	// RetainedWorktreeBootstrap is a shell command run inside each sub-agent
	// worktree after it is created. A git worktree checks out tracked files
	// only, so a repo whose build needs gitignored artifacts (node_modules,
	// build caches, .env) must recreate them here, for example "npm ci".
	RetainedWorktreeBootstrap string `yaml:"retained_worktree_bootstrap"`

	// TurnCheckpointMax caps live turn checkpoints per sub-agent run;
	// the oldest are pruned when the cap is reached. 0 = unlimited.
	// (Per-turn checkpointing itself is enabled per role via
	// subagent.roles.<name>.turn_checkpoints.)
	TurnCheckpointMax int `yaml:"turn_checkpoint_max"`

	// MaxTurnRestores caps turn-level restores per sub-agent run so a
	// deterministically failing turn cannot rewind forever. 0 = default (3).
	MaxTurnRestores int `yaml:"max_turn_restores"`
}

// Hooks holds configuration for external integrations via JSONL hook events.
type Hooks struct {
	Dir string `yaml:"dir"` // directory for JSONL hook event files
}

// MiddlewareConfig controls which middleware runs in the agent pipeline.
// Enabled is additive over the built-in defaults (it extends rather than
// replaces them); Disabled removes names from the union.
type MiddlewareConfig struct {
	Enabled  []string `yaml:"enabled"`
	Disabled []string `yaml:"disabled"`
}

// FallbackConfig configures the fallback provider/model used when the
// primary provider returns auth, billing, or rate-limit errors.
type FallbackConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// AgentConfig holds all agent-related configuration.
type AgentConfig struct {
	Default      Defaults            `yaml:"default"`
	Fallback     FallbackConfig      `yaml:"fallback"`
	SubAgent     SubAgentConfig      `yaml:"subagent"`
	Middleware   MiddlewareConfig    `yaml:"middleware"`
	QualityGates map[string][]string `yaml:"quality_gates"` // role → validator roles dispatched after completion
}

// SubAgentConfig configures the task tool's sub-agent lifecycle:
// nesting depth, concurrency, default timeout, provider/model, and
// per-role overrides. When provider and model are unset, sub-agents
// inherit the planner's provider and model.
type SubAgentConfig struct {
	// Provider selects which provider sub-agents use (default: main provider).
	Provider string `yaml:"provider"`

	// Model selects which model sub-agents use (default: main model).
	Model string `yaml:"model"`

	// MaxConcurrency caps simultaneous task tool calls per iteration.
	// 0 means unlimited.
	MaxConcurrency int `yaml:"max_concurrency"`

	// DefaultTimeout is applied when a task call supplies no timeout
	// and the role profile has none. Seconds. 0 means no timeout.
	// Slow local models can take 30s+ for a trivial completion, so tune
	// per-role timeouts (role file or roles.<role>.timeout) for them
	// rather than raising this global default (#319).
	DefaultTimeout int `yaml:"default_timeout"`

	// StuckChildTimeout is the duration without a heartbeat before a
	// sub-agent is declared stuck and force-cancelled. The timer resets
	// on every iteration (heartbeat), so this is a per-iteration liveness
	// guard, not a total budget. Seconds. 0 disables.
	StuckChildTimeout int `yaml:"stuck_child_timeout"`

	// DefaultMaxTurns is the fallback soft turn cap when no role-specific
	// override is set. 0 means unlimited (off).
	DefaultMaxToolTurns int `yaml:"default_max_turns"`

	// DefaultMinToolTurns is the global floor for tool-using turns: no
	// per-call override or role budget may drop below it. 0 means no
	// floor.
	DefaultMinToolTurns int `yaml:"default_min_turns"`

	// JSONMode enables structured output via response_format json_object.
	// Individual roles may override with their own json_mode setting.
	JSONMode bool `yaml:"json_mode"`

	// OutputLimit caps the final synthesized result from a sub-agent in
	// bytes before it reaches the orchestrator. 0 means unlimited.
	OutputLimit int `yaml:"output_limit"`

	// Containment selects the syscall jail for workdir sub-agent dispatches
	// (Plan 7). "" / "auto" (the default) jail on the platform backend (Landlock
	// on Linux, winacl on Windows) and fall back to advisory elsewhere; "off"
	// (or "none"/"advisory") leaves the host path; "paddock" (alias "jail")
	// fails closed when the host cannot
	// jail. The orchestrator's own commands are never jailed either way, only a
	// dispatched sub-agent's.
	//
	// A confined dispatch cannot run git WRITE commands (add/commit/stash): it
	// works in a `git worktree`, whose index and object store live in the
	// original repository, outside the jail's sole writable root. Read-only git
	// is unaffected, and nothing is lost — the sub-agent prepares changes and
	// the orchestrator commits when it settles the proposal. A denied write says
	// so (see internal/tools/workspace.go confinementHint), and the sub-agent's
	// prompt states the rule up front.
	//
	// A confined dispatch also records the resolved placement (paddock vs
	// advisory, the backend, whether the jail was really established, the
	// writable root) on the sub-agent's trace.
	Containment string `yaml:"containment"`

	// Network selects the confined dispatch's egress posture (Plan 7 Phase 3,
	// alongside Containment). "allow_all" (the default) leaves the network
	// ungoverned; "deny_all" refuses TCP egress entirely; "broker" pins egress
	// to the session's egress broker, which admits only NetworkHosts — so a
	// sub-agent can reach an allowlisted host and nothing else.
	//
	// The two restrictive modes need network-rule support (Linux: Landlock ABI
	// >= 4; winacl has no network axis and refuses them). Where the
	// host cannot enforce them the dispatch FAILS rather than silently running
	// with open egress — a policy that cannot be applied is not applied quietly.
	// Only confined dispatches are governed: with Containment off there is no
	// jail to put the rule in, and the config is reported as unenforced.
	Network string `yaml:"network"`

	// NetworkHosts is the broker's allowlist (exact host names), used only when
	// Network is "broker". Empty means the broker admits nothing, so "broker"
	// with no hosts is a deny-all that still runs the broker.
	NetworkHosts []string `yaml:"network_hosts"`

	// Mode selects the dispatch lane (Plan 7 Phase 2). "retained" (the default)
	// runs each sub-agent in an isolated worktree and returns a sealed proposal
	// the orchestrator settles with the proposal tool; nothing touches the
	// working tree until then. "shared" keeps the older direct-to-host lane.
	Mode string `yaml:"mode"`

	// Roles holds per-role overrides keyed by role name
	// ("analyst", "developer", "tester", "reviewer").
	Roles map[string]RoleConfig `yaml:"roles"`
}

// RoleConfig overrides a single role's default timeout, iteration cap,
// turn cap, provider, model, concurrency, output format, directives, and
// per-turn checkpointing (`turn_checkpoints`).
type RoleConfig struct {
	Timeout           int      `yaml:"timeout"`             // seconds; 0 = use role default
	MaxLoopCycles     int      `yaml:"max_iterations"`      // 0 = use role default
	MinLoopCycles     int      `yaml:"min_iterations"`      // iteration floor; 0 = none
	MaxToolTurns      int      `yaml:"max_turns"`           // soft turn cap; 0 = use role default
	MinToolTurns      int      `yaml:"min_turns"`           // turn floor; 0 = none
	JSONMode          bool     `yaml:"json_mode"`           // structured output toggle
	ContextWindow     int      `yaml:"context_window"`      // 0 = inherit halved parent default
	OutputLimit       int      `yaml:"output_limit"`        // bytes; 0 = use config default
	Provider          string   `yaml:"provider"`            // per-role provider override; "" = inherit
	Model             string   `yaml:"model"`               // per-role model override; "" = inherit
	MaxConcurrency    int      `yaml:"max_concurrency"`     // per-role max sub-agent spawns; 0 = use config default
	StuckChildTimeout int      `yaml:"stuck_child_timeout"` // seconds; 0 = use global default
	Directives        []string `yaml:"directives"`          // injected into this role's sub-agent prompt; empty = none

	// TurnCheckpoints enables per-turn checkpoint/restore inside this
	// role's sub-agent loop. Default false. Only meaningful where a shared
	// shepherd scope manager is configured.
	TurnCheckpoints bool `yaml:"turn_checkpoints"`
}

// MCPServerConfig holds the configuration for a single MCP server,
// mirroring the fields of mcp.Manifest with yaml tags for config.yaml.
type MCPServerConfig struct {
	Command   string            `yaml:"command,omitempty"`
	Args      []string          `yaml:"args,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	URL       string            `yaml:"url,omitempty"`
	Transport string            `yaml:"transport,omitempty"` // "stdio" (default) or "http"
	Framing   string            `yaml:"framing,omitempty"`   // stdio only: "" (auto), "newline", "framed"
	Headers   map[string]string `yaml:"headers,omitempty"`   // HTTP transport only
}

// Config is the full yaah configuration loaded from ~/.yaah/config.yaml.
type Config struct {
	Providers     map[string]Provider        `yaml:"providers"`
	Agent         AgentConfig                `yaml:"agents"`
	MCPServers    map[string]MCPServerConfig `yaml:"mcp_servers"`
	Hooks         Hooks                      `yaml:"hooks"`
	Editor        string                     `yaml:"editor"`
	Observability ObservabilityConfig        `yaml:"observability"`
	Embedding     EmbeddingConfig            `yaml:"embedding"`
	Workspace     WorkspaceConfig            `yaml:"workspace"`
}

// EmbeddingConfig configures semantic memory search via an embedding model.
// The provider is resolved from the providers map to obtain the base URL.
type EmbeddingConfig struct {
	// Provider is the provider name (key in the providers map) whose
	// base URL hosts the embeddings endpoint. When empty, semantic
	// search is disabled.
	Provider string `yaml:"provider"`

	// Model is the embedding model name sent to the /v1/embeddings
	// endpoint. Required when Provider is set.
	Model string `yaml:"model"`
}

// ObservabilityConfig holds OpenTelemetry tracing and metrics settings.
type ObservabilityConfig struct {
	Otel OtelConfig `yaml:"otel"`
}

// OtelConfig controls the OpenTelemetry OTLP exporter.
type OtelConfig struct {
	Enabled     bool   `yaml:"enabled"`      // must be true to activate
	Endpoint    string `yaml:"endpoint"`     // OTLP/HTTP endpoint (e.g. "localhost:4318")
	ServiceName string `yaml:"service_name"` // displayed in the tracing UI
	Traces      bool   `yaml:"traces"`       // enable trace spans
	Metrics     bool   `yaml:"metrics"`      // enable OTLP metrics
	// Verbose enables detailed span attributes/events: full model content,
	// reasoning, tool-call arguments, and conversation context. Off by
	// default to keep Jaeger payloads light; turn on when diagnosing
	// agent-loop behaviour. Only effective when Enabled is true.
	Verbose bool `yaml:"verbose"`
}

// defaultConfig returns the built-in defaults used when no config file exists.
func defaultConfig() *Config {
	return &Config{
		Agent: AgentConfig{
			Default: Defaults{
				Model:                  "deepseek/deepseek-v4-pro",
				CompactionModel:        "deepseek/deepseek-v4-flash",
				MaxLoopCycles:          50,
				ContextWindow:          1048576,
				Approval:               "ask",
				MCPApproval:            "ask",
				CompactionThreshold:    0.5,
				RawCompactionThreshold: 0.5,
				LoopDetectCount:        5,
				LoopDetectWindow:       10,
				EvidenceGuardNudge:     6,
				RetryBackoffSecs:       1,
				ReasoningProtect:       2,
			},
			SubAgent: SubAgentConfig{
				MaxConcurrency:    3,
				StuckChildTimeout: 60,
				OutputLimit:       51200,
			},
		},
		Workspace: WorkspaceConfig{
			Backend: BackendLocal,
			Root:    "/workspace",
		},
		Observability: ObservabilityConfig{
			Otel: OtelConfig{
				Enabled:     false,
				Endpoint:    "localhost:4318",
				ServiceName: "yaah",
				Traces:      true,
				Metrics:     false,
				Verbose:     false,
			},
		},
	}
}

// envVarRe matches ${VAR_NAME} patterns for env substitution.
var envVarRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// SubstituteEnv replaces ${VAR} patterns with the corresponding env var.
func SubstituteEnv(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1] // strip ${ and }
		return os.Getenv(varName)
	})
}

// Resolve returns a copy of the Provider with ${VAR} references
// in APIKey, BaseURL, and OAuth fields substituted by environment variables.
func Resolve(p Provider) Provider {
	return Provider{
		API:             p.API,
		Auth:            p.Auth,
		BaseURL:         SubstituteEnv(p.BaseURL),
		APIKey:          SubstituteEnv(p.APIKey),
		Name:            p.Name,
		Models:          p.Models,
		TimeoutSeconds:  p.TimeoutSeconds,
		Headers:         p.Headers,
		MaxTokens:       p.MaxTokens,
		ReasoningReplay: p.ReasoningReplay,
		OAuthClientID:   SubstituteEnv(p.OAuthClientID),
		OAuthScope:      SubstituteEnv(p.OAuthScope),
		OAuthDomain:     SubstituteEnv(p.OAuthDomain),
	}
}

// Load reads the config file from ConfigPath(). If the file doesn't exist,
// it returns a Config populated with built-in defaults. Environment variable
// references in the form ${VAR_NAME} are substituted after parsing.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// defaultConfig already carries the workspace defaults; the
			// normalize step is idempotent, so run it for uniformity with
			// the parsed path.
			cfg := defaultConfig()
			normalizeWorkspace(&cfg.Workspace)
			return cfg, nil
		}
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config %s: %w", path, err)
	}

	// The `tui:` stanza was removed with the bubbletea TUI (2026-08-21);
	// warn once so upgraded configs know the key is dead.
	var rawKeys map[string]yaml.Node
	if err := yaml.Unmarshal(data, &rawKeys); err == nil {
		if _, ok := rawKeys["tui"]; ok {
			fmt.Fprintf(os.Stderr, "warning: config key `tui:` is no longer used; toggle verbosity inside the TUI with :verbose\n")
		}
	}

	// `shepherd_trace_dir` was renamed to `shepherd_trace_path` and
	// retyped from directory to file path; warn once so an upgraded
	// config knows the old key is ignored and the store resolves to the
	// default path. The supervised_task keys and the per-role
	// `supervised` flag are past their deprecation window and now die
	// silently like any unknown key.
	var legacyRenamed struct {
		Agents struct {
			Default struct {
				ShepherdTraceDir *string `yaml:"shepherd_trace_dir"`
			} `yaml:"default"`
		} `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &legacyRenamed); err == nil && legacyRenamed.Agents.Default.ShepherdTraceDir != nil {
		fmt.Fprintf(os.Stderr, "warning: config key `agents.default.shepherd_trace_dir` was renamed to `shepherd_trace_path` and now names the trace store file, not its directory; the old key is ignored and the store resolves to the default path\n")
	}

	if cfg.Hooks.Dir != "" {
		cfg.Hooks.Dir = expandHomeDir(cfg.Hooks.Dir)
	}

	// `small_model` was renamed to `compaction_model` (yaah-v378.4);
	// warn so an upgraded config knows the old key is ignored and
	// context compaction fell back to the main model.
	var legacySmallModel struct {
		Agents struct {
			Default struct {
				SmallModel *string `yaml:"small_model"`
			} `yaml:"default"`
		} `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &legacySmallModel); err == nil && legacySmallModel.Agents.Default.SmallModel != nil {
		fmt.Fprintf(os.Stderr, "warning: config key `agents.default.small_model` was renamed to `compaction_model`; the old key is ignored — compaction uses the default compaction_model unless you set `compaction_model` explicitly\n")
	}

	// The intent-loop knobs must be coherent: both non-negative (0 =
	// default) and, when both are set, a halt after the steer.
	if cfg.Agent.Default.IntentSteerAfter < 0 || cfg.Agent.Default.IntentHaltAfter < 0 {
		return nil, fmt.Errorf("agents.default.intent_steer_after / intent_halt_after must not be negative")
	}
	// Both knobs fall back to the middleware defaults (4 / 8) when unset;
	// validate the EFFECTIVE pair so neither a low halt nor a high steer can
	// slip through and be silently clamped later.
	steer := cfg.Agent.Default.IntentSteerAfter
	if steer <= 0 {
		steer = 4 // defaultIntentSteerAfter in internal/agent/pipeline
	}
	halt := cfg.Agent.Default.IntentHaltAfter
	if halt <= 0 {
		halt = 8 // defaultIntentHaltAfter in internal/agent/pipeline
	}
	if halt <= steer {
		return nil, fmt.Errorf("agents.default.intent_halt_after (%d) must be greater than intent_steer_after (%d)", halt, steer)
	}

	// reasoning_replay must name one of the three strategies; anything
	// else would silently degrade to the default at replay time.
	for name, p := range cfg.Providers {
		switch strings.ToLower(strings.TrimSpace(p.ReasoningReplay)) {
		case "", "auto", "text", "omit":
		default:
			return nil, fmt.Errorf("provider %s: reasoning_replay must be one of auto, text, omit (got %q)", name, p.ReasoningReplay)
		}
	}

	if cfg.Agent.Default.ShepherdTracePath != "" {
		cfg.Agent.Default.ShepherdTracePath = expandHomeDir(cfg.Agent.Default.ShepherdTracePath)
	}
	if cfg.Agent.Default.RetainedRepoPath != "" {
		cfg.Agent.Default.RetainedRepoPath = expandHomeDir(cfg.Agent.Default.RetainedRepoPath)
	}
	if cfg.Agent.Default.RetainedWorktreeRoot != "" {
		cfg.Agent.Default.RetainedWorktreeRoot = expandHomeDir(cfg.Agent.Default.RetainedWorktreeRoot)
	}

	normalizeWorkspace(&cfg.Workspace)

	return cfg, nil
}

// HasOldConfig checks a raw config file for old-style top-level "default:"
// or singular "agent:" keys that need migration to "agents:".
func HasOldConfig(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "\ndefault:") || strings.Contains(s, "\nagent:")
}

// ResolveEditor returns the editor command to use, with this priority:
//  1. cfg.Editor (config file)
//  2. $EDITOR environment variable
//  3. $VISUAL environment variable
//  4. "vi" (hardcoded fallback)
//
// If cfg is nil, only environment variables and the fallback are checked.
func ResolveEditor(cfg *Config) string {
	if cfg != nil && cfg.Editor != "" {
		return cfg.Editor
	}
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	if visual := os.Getenv("VISUAL"); visual != "" {
		return visual
	}
	return "vi"
}

func expandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "$HOME/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return filepath.Join(home, path[6:])
	}
	return path
}
