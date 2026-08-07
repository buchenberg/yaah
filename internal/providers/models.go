package providers

import (
	"context"
	"log/slog"
	"sort"

	"github.com/buchenberg/yaah/internal/config"
)

// ModelLister is the narrow capability FetchAllModels needs.
// OpenAIClient and AnthropicClient satisfy this interface.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// FetchAllModels gathers model IDs from all configured providers.
// If a provider has a models: override in config, those are used.
// Otherwise, ListModels is called. The makeLister callback constructs a
// ModelLister from a config entry (returning nil, false when the
// provider is unavailable or doesn't support model listing).
// Results are returned in "provider/model" format, sorted by provider.
func FetchAllModels(ctx context.Context, cfg *config.Config,
	makeLister func(name string, p config.Provider) (ModelLister, bool),
) []string {
	var all []string
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Providers[name]
		if len(p.Models) > 0 {
			for _, m := range p.Models {
				all = append(all, name+"/"+m.Name)
			}
			continue
		}
		lister, ok := makeLister(name, p)
		if !ok {
			continue
		}
		models, err := lister.ListModels(ctx)
		if err != nil {
			slog.Warn("fetch models failed", "provider", name, "error", err)
			continue
		}
		for _, m := range models {
			all = append(all, name+"/"+m)
		}
	}
	return all
}
