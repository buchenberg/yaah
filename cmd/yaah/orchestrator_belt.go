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
// curation here cannot narrow a dispatch. The curated default is the
// 2026-11-02 orchestrator belt name-for-name (plus the platform shell
// pair and question, which the platform rule and the one-shot
// unregister already handle), so a bench row on this default is
// directly comparable to the recorded 470-503K family.
//
// Semantics (yaah-6mi9.1):
//   - agents.default.tools omitted/empty — the curated list below;
//   - a non-empty list — exactly what it names (unchanged);
//   - the single entry "*" — the full surface, today's behavior, for
//     setups that want every registered tool back.

// defaultOrchestratorBelt is the curated orchestrator default: the
// pre-file-tools belt (2026-11-02 trace, 27 tools), plus question for
// interactive sessions (one-shot unregisters it) and both shell names
// (the registry's platform shell rule refuses the wrong one and reports
// the absence — see tools.go newRegistry).
//
// Deliberately absent: the specialist file cluster (append, bulk_files,
// checksum, copy, copy_tree, find, mkdir, move, move_tree, rmdir, tail)
// and the wake pair (wait_for, watch). The file cluster is the
// developer role's belt; the wake pair belongs to interactive sessions
// and gets its own registration-shape gate (yaah-6mi9.2) rather than a
// belt default. A tool added to this list is an acknowledged floor cost
// of ~780 tokens/call — cite the usage evidence in the bead when adding.
var defaultOrchestratorBelt = []string{
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
	"glob",
	"grep",
	"http",
	"json_query",
	"list_subagents",
	"ls",
	"patch",
	"plan",
	"powershell", // refused by the platform rule off-Windows
	"question",   // unregistered for one-shot sessions
	"read",
	"replace",
	"role",
	"skill",
	"subagent_jobs",
	"supervisor",
	"todowrite",
	"webfetch",
	"write",
}

// orchestratorBeltNames resolves the configured belt into the list that
// reaches tools.NewBelt: an explicit list wins verbatim (normalized),
// "*" alone restores the full surface, and an empty config takes the
// curated default. The nil return is load-bearing — NewBelt reads it as
// allow-all, exactly the "*" contract.
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
// the curated default withholds, so a test can pin the boundary without
// duplicating the belt list. Keep in sync with the bead yaah-6mi9.1
// evidence header above.
var removedFromOrchestratorDefault = []string{
	"append", "bulk_files", "checksum", "copy", "copy_tree",
	"find", "mkdir", "move", "move_tree", "rmdir", "tail",
	"wait_for", "watch",
}
