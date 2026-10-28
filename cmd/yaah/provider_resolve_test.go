package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
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

// TestIsCopilotEndpoint pins the host matching: a lookalike host must not
// claim Copilot's cache_control opt-in by substring (review 616).
func TestIsCopilotEndpoint(t *testing.T) {
	cases := []struct {
		name string
		r    config.Provider
		want bool
	}{
		{name: "api name", r: config.Provider{API: "copilot", BaseURL: "https://example.com"}, want: true},
		{name: "host exact", r: config.Provider{BaseURL: "https://githubcopilot.com"}, want: true},
		{name: "host subdomain", r: config.Provider{BaseURL: "https://api.githubcopilot.com"}, want: true},
		{name: "host case-insensitive", r: config.Provider{BaseURL: "https://API.GITHUBCOPILOT.COM"}, want: true},
		{name: "lookalike host prefix", r: config.Provider{BaseURL: "https://api-githubcopilot.com.attacker.example"}, want: false},
		{name: "lookalike host suffix", r: config.Provider{BaseURL: "https://githubcopilot.com.evil.example"}, want: false},
		{name: "substring in path", r: config.Provider{BaseURL: "https://evil.example/githubcopilot.com"}, want: false},
		{name: "substring in userinfo", r: config.Provider{BaseURL: "https://githubcopilot.com@evil.example/"}, want: false},
		{name: "unrelated", r: config.Provider{BaseURL: "https://api.z.ai"}, want: false},
	}
	for _, tc := range cases {
		if got := isCopilotEndpoint(tc.r); got != tc.want {
			t.Errorf("isCopilotEndpoint(%q) = %v, want %v", tc.r.BaseURL, got, tc.want)
		}
	}
}

// TestCacheControlOptedIn pins the opt-in gate behind the prompt_caching
// warning (review 616).
func TestCacheControlOptedIn(t *testing.T) {
	if !cacheControlOptedIn(&providers.NoProviderStub{}) {
		t.Error("non-OpenAI providers do not gate cache_control behind opt-in")
	}
	if cacheControlOptedIn(&providers.OpenAIClient{}) {
		t.Error("OpenAI client without opt-in must not send cache_control")
	}
	if !cacheControlOptedIn(&providers.OpenAIClient{SupportsCacheControl: true}) {
		t.Error("OpenAI client with opt-in must send cache_control")
	}
}
