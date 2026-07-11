package yaah

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/spf13/cobra"
)

// mcpCmd is the `yaah mcp` subcommand tree.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP (Model Context Protocol) servers",
}

// mcpListCmd lists discovered MCP servers.
var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List discovered MCP server manifests",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		home := config.HomeDir()
		dirs := mcpSearchPaths(home)
		manifests := mcp.DiscoverManifests(dirs)

		if len(manifests) == 0 {
			cmd.Println("No MCP servers found.")
			cmd.Println()
			cmd.Println("MCP search paths:")
			for _, d := range dirs {
				cmd.Printf("  %s\n", d)
			}
			return nil
		}

		cmd.Printf("Found %d MCP server(s):\n\n", len(manifests))
		for name, m := range manifests {
			cmd.Printf("  %s\n", Bold(name))
			if m.Transport == "http" {
				cmd.Printf("        url: %s (HTTP)\n", m.URL)
			} else {
				cmd.Printf("        command: %s %v (stdio)\n", m.Command, m.Args)
			}
		}
		return nil
	},
}

// mcpAddCmd registers a new MCP server.
var mcpAddCmd = &cobra.Command{
	Use:   "add <name> <command> [args...]",
	Short: "Register a new MCP server (stdio or HTTP)",
	Long: `Register an MCP server manifest.

For stdio servers:
  yaah mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /tmp

For HTTP servers (pass --url):
  yaah mcp add markdownui --url http://localhost:3333/mcp`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		url, _ := cmd.Flags().GetString("url")

		home := config.HomeDir()
		manifestDir := filepath.Join(home, "mcp")

		var manifest mcp.Manifest
		if url != "" {
			manifest = mcp.Manifest{URL: url, Transport: "http"}
		} else {
			if len(args) < 2 {
				return fmt.Errorf("command is required for stdio servers (or use --url for HTTP)")
			}
			manifest = mcp.Manifest{
				Command:   args[1],
				Args:      args[2:],
				Transport: "stdio",
			}
		}

		data, _ := json.MarshalIndent(manifest, "", "  ")
		path := filepath.Join(manifestDir, name+".json")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			return fmt.Errorf("create mcp dir: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}

		cmd.Printf("Registered MCP server %s at %s\n", Bold(name), path)
		return nil
	},
}

// mcpRemoveCmd removes an MCP server.
var mcpRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		home := config.HomeDir()
		path := filepath.Join(home, "mcp", args[0]+".json")
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove manifest: %w", err)
		}
		cmd.Printf("Removed MCP server %s\n", Bold(args[0]))
		return nil
	},
}

// mcpSearchPaths returns directories to scan for MCP manifests.
func mcpSearchPaths(home string) []string {
	return []string{
		filepath.Join(home, "mcp"),
	}
}

func init() {
	mcpAddCmd.Flags().String("url", "", "HTTP URL for HTTP-based MCP servers")
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpAddCmd)
	mcpCmd.AddCommand(mcpRemoveCmd)
	rootCmd.AddCommand(mcpCmd)
}
