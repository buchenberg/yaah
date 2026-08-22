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
	Provider              string  `yaml:"provider"`
	Model                 string  `yaml:"model"`
	SmallModel            string  `yaml:"small_model"`
	MaxLoopCycles         int     `yaml:"max_iterations"`
	MaxToolTurns          int     `yaml:"max_turns"`      // soft cap on tool-using turns; 0 = off
	ContextWindow         int     `yaml:"context_window"` // max context window; resolved window from model is capped by this value
	Approval              string  `yaml:"approval"`
	WorkspaceAsk          bool    `yaml:"workspace_ask"`             // prompt before denying out-of-workspace access (with --workspace)
	MaxInlineToolsPerTurn int     `yaml:"max_inline_tools_per_turn"` // 0 = unlimited
	EstimateFactor        float64 `yaml:"estimate_factor"`           // 0 = default (1.3)

	// Compaction controls context summarisation behaviour.
	CompactionThreshold    float64 `yaml:"compaction_threshold"`     // fraction of ContextWindow; 0 = 0.5
	RawCompactionThreshold float64 `yaml:"raw_compaction_threshold"` // fraction ignoring cache; 0 = 0.5
	CompactMaxMessages     int     `yaml:"compact_max_messages"`     // force compaction above N messages; 0 = off

	// Loop detection governs when the agent halts on repeated tool calls.
	LoopDetectCount  int `yaml:"loop_detect_count"`  // identical calls to trigger halt; 0 = default (5)
	LoopDetectWindow int `yaml:"loop_detect_window"` // sliding window size; 0 = default (10)

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

	// Directives are session-level policy statements injected into the
	// top-level agent's system prompt, immediately after the identity
	// block. Sub-agents do NOT inherit these; they receive per-role
	// directives via SubAgentConfig.Roles[name].Directives instead.
	Directives []string `yaml:"directives"`

	// ShepherdTraceDir is the directory for the Shepherd trace store.
	// When set, Shepherd tracing is enabled: sub-agent tool calls are
	// recorded, the supervisor tool is registered, and the supervised
	// task tool (checkpoint/rollback/retry) becomes available.
	ShepherdTraceDir string `yaml:"shepherd_trace_dir"`

	// SupervisedMaxRetries caps the rollback-and-retry cycles of the
	// supervised_task tool after the initial attempt. 0 (unset) defaults
	// to 1 — one rollback-and-retry.
	SupervisedMaxRetries int `yaml:"supervised_max_retries"`

	// SupervisedRepoPath is the git repository the supervised_task tool
	// checkpoints and rolls back. It is also the repository per-turn
	// checkpoints operate on. Empty (unset) defaults to the working
	// directory at execution time.
	SupervisedRepoPath string `yaml:"supervised_repo_path"`

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
	DefaultTimeout int `yaml:"default_timeout"`

	// StuckChildTimeout is the duration without a heartbeat before a
	// sub-agent is declared stuck and force-cancelled. The timer resets
	// on every iteration (heartbeat), so this is a per-iteration liveness
	// guard, not a total budget. Seconds. 0 disables.
	StuckChildTimeout int `yaml:"stuck_child_timeout"`

	// DefaultMaxTurns is the fallback soft turn cap when no role-specific
	// override is set. 0 means unlimited (off).
	DefaultMaxToolTurns int `yaml:"default_max_turns"`

	// JSONMode enables structured output via response_format json_object.
	// Individual roles may override with their own json_mode setting.
	JSONMode bool `yaml:"json_mode"`

	// OutputLimit caps the final synthesized result from a sub-agent in
	// bytes before it reaches the orchestrator. 0 means unlimited.
	OutputLimit int `yaml:"output_limit"`

	// Roles holds per-role overrides keyed by role name
	// ("analyst", "developer", "tester", "reviewer").
	Roles map[string]RoleConfig `yaml:"roles"`
}

// RoleConfig overrides a single role's default timeout, iteration cap,
// turn cap, provider, model, concurrency, output format, directives, and
// shepherding (supervised dispatch + per-turn checkpointing).
type RoleConfig struct {
	Timeout           int      `yaml:"timeout"`             // seconds; 0 = use role default
	MaxLoopCycles     int      `yaml:"max_iterations"`      // 0 = use role default
	MaxToolTurns      int      `yaml:"max_turns"`           // soft turn cap; 0 = use role default
	JSONMode          bool     `yaml:"json_mode"`           // structured output toggle
	ContextWindow     int      `yaml:"context_window"`      // 0 = inherit halved parent default
	OutputLimit       int      `yaml:"output_limit"`        // bytes; 0 = use config default
	Provider          string   `yaml:"provider"`            // per-role provider override; "" = inherit
	Model             string   `yaml:"model"`               // per-role model override; "" = inherit
	MaxConcurrency    int      `yaml:"max_concurrency"`     // per-role max sub-agent spawns; 0 = use config default
	StuckChildTimeout int      `yaml:"stuck_child_timeout"` // seconds; 0 = use global default
	Directives        []string `yaml:"directives"`          // injected into this role's sub-agent prompt; empty = none

	// Supervised routes the role exclusively through the supervised_task
	// tool (attempt-level git checkpoint + rollback + retry) and hides it
	// from spawn_subagent. Default false: the role is a plain sub-agent
	// dispatched via spawn_subagent with no checkpointing.
	Supervised bool `yaml:"supervised"`
	// TurnCheckpoints enables per-turn checkpoint/restore inside this
	// role's sub-agent loop. Default false. Independent of Supervised, but
	// only meaningful where a shared shepherd scope manager is configured.
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
	Endpoint    string `yaml:"endpoint"`     // OTLP gRPC endpoint (e.g. "localhost:4317")
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
				SmallModel:             "deepseek/deepseek-v4-flash",
				MaxLoopCycles:          50,
				ContextWindow:          1048576,
				Approval:               "ask",
				CompactionThreshold:    0.5,
				RawCompactionThreshold: 0.5,
				LoopDetectCount:        5,
				LoopDetectWindow:       10,
				RetryBackoffSecs:       1,
				ReasoningProtect:       2,
			},
			SubAgent: SubAgentConfig{
				MaxConcurrency:    3,
				StuckChildTimeout: 60,
				OutputLimit:       51200,
			},
		},
		Observability: ObservabilityConfig{
			Otel: OtelConfig{
				Enabled:     false,
				Endpoint:    "localhost:4317",
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
		API:            p.API,
		Auth:           p.Auth,
		BaseURL:        SubstituteEnv(p.BaseURL),
		APIKey:         SubstituteEnv(p.APIKey),
		Name:           p.Name,
		Models:         p.Models,
		TimeoutSeconds: p.TimeoutSeconds,
		Headers:        p.Headers,
		OAuthClientID:  SubstituteEnv(p.OAuthClientID),
		OAuthScope:     p.OAuthScope,
		OAuthDomain:    SubstituteEnv(p.OAuthDomain),
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
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config %s: %w", path, err)
	}

	if cfg.Hooks.Dir != "" {
		cfg.Hooks.Dir = expandHomeDir(cfg.Hooks.Dir)
	}
	if cfg.Agent.Default.ShepherdTraceDir != "" {
		cfg.Agent.Default.ShepherdTraceDir = expandHomeDir(cfg.Agent.Default.ShepherdTraceDir)
	}
	if cfg.Agent.Default.SupervisedRepoPath != "" {
		cfg.Agent.Default.SupervisedRepoPath = expandHomeDir(cfg.Agent.Default.SupervisedRepoPath)
	}

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
