package yaah

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// resolveApproval returns the effective approval mode.
// Order: CLI --approval flag → YAAH_APPROVAL env var → config file → "ask" default.
func resolveApproval(cfg *config.Config) string {
	mode := cfg.Agent.Default.Approval
	if v := os.Getenv("YAAH_APPROVAL"); v != "" {
		mode = v
	}
	if approvalOverride != "" {
		mode = approvalOverride
	}
	switch mode {
	case "allow", "ask", "deny":
		return mode
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
		return providers.NewAnthropicClient(r.BaseURL, r.APIKey, r.TimeoutSeconds), true
	default:
		client := providers.NewOpenAIClient(r.BaseURL, r.APIKey, r.TimeoutSeconds)
		client.ExtraHeaders = r.Headers
		applyThinkingOverrides(client, r)
		return client, true
	}
}

// makeOAuthProvider creates a provider using a stored OAuth token.
// The token is used as a bearer key. If no token is stored, it returns
// a stub that tells the user to run 'yaah login'.
func makeOAuthProvider(name string, r config.Provider) (agent.Provider, bool) {
	token, err := providers.LoadOAuthToken(name)
	if err != nil {
		return &oauthErrorStub{provider: name, err: err}, true
	}
	if token == nil {
		return &oauthErrorStub{provider: name}, true
	}

	switch r.API {
	case "anthropic":
		return providers.NewAnthropicClient(r.BaseURL, token.AccessToken, r.TimeoutSeconds), true
	default:
		client := providers.NewOpenAIClient(r.BaseURL, token.AccessToken, r.TimeoutSeconds)
		client.ExtraHeaders = r.Headers
		client.CopilotMode = r.API == "copilot" || strings.Contains(r.BaseURL, "githubcopilot.com")
		applyThinkingOverrides(client, r)
		return client, true
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
// Uses the configured small_model (no tools, fast summarization) if available,
// otherwise falls back to the main provider and model.
func resolveCompact(cfg *config.Config) (agent.Provider, string) {
	if cfg.Agent.Default.SmallModel != "" {
		compactProviderName, compactModel := "", ""
		if parts := strings.SplitN(cfg.Agent.Default.SmallModel, "/", 2); len(parts) == 2 {
			if _, ok := cfg.Providers[parts[0]]; ok {
				compactProviderName = parts[0]
				compactModel = parts[1]
			} else {
				compactModel = cfg.Agent.Default.SmallModel
				compactProviderName = resolveProviderName(cfg)
			}
		} else {
			compactModel = cfg.Agent.Default.SmallModel
			compactProviderName = resolveProviderName(cfg)
		}
		if compactProviderName != "" {
			if p, ok := cfg.Providers[compactProviderName]; ok {
				if prov, ok2 := makeProvider(compactProviderName, p); ok2 {
					return prov, compactModel
				}
			}
		}
	}
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
			return prov
		}
	}

	// Last resort: return a stub that explains the issue
	return &noProviderStub{}
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

// noProviderStub is returned when no valid provider is configured.
type noProviderStub struct{}

func (s *noProviderStub) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("no provider configured — run 'yaah config edit' to add one")
}

// oauthErrorStub is returned when an OAuth provider has no stored token.
type oauthErrorStub struct {
	provider string
	err      error
}

func (s *oauthErrorStub) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if s.err != nil {
		return nil, fmt.Errorf("provider %q: %w", s.provider, s.err)
	}
	return nil, fmt.Errorf("provider %q not authenticated — run 'yaah login %s'", s.provider, s.provider)
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
func resolveDirectives(cfg *config.Config) []string {
	if len(directiveOverrides) == 0 {
		return cfg.Agent.Default.Directives
	}
	if len(cfg.Agent.Default.Directives) == 0 {
		return directiveOverrides
	}
	out := make([]string, 0, len(directiveOverrides)+len(cfg.Agent.Default.Directives))
	out = append(out, directiveOverrides...)
	out = append(out, cfg.Agent.Default.Directives...)
	return out
}
