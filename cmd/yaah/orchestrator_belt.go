package yaah

// orchestrator_belt.go holds the default orchestrator belt (yaah-6mi9.1).
//
// Agents.default.tools omitted used to mean "register everything" — and
// between the 2026-11-02 and 2026-11-12 builds that went from a 27-tool
// belt to 40: the file-tools era (append, bulk_files, checksum, copy,
// copy_tree, find, mkdir, move, move_tree, rmdir, tail) plus wait_for and
// watch all became orchestrator surface. Measured across the whole
// 2026-11-12 five-task run (36 steps): the orchestrator called only four
// tools (bash 64, write 4, read 4, ls 2) and none of the thirteen new
// ones — while every extra schema (~780 tokens each) rides the per-call
// floor on every turn. The floor doubled, 10.5K to ~20.7K tokens/call.
//
// The file cluster already lives where the architecture wants it — the
// developer role's belt (internal/prompts/roles/developer.md lists all
// of it), reachable through delegate; sub-agent registries are built
// from the role profile (runner/registry.go:79), not from this belt, so
// curation here cannot narrow a dispatch. The core list restores the
// 2026-11-02 orchestrator belt, so a bench row on this default stays
// comparable to the recorded 470-503K family — but it was audited
// against the registry, not the bench traces: the gated feature lanes
// (proposal, memory, Go toolchain, apply_out) and MCP admission ride
// along precisely because a one-shot bench never sees them register
// anyway, so restoring the bench shape verbatim would have been
// importing the bench's gates as policy.
//
// Semantics (yaah-6mi9.1):
//   - agents.default.tools omitted/empty — the curated list below;
//   - a non-empty list — exactly what it names (unchanged);
//   - the single entry "*" (or "all") — the full surface, today's
//     behavior, for setups that want every registered tool back.

// defaultOrchestratorBelt is the curated orchestrator default: the
// pre-file-tools belt (2026-11-02 trace, 27 always-on tools), plus
// question for interactive sessions (one-shot unregisters it), both
// shell names (the registry's platform shell rule refuses the wrong
// one and reports the absence — see tools.go newRegistry), and the
// gated feature lanes — proposal, apply_out, the memory cluster and
// the Go-toolchain quintet.
//
// The belt curates only the always-on core. The gated entries are
// belt-admitted precisely because they carry their own registration
// gates, and a curated default must not second-guess them: proposal
// registers only when the retained lane is active (the default), the
// memory tools only when the store admits readers/writers
// (wiring_stages.go buildToolRegistry), the Go quintet only inside a
// Go module (the GoToolAvailable unregister), apply_out only in an
// isolated/containerd session. When a gate is closed the tool never
// registers — no schema, no floor cost — so admitting the lane costs
// the floor only for the sessions the lane actually serves. MCP
// server tools are admitted at registration (wiring_mcp.go Admit): a
// configured MCP server is itself the opt-in, and curation must not
// silently drop it.
//
// Deliberately absent: the specialist file cluster (append, bulk_files,
// checksum, copy, copy_tree, find, mkdir, move, move_tree, rmdir, tail)
// and the wake pair (wait_for, watch). The file cluster is the
// developer role's belt; the wake pair belongs to interactive sessions
// and gets its own registration-shape gate (yaah-6mi9.2) rather than a
// belt default. An always-on tool added to this list is an acknowledged
// floor cost of ~780 tokens/call — cite the usage evidence in the bead
// when adding.
var defaultOrchestratorBelt = []string{
	"apply_out", // isolated/containerd sessions only; the door changes leave through
	"background_process",
	"bash",
	"bisect",
	"calculate",
	"delegate",
	"delete",
	"diff",
	"edit",
	"file_info",
	"git",
	"go_mod",      // Go-module workspaces only (GoToolAvailable unregister)
	"go_outline",  //
	"go_refactor", //
	"go_test",     //
	"glob",
	"grep",
	"http",
	"json_query",
	"list_subagents",
	"ls",
	"memory_add",             // memory lane: readers/writers gate at registration
	"memory_delete",          //
	"memory_promote",         //
	"memory_search",          //
	"memory_search_sessions", //
	"memory_update",          //
	"patch",
	"plan",
	"powershell", // refused by the platform rule off-Windows
	"proposal",   // retained-lane settlement; the lane is default-on (wiring.go)
	"question",   // unregistered for one-shot sessions
	"read",
	"replace",
	"role",
	"skill",
	"staticcheck", // Go-module workspaces only
	"subagent_jobs",
	"supervisor",
	"todowrite",
	"webfetch",
	"write",
}

// orchestratorBeltNames resolves the configured belt into the list that
// reaches tools.NewBelt: an explicit list wins verbatim (normalized),
// "*" (or "all") alone restores the full surface, and an empty config
// takes the curated default. The nil return is load-bearing — NewBelt
// reads it as allow-all, exactly the "*" contract.
func orchestratorBeltNames(configured []string) []string {
	norm := normalizeToolNames(configured)
	if len(norm) == 1 && (norm[0] == "*" || norm[0] == "all") {
		return nil
	}
	if len(norm) > 0 {
		return norm
	}
	return defaultOrchestratorBelt
}

// removedFromOrchestratorDefault names the registered-but-ungated tools
// the curated default withholds, so a test can pin the boundary at the
// registry — under default config the belt's refusal set must equal
// this list (plus the off-platform shell, the platform rule's own
// refusal), no more and no less. A gap in that equality is how an
// undeclared drop (proposal, a memory tool, an MCP name) escapes the
// curation silently; the registry-level test in
// orchestrator_belt_test.go fails first. Keep in sync with the bead
// yaah-6mi9.1 evidence header above.
var removedFromOrchestratorDefault = []string{
	"append", "bulk_files", "checksum", "copy", "copy_tree",
	"find", "mkdir", "move", "move_tree", "rmdir", "tail",
	"wait_for", "watch",
}
