package yaah

import (
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
)

// routeTestConfig is the minimal config for routing tests: one usable
// provider ("zai", base-url backed so no API key is needed) as the
// default, a second usable provider ("alt") for cross-provider cases, one
// unusable provider ("nokey"), one absent everywhere.
func routeTestConfig() *config.Config {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"zai":   {API: "anthropic", BaseURL: "https://example.invalid", APIKey: "k"},
			"alt":   {API: "openai", BaseURL: "https://alt.invalid", APIKey: "k2"},
			"nokey": {API: "anthropic", APIKey: ""}, // no base url, no key → unusable
		},
	}
	cfg.Agent.Default.Provider = "zai"
	cfg.Agent.Default.Model = "zai-model"
	return cfg
}

// TestPersonaRouteFor pins the W1 routing resolution: the persona file's
// provider/model override the config default; a "provider/model"-prefixed
// model without an explicit provider splits when the prefix names a
// configured provider; an unknown or unusable provider warns and falls
// back to the config default.
func TestPersonaRouteFor(t *testing.T) {
	tests := []struct {
		name         string
		persona      subagent.Persona
		wantProvider string
		wantModel    string
		wantInstance bool // persona names a USABLE provider → live instance
	}{
		{"no routing keys", subagent.Persona{Name: "p"}, "zai", "zai-model", false},
		{"provider only", subagent.Persona{Name: "p", Provider: "zai"}, "zai", "zai-model", true},
		{"provider only, other provider", subagent.Persona{Name: "p", Provider: "alt"}, "alt", "zai-model", true},
		{"provider and model", subagent.Persona{Name: "p", Provider: "zai", Model: "glm-5"}, "zai", "glm-5", true},
		{"prefixed model, no provider", subagent.Persona{Name: "p", Model: "zai/glm-5"}, "zai", "glm-5", true},
		{"unknown provider falls back", subagent.Persona{Name: "p", Provider: "ghost", Model: "m"}, "zai", "m", false},
		{"unusable provider falls back", subagent.Persona{Name: "p", Provider: "nokey"}, "zai", "zai-model", false},
		{"prefixed model with unknown prefix stays", subagent.Persona{Name: "p", Model: "openrouter/x/y"}, "zai", "openrouter/x/y", false},
		{"prefixed model with unusable prefix", subagent.Persona{Name: "p", Model: "nokey/m"}, "zai", "m", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := personaRouteFor(routeTestConfig(), tc.persona)
			if got.providerName != tc.wantProvider {
				t.Errorf("providerName = %q, want %q", got.providerName, tc.wantProvider)
			}
			if got.modelName != tc.wantModel {
				t.Errorf("modelName = %q, want %q", got.modelName, tc.wantModel)
			}
			if (got.provider != nil) != tc.wantInstance {
				t.Errorf("provider instance = %v, want live=%v", got.provider != nil, tc.wantInstance)
			}
		})
	}
}

// TestApplyPersonaRouting pins the session seam: a routing persona swaps
// the session's provider name and model; a persona without routing
// restores the config default (a user's earlier pick is superseded by
// the switch and can be re-picked after).
func TestApplyPersonaRouting(t *testing.T) {
	cfg := routeTestConfig()
	s := &agentSession{cfg: cfg}
	s.providerName = "zai"
	s.modelName = "user-picked"

	routed := subagent.Persona{Name: "architect", Provider: "zai", Model: "fast-model"}
	s.mu.Lock()
	s.applyPersonaRouting(routed)
	s.mu.Unlock()
	if got := s.modelName; got != "fast-model" {
		t.Errorf("after routing persona, modelName = %q, want fast-model", got)
	}

	plain := subagent.Persona{Name: "build"}
	s.mu.Lock()
	s.applyPersonaRouting(plain)
	s.mu.Unlock()
	if got, want := s.modelName, "zai-model"; got != want {
		t.Errorf("after plain persona, modelName = %q, want the config default %q", got, want)
	}
	if got := s.providerName; got != "zai" {
		t.Errorf("after plain persona, providerName = %q, want the config default zai", got)
	}
}

// TestApplyPersonaRoutingUnusablePrefix pins the mislabeling guard: a model
// prefix naming a configured-but-unusable provider must not rename the
// session's provider. The session keeps the config default's name and only
// the model part survives — its client is the default provider's, built
// from the default's config entry.
func TestApplyPersonaRoutingUnusablePrefix(t *testing.T) {
	cfg := routeTestConfig()
	s := &agentSession{cfg: cfg}
	prov, usable := makeProvider("zai", cfg.Providers["zai"])
	if !usable {
		t.Fatal("test config's default provider is unusable")
	}
	s.provider = prov
	s.providerName = "zai"
	s.modelName = "zai-model"

	s.mu.Lock()
	s.applyPersonaRouting(subagent.Persona{Name: "p", Model: "nokey/some-model"})
	s.mu.Unlock()

	if got, want := s.providerName, "zai"; got != want {
		t.Errorf("providerName = %q, want the config default %q (an unusable prefix must not rename the session)", got, want)
	}
	if got, want := s.modelName, "some-model"; got != want {
		t.Errorf("modelName = %q, want %q", got, want)
	}
	if s.provider == nil {
		t.Error("provider is nil; an unusable prefix must keep a usable client")
	}
}
