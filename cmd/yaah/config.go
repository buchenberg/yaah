package yaah

import (
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/spf13/cobra"
)

// configCmd is the `yaah config` subcommand tree.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage yaah configuration",
}

// configShowCmd prints the effective config with API keys redacted.
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective configuration (secrets redacted)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("cannot load config: %w", err)
		}

		cmd.Println("# yaah effective configuration")
		cmd.Println()

		// Default settings
		cmd.Println("[default]")
		cmd.Printf("  model:          %s\n", cfg.Default.Model)
		cmd.Printf("  small_model:    %s\n", cfg.Default.SmallModel)
		cmd.Printf("  max_iterations: %d\n", cfg.Default.MaxIterations)
		cmd.Printf("  approval:       %s\n", cfg.Default.Approval)
		cmd.Println()

		// Providers (with redacted keys)
		if len(cfg.Providers) > 0 {
			cmd.Println("[providers]")
			for name, p := range cfg.Providers {
				cmd.Printf("  %s:\n", name)
				cmd.Printf("    base_url: %s\n", p.BaseURL)
				cmd.Printf("    api_key:  %s\n", redactKey(p.APIKey))
			}
		} else {
			cmd.Println("[providers]")
			cmd.Println("  (none configured — run 'yaah config edit' to add one)")
		}

		cmd.Println()
		cmd.Printf("[other]\n")
		cmd.Printf("  log_level: %s\n", cfg.LogLevel)

		return nil
	},
}

// configEditCmd opens the config file in $EDITOR.
var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open ~/.yaah/config.yaml in $EDITOR",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Ensure config exists before editing
		if err := config.CreateDefault(); err != nil {
			return err
		}

		path, err := config.ConfigPath()
		if err != nil {
			return err
		}

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}

		cmd.Printf("Opening %s with %s\n", path, editor)
		return nil
	},
}

// redactKey masks all but the last 4 characters of an API key.
// Empty keys return "(not set)". Keys shorter than 8 chars return "(too short)".
func redactKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) < 8 {
		return "(too short)"
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

func init() {
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configEditCmd)
	rootCmd.AddCommand(configCmd)
}
