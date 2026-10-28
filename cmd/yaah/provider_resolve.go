package yaah

import (
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
)

// resolveApproval returns the effective approval mode.
// Order: CLI --approval flag → YAAH_APPROVAL env var → config file → "ask" default.
func resolveApproval(cfg *config.Config, opts SessionOptions) string {
	mode := cfg.Agent.Default.Approval
	if v := os.Getenv("YAAH_APPROVAL"); v != "" {
		mode = v
	}
	if opts.ApprovalOverride != "" {
		mode = opts.ApprovalOverride
	}
	switch mode {
	case "allow", "ask", "deny":
		return mode
	default:
		return "ask"
	}
}

// resolveMCPApproval returns the effective approval policy for
// MCP-served tools: config file → "ask" default. Remote tools cannot
// classify their own danger, so the default gates them behind user
// approval (review finding S3).
func resolveMCPApproval(cfg *config.Config) string {
	switch cfg.Agent.Default.MCPApproval {
	case "allow", "ask", "deny":
		return cfg.Agent.Default.MCPApproval
	default:
		return "ask"
	}
}

// resolveProviderName extracts the provider name from the config.
func resolveProviderName(cfg *config.Config) string {
	// 1. Explicit default.provider setting
	if cfg.Agent.Default.Provider != "" {
		if _, ok := cfg.Providers[cfg.Agent.Default.Provider]; ok {
			return cfg.Agent.Default.Provider
		}
	}
	// 2. Provider/model prefix in default.model
	if parts := strings.SplitN(cfg.Agent.Default.Model, "/", 2); len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[0]
		}
	}
	// 3. First provider alphabetically
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return "local"
}

// resolveModel extracts the model name part after the provider prefix.
// "openai/gpt-4o-mini" → "gpt-4o-mini", "gpt-4o-mini" → "gpt-4o-mini".
// When the prefix before "/" is not a known provider, the full model name
// is kept (e.g. "prism-ml/Bonsai-27B-gguf:Q1_0" stays intact).
func resolveModel(cfg *config.Config) string {
	parts := strings.SplitN(cfg.Agent.Default.Model, "/", 2)
	if len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[1]
		}
	}
	return cfg.Agent.Default.Model
}

// makeProvider returns a provider for the given config entry if it's usable
// (has a real API key, an OAuth token, or a local base URL). Returns nil, false otherwise.
func makeProvider(name string, p config.Provider) (agent.Provider, bool) {
	r := config.Resolve(p)

	// OAuth-authenticated providers load their token from disk.
	if r.Auth == "oauth" {
		return makeOAuthProvider(name, r)
	}

	if !isRealKey(r.APIKey) && r.BaseURL == "" {
		return nil, false
	}
	switch r.API {
	case "anthropic":
		return newAnthropicProvider(r, r.APIKey), true
	default:
		return newOpenAICompatProvider(r, r.APIKey), true
	}
}

// newAnthropicProvider builds the native (or Anthropic-compatible) Messages API
// client from a resolved provider config.
func newAnthropicProvider(r config.Provider, apiKey string) *providers.AnthropicClient {
	client := providers.NewAnthropicClient(r.BaseURL, apiKey, r.TimeoutSeconds)
	client.DefaultMaxTokens = r.MaxTokens
	client.ReasoningReplay = r.ReasoningReplay
	return client
}

// newOpenAICompatProvider builds an OpenAI-compatible client from a resolved
// provider config and is the ONE place that applies provider policy, so the
// api_key and OAuth paths cannot drift apart. Copilot is derived here exactly
// once: it drives both the system-message merge (CopilotMode) and the implicit
// cache_control opt-in below.
func newOpenAICompatProvider(r config.Provider, apiKey string) *providers.OpenAIClient {
	client := providers.NewOpenAIClient(r.BaseURL, apiKey, r.TimeoutSeconds)
	client.ExtraHeaders = r.Headers
	client.DefaultMaxTokens = r.MaxTokens
	client.CopilotMode = isCopilotEndpoint(r)
	// Copilot is the one OpenAI-compatible endpoint already accepting
	// cache_control breakpoints in practice, so it keeps emitting them;
	// every other OpenAI-shaped provider must opt in explicitly.
	client.SupportsCacheControl = r.SupportsCacheControl || client.CopilotMode
	applyThinkingOverrides(client, r)
	return client
}

// isCopilotEndpoint reports whether the provider talks to GitHub Copilot. The
// API name is authoritative, but Copilot is also configured with a plain
// api_key + base_url by hand, so the host is checked too — missing it would
// silently disable both the system-message merge and the cache_control opt-in.
// The host is parsed and compared on dot boundaries: a substring match would
// also fire for a lookalike like api-githubcopilot.com.attacker.example and
// hand an arbitrary endpoint Copilot's cache_control opt-in.
func isCopilotEndpoint(r config.Provider) bool {
	if r.API == "copilot" {
		return true
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "githubcopilot.com" || strings.HasSuffix(host, ".githubcopilot.com")
}

// cacheControlOptedIn reports whether the provider sends cache_control: only
// providers that opted in (supports_cache_control, or Copilot which sets it)
// do; the loop's prompt_caching breakpoints reach the wire only then.
func cacheControlOptedIn(prov agent.Provider) bool {
	oc, ok := prov.(*providers.OpenAIClient)
	return !ok || oc.SupportsCacheControl
}

// warnPromptCachingNotOptedIn emits a one-time warning when prompt_caching is
// enabled but the resolved provider has not opted in: the setting then caches
// nothing, and without the warning a caching-enabled config silently sends
// plain messages with no trace of why.
func warnPromptCachingNotOptedIn(cfg *config.Config, name string, prov agent.Provider) {
	if cfg.Agent.Default.PromptCaching && !cacheControlOptedIn(prov) {
		slog.Warn("prompt_caching is enabled but this provider has not opted in to cache_control; prompt caching is not sent",
			"provider", name)
	}
}

// makeOAuthProvider creates a provider using a stored OAuth token.
// The token is used as a bearer key. If no token is stored, it returns
// a stub that tells the user to run 'yaah login'.
func makeOAuthProvider(name string, r config.Provider) (agent.Provider, bool) {
	token, err := oauthTokenStore().Load(name)
	if err != nil {
		return &providers.OAuthErrorStub{Provider: name, Err: err}, true
	}
	if token == nil {
		return &providers.OAuthErrorStub{Provider: name}, true
	}

	switch r.API {
	case "anthropic":
		return newAnthropicProvider(r, token.AccessToken), true
	default:
		return newOpenAICompatProvider(r, token.AccessToken), true
	}
}

// applyThinkingOverrides sets per-model thinking overrides from config on an OpenAIClient.
func applyThinkingOverrides(client *providers.OpenAIClient, r config.Provider) {
	overrides := make(map[string]*bool, len(r.Models))
	for _, m := range r.Models {
		if m.Thinking != nil {
			overrides[m.Name] = m.Thinking
		}
	}
	if len(overrides) > 0 {
		client.ThinkingOverrides = overrides
	}
}

// resolveCompact returns the provider and model to use for context compaction.
// Uses the configured compaction_model (no tools, fast summarization) when
// set, otherwise falls back to the main provider and model — with a warning,
// because the main model is slower and worse at summarizing, and a silent
// fallback looks like compaction working while it degrades.
func resolveCompact(cfg *config.Config) (agent.Provider, string) {
	name := cfg.Agent.Default.CompactionModel
	if name == "" {
		slog.Warn("compaction: no compaction_model configured; context compaction will run on the main model")
		return nil, ""
	}

	compactProviderName, compactModel := "", ""
	if parts := strings.SplitN(name, "/", 2); len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			compactProviderName = parts[0]
			compactModel = parts[1]
		} else {
			compactModel = name
			compactProviderName = resolveProviderName(cfg)
		}
	} else {
		compactModel = name
		compactProviderName = resolveProviderName(cfg)
	}
	if compactProviderName != "" {
		if p, ok := cfg.Providers[compactProviderName]; ok {
			if prov, ok2 := makeProvider(compactProviderName, p); ok2 {
				// Compare the resolved pair: the configured main model may
				// carry a "provider/" prefix while compactModel is already
				// stripped.
				mainProvider := resolveProviderName(cfg)
				if compactModel == resolveModel(cfg) && compactProviderName == mainProvider {
					slog.Warn("compaction: compaction_model equals the main model; "+
						"set a cheaper compaction_model for faster, more reliable summarization",
						"model", compactModel)
				}
				return prov, compactModel
			}
		}
	}
	slog.Warn("compaction: compaction_model could not be resolved to a usable provider; "+
		"context compaction will run on the main model",
		"compaction_model", name)
	return nil, ""
}

// resolveFallback returns the provider and model to use when the primary
// provider fails with auth, billing, or rate-limit errors.
// Returns nil if no fallback is configured.
func resolveFallback(cfg *config.Config) (agent.Provider, string, string) {
	if cfg.Agent.Fallback.Provider == "" {
		return nil, "", ""
	}
	if p, ok := cfg.Providers[cfg.Agent.Fallback.Provider]; ok {
		if prov, ok2 := makeProvider(cfg.Agent.Fallback.Provider, p); ok2 {
			return prov, cfg.Agent.Fallback.Model, cfg.Agent.Fallback.Provider
		}
	}
	return nil, "", ""
}

// resolveSubAgent returns the provider and model to use for sub-agents
// spawned via the task tool. When unconfigured, returns nil — sub-agents
// fall back to the loop's provider and model (inherited from the planner).
//
// Use agent.subagent.provider and agent.subagent.model to select a
// different provider or model for sub-agents.
func resolveSubAgent(cfg *config.Config) (agent.Provider, string) {
	sc := cfg.Agent.SubAgent
	if sc.Provider == "" && sc.Model == "" {
		return nil, ""
	}

	providerName := sc.Provider
	if providerName == "" {
		providerName = resolveProviderName(cfg)
	}

	model := sc.Model
	if model == "" {
		model = resolveModel(cfg)
	}

	if p, ok := cfg.Providers[providerName]; ok {
		if prov, ok2 := makeProvider(providerName, p); ok2 {
			return prov, model
		}
	}
	return nil, ""
}

// resolveProviderByName looks up a provider by name from the config map.
// Returns nil if the name is not found or the provider has no valid key/URL.
// The caller falls through to the next step in the resolution chain.
func resolveProviderByName(pmap map[string]config.Provider, name string) agent.Provider {
	if p, ok := pmap[name]; ok {
		if prov, ok2 := makeProvider(name, p); ok2 {
			return prov
		}
	}
	return nil
}

// resolveProvider picks the best available provider from the config.
func resolveProvider(cfg *config.Config) agent.Provider {
	providerName := resolveProviderName(cfg)
	if p, ok := cfg.Providers[providerName]; ok {
		if prov, ok2 := makeProvider(providerName, p); ok2 {
			warnPromptCachingNotOptedIn(cfg, providerName, prov)
			return prov
		}
	}

	// Deterministic fallback: prefer the first provider by sorted name.
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Providers[name]
		if prov, ok := makeProvider(name, p); ok {
			warnPromptCachingNotOptedIn(cfg, name, prov)
			return prov
		}
	}

	// Last resort: return a stub that explains the issue
	return &providers.NoProviderStub{}
}

// makeModelLister adapts makeProvider to the providers.ModelLister
// capability for FetchAllModels. Returns nil, false when the provider is
// unavailable or doesn't support model listing.
func makeModelLister(name string, p config.Provider) (providers.ModelLister, bool) {
	prov, ok := makeProvider(name, p)
	if !ok {
		return nil, false
	}
	ml, ok := prov.(providers.ModelLister)
	if !ok {
		return nil, false
	}
	return ml, true
}

// isRealKey returns true if the API key looks like a real key (not empty,
// not a placeholder, not an unsubstituted env var).
func isRealKey(key string) bool {
	if key == "" || key == "(not set)" || key == "(too short)" {
		return false
	}
	if strings.Contains(key, "${") {
		return false
	}
	return true
}

// buildStuckChildTimeouts converts per-role StuckChildTimeout seconds from
// config into a map of role name → time.Duration for the Loop.
func buildStuckChildTimeouts(cfg config.SubAgentConfig) map[string]time.Duration {
	if len(cfg.Roles) == 0 {
		return nil
	}
	m := make(map[string]time.Duration, len(cfg.Roles))
	for name, rc := range cfg.Roles {
		if rc.StuckChildTimeout > 0 {
			m[name] = time.Duration(rc.StuckChildTimeout) * time.Second
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// resolveDirectives merges CLI --directive flags (prepended) with config
// directives. CLI flags take positional priority.
func resolveDirectives(cfg *config.Config, opts SessionOptions) []string {
	if len(opts.DirectiveOverrides) == 0 {
		return cfg.Agent.Default.Directives
	}
	if len(cfg.Agent.Default.Directives) == 0 {
		return opts.DirectiveOverrides
	}
	out := make([]string, 0, len(opts.DirectiveOverrides)+len(cfg.Agent.Default.Directives))
	out = append(out, opts.DirectiveOverrides...)
	out = append(out, cfg.Agent.Default.Directives...)
	return out
}
