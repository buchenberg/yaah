package yaah

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// memoryCmd is the `yaah memory` subcommand tree.
var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Manage persistent memory notes",
}

// memorySearchCmd searches memory entries.
var memorySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search memory entries by keyword",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		initDBEmbedder(db)

		// Try vector search first, fall back to FTS5.
		if vecResults, vErr := db.SearchMemoryVector(cmd.Context(), query, 10); vErr == nil && len(vecResults) > 0 {
			cmd.Printf("Found %d result(s):\n\n", len(vecResults))
			for _, r := range vecResults {
				cmd.Printf("  %s  (score: %.2f)\n", Bold(r.Text), r.Score)
				cmd.Printf("        id: %s\n", Dim(r.ID))
				if r.Tags != "" && r.Tags != "null" {
					cmd.Printf("        tags: %s\n", Dim(r.Tags))
				}
				cmd.Printf("        %s\n", Dim(fmt.Sprintf("source: %s | created: %s", r.Source,
					time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"))))
			}
			return nil
		}

		results, err := db.SearchMemory(query, 10)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		if len(results) == 0 {
			cmd.Println("No matching memories found.")
			return nil
		}

		cmd.Printf("Found %d result(s):\n\n", len(results))
		for _, r := range results {
			cmd.Printf("  %s\n", Bold(r.Text))
			cmd.Printf("        id: %s\n", Dim(r.ID))
			if r.Tags != "" && r.Tags != "null" {
				cmd.Printf("        tags: %s\n", Dim(r.Tags))
			}
			cmd.Printf("        %s\n", Dim(fmt.Sprintf("source: %s | created: %s", r.Source,
				time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"))))
		}
		return nil
	},
}

// memoryAddCmd adds a memory entry.
var memoryAddCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new memory note",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := args[0]
		tags, _ := cmd.Flags().GetString("tags")

		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		initDBEmbedder(db)

		id := memory.NewEntryID()
		entry := memory.Entry{
			ID:        id,
			Text:      text,
			Tags:      tags,
			Source:    "cli",
			CreatedAt: time.Now().Unix(),
		}

		if err := db.AddMemory(entry); err != nil {
			return fmt.Errorf("add memory: %w", err)
		}

		// Embed synchronously so the entry is immediately searchable.
		if ch := db.EmbedMemoryAsync(entry.ID, entry.Text); ch != nil {
			<-ch
		}

		cmd.Printf("Added memory: %s\n", text)
		// The full ID is the canonical handle for the entry: the agent tools
		// accept it, and it is the only way to refer to this exact row later
		// (including from a future CLI update/delete subcommand).
		cmd.Printf("ID: %s\n", id)
		return nil
	},
}

// embedderFor builds the configured embedder without touching a DB.
// Separated from attachEmbedder so the provider→embedder field mapping
// can be tested directly (it was once shipped with model and API key
// swapped, silently breaking semantic search).
func embedderFor(cfg *config.Config) (*memory.HTTPEmbedder, bool) {
	if cfg.Embedding.Provider == "" || cfg.Embedding.Model == "" {
		return nil, false
	}
	p, ok := cfg.Providers[cfg.Embedding.Provider]
	if !ok {
		fmt.Fprintf(os.Stderr, "warning: embedding provider %q not found in providers\n", cfg.Embedding.Provider)
		return nil, false
	}
	// Resolve ${VAR} placeholders — provider_resolve does this for LLM
	// providers; the embedder path must too or literal placeholders
	// reach the endpoint.
	p = config.Resolve(p)
	return memory.NewEmbedder(p.BaseURL, p.APIKey, cfg.Embedding.Model, nil), true
}

// attachEmbedder resolves the configured embedding provider and sets the
// embedder on db. It reports configuration mistakes on stderr.
func attachEmbedder(db *memory.DB, cfg *config.Config) bool {
	e, ok := embedderFor(cfg)
	if !ok {
		return false
	}
	db.SetEmbedder(e)
	return true
}

// initDBEmbedder loads config and attaches the embedder for CLI commands.
func initDBEmbedder(db *memory.DB) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	attachEmbedder(db, cfg)
}

// staleMemoryAge is the floor under PruneStaleCompaction: a global compaction
// atom younger than this had its whole lifetime to be recalled and may still
// be. A month is deliberately generous — the sweep exists to stop unbounded
// accumulation, not to reclaim anything soon.
const staleMemoryAge = 30 * 24 * time.Hour

// pruneMemoryAtClose runs the memory-hygiene staleness sweep on the session
// teardown path. It deliberately does NOT prune session-scoped atoms by
// session: yaah sessions are resumable, so a session's working context may be
// recalled again later. The sweep is age-based (30-day floor) and never
// touches an accessed atom, so a close cannot race an in-flight consolidation
// or destroy recall a resume might want. Best-effort: a failed sweep is
// logged and the close continues.
func pruneMemoryAtClose(db *memory.DB) {
	if db == nil {
		return
	}
	if n, err := db.PruneStaleCompaction(time.Now().Add(-staleMemoryAge)); err != nil {
		slog.Warn("session: prune stale compaction memory failed", "err", err)
	} else if n > 0 {
		slog.Debug("session: pruned stale compaction memory", "deleted", n)
	}
}

// memoryStatsCmd renders the introspection view: aggregate memory/skill stats
// plus a merged, newest-first journey of what the store has learned.
// docs/compaction-as-sleep.md §8 P4 — a render, never a write.
var memoryStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show memory and skill stats plus a recent learning journey",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()

		stats, err := db.Stats()
		if err != nil {
			return err
		}
		cmd.Printf("%s\n", Bold("Memory"))
		cmd.Printf("  total:            %d\n", stats.Total)
		cmd.Printf("  global:           %d\n", stats.Global)
		cmd.Printf("  session-scoped:   %d\n", stats.Session)
		cmd.Printf("  never accessed:   %d\n", stats.NeverAccessed)
		cmd.Printf("  stale candidates: %d\n", stats.StaleCandidates)
		cmd.Printf("  created:          %s → %s\n",
			formatStatsTime(stats.OldestCreatedAt), formatStatsTime(stats.NewestCreatedAt))
		cmd.Printf("\n%s\n", Bold("Skills"))
		cmd.Printf("  tracked:          %d\n", stats.SkillsTracked)
		cmd.Printf("  ledger entries:   %d\n", stats.SkillLedgerEntries)

		events, err := db.Journey(20)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		cmd.Printf("\n%s\n", Bold("Recent learning journey"))
		for _, e := range events {
			kind := e.Kind
			if kind == "memory" {
				kind = Bold("mem ")
			} else {
				kind = Bold("skill")
			}
			detail := e.Detail
			if len(detail) > 60 {
				detail = detail[:60] + "…"
			}
			cmd.Printf("  %s %s  %s\n", kind, Dim(formatStatsTime(e.CreatedAt)), detail)
		}
		return nil
	},
}

// formatStatsTime renders a Unix-seconds timestamp, or "-" for a zero value
// (an empty store has no oldest/newest).
func formatStatsTime(unixSeconds int64) string {
	if unixSeconds == 0 {
		return "-"
	}
	return time.Unix(unixSeconds, 0).Format("2006-01-02 15:04")
}

func init() {
	memoryAddCmd.Flags().String("tags", "", "JSON array of tags (e.g. '[\"preference\",\"ui\"]')")
	memoryCmd.AddCommand(memorySearchCmd)
	memoryCmd.AddCommand(memoryAddCmd)
	memoryCmd.AddCommand(memoryStatsCmd)
	rootCmd.AddCommand(memoryCmd)
}
