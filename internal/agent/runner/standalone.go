// standalone.go builds the sub-agent runner a STANDALONE command dispatches
// through: a command with no session composition behind it (no REPL/TUI/serve
// wiring, no supervision infrastructure, no retained lane) and therefore only
// the user's configuration, the store, and the working tree to go on.
//
// It exists so that a command that is not a session has one named production
// default instead of re-deriving the option set. The narrow option struct is
// deliberate: exporting taskRunnerOpts would hand callers the session-only
// knobs (retained lane, dispatch worlds, supervision bridges, resumed scope
// sets) whose zero values mean something quite different outside a session.
package runner

import (
	"errors"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
)

// StandaloneOpts is what a standalone command can honestly supply to build a
// dispatch runner. Every field is a fact the caller resolved from the user's
// configuration — nothing here is defaulted on the caller's behalf, because a
// dispatch that ran on invented facts is a dispatch the user never chose.
type StandaloneOpts struct {
	// Cfg is the validated configuration the dispatch inherits: the
	// sub-agent stanza, the provider table (for per-role routing), and the
	// agent defaults. Required.
	Cfg *config.Config

	// Provider is the provider the dispatch runs on — the one a one-shot
	// session resolves from the same configuration. Required.
	Provider     agent.Provider
	ProviderName string
	ModelName    string

	// SystemPrompt is the base prompt every dispatched sub-agent starts
	// from. The composition root assembles it (the session wiring and the
	// standalone commands pass the same one), so this package never reaches
	// into prompt paths itself.
	SystemPrompt string

	// DB is the store dispatches record into. Nil disables persistence:
	// sub-agent transcripts, the memory tools a role declares, and the
	// trace-owner mapping all degrade to absent.
	DB *memory.DB

	// SessionID names the parent a dispatch's records stay joinable to.
	// Empty disables the trace-owner mapping.
	SessionID string

	// SubAgentProvider and SubAgentModel are the global sub-agent override
	// (agents.subagent.provider/.model). Nil/empty inherits Provider.
	SubAgentProvider agent.Provider
	SubAgentModel    string

	// SubContextWindow is the sub-agent context window — the parent's,
	// halved and floored by the caller. Zero disables compaction.
	SubContextWindow int

	// PathValidator confines the dispatch's file tools to the working tree,
	// mirroring the session's local substrate. Nil runs them unrestricted.
	PathValidator *tools.PathValidator

	// ToolSpillDir is where oversized tool results spill. Empty disables
	// spilling.
	ToolSpillDir string

	// ResolveProviderByName resolves a configured provider name for per-role
	// routing. Nil means per-role provider overrides fall through to
	// Provider — provider construction is CLI wiring and does not belong
	// here.
	ResolveProviderByName func(map[string]config.Provider, string) agent.Provider
}

// standaloneDepth is the nesting depth a standalone dispatch grants: zero, so
// a dispatched sub-agent's registry carries no delegate tool of its own. A
// workflow document is the decomposition — the run already decided what work
// exists and in what order, and a sub-agent spawning further sub-agents would
// put dispatches in flight that the document neither declares nor budgets.
const standaloneDepth = 0

// NewStandaloneTaskRunner builds the runner a standalone command dispatches
// through. It refuses a composition that is missing a fact it cannot invent:
// a nil configuration or provider is an error, never a defaulted dispatch.
func NewStandaloneTaskRunner(opts StandaloneOpts) (tools.TaskRunner, error) {
	if opts.Cfg == nil {
		return nil, errors.New("runner: a standalone task runner needs a configuration; none was supplied")
	}
	if opts.Provider == nil {
		return nil, errors.New("runner: a standalone task runner needs a provider; none was supplied")
	}
	return makeTaskRunner(taskRunnerOpts{
		provider:              opts.Provider,
		providerName:          opts.ProviderName,
		systemPrompt:          opts.SystemPrompt,
		modelName:             opts.ModelName,
		db:                    opts.DB,
		parentSession:         opts.SessionID,
		subCfg:                opts.Cfg.Agent.SubAgent,
		subAgentProvider:      opts.SubAgentProvider,
		subAgentModel:         opts.SubAgentModel,
		estimateFactor:        opts.Cfg.Agent.Default.EstimateFactor,
		subContextWindow:      opts.SubContextWindow,
		outputLimit:           opts.Cfg.Agent.SubAgent.OutputLimit,
		providerMap:           opts.Cfg.Providers,
		defaults:              opts.Cfg.Agent.Default,
		toolSpillDir:          opts.ToolSpillDir,
		pathValidator:         opts.PathValidator,
		resolveProviderByName: opts.ResolveProviderByName,

		// Deliberately unset, because a standalone command has no session to
		// resolve them from:
		//
		//   workspace / subNetwork  → the local, unconfined posture the
		//                             config's default egress describes.
		//   rootScopeID / runs / scopeManager / traceStore
		//                           → no supervision scope tree; a launch
		//                             that fabricated one would put the
		//                             supervisor's tree out of step with the
		//                             process that owns it.
		//   guardCheck / tracker    → no session guard, no conflict tracker.
	}, standaloneDepth), nil
}
