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
//
// Isolated mode (register=false) still starts the servers — the session
// listing and teardown own the clients — but registers none of their
// tools: MCP tools execute on the host through stdio/HTTP clients no
// Workspace can intercept, so registering them would open direct host
// mutation paths around the apply-out boundary. The names are still
// returned so the caller can record the omission; the caller must then
// NOT pass them to the approval gate, which would prompt for calls to
// tools that cannot dispatch.
func initMCP(cfg *config.Config, toolReg *tools.Registry, skipMCP, register bool) ([]mcp.MCPClient, []mcp.ServerInfo, map[string]bool) {
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
		if register {
			toolReg.Register(t)
		}
		names[t.Name()] = true
	}
	return clients, infos, names
}
