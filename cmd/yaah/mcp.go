package yaah

import (
	"fmt"
	"sort"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/spf13/cobra"
)

// mcpCmd is the `yaah mcp` subcommand tree.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP (Model Context Protocol) servers",
}

// mcpListCmd lists configured MCP servers from config.yaml.
var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured MCP servers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		if len(cfg.MCPServers) == 0 {
			cmd.Println("No MCP servers configured.")
			cmd.Println()
			cmd.Println("Add one with:")
			cmd.Println("  yaah mcp add <name> <command> [args...]")
			cmd.Println("  yaah mcp add <name> --url <http-url>")
			return nil
		}

		names := make([]string, 0, len(cfg.MCPServers))
		for name := range cfg.MCPServers {
			names = append(names, name)
		}
		sort.Strings(names)

		cmd.Printf("Found %d MCP server(s):\n\n", len(names))
		for _, name := range names {
			m := cfg.MCPServers[name]
			cmd.Printf("  %s\n", Bold(name))
			if m.Transport == "http" {
				cmd.Printf("        url: %s (HTTP)\n", m.URL)
			} else {
				args := append([]string{m.Command}, m.Args...)
				cmd.Printf("        command: %v (stdio)\n", args)
			}
		}
		return nil
	},
}

// mcpAddCmd registers a new MCP server in config.yaml.
var mcpAddCmd = &cobra.Command{
	Use:   "add <name> <command> [args...]",
	Short: "Register a new MCP server (stdio or HTTP)",
	Long: `Register an MCP server in config.yaml.

For stdio servers:
  yaah mcp add filesystem npx -y @modelcontextprotocol/server-filesystem /tmp

For HTTP servers (pass --url):
  yaah mcp add github --url http://localhost:3333/mcp`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		url, _ := cmd.Flags().GetString("url")

		var mcpCfg config.MCPServerConfig
		if url != "" {
			mcpCfg = config.MCPServerConfig{URL: url, Transport: "http"}
		} else {
			if len(args) < 2 {
				return fmt.Errorf("command is required for stdio servers (or use --url for HTTP)")
			}
			mcpCfg = config.MCPServerConfig{
				Command:   args[1],
				Args:      args[2:],
				Transport: "stdio",
			}
		}

		cfgPath, err := config.ConfigPath()
		if err != nil {
			return err
		}

		if err := config.AddMCPServer(cfgPath, name, mcpCfg); err != nil {
			return err
		}

		cmd.Printf("Registered MCP server %q in config.yaml\n", Bold(name))
		return nil
	},
}

// mcpRemoveCmd removes an MCP server from config.yaml.
var mcpRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath, err := config.ConfigPath()
		if err != nil {
			return err
		}

		if err := config.RemoveMCPServer(cfgPath, args[0]); err != nil {
			return err
		}

		cmd.Printf("Removed MCP server %q from config.yaml\n", Bold(args[0]))
		return nil
	},
}

func init() {
	mcpAddCmd.Flags().String("url", "", "HTTP URL for HTTP-based MCP servers")
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpAddCmd)
	mcpCmd.AddCommand(mcpRemoveCmd)
	rootCmd.AddCommand(mcpCmd)
}
