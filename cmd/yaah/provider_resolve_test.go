package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/config"
)

// TestResolveCompact_UsesConfiguredModel pins yaah-v378.4: the resolved
// compaction provider carries the configured model, and an unset value falls
// back to the main model (nil provider) — the resolution the warning exists
// to make visible.
func TestResolveCompact_UsesConfiguredModel(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"p": {
				API:     "anthropic",
				BaseURL: "https://example.invalid",
				APIKey:  "k",
			},
		},
	}
	cfg.Agent.Default.Provider = "p"
	cfg.Agent.Default.Model = "main-model"
	cfg.Agent.Default.CompactionModel = "compact-model"

	prov, model := resolveCompact(cfg)
	if prov == nil {
		t.Fatal("expected a resolved compaction provider")
	}
	if model != "compact-model" {
		t.Errorf("model = %q, want compact-model", model)
	}
}

// TestResolveCompact_PrefixedModelStripsProvider: "p/compact-model" must
// resolve to provider p and model compact-model.
func TestResolveCompact_PrefixedModelStripsProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"p": {API: "anthropic", BaseURL: "https://example.invalid", APIKey: "k"},
		},
	}
	cfg.Agent.Default.Provider = "p"
	cfg.Agent.Default.Model = "main-model"
	cfg.Agent.Default.CompactionModel = "p/compact-model"

	prov, model := resolveCompact(cfg)
	if prov == nil || model != "compact-model" {
		t.Fatalf("resolveCompact = (%v, %q), want (provider, compact-model)", prov != nil, model)
	}
}

// TestResolveCompact_UnsetFallsBackToMain: unset means the main provider and
// model (nil, ""), with the warning explaining the consequence.
func TestResolveCompact_UnsetFallsBackToMain(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{}}
	cfg.Agent.Default.Model = "main-model"

	prov, model := resolveCompact(cfg)
	if prov != nil || model != "" {
		t.Fatalf("resolveCompact = (%v, %q), want (nil, \"\")", prov != nil, model)
	}
}
