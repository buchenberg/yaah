package yaah

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
)

// resolveApproval returns the effective approval mode.
// Order: CLI --approval flag → YAAH_APPROVAL env var → config file → "ask" default.
func resolveApproval(cfg *config.Config) string {
	mode := cfg.Default.Approval
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
	if cfg.Default.Provider != "" {
		if _, ok := cfg.Providers[cfg.Default.Provider]; ok {
			return cfg.Default.Provider
		}
	}
	// 2. Provider/model prefix in default.model
	if parts := strings.SplitN(cfg.Default.Model, "/", 2); len(parts) == 2 {
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
	parts := strings.SplitN(cfg.Default.Model, "/", 2)
	if len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[1]
		}
	}
	return cfg.Default.Model
}

// makeProvider returns a provider for the given config entry if it's usable
// (has a real API key or a local base URL). Returns nil, false otherwise.
func makeProvider(p config.Provider) (agent.Provider, bool) {
	if isRealKey(p.APIKey) || p.BaseURL != "" {
		return providers.NewOpenAIClient(p.BaseURL, p.APIKey, p.TimeoutSeconds), true
	}
	return nil, false
}

// resolveCompact returns the provider and model to use for context compaction.
// Uses the configured small_model (no tools, fast summarization) if available,
// otherwise falls back to the main provider and model.
func resolveCompact(cfg *config.Config) (agent.Provider, string) {
	if cfg.Default.SmallModel != "" {
		compactProviderName, compactModel := "", ""
		if parts := strings.SplitN(cfg.Default.SmallModel, "/", 2); len(parts) == 2 {
			if _, ok := cfg.Providers[parts[0]]; ok {
				compactProviderName = parts[0]
				compactModel = parts[1]
			} else {
				compactModel = cfg.Default.SmallModel
				compactProviderName = resolveProviderName(cfg)
			}
		} else {
			compactModel = cfg.Default.SmallModel
			compactProviderName = resolveProviderName(cfg)
		}
		if compactProviderName != "" {
			if p, ok := cfg.Providers[compactProviderName]; ok {
				if prov, ok2 := makeProvider(p); ok2 {
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
func resolveFallback(cfg *config.Config) (agent.Provider, string) {
	if cfg.Default.FallbackProvider == "" {
		return nil, ""
	}
	if p, ok := cfg.Providers[cfg.Default.FallbackProvider]; ok {
		if prov, ok2 := makeProvider(p); ok2 {
			return prov, cfg.Default.FallbackModel
		}
	}
	return nil, ""
}

// resolveExecutor returns the provider and model for the inner executor loop
// used by the dual-loop architecture. Returns nil if no separate executor is
// configured — the inner loop falls back to the main provider and model.
func resolveExecutor(cfg *config.Config) (agent.Provider, string) {
	ec := cfg.Agent.Executor
	if ec.Provider == "" {
		return nil, ""
	}
	if p, ok := cfg.Providers[ec.Provider]; ok {
		if prov, ok2 := makeProvider(p); ok2 {
			return prov, ec.Model
		}
	}
	return nil, ""
}

// resolveProvider picks the best available provider from the config.
func resolveProvider(cfg *config.Config) agent.Provider {
	providerName := resolveProviderName(cfg)
	if p, ok := cfg.Providers[providerName]; ok {
		if prov, ok2 := makeProvider(p); ok2 {
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
		if prov, ok := makeProvider(p); ok {
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
