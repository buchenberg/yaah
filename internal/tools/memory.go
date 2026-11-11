package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
)

// memoryMaxTopK bounds one memory search. The schema advertises the same
// maximum; a larger request would materialize an unbounded result set.
const memoryMaxTopK = 100

// MemorySearchTool searches persistent memory using FTS5 and, when
// configured, vector embeddings for semantic search.
type MemorySearchTool struct {
	DB *memory.DB
	// Scope is the default tier filter for the search. Empty means
	// ScopeGlobal: session-scoped atoms are working-context and must not leak
	// into a default (cross-session) recall; a caller that wants them sets
	// Scope explicitly. The call's own "scope" argument outranks this
	// default — the field exists so an unwidened search stays global, not so
	// a search can never be widened.
	Scope string
}

func (t *MemorySearchTool) Name() string { return "memory_search" }
func (t *MemorySearchTool) Description() string {
	return prompts.ToolDescription("memory_search")
}

func (t *MemorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query (empty to list all)"},
			"tag": {"type": "string", "description": "Optional tag to filter by (e.g. 'user_info', 'preferences'). Supplying a tag forces keyword-only (FTS5) matching and withdraws semantic recall; omit it when searching by paraphrase."},
			"top_k": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum number of results (default 10; values above 100 are clamped)"},
			"scope": {"type": "string", "enum": ["global", "session", "all"], "description": "Tier filter. 'global' (default) returns facts that outlive any single session; 'session' returns atoms distilled from conversations, including past ones; 'all' spans both. Session atoms are excluded unless you ask for them."}
		},
		"required": []
	}`)
}

func (t *MemorySearchTool) Execute(ctx context.Context, args string) (string, error) {

	var params struct {
		Query string `json:"query"`
		Tag   string `json:"tag"`
		TopK  int    `json:"top_k"`
		Scope string `json:"scope"`
	}
	if err := StrictToolArgs("memory_search", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}
	if params.TopK <= 0 {
		params.TopK = 10
	}
	params.TopK = clampPositiveInt(params.TopK, memoryMaxTopK)
	scope := t.Scope
	if params.Scope != "" {
		// An explicit request outranks the instance default: the default
		// exists so an unwidened search stays global, not so a search can
		// never be widened.
		scope = params.Scope
	}
	if scope == "" {
		// An unwidened recall is global by design — session atoms are one
		// conversation's working context and must not surface unless asked
		// for. The store demands an explicit tier, so the default lives here,
		// in the one place that is allowed to have one.
		scope = memory.ScopeGlobal
	}
	scope, err := memory.NormalizeScopeFilter(scope)
	if err != nil {
		return "", fmt.Errorf("memory_search: %w", err)
	}

	// Label the path before the entries. Three distinct degradations — no
	// embedder, a failed embed, and a vector search with no candidates — used
	// to collapse into one unlabelled FTS result set, so an empty recall could
	// not be told apart from a semantic path that was down (yaah-17s).
	hasEmbedder := t.DB.Embedder() != nil
	attemptVector := params.Query != "" && params.Tag == "" && hasEmbedder
	var vecResults []memory.VectorResult
	var vecErr error
	var report memory.VectorSearchReport
	// note names the rows a vector search could not compare at their stored
	// embedding width. It is computed before the page-size branch because every
	// path below can carry that omission: a store whose only stale rows share no
	// word with the query yields zero vector hits, falls through to FTS, and
	// would report a plain miss — the exact silent omission the note exists to
	// fix (shq review).
	var note string
	if attemptVector {
		report, vecErr = t.DB.SearchMemoryVectorScopedReport(ctx, params.Query, params.TopK, scope)
		vecResults = report.Hits
		note = DimMismatchNote(report)
		if vecErr == nil && len(vecResults) > 0 {
			// Rows left at a previous embedding model's width come back
			// keyword-scored; the note says how many so a degraded page is
			// never passed off as semantic recall (yaah-shq).
			if len(vecResults) >= params.TopK {
				return formatVectorResults(vecResults, note), nil
			}
			// Partial semantic page: union with FTS so rows the vector path
			// paged out are not silently dropped (shq review). Vector hits
			// lead, deduped by id, capped at TopK, labeled per row.
			var lines []string
			kwRows := 0
			seen := make(map[string]bool, len(vecResults))
			for _, r := range vecResults {
				seen[r.ID] = true
				score := r.Score
				lines = append(lines, entryLine(r.ID, r.Tags, r.Scope, r.Text, r.CreatedAt, &score, vectorHitMarker(r)))
			}
			if fts, err := t.DB.SearchMemoryScoped(params.Query, params.TopK, params.Tag, scope); err == nil {
				for _, e := range fts {
					if seen[e.ID] || len(seen) >= params.TopK {
						continue
					}
					seen[e.ID] = true
					kwRows++
					lines = append(lines, entryLine(e.ID, e.Tags, e.Scope, e.Text, e.CreatedAt, nil, "keyword"))
				}
			}
			out := pageLabel(vecResults, kwRows) + "\n" + strings.Join(lines, "\n")
			return withDimNote(out, note), nil
		}
	}
	path := RetrievalLabel(params.Query, params.Tag, hasEmbedder, vecErr, len(vecResults))

	ftsResults, err := t.DB.SearchMemoryScoped(params.Query, params.TopK, params.Tag, scope)
	if err != nil {
		return "", fmt.Errorf("memory_search: %w", err)
	}

	if len(ftsResults) == 0 {
		return withDimNote(path+"\n"+t.emptyHint(scope), note), nil
	}

	return withDimNote(path+"\n"+formatEntries(ftsResults), note), nil
}

// withDimNote appends the stale-width note to a result page. The fall-through
// and empty pages get it too: a keyword-only or empty recall can still be
// missing rows the vector path could not compare, and dropping the note there
// makes that omission silent again (shq review).
func withDimNote(out, note string) string {
	if note == "" {
		return out
	}
	return out + "\n" + note
}

// Retrieval path labels. Every memory_search result names the path that
// produced it: a model asking "was this not known or was recall degraded?"
// must be able to tell the difference from the output alone.
const (
	RetrievalSemantic = "Retrieval: semantic (vector)"
	RetrievalKeyword  = "Retrieval: keyword (FTS5)"
	// RetrievalHybrid is the merged path: a partial vector page unioned
	// with FTS hits so neither path's rows are silently dropped.
	RetrievalHybrid = "Retrieval: semantic (vector) + keyword (FTS5)"
	// RetrievalHybridFallback labels a page that mixes vector-scored rows with
	// keyword-fallback rows from the store: a previous embedding model left
	// those rows at a width the current one cannot compare (yaah-shq), so they
	// are ranked on words rather than by FTS5.
	RetrievalHybridFallback = "Retrieval: semantic (vector) + keyword fallback (stale embeddings)"
	// RetrievalListing is the no-query branch: SearchMemoryScoped orders by
	// created_at and runs no FTS match, so calling it (FTS5) would be a lie.
	RetrievalListing = "Retrieval: listing (newest first)"
)

// RetrievalLabel renders the one-line path description shared by the
// memory_search tool and the `yaah memory search` CLI, so the two front-ends
// cannot drift. hasEmbedder reports whether an embedding model is configured;
// semanticErr and candidates describe the vector attempt when one ran.
func RetrievalLabel(query, tag string, hasEmbedder bool, semanticErr error, candidates int) string {
	switch {
	case query == "":
		label := RetrievalListing
		if tag != "" {
			label += fmt.Sprintf(" — tag %q narrows the listing", tag)
		}
		return label
	case tag != "":
		return RetrievalKeyword + " — tag filter selects keyword-only matching"
	case !hasEmbedder:
		return RetrievalKeyword + " — no embedding model configured"
	case semanticErr != nil:
		return RetrievalKeyword + " — " + semanticFailureReason(semanticErr)
	case candidates == 0:
		return RetrievalKeyword + " — semantic search returned no candidates"
	}
	return RetrievalSemantic
}

// semanticFailureReason distinguishes a failed embedding (the semantic path
// itself is down) from a store/scan failure, so a database problem is never
// reported as an embedding outage.
func semanticFailureReason(err error) string {
	if memory.IsEmbedFailure(err) {
		return "semantic search failed (embedding): " + err.Error()
	}
	return "semantic search failed (store): " + err.Error()
}

// VectorPageLabel names the path a vector page used: pure semantic scoring, or
// semantic with keyword-fallback rows mixed in because a previous embedding
// model wrote part of the store at a width the current one cannot compare
// (yaah-shq). Shared with `yaah memory search` so neither front-end can present
// a degraded page as semantic recall.
func VectorPageLabel(results []memory.VectorResult) string {
	for _, r := range results {
		if r.Keyword {
			return RetrievalHybridFallback
		}
	}
	return RetrievalSemantic
}

// pageLabel names the path a vector page used, counting the FTS rows folded
// into it. A keyword-fallback row anywhere in the final page is the strongest
// signal — part of the store cannot be compared with the current model — so it
// names the page even when FTS also contributed; the FTS contribution is named
// after it rather than replacing it, so neither path's degradation hides the
// other (shq review).
func pageLabel(results []memory.VectorResult, kwRows int) string {
	vecLabel := VectorPageLabel(results)
	switch {
	case kwRows == 0:
		return vecLabel
	case vecLabel == RetrievalHybridFallback:
		return RetrievalHybridFallback + " + keyword (FTS5)"
	default:
		return RetrievalHybrid
	}
}

// vectorHitMarker names the path that scored one vector hit. A hit marked
// Keyword was ranked on word overlap because its stored embedding could not be
// compared with the current model — calling it semantic would hide exactly the
// degradation the marker exists to expose.
func vectorHitMarker(r memory.VectorResult) string {
	if r.Keyword {
		return "keyword"
	}
	return "semantic"
}

// DimMismatchNote names the rows a vector search could not score because a
// previous embedding model wrote them, and how many there are: the omission has
// to be visible, or a keyword-scored page reads as a complete semantic recall
// (yaah-shq). Empty when nothing was left behind. Shared with
// `yaah memory search`.
func DimMismatchNote(r memory.VectorSearchReport) string {
	if !r.HasDimMismatch() {
		return ""
	}
	return fmt.Sprintf(
		"note: %d memory row(s) are stored at a different embedding dimension (%s) and were ranked by keyword fallback; re-embedding them restores semantic recall",
		r.DimMismatch, staleWidthList(r.DimMismatchByDim))
}

// staleWidthList renders the mismatched widths ascending, e.g.
// "384: 2 row(s), 512: 1 row(s)".
func staleWidthList(byDim map[int]int) string {
	dims := make([]int, 0, len(byDim))
	for dim := range byDim {
		dims = append(dims, dim)
	}
	sort.Ints(dims)
	parts := make([]string, 0, len(dims))
	for _, dim := range dims {
		parts = append(parts, fmt.Sprintf("%d: %d row(s)", dim, byDim[dim]))
	}
	return strings.Join(parts, ", ")
}

// emptyHint explains a miss instead of dead-ending on it. The usual cause is a
// default (global-only) recall against a store whose knowledge is mostly
// session atoms — reporting a bare "not found" is exactly what made 149 of 152
// atoms look like they did not exist.
func (t *MemorySearchTool) emptyHint(scope string) string {
	const miss = "No matching memories found."
	if scope != memory.ScopeGlobal || t.DB == nil {
		return miss
	}
	n, err := t.DB.CountMemories(memory.ScopeSession)
	if err != nil || n == 0 {
		return miss
	}
	return fmt.Sprintf(`%s %d session-scoped atom(s) distilled from earlier conversations are excluded by the default scope — re-run with scope "session" or "all" to search them.`, miss, n)
}

// entryLine renders one hit. Tier and age are part of the line because a
// recall is only as useful as the reader's ability to weigh it: a durable
// global fact and a session atom from four days ago deserve different trust,
// and that cannot be read off the text alone.
func entryLine(id, tags, scope, text string, createdAt int64, score *float32, marker string) string {
	var b strings.Builder
	if marker != "" {
		b.WriteString("[")
		b.WriteString(marker)
		b.WriteString("] ")
	} else {
		b.WriteString("- ")
	}
	b.WriteString("[")
	b.WriteString(id)
	b.WriteString("]")
	if meta := entryMeta(scope, createdAt); meta != "" {
		b.WriteString(" (")
		b.WriteString(meta)
		b.WriteString(")")
	}
	if tags != "" && tags != "null" {
		b.WriteString(" ")
		b.WriteString(tags)
	}
	b.WriteString(" ")
	b.WriteString(text)
	if score != nil {
		fmt.Fprintf(&b, "  (score: %.2f)", *score)
	}
	return b.String()
}

// entryMeta renders the tier and a coarse age, e.g. "global, 3d ago".
func entryMeta(scope string, createdAt int64) string {
	parts := make([]string, 0, 2)
	if scope != "" {
		parts = append(parts, scope)
	}
	if age := memoryAge(createdAt); age != "" {
		parts = append(parts, age)
	}
	return strings.Join(parts, ", ")
}

// memoryAge renders a creation timestamp as a deliberately coarse age. A reader
// needs "old durable fact" versus "last turn", not a minute.
func memoryAge(createdAt int64) string {
	if createdAt <= 0 {
		return ""
	}
	d := time.Since(time.Unix(createdAt, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours())/24/7)
	}
}

func formatEntries(results []memory.Entry) string {
	lines := make([]string, 0, len(results))
	for _, r := range results {
		lines = append(lines, entryLine(r.ID, r.Tags, r.Scope, r.Text, r.CreatedAt, nil, ""))
	}
	return strings.Join(lines, "\n")
}

// formatVectorResults renders a vector page with its path label; note, when
// non-empty, is appended as the closing line (yaah-shq's dimension mismatch).
func formatVectorResults(results []memory.VectorResult, note string) string {
	lines := make([]string, 0, len(results)+2)
	lines = append(lines, VectorPageLabel(results))
	for _, r := range results {
		score := r.Score
		lines = append(lines, entryLine(r.ID, r.Tags, r.Scope, r.Text, r.CreatedAt, &score, vectorHitMarker(r)))
	}
	if note != "" {
		lines = append(lines, note)
	}
	return strings.Join(lines, "\n")
}

// MemoryAddTool adds a new memory entry. Scope is the tier the entry is
// written to when the caller does not name one: ScopeSession for atoms
// distilled from the running conversation (the consolidator's deep sleep),
// ScopeGlobal for anything meant to outlive it. Empty means global.
//
// SessionID is the conversation the tool's writes belong to, recorded on the
// entry so the session-close pruning pass can tell this session's atoms from
// every other session's. It is an instance default, not a call argument: the
// caller cannot choose whose atoms its writes will look like. Empty (the
// session's own registry, the CLI) leaves the link NULL, so those atoms are
// never pruned by session.
type MemoryAddTool struct {
	DB        *memory.DB
	Scope     string
	SessionID string
}

func (t *MemoryAddTool) Name() string { return "memory_add" }
func (t *MemoryAddTool) Description() string {
	return prompts.ToolDescription("memory_add")
}

func (t *MemoryAddTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "description": "The memory note text"},
			"tags": {"type": "array", "items": {"type": "string"}, "description": "Optional tags"},
			"scope": {"type": "string", "enum": ["session", "global"], "description": "Optional tier: 'session' for atoms distilled from this conversation, 'global' (default) for facts meant to outlive it"},
			"supersedes": {"type": "string", "description": "Optional ID of an existing atom this one corrects or replaces. The older atom is marked superseded: kept for audit, no longer returned by memory_search."}
		},
		"required": ["text"]
	}`)
}

func (t *MemoryAddTool) Execute(ctx context.Context, args string) (string, error) {

	var params struct {
		Text       string          `json:"text"`
		Tags       json.RawMessage `json:"tags"`
		Scope      string          `json:"scope"`
		Supersedes string          `json:"supersedes"`
	}
	if err := StrictToolArgs("memory_add", t.Schema(), args, &params); err != nil {
		return "", err
	}
	tags, err := normalizeTags(params.Tags)
	if err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}

	// Scope precedence: the call's own scope, then the tool's configured
	// default, then global (via NormalizeScope). An unknown tier is rejected
	// here rather than persisted as an unfilterable row. The consolidator's
	// session scope is directive-driven: its prompt instructs scope:"session"
	// on every call (cmd/yaah/consolidate.go), not enforced by a tool default.
	scope := params.Scope
	if scope == "" {
		scope = t.Scope
	}
	normScope, err := memory.NormalizeScope(scope)
	if err != nil {
		return "", fmt.Errorf("memory_add: %w", err)
	}

	entry := memory.Entry{
		ID:        memory.NewEntryID(),
		Text:      params.Text,
		Tags:      tags,
		Source:    "agent",
		Scope:     normScope,
		SessionID: t.SessionID,
		CreatedAt: time.Now().Unix(),
	}

	// The write and the supersede share one transaction: inserting first and
	// superseding after leaves the worst half-state available on failure — the
	// correction saved, the claim it corrects still live and still surfacing.
	id, existed, err := t.DB.AddMemorySuperseding(entry, params.Supersedes)
	if err != nil {
		return "", fmt.Errorf("memory_add: %w", err)
	}

	if existed {
		note := ""
		if params.Supersedes != "" && params.Supersedes != id {
			note = fmt.Sprintf("\nSuperseded %s with the existing atom.", params.Supersedes)
		}
		return fmt.Sprintf("Memory already exists (id: %s): %s\nUse memory_search to retrieve this fact when relevant.%s", id, params.Text, note), nil
	}

	// Embed the new entry synchronously so it is searchable immediately.
	if ch := t.DB.EmbedMemoryAsync(id, entry.Text); ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}

	note := ""
	if params.Supersedes != "" {
		note = fmt.Sprintf(" It supersedes %s, which is kept for audit and no longer surfaced by search.", params.Supersedes)
	}

	return fmt.Sprintf("Memory saved (id: %s): %s. This fact is now in persistent memory — use memory_search to recall it when relevant.%s", id, params.Text, note), nil
}

// MemoryPromoteTool moves an atom from the session tier into the global tier,
// so a fact that proved to outlive its conversation stops expiring with it.
type MemoryPromoteTool struct {
	DB *memory.DB
	// SessionID, when set, restricts promotion to atoms distilled by that
	// session. A sub-agent gets its parent's id, so it can promote what its own
	// conversation learned; without the guard a narrow worker could publish
	// another session's working context to every future recall.
	SessionID string
}

func (t *MemoryPromoteTool) Name() string { return "memory_promote" }
func (t *MemoryPromoteTool) Description() string {
	return prompts.ToolDescription("memory_promote")
}

func (t *MemoryPromoteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The ID of the memory entry to promote — a session atom that turned out to be a durable fact"}
		},
		"required": ["id"]
	}`)
}

func (t *MemoryPromoteTool) Execute(_ context.Context, args string) (string, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := StrictToolArgs("memory_promote", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}
	if params.ID == "" {
		return "", fmt.Errorf("memory_promote: id is required")
	}
	if t.SessionID != "" {
		entry, err := t.DB.GetMemory(params.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", memory.ErrMemoryNotFound
			}
			return "", fmt.Errorf("memory_promote: %w", err)
		}
		if entry.SessionID != "" && entry.SessionID != t.SessionID {
			return "", fmt.Errorf("memory_promote: entry %s belongs to session %s, expected %s", params.ID, entry.SessionID, t.SessionID)
		}
	}
	from, err := t.DB.PromoteMemory(params.ID)
	if err != nil {
		return "", fmt.Errorf("memory_promote: %w", err)
	}
	if from == memory.ScopeGlobal {
		return fmt.Sprintf("Memory %s is already in the global tier; nothing to promote.", params.ID), nil
	}
	return fmt.Sprintf("Memory %s promoted from %s to %s: it now outlives any single session and appears in default recall.", params.ID, from, memory.ScopeGlobal), nil
}

// MemoryDeleteTool removes a memory entry by ID.
type MemoryDeleteTool struct {
	DB *memory.DB
}

func (t *MemoryDeleteTool) Name() string        { return "memory_delete" }
func (t *MemoryDeleteTool) Description() string { return prompts.ToolDescription("memory_delete") }

func (t *MemoryDeleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The ID of the memory entry to delete"}
		},
		"required": ["id"]
	}`)
}

func (t *MemoryDeleteTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ID string `json:"id"`
	}
	if err := StrictToolArgs("memory_delete", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}
	if err := t.DB.DeleteMemory(params.ID); err != nil {
		return "", fmt.Errorf("memory_delete: %w", err)
	}
	return fmt.Sprintf("Memory deleted: %s", params.ID), nil
}

// MemoryUpdateTool updates the text of an existing memory entry.
type MemoryUpdateTool struct {
	DB        *memory.DB
	Scope     string
	SessionID string
}

func (t *MemoryUpdateTool) Name() string { return "memory_update" }
func (t *MemoryUpdateTool) Description() string {
	return prompts.ToolDescription("memory_update")
}

func (t *MemoryUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "The ID of the memory entry to update"},
			"text": {"type": "string", "description": "The new text for the memory entry"}
		},
		"required": ["id", "text"]
	}`)
}

func (t *MemoryUpdateTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := StrictToolArgs("memory_update", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("memory database not available")
	}

	// Ownership guard. Each restriction applies on its own trigger and neither
	// is nested inside the other: a narrowed writer must not lose the session
	// check just because it left its scope unset. That asymmetry — the write
	// path guarding by session only when a scope was configured, while promotion
	// guarded by session always — is what let a scoped-to-nothing updater rewrite
	// another session's atom.
	//
	//   - Scope: the atom must live in that tier (the consolidator's session
	//     tier, so it cannot rewrite global facts).
	//   - SessionID: the atom must belong to that session, or to no session at
	//     all. An unlinked atom was written by this session's own registry or by
	//     the CLI, so it is not somebody else's working context.
	if t.Scope != "" || t.SessionID != "" {
		entry, err := t.DB.GetMemory(params.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", memory.ErrMemoryNotFound
			}
			return "", fmt.Errorf("memory_update: %w", err)
		}
		if t.Scope != "" {
			normScope, err := memory.NormalizeScope(t.Scope)
			if err != nil {
				return "", fmt.Errorf("memory_update: %w", err)
			}
			if entry.Scope != normScope {
				return "", fmt.Errorf("memory_update: entry %s has scope %s, expected %s", params.ID, entry.Scope, normScope)
			}
		}
		if t.SessionID != "" && entry.SessionID != "" && entry.SessionID != t.SessionID {
			return "", fmt.Errorf("memory_update: entry %s belongs to session %s, expected %s", params.ID, entry.SessionID, t.SessionID)
		}
	}

	if err := t.DB.UpdateMemory(params.ID, params.Text); err != nil {
		return "", fmt.Errorf("memory_update: %w", err)
	}
	return fmt.Sprintf("Memory updated: %s", params.Text), nil
}

// MemorySessionSearchTool searches past session messages using FTS5.
type MemorySessionSearchTool struct {
	DB *memory.DB
}

func (t *MemorySessionSearchTool) Name() string { return "memory_search_sessions" }
func (t *MemorySessionSearchTool) Description() string {
	return prompts.ToolDescription("memory_session_search")
}

func (t *MemorySessionSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "The search query (empty to list recent messages)"},
			"top_k": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum number of results (default 10; values above 100 are clamped)"}
		},
		"required": []
	}`)
}

func (t *MemorySessionSearchTool) Execute(ctx context.Context, args string) (string, error) {

	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := StrictToolArgs("memory_search_sessions", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if t.DB == nil {
		return "", fmt.Errorf("session search database not available")
	}
	if params.TopK <= 0 {
		params.TopK = 10
	}
	params.TopK = clampPositiveInt(params.TopK, memoryMaxTopK)

	if params.Query == "" {
		return t.listRecentMessages(params.TopK)
	}

	// Label the path before the entries: a failed embed must not read as an
	// ordinary full-text search (yaah-17s), matching memory_search.
	hasEmbedder := t.DB.Embedder() != nil
	vecResults, vecErr := t.DB.SearchMessagesVectorDiag(ctx, params.Query, params.TopK)
	if vecErr == nil && len(vecResults) > 0 {
		if len(vecResults) >= params.TopK {
			return formatMessageVectorResults(vecResults), nil
		}
		// Partial semantic page: union with FTS so keyword matches the vector
		// path paged out are not dropped (mirrors memory_search's hybrid union).
		seen := make(map[string]bool, len(vecResults))
		var lines []string
		kwRows := 0
		for _, r := range vecResults {
			seen[r.ID] = true
			score := r.Score
			lines = append(lines, messageLine(r.SessionID, r.Role, r.Content, &score, true))
		}
		if fts, err := t.DB.SearchMessages(params.Query, params.TopK); err == nil {
			for _, m := range fts {
				if seen[m.ID] || len(seen) >= params.TopK {
					continue
				}
				seen[m.ID] = true
				kwRows++
				lines = append(lines, messageLine(m.SessionID, m.Role, m.Content, nil, false))
			}
		}
		header := RetrievalSemantic
		if kwRows > 0 {
			header = RetrievalHybrid
		}
		return header + "\n" + strings.Join(lines, "\n"), nil
	}

	path := RetrievalLabel(params.Query, "", hasEmbedder, vecErr, len(vecResults))

	results, err := t.DB.SearchMessages(params.Query, params.TopK)
	if err != nil {
		return "", fmt.Errorf("memory_search_sessions: %w", err)
	}

	if len(results) == 0 {
		return path + "\n" + "No matching session messages found.", nil
	}

	var output string
	for _, m := range results {
		output += messageLine(m.SessionID, m.Role, m.Content, nil, false) + "\n"
	}
	return path + "\n" + output, nil
}

// messageLine renders one session-message hit: the session prefix, role, and
// content, with an optional [semantic] marker and score.
func messageLine(sessionID, role, content string, score *float32, semantic bool) string {
	var b strings.Builder
	if semantic {
		b.WriteString("[semantic] ")
	}
	if len(sessionID) > 12 {
		sessionID = sessionID[:12]
	}
	fmt.Fprintf(&b, "[%s] %s: %s", sessionID, role, content)
	if score != nil {
		fmt.Fprintf(&b, "  (score: %.2f)", *score)
	}
	return b.String()
}

// formatMessageVectorResults renders a full semantic page with the retrieval
// path label.
func formatMessageVectorResults(results []memory.MessageVectorResult) string {
	lines := make([]string, 0, len(results)+1)
	lines = append(lines, RetrievalSemantic)
	for _, r := range results {
		score := r.Score
		lines = append(lines, messageLine(r.SessionID, r.Role, r.Content, &score, true))
	}
	return strings.Join(lines, "\n")
}

func (t *MemorySessionSearchTool) listRecentMessages(limit int) (string, error) {
	sessions, err := t.DB.ListSessions(limit + 10)
	if err != nil {
		return "", fmt.Errorf("memory_search_sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "No past sessions found.", nil
	}

	var mainSessions []memory.Session
	var childSessions []memory.Session
	for _, s := range sessions {
		if strings.Contains(s.ID, "-sub-") {
			childSessions = append(childSessions, s)
		} else {
			mainSessions = append(mainSessions, s)
		}
	}

	var output string
	count := 0
	// Show main sessions first, newest first.
	for _, s := range mainSessions {
		if summary := t.sessionSummary(s); summary != "" {
			output += summary + "\n"
			count++
			if count >= limit {
				goto done
			}
		}
	}
	// Fall back to child sessions if limit not yet met.
	for _, s := range childSessions {
		if summary := t.sessionSummary(s); summary != "" {
			output += summary + "\n"
			count++
			if count >= limit {
				goto done
			}
		}
	}
done:
	if count == 0 {
		return "No messages found in recent sessions.", nil
	}
	if count == 1 {
		output += "This is the most recent session stored in memory.\n"
	}
	return output, nil
}

func (t *MemorySessionSearchTool) sessionSummary(s memory.Session) string {
	msgs, err := t.DB.GetMessages(s.ID)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	topic := ""
	for _, m := range msgs {
		if m.Role == "user" {
			topic = m.Content
			if len(topic) > 120 {
				topic = topic[:117] + "..."
			}
			break
		}
	}
	status := "active"
	if s.EndedAt > 0 {
		status = time.Unix(s.EndedAt, 0).Format("Jan 2 15:04")
	}
	tokenInfo := ""
	if s.TokensIn > 0 || s.TokensOut > 0 {
		tokenInfo = fmt.Sprintf(" | %d in / %d out tokens", s.TokensIn, s.TokensOut)
	}
	compactNote := ""
	if s.CompactedSummary != "" {
		compactNote = " | compacted"
	}
	prefix := ""
	if strings.Contains(s.ID, "-sub-") {
		prefix = "[SUB] "
	}
	return fmt.Sprintf("%s[%s] %s | %s | %d msgs | model: %s%s%s\n  %s",
		prefix, s.ID[:12], time.Unix(s.StartedAt, 0).Format("Jan 2 15:04"), status,
		len(msgs), s.Model, tokenInfo, compactNote, topic)
}

// normalizeTags accepts both the schema's array form and the historical
// JSON-array string, and stores the canonical JSON-array string the memory
// store has always carried. Anything else is a tool error, not a silent "".
func normalizeTags(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err == nil {
		out, _ := json.Marshal(tags)
		return string(out), nil
	}
	var legacy string
	if err := json.Unmarshal(raw, &legacy); err == nil {
		legacy = strings.TrimSpace(legacy)
		if legacy == "" {
			return "", nil
		}
		var parsed []string
		if err := json.Unmarshal([]byte(legacy), &parsed); err != nil {
			return "", fmt.Errorf("memory_add: tags must be an array of strings (or a JSON array string): %w", err)
		}
		out, _ := json.Marshal(parsed)
		return string(out), nil
	}
	return "", fmt.Errorf("memory_add: tags must be an array of strings")
}
