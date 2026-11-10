package runner

import (
	"context"
	"testing"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// routeProvider is a named no-op provider: the routing tests assert
// identity, so each configured provider needs a distinguishable instance.
type routeProvider struct{ name string }

func (p routeProvider) Send(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}

// routeTestOpts builds the dispatch-routing fixture:
//
//	planner (parent session)   → provider "zai", model "zai-model"
//	global sub-agent override  → provider "sub", model "sub-model"
//	config role entry (dev)    → provider "cfg",  model "cfg-model"
//	role file (dev)            → provider "file", model "file-model"
//
// Every name resolves to its own routeProvider instance, so the tests
// can assert which provider actually won, not just its label.
func routeTestOpts() (taskRunnerOpts, map[string]agent.Provider) {
	providers := map[string]agent.Provider{
		"zai":   routeProvider{"zai"},
		"sub":   routeProvider{"sub"},
		"cfg":   routeProvider{"cfg"},
		"file":  routeProvider{"file"},
		"alt":   routeProvider{"alt"},
		"nokey": nil, // configured but unusable: the resolver returns nil
	}
	opts := taskRunnerOpts{
		provider:         providers["zai"],
		providerName:     "zai",
		modelName:        "zai-model",
		subAgentProvider: providers["sub"],
		subAgentModel:    "sub-model",
		subCfg: config.SubAgentConfig{
			Provider: "sub",
			Roles: map[string]config.RoleConfig{
				"dev": {Provider: "cfg", Model: "cfg-model"},
			},
		},
		resolveProviderByName: func(_ map[string]config.Provider, name string) agent.Provider {
			return providers[name]
		},
	}
	return opts, providers
}

// TestResolveDispatchRoute pins the parity-W1 dispatch chain:
// per-call > config role entry > role file > global sub-agent override >
// parent session model > config default, field-wise, with every
// unresolvable provider warning and falling through.
func TestResolveDispatchRoute(t *testing.T) {
	tests := []struct {
		name         string
		role         subagent.SubAgentRole
		params       tools.SubAgentParams
		profile      subagent.RoleProfile
		wantProvider string
		wantModel    string
	}{
		{
			name:         "nothing set inherits the global sub-agent override",
			role:         "other",
			wantProvider: "sub",
			wantModel:    "sub-model",
		},
		{
			name:         "call provider and model win over every file",
			role:         "dev",
			params:       tools.SubAgentParams{Provider: "alt", Model: "call-model"},
			profile:      subagent.RoleProfile{Provider: "file", Model: "file-model"},
			wantProvider: "alt",
			wantModel:    "call-model",
		},
		{
			name:         "call provider alone still takes the call model from below",
			role:         "other",
			params:       tools.SubAgentParams{Provider: "alt", Model: "m"},
			wantProvider: "alt",
			wantModel:    "m",
		},
		{
			name:         "call model alone keeps the lower provider",
			role:         "other",
			params:       tools.SubAgentParams{Model: "call-model"},
			wantProvider: "sub",
			wantModel:    "call-model",
		},
		{
			name:         "config role entry wins over the role file",
			role:         "dev",
			profile:      subagent.RoleProfile{Provider: "file", Model: "file-model"},
			wantProvider: "cfg",
			wantModel:    "cfg-model",
		},
		{
			name:         "role file routes the dispatch",
			role:         "other",
			profile:      subagent.RoleProfile{Provider: "file", Model: "file-model"},
			wantProvider: "file",
			wantModel:    "file-model",
		},
		{
			name:         "role file provider switches, model falls to the parent model",
			role:         "other",
			profile:      subagent.RoleProfile{Provider: "file"},
			wantProvider: "file",
			wantModel:    "zai-model",
		},
		{
			name:         "unknown call provider warns and falls through to the role file",
			role:         "other",
			params:       tools.SubAgentParams{Provider: "ghost"},
			profile:      subagent.RoleProfile{Provider: "file", Model: "file-model"},
			wantProvider: "file",
			wantModel:    "file-model",
		},
		{
			name:         "unusable role file provider falls through to the global override",
			role:         "other",
			profile:      subagent.RoleProfile{Provider: "nokey", Model: "file-model"},
			wantProvider: "sub",
			wantModel:    "file-model",
		},
		{
			name:         "call provider with no model and no lower source takes the parent model",
			role:         "nobody",
			params:       tools.SubAgentParams{Provider: "alt"},
			wantProvider: "alt",
			wantModel:    "zai-model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, providers := routeTestOpts()
			got := resolveDispatchRoute(opts, tc.role, tc.params, tc.profile)
			if got.providerName != tc.wantProvider {
				t.Errorf("providerName = %q, want %q", got.providerName, tc.wantProvider)
			}
			if got.model != tc.wantModel {
				t.Errorf("model = %q, want %q", got.model, tc.wantModel)
			}
			if tc.wantProvider == "nokey" || tc.wantProvider == "ghost" {
				t.Fatalf("test expectation names a provider that must never win")
			}
			if got.provider == nil {
				t.Fatalf("provider is nil, want the %q instance", tc.wantProvider)
			}
			if got.provider != providers[tc.wantProvider] {
				t.Errorf("provider instance does not match %q", tc.wantProvider)
			}
		})
	}
}

// TestResolveDispatchRouteUnresolvableConfigEntryKeepsGlobalOverride pins
// the fall-through fix: a config role entry naming an unusable provider
// must not drop the global sub-agent provider on the way down.
func TestResolveDispatchRouteUnresolvableConfigEntryKeepsGlobalOverride(t *testing.T) {
	opts, providers := routeTestOpts()
	opts.subCfg.Roles["dev"] = config.RoleConfig{Provider: "nokey", Model: "cfg-model"}

	got := resolveDispatchRoute(opts, "dev", tools.SubAgentParams{}, subagent.RoleProfile{})
	if got.providerName != "sub" {
		t.Errorf("providerName = %q, want the global sub-agent override %q", got.providerName, "sub")
	}
	if got.provider != providers["sub"] {
		t.Error("provider instance is not the global sub-agent override")
	}
	if got.model != "cfg-model" {
		t.Errorf("model = %q, want the config role entry's model", got.model)
	}
}
