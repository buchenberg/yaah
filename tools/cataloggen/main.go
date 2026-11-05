// Command cataloggen regenerates internal/providers/catalog/data.json from
// https://models.dev/api.json.
//
// Regeneration is manual and deliberate — this repo forbids //go:generate
// directives — so run:
//
//	go run ./tools/cataloggen
//
// and commit the resulting data.json diff after reviewing it. The transform is
// deterministic (providers and models are walked in sorted order, ties are
// resolved by explicit rules, and the encoder sorts map keys), so re-running
// over the same upstream payload produces a byte-identical file.
//
//	go run ./tools/cataloggen -verify
//
// fetches upstream and reports whether the checked-in snapshot has drifted,
// without writing anything.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/providers/catalog"
)

const (
	// maxPayload bounds the download; the upstream file is ~6 MB and 64 MB
	// leaves plenty of headroom without letting a wrong URL stream forever.
	maxPayload = 64 << 20

	// fetchTimeout bounds the whole fetch-and-transform run.
	fetchTimeout = 3 * time.Minute
)

func main() {
	source := flag.String("source", catalog.SourceURL, "upstream models.dev catalog URL")
	out := flag.String("out", "internal/providers/catalog/data.json", "snapshot file to write")
	verify := flag.Bool("verify", false, "check the snapshot is current instead of writing it")
	flag.Parse()

	if err := run(context.Background(), *source, *out, *verify); err != nil {
		fmt.Fprintf(os.Stderr, "cataloggen: %v\n", err)
		os.Exit(1)
	}
}

// run fetches, transforms, and writes (or verifies) the snapshot.
func run(ctx context.Context, source, out string, verify bool) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	raw, err := fetch(ctx, source)
	if err != nil {
		return err
	}
	snap, err := Build(raw)
	if err != nil {
		return err
	}
	data, err := snap.Marshal()
	if err != nil {
		return err
	}

	if verify {
		existing, err := os.ReadFile(out)
		if err != nil {
			return fmt.Errorf("read snapshot: %w", err)
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("%s is stale; run go run ./tools/cataloggen", out)
		}
		fmt.Printf("cataloggen: %s is current (%d models)\n", out, len(snap.Models))
		return nil
	}

	if err := os.WriteFile(out, data, 0o644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Printf("cataloggen: wrote %s (%d models, %d bytes)\n", out, len(snap.Models), len(data))
	return nil
}

// fetch downloads the upstream catalog.
func fetch(ctx context.Context, source string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %s", source, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPayload))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	if len(raw) == 0 {
		return nil, errors.New("read upstream: empty payload")
	}
	return raw, nil
}

// upstreamProvider is the slice of a models.dev provider entry the generator
// reads.
type upstreamProvider struct {
	Models map[string]upstreamModel `json:"models"`
}

// upstreamModel is the slice of a models.dev model entry the generator reads.
type upstreamModel struct {
	ID        string `json:"id"`
	Family    string `json:"family"`
	Reasoning bool   `json:"reasoning"`
	ToolCall  bool   `json:"tool_call"`
	Type      string `json:"type"`
	Canonical string `json:"canonical_model_id"`
	Limit     struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// candidate is one provider's claim about a normalized model ID. Several
// providers usually register the same model, so Build has to pick one.
type candidate struct {
	provider  string
	model     string
	canonical string
	entry     catalog.Entry
}

// Build transforms the raw models.dev payload into yaah's snapshot shape.
//
// Providers and models are walked in sorted order and disputes over the same
// normalized model ID are settled by prefer, so the result depends only on the
// payload content.
func Build(raw []byte) (*catalog.Snapshot, error) {
	var upstream map[string]upstreamProvider
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, fmt.Errorf("decode upstream: %w", err)
	}
	if len(upstream) == 0 {
		return nil, errors.New("decode upstream: no providers")
	}

	providerIDs := make([]string, 0, len(upstream))
	for id := range upstream {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)

	chosen := make(map[string]candidate)
	for _, pid := range providerIDs {
		p := upstream[pid]
		modelIDs := make([]string, 0, len(p.Models))
		for id := range p.Models {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)

		for _, mid := range modelIDs {
			m := p.Models[mid]
			if m.ID != "" {
				mid = m.ID
			}
			c, ok := candidateFor(pid, mid, m)
			if !ok {
				continue
			}
			key := catalog.Normalize(mid)
			if cur, ok := chosen[key]; !ok || prefer(c, cur) {
				chosen[key] = c
			}
		}
	}

	snap := &catalog.Snapshot{
		Schema:  1,
		Source:  catalog.SourceURL,
		Models:  make(map[string]catalog.Entry, len(chosen)),
		Aliases: make(map[string]string),
	}
	for key, c := range chosen {
		snap.Models[key] = c.entry
		if alias, ok := aliasFor(key, c, chosen); ok {
			snap.Aliases[key] = alias
		}
	}
	for key := range snap.Aliases {
		if !aliasHelps(snap.Models[key], snap.Models[snap.Aliases[key]]) {
			delete(snap.Aliases, key)
		}
	}
	snap.Aliases = resolveAliases(snap.Aliases, snap.Models)
	return snap, nil
}

// candidateFor turns an upstream model into a catalog candidate, dropping
// entries that carry no metadata yaah can use.
func candidateFor(provider, model string, m upstreamModel) (candidate, bool) {
	// Non-chat artifacts (image, embedding, rerank) never reach the
	// context/output/reasoning questions yaah asks.
	if m.Type != "" && m.Type != "chat" {
		return candidate{}, false
	}
	entry := catalog.Entry{
		Context:   m.Limit.Context,
		Output:    m.Limit.Output,
		Reasoning: m.Reasoning,
		ToolCall:  m.ToolCall,
		Family:    catalog.Normalize(m.Family),
	}
	if entry.Context <= 0 && entry.Output <= 0 && !entry.Reasoning && entry.Family == "" {
		return candidate{}, false
	}
	return candidate{provider: provider, model: model, canonical: m.Canonical, entry: entry}, true
}

// Registration ranks, highest first. The model author's own row outranks a row
// that carries no authorship signal, which outranks a mirror that names
// somebody else's provider.
const (
	rankMirror  = 0 // canonical_model_id names a different provider
	rankNeutral = 1 // no canonical_model_id, or a bare one with no provider part
	rankAuthor  = 2 // canonical_model_id names this candidate's own provider
)

// prefer reports whether a should represent the model instead of b.
//
// Authorship ranks first, then a deliberately conservative limit comparison,
// then the smallest provider and model IDs so the tie-break never depends on
// map order.
//
// The limit tie-break is the same policy catalog.conservativeMerge uses for a
// family: the smallest NON-ZERO value wins, and an unstated limit (zero) loses
// to any stated one. The snapshot is a ceiling source, so an inflated claim
// must never raise a limit — the opposite rule ("widest wins") turns one
// reseller's optimistic copy of a model into the number every caller trusts,
// which is exactly the failure the rank signal is meant to prevent.
func prefer(a, b candidate) bool {
	if ar, br := authorshipRank(a), authorshipRank(b); ar != br {
		return ar > br
	}
	if aWins, decided := smallestNonZeroWins(a.entry.Context, b.entry.Context); decided {
		return aWins
	}
	if aWins, decided := smallestNonZeroWins(a.entry.Output, b.entry.Output); decided {
		return aWins
	}
	if a.provider != b.provider {
		return a.provider < b.provider
	}
	return a.model < b.model
}

// smallestNonZeroWins reports (aWins, decided) for one conservative limit
// comparison: the smaller positive value wins, an unstated (zero) limit loses
// to any stated one, and identical values decide nothing so the caller can
// fall through to its next tie-break.
func smallestNonZeroWins(a, b int) (aWins, decided bool) {
	switch {
	case a == b:
		return false, false
	case a <= 0:
		return false, true // a unstated, b stated
	case b <= 0:
		return true, true // a stated, b unstated
	default:
		return a < b, true
	}
}

// authorshipRank classifies how strongly a registration looks like the model
// author's own.
//
// The signal is canonical_model_id, the field models.dev uses to point a
// mirror at the model it clones, so mirrors name somebody else's provider.
// Only a canonical that names the candidate's OWN provider is positive
// evidence: anything else would let a reseller that omits canonical, or writes
// it bare with no provider part, pass itself off as the author and win a
// tie-break on inflated limits. Absent signals stay neutral — they are
// indistinguishable from an author with nothing to point at — and the
// conservative limit rule below is what keeps a neutral imposter harmless.
func authorshipRank(c candidate) int {
	prefix, rest, ok := strings.Cut(c.canonical, "/")
	if c.canonical == "" || !ok || prefix == "" || rest == "" {
		return rankNeutral // no canonical, or bare/empty — no authorship claim
	}
	if strings.EqualFold(prefix, c.provider) {
		return rankAuthor
	}
	return rankMirror
}

// aliasFor derives an alias when a model's canonical ID names a different ID
// the snapshot already knows (models.dev points an alias at the current model
// of a family).
func aliasFor(key string, c candidate, chosen map[string]candidate) (string, bool) {
	if c.canonical == "" {
		return "", false
	}
	target := catalog.Normalize(c.canonical)
	if target == "" || target == key {
		return "", false
	}
	if _, ok := chosen[target]; !ok {
		return "", false
	}
	return target, true
}

// aliasHelps reports whether following src → dst actually yields more
// metadata. Aliases that merely duplicate an existing row are dropped, so the
// alias table stays a small fix-up list instead of a copy of the model table.
func aliasHelps(src, dst catalog.Entry) bool {
	if dst.Context > src.Context || dst.Output > src.Output {
		return true
	}
	return dst.Reasoning && !src.Reasoning
}

// resolveAliases flattens alias chains and drops cycles and dangling targets,
// so a lookup needs at most one hop.
func resolveAliases(aliases map[string]string, models map[string]catalog.Entry) map[string]string {
	resolved := make(map[string]string, len(aliases))
	for src, dst := range aliases {
		seen := map[string]bool{src: true}
		cur := dst
		for {
			if seen[cur] {
				cur = "" // cycle
				break
			}
			seen[cur] = true
			next, ok := aliases[cur]
			if !ok {
				break
			}
			cur = next
		}
		if cur == "" || cur == src {
			continue
		}
		if _, ok := models[cur]; !ok {
			continue
		}
		resolved[src] = cur
	}
	return resolved
}
