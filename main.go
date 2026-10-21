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
	"github.com/buchenberg/yaah/internal/jail"
)

func main() {
	// A confined exec re-enters this binary with a trampoline sentinel as
	// argv[1]: the Landlock backend confines the current process, then execs the
	// target (see jail.MaybeRunTrampoline). Handle it before any CLI work so
	// a confined sub-agent command never starts a second yaah.
	if code, handled := jail.MaybeRunTrampoline(os.Args); handled {
		os.Exit(code)
	}

	if err := yaah.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
