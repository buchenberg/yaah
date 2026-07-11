// Package main is the entry point for the yaah CLI.
//
// yaah — Yet Another Agent Harness. A vendor-free, open-source AI agent
// CLI that follows the emerging cross-tool standards (~/.agents/, SKILL.md,
// AGENTS.md, MCP over stdio JSON-RPC). See ./README.md and the design plan
// at Markdown/agentic/yaah-plan.md.
package main

import (
	"fmt"
	"os"

	"github.com/buchenberg/yaah/cmd/yaah"
)

func main() {
	if err := yaah.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
