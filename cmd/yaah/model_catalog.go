// model_catalog.go holds the one model-catalog cache the model pickers share.
// Discovery runs once per process: the TUI picker, the web command palette and
// ACP's session/set_config_option all read the same snapshot, so no surface can
// offer a model another one lacks — and none of them re-runs the network round.
package yaah

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
)

const (
	// modelCatalogBudget bounds the single network round of model discovery.
	// A provider endpoint that is slow or unreachable must not hold up a
	// surface, so the fetch is abandoned at the deadline and the catalog
	// keeps whatever config already declares.
	modelCatalogBudget = 2 * time.Second

	// modelCatalogWait bounds how long a request-path caller waits for a
	// discovery attempt that is already running. Discovery keeps going in
	// the background; a caller that misses the window gets the current
	// snapshot and the next one sees the result. Without this bound a slow
	// provider would stall an SSE stream — or the ACP dispatch goroutine and
	// every message queued behind it — for the whole budget.
	modelCatalogWait = 500 * time.Millisecond

	// modelCatalogRetry is how long an incomplete result is cached before a
	// later request may retry discovery. Without the cooldown a provider that
	// was unreachable at startup would make every request pay the full budget
	// again.
	modelCatalogRetry = 30 * time.Second
)

// modelSnapshot is one cached catalog result: the "provider/model" ids a picker
// offers plus the provider display names config declares.
type modelSnapshot struct {
	IDs           []string
	ProviderNames map[string]string
}

// modelCatalog is the process-lifetime model catalog behind every picker.
//
// Discovery is two-phase. The fast path answers from config alone — every model
// a provider declares under models: is offered with no network call, so a
// picker always has content. The slow path then asks each provider that
// declares no models to list its own; it runs once, under modelCatalogBudget,
// and the result is cached for the process lifetime. Discovery failures are
// logged and dropped: model discovery is a convenience and fails nothing.
type modelCatalog struct {
	cfg  *config.Config
	logf func(format string, args ...any)

	budget time.Duration
	retry  time.Duration

	// wait overrides modelCatalogWait when non-zero (tests).
	wait time.Duration

	// discoverFn replaces the discovery phase in tests. It must be set before
	// the first Load/Current call.
	discoverFn func(ctx context.Context) (modelSnapshot, bool)

	mu       sync.Mutex
	settled  bool
	snap     modelSnapshot
	inflight chan struct{} // non-nil while a discovery attempt runs
	lastTry  time.Time     // when the last attempt published its result
}

// newModelCatalog builds the catalog for cfg. Nothing is fetched here: the
// first Load or Current starts discovery, so a surface warms it from a
// background goroutine and never blocks its own startup on the network.
func newModelCatalog(cfg *config.Config, logf func(format string, args ...any)) *modelCatalog {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &modelCatalog{
		cfg:    cfg,
		logf:   logf,
		budget: modelCatalogBudget,
		retry:  modelCatalogRetry,
	}
}

// Load returns the catalog snapshot, running discovery when none is cached and
// waiting for that attempt to finish (the network phase is bounded by the
// budget). Surfaces call it once at startup from a background goroutine, so the
// frame they publish carries the full provider list rather than the
// config-only subset.
func (c *modelCatalog) Load(ctx context.Context) modelSnapshot {
	done, current := c.begin()
	if done == nil {
		return current
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	return c.current()
}

// Current returns the snapshot available now, waiting at most modelCatalogWait
// for an attempt that is already running: the attempt keeps going and publishes
// its result for whoever asks next. Request paths (an SSE connect, an ACP
// session/new) use this so a slow provider never stalls them.
func (c *modelCatalog) Current(ctx context.Context) modelSnapshot {
	done, current := c.begin()
	if done == nil {
		return current
	}
	wait := c.wait
	if wait <= 0 {
		wait = modelCatalogWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return c.current()
}

// begin returns the attempt the caller must wait on — nil when the snapshot is
// final or cooling down — together with the snapshot it can use meanwhile.
func (c *modelCatalog) begin() (chan struct{}, modelSnapshot) {
	if c == nil || c.cfg == nil {
		return nil, modelSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settled {
		return nil, c.snap
	}
	if c.inflight != nil {
		return c.inflight, c.snap
	}
	// An incomplete result is cached for one cooldown, so a provider that is
	// down at startup cannot make every request pay the full budget.
	if !c.lastTry.IsZero() && time.Since(c.lastTry) < c.retry {
		return nil, c.snap
	}
	done := make(chan struct{})
	c.inflight = done
	go c.refresh(done)
	return done, c.snap
}

// current reads the published snapshot.
func (c *modelCatalog) current() modelSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

// refresh runs one discovery attempt and publishes the result. It holds no lock
// across the network call, so callers wait on the channel instead of a mutex.
func (c *modelCatalog) refresh(done chan struct{}) {
	defer close(done)
	discover := c.discover
	if c.discoverFn != nil {
		discover = c.discoverFn
	}
	snap, complete := discover(context.Background())
	// Config-declared models are worth keeping even when the fetch did not
	// finish, but the slow path is only marked settled once it completed: a
	// caller whose wait was abandoned must not leave the catalog stuck at the
	// static subset for the rest of the process.
	c.mu.Lock()
	c.snap = snap
	c.settled = complete
	c.inflight = nil
	// The cooldown starts when the result is published, not when the attempt
	// started, so a full-budget attempt does not eat into it.
	c.lastTry = time.Now()
	c.mu.Unlock()
}

// discover builds the snapshot — config-declared models first, then whatever
// the providers report — and reports whether the network phase finished inside
// the budget, which is what makes the result final.
func (c *modelCatalog) discover(ctx context.Context) (modelSnapshot, bool) {
	snap := modelSnapshot{ProviderNames: make(map[string]string, len(c.cfg.Providers))}
	seen := make(map[string]bool, len(c.cfg.Providers))
	for _, name := range sortedProviderNames(c.cfg) {
		p := c.cfg.Providers[name]
		if p.Name != "" {
			snap.ProviderNames[name] = p.Name
		}
		for _, m := range p.Models {
			if m.Name == "" {
				continue
			}
			id := name + "/" + m.Name
			if seen[id] {
				continue
			}
			seen[id] = true
			snap.IDs = append(snap.IDs, id)
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()
	for _, id := range providers.FetchAllModels(fetchCtx, c.cfg, makeModelLister) {
		if seen[id] {
			continue
		}
		seen[id] = true
		snap.IDs = append(snap.IDs, id)
	}
	if err := fetchCtx.Err(); err != nil {
		c.logf("model catalog: discovery incomplete (%v), offering %d model(s) from config", err, len(snap.IDs))
		return snap, false
	}
	return snap, true
}

// newACPModelCatalog builds the process-lifetime catalog handed to the ACP
// server. Discovery reuses the cache behind the TUI picker and the web command
// palette (the shared modelCatalog), so ACP offers exactly the models those
// surfaces offer rather than a second, drifting code path.
//
// A config that cannot be loaded, or that declares no providers, disables the
// feature: the catalog is nil, which the ACP server treats as "no model
// picker", and serve carries on. Nothing here can fail acp-serve. Discovery
// starts here, off the request path, so the first session/new normally finds a
// warm catalog instead of paying for the network round.
func newACPModelCatalog(logf func(format string, args ...any)) acp.ModelCatalog {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cfg, err := config.Load()
	if err != nil {
		logf("model catalog disabled: %v", err)
		return nil
	}
	if len(cfg.Providers) == 0 {
		logf("model catalog disabled: no providers configured")
		return nil
	}
	catalog := newModelCatalog(cfg, logf)
	go catalog.Load(context.Background())
	return &acpModelCatalog{catalog: catalog}
}

// acpModelCatalog adapts the shared catalog to acp.ModelCatalog: the flat
// "provider/model" ids become the flat options a client's picker shows, each
// labelled with the provider's display name when config sets one so the picker
// reads "claude-sonnet-4 (Anthropic)" instead of a bare slug.
type acpModelCatalog struct{ catalog *modelCatalog }

// Options implements acp.ModelCatalog.
func (c *acpModelCatalog) Options(ctx context.Context) []acp.ModelOption {
	if c == nil || c.catalog == nil {
		return nil
	}
	snap := c.catalog.Current(ctx)
	if len(snap.IDs) == 0 {
		return nil
	}
	options := make([]acp.ModelOption, 0, len(snap.IDs))
	for _, id := range snap.IDs {
		providerName, modelName := splitModelID(id)
		label := providerName
		if display, ok := snap.ProviderNames[providerName]; ok {
			label = display
		}
		options = append(options, acp.ModelOption{
			Provider: providerName,
			Model:    modelName,
			Name:     modelName + " (" + label + ")",
		})
	}
	return options
}

// sortedProviderNames lists the configured provider keys in stable order so the
// picker's contents do not shuffle between processes.
func sortedProviderNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// splitModelID splits a "provider/model" id. Only the first separator belongs
// to the provider: model names may contain slashes of their own (e.g.
// "prism-ml/Bonsai-27B-gguf:Q1_0").
func splitModelID(id string) (providerName, modelName string) {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, id
}
