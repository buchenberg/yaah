package yaah

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/control"
)

// testModelCatalog builds a modelCatalog whose discovery phase is a test stub.
// The wait is long so a coordinated test controls when an attempt ends.
func testModelCatalog(discover func(context.Context) (modelSnapshot, bool)) *modelCatalog {
	return &modelCatalog{
		cfg:        &config.Config{},
		logf:       func(string, ...any) {},
		budget:     time.Second,
		retry:      modelCatalogRetry,
		wait:       5 * time.Second,
		discoverFn: discover,
	}
}

// TestModelCatalogSharesOneDiscovery pins single-flight: concurrent callers wait
// on one discovery attempt instead of triggering one each.
func TestModelCatalogSharesOneDiscovery(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return modelSnapshot{IDs: []string{"p/m"}}, true
	})

	first := make(chan modelSnapshot, 1)
	go func() { first <- catalog.Load(context.Background()) }()
	<-started

	second := make(chan modelSnapshot, 1)
	go func() { second <- catalog.Current(context.Background()) }()

	close(release)
	for i, got := range []modelSnapshot{<-first, <-second} {
		if len(got.IDs) != 1 || got.IDs[0] != "p/m" {
			t.Errorf("caller %d snapshot = %#v, want the discovered model", i, got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times, want 1", n)
	}
}

// TestModelCatalogCurrentHonorsContext pins that a caller's cancellation bounds
// only its own wait: the attempt keeps running and its result is cached for the
// next request (the SSE stream that went away did not cancel discovery).
func TestModelCatalogCurrentHonorsContext(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		started <- struct{}{}
		<-release
		return modelSnapshot{IDs: []string{"p/m"}}, true
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan modelSnapshot, 1)
	go func() { result <- catalog.Current(ctx) }()
	<-started
	cancel()

	select {
	case got := <-result:
		if len(got.IDs) != 0 {
			t.Errorf("canceled caller snapshot = %#v, want the empty one", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Current ignored its canceled context")
	}

	close(release)
	if got := catalog.Current(context.Background()); len(got.IDs) != 1 {
		t.Errorf("next caller snapshot = %#v, want the discovered model", got)
	}
}

// TestModelCatalogRetryCooldown pins that an incomplete discovery is not re-run
// on every call, and may be retried once the cooldown expired.
func TestModelCatalogRetryCooldown(t *testing.T) {
	var calls atomic.Int32
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		calls.Add(1)
		return modelSnapshot{IDs: []string{"p/m"}}, false
	})

	if got := catalog.Load(context.Background()); len(got.IDs) != 1 {
		t.Fatalf("first snapshot = %#v, want the config-declared model", got)
	}
	if got := catalog.Current(context.Background()); len(got.IDs) != 1 {
		t.Fatalf("cached snapshot = %#v, want the previous one", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery ran %d times inside the cooldown, want 1", n)
	}

	catalog.mu.Lock()
	catalog.lastTry = time.Now().Add(-modelCatalogRetry - time.Second)
	catalog.mu.Unlock()
	if got := catalog.Current(context.Background()); len(got.IDs) != 1 {
		t.Fatalf("retried snapshot = %#v, want the discovered model", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery ran %d times after the cooldown, want 2", n)
	}
}

// TestModelCatalogCurrentWaitIsBounded pins that a request never waits out a
// whole slow attempt: it gets the current snapshot and the attempt's result is
// there for the next request.
func TestModelCatalogCurrentWaitIsBounded(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		started <- struct{}{}
		<-release
		return modelSnapshot{IDs: []string{"p/m"}}, true
	})
	catalog.wait = 20 * time.Millisecond

	begin := time.Now()
	if got := catalog.Current(context.Background()); len(got.IDs) != 0 {
		t.Errorf("snapshot = %#v, want the empty one", got)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("Current waited %v for a slow attempt", elapsed)
	}
	<-started

	close(release)
	if got := catalog.Current(context.Background()); len(got.IDs) != 1 || got.IDs[0] != "p/m" {
		t.Errorf("next snapshot = %#v, want the discovered model", got)
	}
}

// TestModelCatalogSharedBySurfaces pins the point of the shared cache: the TUI
// picker, the web command palette and ACP's model picker read one snapshot, so
// discovery runs once and no surface can offer a model another one lacks.
func TestModelCatalogSharedBySurfaces(t *testing.T) {
	var builds atomic.Int32
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		builds.Add(1)
		return modelSnapshot{
			IDs:           []string{"anthropic/claude-sonnet-4", "zai/zai-glm-5-3"},
			ProviderNames: map[string]string{"anthropic": "Anthropic"},
		}, true
	})

	// The TUI warms the catalog at startup and publishes a control.ModelList
	// from it; the web palette reads the same snapshot on connect.
	tui := catalog.Load(context.Background())
	web := catalog.Current(context.Background())
	// ACP renders the same ids as picker options.
	options := (&acpModelCatalog{catalog: catalog}).Options(context.Background())

	if n := builds.Load(); n != 1 {
		t.Fatalf("catalog built %d times for three surfaces, want 1", n)
	}
	if len(tui.IDs) != 2 || len(web.IDs) != 2 {
		t.Fatalf("tui = %#v, web = %#v, want the same two ids", tui.IDs, web.IDs)
	}
	for i := range tui.IDs {
		if tui.IDs[i] != web.IDs[i] {
			t.Fatalf("surface snapshots diverge: tui = %v, web = %v", tui.IDs, web.IDs)
		}
	}
	if len(options) != 2 {
		t.Fatalf("ACP options = %#v, want the same two models", options)
	}
	if web.ProviderNames["anthropic"] != "Anthropic" {
		t.Errorf("provider display names = %#v", web.ProviderNames)
	}
}

// TestACPModelCatalogAdapterLabels pins the ACP projection: provider/model are
// split on the first separator only and the label uses the configured display
// name, falling back to the provider key.
func TestACPModelCatalogAdapterLabels(t *testing.T) {
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		return modelSnapshot{
			IDs:           []string{"prism/prism-ml/Bonsai-27B-gguf:Q1_0", "zai/glm"},
			ProviderNames: map[string]string{"prism": "Prism Labs"},
		}, true
	})
	catalog.Load(context.Background())

	options := (&acpModelCatalog{catalog: catalog}).Options(context.Background())
	if len(options) != 2 {
		t.Fatalf("options = %#v, want 2", options)
	}
	first := options[0]
	if first.Provider != "prism" || first.Model != "prism-ml/Bonsai-27B-gguf:Q1_0" {
		t.Errorf("first option = %+v, want the slash-bearing model name intact", first)
	}
	if first.Name != "prism-ml/Bonsai-27B-gguf:Q1_0 (Prism Labs)" {
		t.Errorf("first label = %q", first.Name)
	}
	if options[1].Name != "glm (zai)" {
		t.Errorf("second label = %q, want the provider key as the fallback", options[1].Name)
	}
}

// An unbuilt catalog (no config, no providers) is a disabled picker, never a
// failure: both surfaces see an empty snapshot.
func TestModelCatalogDisabledIsQuiet(t *testing.T) {
	empty := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		return modelSnapshot{}, true
	})
	var nilCatalog *modelCatalog
	for name, catalog := range map[string]*modelCatalog{"empty": empty, "nil": nilCatalog} {
		if got := catalog.Current(context.Background()); len(got.IDs) != 0 {
			t.Errorf("%s: snapshot = %#v, want empty", name, got)
		}
		if got := catalog.Load(context.Background()); len(got.IDs) != 0 {
			t.Errorf("%s: load = %#v, want empty", name, got)
		}
		if got := (&acpModelCatalog{catalog: catalog}).Options(context.Background()); len(got) != 0 {
			t.Errorf("%s: ACP options = %#v, want none", name, got)
		}
	}
}

// TestWarmModelListRepublishesWhenDiscoveryLands pins the TUI's warm loop: a
// snapshot that discovery cut short must not be the picker's last word. The
// first attempt misses the budget, the retry finds the slow provider's model,
// and the picker is re-sent the larger list.
func TestWarmModelListRepublishesWhenDiscoveryLands(t *testing.T) {
	var calls atomic.Int32
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		if calls.Add(1) == 1 {
			return modelSnapshot{IDs: []string{"fast/one"}}, false
		}
		return modelSnapshot{IDs: []string{"fast/one", "slow/two"}}, true
	})
	catalog.retry = time.Millisecond

	ch := make(chan control.Msg, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmModelList(catalog, func(snap modelSnapshot) { sendModelList(ch, snap) })
	}()

	first := <-ch
	second := <-ch
	<-done // settles after the retry, so no further attempt is made

	firstList, ok := first.(*control.ModelList)
	if !ok {
		t.Fatalf("first message = %T, want *control.ModelList", first)
	}
	if got := strings.Join(firstList.Models, ","); got != "fast/one" {
		t.Errorf("first list = %q, want the truncated snapshot", got)
	}
	secondList := second.(*control.ModelList)
	if got := strings.Join(secondList.Models, ","); got != "fast/one,slow/two" {
		t.Errorf("second list = %q, want the completed snapshot", got)
	}
	if extra := len(ch); extra != 0 {
		t.Errorf("warm loop sent %d more lists after the catalog settled, want 0", extra)
	}
}

// TestWarmModelListStopsWhenRetrySettles pins the loop's exit condition. A retry
// that finishes discovery without changing the ids is still terminal: the earlier
// shape only adopted the new snapshot when the ids changed, so a settled catalog
// kept the loop sleeping through the remaining attempts.
func TestWarmModelListStopsWhenRetrySettles(t *testing.T) {
	var calls atomic.Int32
	catalog := testModelCatalog(func(context.Context) (modelSnapshot, bool) {
		// Same models both times; the first attempt is cut short, the second
		// settles with an identical list.
		return modelSnapshot{IDs: []string{"fast/one"}}, calls.Add(1) > 1
	})
	catalog.retry = time.Millisecond // a third attempt would show up as a third call

	ch := make(chan control.Msg, 4)
	warmModelList(catalog, func(snap modelSnapshot) { sendModelList(ch, snap) })

	if got := calls.Load(); got != 2 {
		t.Errorf("discovery attempts = %d, want 2 (the settling retry must end the loop)", got)
	}
	if got := len(ch); got != 1 {
		t.Errorf("lists delivered = %d, want 1 — an unchanged snapshot is not re-sent", got)
	}
}
