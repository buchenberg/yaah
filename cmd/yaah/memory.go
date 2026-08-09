package yaah

import (
	"fmt"
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

		id := fmt.Sprintf("mem-%d", time.Now().UnixNano())
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
		return nil
	},
}

// initDBEmbedder loads the embedding config and sets the embedder on the DB.
func initDBEmbedder(db *memory.DB) {
	cfg, err := config.Load()
	if err != nil || cfg.Embedding.Provider == "" || cfg.Embedding.Model == "" {
		return
	}
	p, ok := cfg.Providers[cfg.Embedding.Provider]
	if !ok {
		return
	}
	db.SetEmbedder(memory.NewEmbedder(p.BaseURL, cfg.Embedding.Model, nil))
}

func init() {
	memoryAddCmd.Flags().String("tags", "", "JSON array of tags (e.g. '[\"preference\",\"ui\"]')")
	memoryCmd.AddCommand(memorySearchCmd)
	memoryCmd.AddCommand(memoryAddCmd)
	rootCmd.AddCommand(memoryCmd)
}
