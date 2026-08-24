package yaah

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/tools"
)

// initMCP starts configured MCP servers, registers their tools into
// toolReg, and returns client handles, server info, and the set of
// tool names registered from MCP servers (for approval gating —
// remote tools cannot implement tools.DangerClassifier). Start errors
// are non-fatal and reported to stderr.
func initMCP(cfg *config.Config, toolReg *tools.Registry, skipMCP bool) ([]mcp.MCPClient, []mcp.ServerInfo, map[string]bool) {
	if skipMCP {
		return nil, nil, nil
	}
	mcpManifests := make(map[string]*mcp.Manifest)
	for name, s := range cfg.MCPServers {
		mcpManifests[name] = &mcp.Manifest{
			Command:   s.Command,
			Args:      s.Args,
			Env:       s.Env,
			URL:       s.URL,
			Transport: s.Transport,
			Framing:   s.Framing,
			Headers:   s.Headers,
		}
	}
	clients, mcpTools, infos, err := mcp.StartMCPClientsFromConfig(context.Background(), mcpManifests, io.Discard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP startup error: %v\n", err)
	}
	names := make(map[string]bool, len(mcpTools))
	for _, t := range mcpTools {
		toolReg.Register(t)
		names[t.Name()] = true
	}
	return clients, infos, names
}
