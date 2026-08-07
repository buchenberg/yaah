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
// toolReg, and returns client handles and server info. Start errors
// are non-fatal and reported to stderr.
func initMCP(cfg *config.Config, toolReg *tools.Registry, skipMCP bool) ([]mcp.MCPClient, []mcp.ServerInfo) {
	if skipMCP {
		return nil, nil
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
	for _, t := range mcpTools {
		toolReg.Register(t)
	}
	return clients, infos
}
