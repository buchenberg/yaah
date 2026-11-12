// This file holds the seven construction stages newAgentSessionWithOptions
// runs, in order: loadConfig, buildWorkspace, buildToolRegistry,
// buildContainment, buildMCPSkills, buildLoopInfra, assemble.
//
// The split is mechanical. Every statement, error message, warning, default and
// cleanup path was moved verbatim out of the single function body that used to
// hold them, so a session build behaves exactly as it did before. Each stage
// takes what it needs (opts, config, the stage results before it) and returns
// what it built.
//
// Ownership convention: a stage hands back the resources it acquired instead of
// cleaning them up itself, because the caller's startup guard has to release a
// partially-built session on every error path. A stage therefore returns its
// (possibly partial) result even when it also returns an error. Only the
// caller's defers release what a stage returned: the workspace stage hands back
// the sandbox and the cancel func for its startup bound, the registry stage
// hands back the memory store.
package yaah

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/goatherd-runtime/sandbox/egress"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/budget"
	"github.com/buchenberg/yaah/internal/agent/events"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/agent/runner"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/agent/supervision"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/dispatch"
	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/providers/catalog"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/buchenberg/yaah/internal/wake"
	"github.com/buchenberg/yaah/internal/worldinfo"
)

// sessionConfig is what loadConfig produces: the validated configuration, the
// provider/model selection resolved from it, and the OTel exporters — whose
// shutdown belongs to the caller's startup guard, not to this stage.
type sessionConfig struct {
	cfg          *config.Config
	provider     agent.Provider
	modelName    string
	providerName string
	otelShutdown func(context.Context) error
	otelActive   bool

	// persona is the session's resolved orchestrator persona (yaah-21uk
	// D4), set right after buildWorkspace rebuilds the role registry.
	// buildMCPSkills binds Persona.Body into the orchestrator system
	// prompt (yaah-21uk.6).
	persona sessionPersona

	// readOnly is the session mode axis's configured default
	// (agents.default.mode, decouple plan Phase 1): whether the new
	// session may mutate the workspace. Set alongside the persona; both
	// land on the agentSession in newSessionFromStages.
	readOnly bool
}

// loadConfig loads and validates the configuration, resolves the provider and
// model from it, and initialises OpenTelemetry.
func loadConfig(opts SessionOptions, skipOtel bool) (*sessionConfig, error) {
	// The bare --resume sentinel resolves only on interactive surfaces
	// (REPL, TUI, one-shot) which run maybeResolveResumePicker first.
	// Non-interactive readers (serve, web, ACP) must name a session.
	if opts.ResumeSessionID == resumePickerSentinel {
		return nil, fmt.Errorf("--resume without a value opens the interactive picker, which is not available in this mode; pass --resume=<session-id>")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if verr := config.Validate(cfg); verr != nil {
		return nil, fmt.Errorf("config: %w", verr)
	}

	out := &sessionConfig{
		cfg:          cfg,
		provider:     resolveProvider(cfg),
		modelName:    resolveModel(cfg),
		providerName: resolveProviderName(cfg),
	}

	// --- OpenTelemetry ---------------------------------------------------
	otelShutdown, otelActive, err := initOtel(cfg, opts, skipOtel)
	if err != nil {
		return nil, err
	}
	out.otelShutdown = otelShutdown
	out.otelActive = otelActive
	return out, nil
}

// workspaceStage is what buildWorkspace and buildContainment produce: the
// session's working directory, the deny patterns, and the one containment
// substrate that applies to it (filled in by buildContainment).
type workspaceStage struct {
	cwd  string
	deny []string

	pathValidator  *tools.PathValidator
	sessionSandbox *tools.SessionSandbox
	// harness guards the harness's own configuration (role files, skills,
	// plans, yaah state) against agent writes. It is built before the tool
	// registry so direct writers (role, apply_out) can hold it, and its Ask
	// surface is attached later in wireSessionHooks.
	harness *tools.HarnessPaths

	// cancelStart releases the containerd backend's startup bound. It is a
	// no-op on the local path so the caller can always defer it.
	cancelStart context.CancelFunc

	// teardownHandled flips when the sandbox is either handed to the session
	// (success path — close() destroys it) or already destroyed by an error
	// branch further down; the caller's guard exists so no error return leaks
	// a container, and so one failure is never reported on two channels.
	teardownHandled bool
}

// buildWorkspace resolves the session's working directory, rebuilds the
// process-wide role registry over it, and resolves the deny patterns the
// substrate consumes. It starts nothing: the containment substrate is built by
// buildContainment, after the tool registry and its gates have run.
func buildWorkspace(cfg *config.Config, opts SessionOptions) (*workspaceStage, error) {
	// cancelStart is set on every path, so the caller can defer it
	// unconditionally.
	st := &workspaceStage{cancelStart: func() {}}

	// A failed Getwd must not limp on as an empty cwd: the isolated
	// bootstrap reads "" as "no source requested" and would silently skip
	// the copy-in, starting the containerd session on an empty container
	// with no baseline.
	//
	// A caller-supplied cwd (ACP sessions) wins over the process cwd and
	// is never applied with os.Chdir: several sessions with different
	// workspaces must be able to share one process.
	st.cwd = opts.Cwd
	if st.cwd == "" {
		var gerr error
		st.cwd, gerr = os.Getwd()
		if gerr != nil {
			return st, fmt.Errorf("determine the working directory: %w", gerr)
		}
	}

	// Role registry: built-in (embedded) definitions plus user-defined
	// rolefiles from the searched directories (project dirs walked up
	// from cwd, then ~/.agents/roles). Multi-session (ACP): a session
	// with a different cwd must not *overwrite* the registry earlier
	// sessions still dispatch against, so one union registry is rebuilt
	// and atomically swapped per build over every dir seen so far.
	// Fully per-session registries remain future work — dispatch
	// consults the process-wide DefaultRegistry — so the union trades
	// same-name rolefile isolation for keeping earlier sessions usable.
	rebuildDefaultRoleRegistry(st.cwd)

	// Deny patterns: explicit config wins; unset falls back to the
	// built-in secret-file defaults; an explicit empty list disables. Both
	// substrates resolve the same list — the local validator gates tool
	// access, and the isolated bootstrap excludes matching files from the
	// copy-in so they never become agent-readable inside the container
	// either.
	st.deny = cfg.Agent.Default.WorkspaceDenyPatterns
	if st.deny == nil {
		st.deny = defaultWorkspaceDenyPatterns
	}

	// Harness-configuration guard: built here, before the tool registry, so
	// the role and apply_out tools can hold the same policy the path
	// validator uses. Both the session cwd and an explicit --workspace root
	// are protected; a session started in $HOME still cannot downgrade the
	// global rules, which are ordered first.
	userHome, _ := os.UserHomeDir()
	st.harness = tools.NewHarnessPaths(userHome, config.HomeDir(), st.cwd, opts.WorkspaceRoot)

	return st, nil
}

// buildContainment installs the session's workspace containment substrate and
// attaches it to the tool registry. It runs after buildToolRegistry rather than
// inside buildWorkspace because starting a containerd sandbox is the one
// expensive, externally visible side effect of a session build: doing it first
// meant a registry or tool-gate failure created a container only to destroy it
// again. Nothing earlier needs the substrate — the isolated/local split reads
// the config, not the sandbox.
//
// Exactly one substrate is installed. A sandbox started here stays the
// caller's to destroy: it is returned on the workspace stage, with the
// cancelStart bound that releases its startup timeout.
func buildContainment(cfg *config.Config, opts SessionOptions, ws *workspaceStage, tr *toolRegistryStage) error {
	// Workspace containment. Exactly one substrate may be installed:
	//
	//   - containerd backend: the session's sandbox is constructed, image-
	//     probed (D5), and created here, once per session. SetWorkspace
	//     (after every registration) hands the registry the sandbox
	//     workspace. The host path validator must NOT also run — it would
	//     install a local workspace over the registry's nil one, and the
	//     two substrates must not race for tools.
	//   - local backend with --workspace: the host validator gates paths.
	//   - neither: no validator; legacy ~-expansion behaviour.
	switch {
	case cfg.Workspace.Backend == config.BackendContainerd:
		// A wedged daemon must not hang session startup: the probe, the
		// Create, and the contract exec all run under one generous
		// bound (large enough for a first-run image pull).
		startCtx, cancelStart := context.WithTimeout(context.Background(), workspaceSandboxStartTimeout)
		ws.cancelStart = cancelStart
		sessionSandbox, serr := tools.StartSessionSandbox(
			startCtx, cfg.Workspace, tools.OpenContainerdWorkspaceSandbox, ws.cwd, ws.deny)
		if serr != nil {
			return serr
		}
		ws.sessionSandbox = sessionSandbox
		if opts.WorkspaceRoot != "" {
			fmt.Fprintf(os.Stderr, "warning: --workspace %s has no effect: the workspace.backend containerd session is isolated from the host\n", opts.WorkspaceRoot)
		}
	default:
		// Every local session gets a validator so relative paths and
		// shell commands resolve against the session's own cwd rather
		// than the process cwd (ACP serves several sessions, each
		// with its own workspace, in one process). With no
		// --workspace there is no containment: keep the legacy
		// unrestricted behaviour, including ~ expansion and no deny
		// patterns.
		allowHome := opts.AllowHomeAccess
		var patterns []string
		if opts.WorkspaceRoot != "" {
			patterns = ws.deny
		} else {
			allowHome = true
		}
		ws.pathValidator = tools.NewPathValidator(opts.WorkspaceRoot, allowHome, patterns)
		ws.pathValidator.WorkDir = ws.cwd
		ws.pathValidator.Harness = ws.harness
		// The local substrate's paths gate this registry directly. Exactly
		// one substrate: the isolated backend instead hands the registry
		// its workspace at the SetWorkspace gate, after every tool is
		// registered.
		tr.reg.SetPathValidator(ws.pathValidator)
	}

	// The accept seam (plan §2.3, D1): the single door changes leave the
	// container through, registered only in isolated mode — in local mode
	// the tools write the host directly and there is nothing to let out.
	//
	// The pointer is held because the effect sink cannot be attached here: the
	// trace store, effect bus and root scope are all created further down. The
	// registry holds the same pointer, so setting the field later reaches the
	// registered tool — and doing it during wiring, before any turn can
	// dispatch, means no call can observe the tool half-configured.
	if ws.sessionSandbox != nil {
		tr.applyOutTool = &tools.ApplyOutTool{Sandbox: ws.sessionSandbox, HostDir: ws.cwd, Harness: ws.harness}
		tr.reg.Register(tr.applyOutTool)
	} else {
		// Local mode has no accept seam, so nothing registers under the
		// name — and since the curated default belt admits apply_out
		// (yaah-6mi9.1), an unknown entry would read as a typo in the
		// config warning at startup. Name it on the belt for the same
		// reason the memory gates do (couldn't, not didn't): the tool
		// exists; this session shape just has no door for it.
		tr.belt.Know("apply_out")
		tr.omittedTools = append(tr.omittedTools, "apply_out")
	}
	return nil
}

// toolRegistryStage is what buildToolRegistry produces: the session's tool
// registry under its belt, the tool names the session deliberately leaves out,
// the accept seam's tool, and the memory store the db-backed tools read.
type toolRegistryStage struct {
	reg              *tools.Registry
	belt             *tools.Belt
	omittedTools     []string
	applyOutTool     *tools.ApplyOutTool
	toolbeltTool     *tools.ToolbeltTool
	db               *memory.DB
	embedderAttached bool

	// personaName/personaTools carry the resolved session persona into
	// the guidance-time unknown-name check (yaah-21uk.7). personaTools
	// is the normalized tools list — empty means no narrowing — and the
	// name keeps the warning truthful about where a bad name came from.
	personaName  string
	personaTools []string
}

// buildToolRegistry builds the session's tool registry and registers the tools
// whose dependencies are available this early (the leaf tools through the belt,
// the accept seam, the memory tools). The construction belt is config-only
// since the mid-session persona switch (yaah-21uk.12): the persona's tools
// list narrows the ADVERTISED set at loop build instead of the belt, so a
// switch re-narrows without registry surgery. The persona still rides this
// stage — its name and tools feed the unknown-name warning at guidance
// time. The session store arrives pre-opened (yaah-21uk.9). It has no
// failure of its own: an open failure was already reported warn-and-continue
// by the wiring.
func buildToolRegistry(cfg *config.Config, opts SessionOptions, ws *workspaceStage, persona subagent.Persona, db *memory.DB) (*toolRegistryStage, error) {
	st := &toolRegistryStage{db: db}

	// The tool registry (plan §3, Option A): local mode installs every
	// built-in; isolated mode curates the set — the host-only leaf tools
	// are left out here, the runtime-wired host-only tools at their
	// registration sites below — so Registry.SetWorkspace's gate finds
	// both diagnostic lists empty and the isolated session can actually
	// be selected. The model is told about the delta (plan §2.2) and the
	// record keeps it (decision D4).
	isolated := cfg.Workspace.Backend == config.BackendContainerd
	// Tool belt (yaah-w8x): the session's allowlist of tools. The belt
	// is enforced by the registry at registration — a tool it does not
	// admit is never installed, so a late registration is refused like
	// any other. An unset list resolves to the curated orchestrator
	// default (yaah-6mi9.1, orchestrator_belt.go); "*" alone restores
	// the full surface.
	//
	// Persona narrowing (yaah-21uk.7) rode this belt; since the
	// mid-session switch (yaah-21uk.12) it happens per loop build — the
	// advertised set intersects the session persona's tools list, so a
	// switch re-narrows without registry surgery. The belt is
	// config-only: agents.default.tools shapes what registers, and the
	// persona feeds the per-run seam instead. personaName/personaTools
	// are still carried for the unknown-name warning below. Ordering
	// with plan mode is unchanged: this shapes what is registered; the
	// read-only tools level (effectiveToolsLevel) still strips write
	// tools on top at loop build.
	pTools := normalizeToolNames(persona.Tools)
	st.personaName = persona.Name
	st.personaTools = pTools
	// The configured belt resolves through the curated default
	// (yaah-6mi9.1): empty config takes defaultOrchestratorBelt, an
	// explicit list wins verbatim, and "*" alone restores the full
	// surface. The persona narrows at loop build (narrowToPersonaTools,
	// also narrow-only), not here — personaTools does not feed this
	// call; the nil keeps the resolved list verbatim.
	belt := tools.NewBelt(effectiveBeltNames(orchestratorBeltNames(cfg.Agent.Default.Tools), nil))
	st.belt = belt
	if isolated {
		st.reg = tools.NewIsolatedRegistryWithBelt(belt)
		// Derived from the same hostOnly() marker the registry filter
		// and the SetWorkspace gate read, so the prompt delta and the
		// D4 record can never name a tool the session actually has, or
		// miss one it does not.
		st.omittedTools = append(st.omittedTools, tools.HostOnlyLeafTools()...)
	} else {
		st.reg = tools.NewRegistryWithBelt(belt)
	}

	// One-shot sessions are non-interactive: a blocking question has
	// no one to answer it, so the tool is left out of the belt and the
	// prompt guidance (below) says so.
	if opts.OneShot {
		st.reg.Unregister("question")
	}

	// Go toolchain tools in a workspace that is not a Go module: the
	// same rule the sub-agent path already applies (runner.go's
	// subAgentUnavailableTools, yaah-9n0). go_test/go_mod/go_outline/
	// go_refactor/staticcheck drive the Go toolchain against the
	// workspace, so outside a module they cannot succeed — the schema is
	// per-call cost that buys a failed call later.
	//
	// Resolved ONCE here, from the project root, and never re-checked:
	// the tools array is part of the cached prompt prefix, so a set that
	// flipped mid-session (the agent runs `go mod init`) would invalidate
	// the cache on every later call and cost far more than the schemas
	// save. In isolated mode the container holds a copy of this project,
	// so the host root is the right proxy — the same substitution
	// subAgentUnavailableTools documents.
	if !tools.GoToolAvailable(ws.cwd) {
		for _, name := range tools.GoToolchainLeafTools() {
			// The tool exists and this session merely cannot use it, so an
			// allowlist naming one is not a typo.
			belt.Know(name)
			// Only a tool this registry actually holds is dropped by it. In
			// isolated mode go_refactor is already absent as host-only and
			// already named in omittedTools; recording it a second time
			// would duplicate the entry in the prompt delta and in the
			// session's workspace_omitted_tools.
			if st.reg.Get(name) == nil {
				continue
			}
			st.reg.Unregister(name)
			st.omittedTools = append(st.omittedTools, name)
		}
	}

	// --- Embedding server ------------------------------------------------
	// The embedder is attached here (skillTool reads it below), but the
	// backfill goroutine launches only after the last fallible step
	// (the SetWorkspace gate, further down): the startup guard closes db
	// on every error return, and a backfill racing that close aborts
	// under a spurious warning.
	if db != nil {
		st.embedderAttached = attachEmbedder(db, cfg)
		// One probe per session (yaah-ka6): a configured-but-down embedder
		// warns once here rather than degrading recall to keyword matching
		// silently. It never refuses the session.
		if st.embedderAttached {
			checkEmbedderAtStartup(db.Embedder(), cfg)
			// Reconcile the vec0 vector index against the embedder's width so
			// a model swap recreates the table before the first search; the
			// write/reconcile paths retry lazily if this is still unreachable.
			ensureVectorIndexAtStartup(db)
		}
	}

	// Memory tools (yaah-zx18). The readers and the writers gate separately,
	// because the argument against each is different.
	//
	// Writers need a session that can act on what it records. A one-shot run
	// is a single non-interactive turn whose store is typically discarded
	// with its container, so "saving a finding" spends output tokens on a
	// note nobody will read — a bench run was measured doing exactly that.
	//
	// Readers are not covered by that argument. Recall is useful in one shot
	// whenever there is something to recall, and the prompt preloads only 50
	// entries (memoryLayer) with no query, so anything past that cap — or
	// needing semantic rather than recency matching — is reachable only
	// through memory_search. So the readers are gated on the store actually
	// holding something: a fresh YAAH_HOME (CI, benchmarks, a throwaway
	// container) still drops all six and keeps the measured floor, while a
	// real one-shot run against a populated store keeps recall.
	memoryReaderNames := []string{"memory_search", "memory_search_sessions"}
	memoryWriterNames := []string{"memory_add", "memory_update", "memory_delete", "memory_promote"}

	memoryWriters := db != nil && !opts.OneShot
	// Short-circuits, so the store probe only runs on the path that needs it.
	memoryReaders := db != nil && (memoryWriters || storeHasRecall(db))

	if memoryReaders {
		st.reg.Register(&tools.MemorySearchTool{DB: db})
		st.reg.Register(&tools.MemorySessionSearchTool{DB: db})
	}
	if memoryWriters {
		st.reg.Register(&tools.MemoryAddTool{DB: db})
		st.reg.Register(&tools.MemoryUpdateTool{DB: db})
		st.reg.Register(&tools.MemoryDeleteTool{DB: db})
		st.reg.Register(&tools.MemoryPromoteTool{DB: db})
	}
	// Name what THIS gate dropped, so an allowlist mentioning one is not
	// reported as a typo and the session record can say "couldn't" rather
	// than "didn't". Only the gate's own omissions: a tool the belt refused
	// is already in belt.Refused(), and recording it here as well would put
	// the name in workspace_omitted_tools twice.
	if !memoryReaders {
		belt.Know(memoryReaderNames...)
		st.omittedTools = append(st.omittedTools, memoryReaderNames...)
	}
	if !memoryWriters {
		belt.Know(memoryWriterNames...)
		st.omittedTools = append(st.omittedTools, memoryWriterNames...)
	}

	return st, nil
}

// mcpSkillsStage is what buildMCPSkills produces: the clean system prompt, the
// MCP servers this session started (and the tool names isolated mode left
// out), and the host process manager behind the background_process tool.
type mcpSkillsStage struct {
	systemPrompt string
	// identityEnd is the anchor BuildMarked returned for systemPrompt: the
	// byte offset just past the identity block, where directive and mode
	// injections anchor. It threads to the session so per-run injections
	// never re-locate the identity by substring search, which a non-default
	// identity would fail.
	identityEnd int
	// layers retains the construction-time persona-independent prompt
	// layers (yaah-21uk.12): the session keeps them so a mid-session
	// persona switch rebinds only the persona body and re-renders a
	// byte-exact-except-the-persona prompt. promptMemory is the guidance
	// the memory guidance block rendered from (resumes render none).
	layers       prompts.Layers
	promptMemory prompts.MemoryGuidance
	mcpClients   []mcp.MCPClient
	mcpInfos     []mcp.ServerInfo
	mcpToolNames map[string]bool
	omittedMCP   []string
	procMgr      *processpkg.Manager
}

// buildMCPSkills assembles the system prompt, starts the MCP servers, and
// registers the skill, plan, role, todo and background_process tools. Like
// buildToolRegistry it has no failure of its own: every one of these pieces
// degrades (a warning, a nil store) rather than failing the session. The
// system prompt carries the session's resolved persona (lc.persona, bound
// by buildSystemPromptMarked).
func buildMCPSkills(cfg *config.Config, opts SessionOptions, lc *sessionConfig, ws *workspaceStage, tr *toolRegistryStage, skipMCP bool) (*mcpSkillsStage, error) {
	st := &mcpSkillsStage{}

	// --- Prompt assembly -------------------------------------------------
	// Derived from the registry, NOT from the intent above: an
	// `agents.default.tools` allowlist can refuse a registration through the
	// belt, and gating on intent alone would leave the prompt advertising
	// tools that are not in the request. Each half is checked against every
	// tool its bullets name, so a partial allowlist cannot over-advertise.
	mem := prompts.MemoryGuidance{
		Recall: tr.reg.Get("memory_search") != nil && tr.reg.Get("memory_search_sessions") != nil,
		Writers: tr.reg.Get("memory_add") != nil && tr.reg.Get("memory_update") != nil &&
			tr.reg.Get("memory_delete") != nil && tr.reg.Get("memory_promote") != nil,
	}
	st.layers = orchestratorPromptLayers(cfg, ws.cwd, tr.db)
	st.promptMemory = mem
	st.systemPrompt, st.identityEnd = buildMarkedPrompt(st.layers, opts.ResumeSessionID, mem, lc.persona.Persona)

	// --- MCP servers ------------------------------------------------------
	// Isolated mode omits MCP tools by policy: they execute on the host
	// through stdio/HTTP clients no Workspace can intercept, so
	// registering them would open host mutation paths around the
	// apply-out boundary. The servers still start (the session owns
	// their lifecycle); their tool names are recorded so the prompt and
	// the D4 session row can name what was left out. mcpToolNames is
	// then cleared for the loop config — the approval gate would prompt
	// for calls to tools that cannot dispatch.
	st.mcpClients, st.mcpInfos, st.mcpToolNames = initMCP(cfg, tr.reg, skipMCP, ws.sessionSandbox == nil, opts.MCPManifests, ws.cwd)
	if ws.sessionSandbox != nil {
		for name := range st.mcpToolNames {
			st.omittedMCP = append(st.omittedMCP, name)
			// The MCP tools exist in this session's configuration even
			// though the workspace leaves them out: the belt must know
			// the names, or an allowlist that names them reads as a typo.
			tr.belt.Know(name)
		}
		slices.Sort(st.omittedMCP)
		if len(st.omittedMCP) > 0 {
			fmt.Fprintf(os.Stderr, "warning: isolated mode omits %d MCP tool(s) — they run on the host and the workspace cannot contain them: %s\n",
				len(st.omittedMCP), strings.Join(st.omittedMCP, ", "))
		}
		st.mcpToolNames = nil
	} else {
		// A persona tools list is exact-match, and the MCP names exist
		// only once the servers answered tools/list. A role file that
		// predates the server (or simply lists its local tools) loses
		// every remote tool with no other trace — say so at startup.
		warnPersonaMCPOmissions(lc.persona.Persona.Name, lc.persona.Persona.Tools, st.mcpToolNames)
	}

	skillDirs := skillSearchPaths()
	// Strict (the autonomous-write security scan) is opt-in and defaults off:
	// it is meant to guard unattended consolidation writes, not human-directed
	// skill editing. No caller enables it yet — the unattended write path lands
	// in P3/P4 (docs/compaction-as-sleep.md §8). When that lands, derive it
	// from an explicit "unattended" session mode, not the sandbox flag.
	skillTool := &tools.SkillTool{Dirs: skillDirs, ReadOnly: ws.sessionSandbox != nil}
	if tr.db != nil {
		skillTool.Embedder = tr.db.Embedder()
		// Skill lifecycle tracking (usage, provenance, mutation ledger).
		// Left nil when the session has no store: the tool then behaves
		// exactly as it did before P1.
		skillTool.Store = tr.db
	}
	tr.reg.Register(skillTool)

	// skill and plan stay available in isolated mode but read-only:
	// their mutating actions write host harness configuration (.agents/,
	// tracked in git), which must not be written around apply_out. The
	// prompt delta says so; the tools refuse the mutating actions
	// themselves.
	planDirs := planSearchPaths()
	tr.reg.Register(&tools.PlanTool{Dirs: planDirs, ReadOnly: ws.sessionSandbox != nil})

	roleDirs := runner.RoleSearchPaths(ws.cwd)
	// role is host-only (plan §3, decision D3): role files are harness
	// configuration the host reads. The registry above still loads the
	// host's roles; only the management tool is left out of the isolated
	// session.
	if ws.sessionSandbox == nil {
		tr.reg.Register(&tools.RoleTool{
			Dirs:    roleDirs,
			Harness: ws.harness,
			// Reload through the union: the process-wide registry carries
			// every session's contributions (multi-session ACP); a reload
			// from this session's paths alone would evict the rest. The
			// union resolver ignores the tool's Dirs for reloads only —
			// writes still land in Dirs[0], which the union already holds.
			BuiltinFiles: runner.BuiltinRoleFiles(),
			Resolver:     unionRoleResolver{},
		})
	} else {
		tr.omittedTools = append(tr.omittedTools, "role")
		tr.belt.Know("role")
	}

	var todoStore *todo.Store
	if tr.db != nil {
		todoStore = todo.NewStoreWithDB(tr.db)
		if err := todoStore.LoadFromDB(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load todos: %v\n", err)
		}
	} else {
		todoStore = todo.NewStore()
	}
	tr.reg.Register(&tools.TodoWriteTool{
		Store: todoStore,
		OnWrite: func() {
			if formatted := todoStore.Format(); formatted != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", formatted)
			}
		},
	})

	st.procMgr = processpkg.NewManager()
	// background_process is host-only: it drives this in-memory host
	// process manager. The manager itself stays — session teardown still
	// owns it — but the tool is not exposed in isolated mode.
	if ws.sessionSandbox == nil {
		tr.reg.Register(&tools.BackgroundProcessTool{Manager: st.procMgr})
	} else {
		tr.omittedTools = append(tr.omittedTools, "background_process")
		tr.belt.Know("background_process")
	}

	registerLazyToolbelt(lc, tr)

	return st, nil
}

// registerLazyToolbelt registers the toolbelt's loader (yaah-6mi9.5,
// docs/lazy-toolbelt-design.md). Belt admission of "toolbelt" is the
// opt-in, exactly the MCP precedent: the curated default names it, an
// explicit agents.default.tools list that omits it keeps the session
// exactly as configured, and one that names it sanctions the widening
// the loader exists for.
//
// Admits answers the mount-time nameAdmission check: the persona's
// tools list (tr.personaTools — the same normalization the per-loop
// seam narrows with; a persona allowlist cannot be widened by a
// mount, per the design's matrix) and the configured read-only
// default (the inspection set, agent.InspectionSetNames). The answers
// are for the mount's atomicity only — a family that could surface no
// member fails citing the refusing origin. The enforcement floors are
// unchanged and stay authoritative: advertise-time narrowing
// re-narrows every loop build (mid-session role switches and ACP
// mode flips outrank this snapshot), and per-call gates evaluate
// every mounted call as they would a startup-registered one.
func registerLazyToolbelt(lc *sessionConfig, tr *toolRegistryStage) {
	// Startup validation (yaah-6mi9.5 review): the family map is pinned
	// by tests against the curation and the leaf factory, but a name
	// that drifts should say so at startup rather than surface first
	// as a mount-time refusal with origin "unknown".
	for _, f := range tools.ToolFamilies() {
		for _, name := range f.Tools {
			if tools.NewLeafTool(name) == nil {
				fmt.Fprintf(os.Stderr, "warning: toolbelt family %s names %q, but no such leaf tool exists\n", f.Name, name)
			}
		}
	}
	inspectSet := agent.InspectionSetNames()
	tr.toolbeltTool = &tools.ToolbeltTool{
		Reg: tr.reg,
		Admits: func(name string) (bool, string) {
			if len(tr.personaTools) > 0 && !slices.Contains(tr.personaTools, name) {
				return false, "persona"
			}
			if lc.readOnly && !slices.Contains(inspectSet, name) {
				return false, "mode_floor"
			}
			return true, ""
		},
	}
	tr.reg.Register(tr.toolbeltTool)
}

// bindToolbeltLiveAdmits re-points the toolbelt's mount-time admission
// answer at the session's live axes (yaah-6mi9.5 review: the startup
// closure consulted the construction-time snapshot, so a session that
// flipped read-only→full over ACP was falsely refused a mutating
// mount, and a mid-session role switch left the old persona's answer
// in place). Called from assemble() once the agentSession exists; the
// startup closure remains the pre-construction standby. Both accessors
// take the session lock briefly — the mount executes on the loop,
// where the session lock is not held, so there is no deadlock path.
// Advertise-time narrowing and the per-call gates remain the
// enforcement floors either way; this answers the mount's atomicity.
func bindToolbeltLiveAdmits(sess *agentSession, tr *toolRegistryStage) {
	if tr.toolbeltTool == nil {
		return
	}
	inspectSet := agent.InspectionSetNames()
	tr.toolbeltTool.Admits = func(name string) (bool, string) {
		if personaTools := normalizeToolNames(sess.PersonaTools()); len(personaTools) > 0 &&
			!slices.Contains(personaTools, name) {
			return false, "persona"
		}
		if sess.IsReadOnly() && !slices.Contains(inspectSet, name) {
			return false, "mode_floor"
		}
		return true, ""
	}
}

// loopInfraStage is what buildLoopInfra produces: the restored transcript, the
// session identity, shepherd/the supervisor infrastructure, the egress posture,
// the delegate tool and the remaining registrations.
type loopInfraStage struct {
	messages     []types.Message
	sessionID    string
	msgIdx       int
	usage        restoredUsage
	systemPrompt string
	mainPrompt   string
	// previousSummary is the resumed session's persisted anchored summary:
	// the next compaction folds it into its prompt (yaah-an55). Empty for a
	// fresh session and for a trim-marker rollover, which carries no
	// anchored summary to update.
	previousSummary string
	// identityEnd anchors after-identity injections (see mcpSkillsStage);
	// it stays valid across the tool-guidance appends, which only extend
	// the prompt's tail.
	identityEnd int

	tracker        *tools.ConflictTracker
	backgroundJobs *jobs.BackgroundJobs
	followupCh     chan string
	steerCh        chan string
	wakeMgr        *wake.Manager
	wakeHooks      *events.HookEmitter

	scopeMgr      *goatherd.ScopeManager
	traceStore    *shepherdtrace.SQLiteTraceStore
	traceBus      *shepherdtrace.EffectBus
	rootScopeID   string
	runs          *supervision.Registry
	supervisorSvc *supervision.Service
	reaper        *supervision.Reaper
	egressBroker  *egress.Broker

	// resumedScopes is the scope-ID set startup recovery re-admitted from
	// the trace (yaah-95ri.7): nothing runs under them in this process, so
	// the dispatch cap skips them and the reaper retires them on its
	// startup pass. Nil when recovery adopted nothing.
	resumedScopes map[string]bool

	worldStatus worldinfo.Status
	taskTool    *tools.DelegateTool

	// roleWatcher keeps the default role registry in sync with raw role
	// file edits the role tool did not make (W6): started with the
	// session, stopped in the session close and in the startup guard.
	roleWatcher *runner.RoleWatcher
}

// buildLoopInfra restores the transcript, builds the loop's shared
// infrastructure (background jobs, shepherd scope tree, egress posture), and
// registers the tools that depend on it: delegate, sub-agent jobs, supervisor,
// proposal and list_sub_agents.
//
// The body is the focused constructors below, in the order a session needs
// them: loadProviderCatalog, restoreTranscript, buildBackgroundLoop,
// buildShepherdInfra, wireDispatchPosture, buildDelegateTool, startScopeReaper,
// wireLaneRouting, wireBackgroundJobs, registerWakeTools,
// registerSubAgentTools.
func buildLoopInfra(cfg *config.Config, opts SessionOptions, lc *sessionConfig, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage) (*loopInfraStage, error) {
	st := &loopInfraStage{}

	// Every focused constructor writes the resources it acquires straight
	// onto st, so a constructor that fails still leaves them visible to the
	// caller's startup guard — the same ownership rule the stages themselves
	// keep (see wiring.go). Two constructors additionally return a small
	// value struct (subAgentDefaults, dispatchPosture) that only feeds later
	// constructors. The calls stay in the original statement order: the
	// resource-owning pieces the guard releases (supervisorSvc, reaper,
	// egressBroker) are on st before any later error return can fire.
	loadProviderCatalog()
	if err := restoreTranscript(st, cfg, opts, lc, ws, tr, mcps); err != nil {
		return st, err
	}

	subs := buildBackgroundLoop(st, cfg, lc)
	// The wait/watch engine is session-wide: a watch outlives the turn that
	// armed it, so it lives beside the background-job manager rather than
	// inside a tool call.
	buildWakeEngine(st, cfg, ws, tr, mcps)
	// retainCapable reports whether the session root can capture workspace
	// state, which decides the retained lane further down.
	retainCapable := buildShepherdInfra(st, cfg, ws, tr)

	posture, err := wireDispatchPosture(st, cfg, opts, ws, retainCapable)
	if err != nil {
		return st, err
	}

	buildDelegateTool(st, cfg, lc, ws, tr, posture, subs)
	startScopeReaper(st)
	wireLaneRouting(st)
	wireBackgroundJobs(st, tr)
	registerWakeTools(st, tr, opts.OneShot)
	registerSubAgentTools(st, cfg, tr)
	startRoleWatcher(st)

	return st, nil
}

// startRoleWatcher starts the polling watcher that reloads the default
// role registry when role files change outside the role tool (W6). It
// watches and reloads the accumulated union of role directories — the set
// the process-wide registry was actually built from, which spans every
// session's cwd in a multi-session process, not just this one's — so no
// reload can evict a sibling session's contributions and no contributed
// directory goes unwatched: each session build starts a watcher scoped to
// the union of everything contributed so far, including its own paths. A
// construction failure only disables hot reload — the registry keeps
// serving the definitions loaded at startup — and reports the reason on
// stderr, matching the store-open posture above.
func startRoleWatcher(st *loopInfraStage) {
	w, err := runner.NewRoleWatcher(roleDirsUnion(), runner.BuiltinRoleFiles(), unionRoleResolver{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: role hot reload disabled: %v\n", err)
		return
	}
	w.Start()
	st.roleWatcher = w
}

// loadProviderCatalog forces the embedded models.dev snapshot's one-time decode
// during session startup, while os.Stderr still points at the real terminal.
//
// The catalog logs, once, the moment it finds data.json unusable — a build
// error that otherwise degrades silently to "every limit and reasoning answer
// is unknown". The TUI redirects os.Stderr to /dev/null before it enters the
// alternate screen (see runTUI), so a decode left until the first lookup would
// have its warning swallowed by exactly the surface that runs without a log
// file. Decoding here, in the composition root every surface shares, gets the
// warning out first.
//
// A broken snapshot still degrades instead of failing the build: the empty
// index answers "unknown" and callers fall back to their own tables, so a bad
// data.json never blocks startup — it is just no longer silent.
func loadProviderCatalog() {
	_ = catalog.DecodeErr()
}

// restoreTranscript restores the stored transcript for a resumed session (or
// starts a fresh one), persists the new session record, and derives the
// top-level prompt from the clean system prompt. It owns no resource the
// startup guard must release.
func restoreTranscript(st *loopInfraStage, cfg *config.Config, opts SessionOptions, lc *sessionConfig, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage) error {
	var restoreStore sessionRestoreStore
	if tr.db != nil {
		restoreStore = tr.db
	}
	restored, err := restoreSession(restoreStore, opts.ResumeSessionID, mcps.systemPrompt)
	if err != nil {
		return err
	}
	st.messages, st.sessionID, st.msgIdx, st.systemPrompt = restored.Messages, restored.SessionID, restored.MsgIdx, restored.SystemPrompt
	st.previousSummary = restored.PreviousSummary
	st.usage = restored.Usage
	if opts.ResumeSessionID == "" {
		st.sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		if tr.db != nil {
			// The resolved persona rides the row (yaah-21uk.9): resume
			// reads the name back and re-resolves it against the registry
			// of that day, so budget and belt keep the session's own
			// selection after the config default moves on. The prompt
			// needs no such restore — its persona layer is already in
			// system_prompt's bytes.
			tr.db.CreateSession(memory.Session{
				ID:        st.sessionID,
				StartedAt: time.Now().Unix(),
				CWD:       canonicalStoredCwd(ws.cwd),
				// The resolved pick in the "provider/model" wire form
				// (parity W4): the pick rides the row so a resume
				// re-routes the session even when a persona switch or a
				// config change moved the default in between. An unset
				// model stores "" — storedSessionModel reads it as no
				// pick and the chain decides.
				Model:        wireModelValue(lc.providerName, lc.modelName),
				SystemPrompt: st.systemPrompt,
				Persona:      lc.persona.Persona.Name,
			})
		}
	}

	// The identity anchor is located in the session's OWN prompt, not
	// reused from today's fresh assembly: a resumed row's stored prompt can
	// represent a replaced identity whose role file has since changed (or an
	// anchor the fresh assembly computed at a different offset), and
	// InjectAfterIdentityAt trusts a supplied offset without being able to
	// relocate a non-embedded identity. identityAnchorFor finds the
	// embedded identity or the stored replace body; -1 degrades the
	// directive injection to the prompt end rather than mid-identity.
	st.identityEnd = identityAnchorFor(st.systemPrompt, lc.persona.Persona)

	// Derive the top-level agent prompt (with directives) from the clean
	// system prompt. systemPrompt stays clean so child sub-agent prompts
	// never inherit top-level directives.
	st.mainPrompt = buildMainPrompt(cfg, opts, st.systemPrompt, st.identityEnd)
	return nil
}

// subAgentDefaults is what buildBackgroundLoop resolves for the sub-agent lane
// before any dispatch: the model pair delegate runs sub-agents with, and the
// sub-agent context window (half the orchestrator's, floored).
type subAgentDefaults struct {
	provider      agent.Provider
	model         string
	contextWindow int
}

// buildBackgroundLoop builds the loop's shared channels and background job
// manager, and resolves the sub-agent model and window the delegate tool needs.
// It starts nothing: the job manager and the channels belong to the session,
// not to the startup guard.
func buildBackgroundLoop(st *loopInfraStage, cfg *config.Config, lc *sessionConfig) subAgentDefaults {
	var subs subAgentDefaults

	st.tracker = &tools.ConflictTracker{}
	subs.provider, subs.model = resolveSubAgent(cfg)
	subs.contextWindow = providers.ResolveWindow(lc.modelName, cfg.Agent.Default.ContextWindow) / 2
	if subs.contextWindow < 32000 {
		subs.contextWindow = 32000
	}

	st.followupCh = make(chan string, 32)

	// Background sub-agents are managed session-wide: they derive
	// cancellation from a session root (not the per-call context), so
	// they survive the dispatching tool call and turn. Results are
	// delivered as follow-ups; usage is attributed to totalUsage.
	st.backgroundJobs = jobs.NewBackgroundJobs()
	st.backgroundJobs.MaxConcurrent = cfg.Agent.SubAgent.MaxConcurrency
	if st.backgroundJobs.MaxConcurrent <= 0 {
		st.backgroundJobs.MaxConcurrent = 4
	}

	return subs
}

// buildWakeEngine creates the session's event-driven wait/watch engine. Its
// predicates observe the same workspace the tools act on (the sandbox in an
// isolated session, the host otherwise), and the process source is host-only
// to match background_process. The manager starts no watches here; Rehydrate
// runs once the session owns it.
func buildWakeEngine(st *loopInfraStage, cfg *config.Config, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage) {
	isolated := ws.sessionSandbox != nil
	wsx := tools.NewLocalWorkspace(ws.pathValidator)
	if isolated {
		wsx = ws.sessionSandbox.Workspace()
	}
	var store wake.Store
	if tr.db != nil {
		store = wakeStore{db: tr.db}
	}
	var procs *processpkg.Manager
	if !isolated && mcps != nil {
		procs = mcps.procMgr
	}
	// The wake engine is session-scoped and outlives a run, so it owns its
	// hook emitter; the session closes it during teardown.
	st.wakeHooks = events.NewHookEmitter(cfg.Hooks.Dir, st.sessionID)
	st.wakeMgr = wake.NewManager(wake.Options{
		SessionID: st.sessionID,
		Deps:      tools.NewWakeDeps(wsx, st.backgroundJobs, procs),
		Store:     store,
		Observer:  newWakeObserver(st.wakeHooks),
	})
}

// buildShepherdInfra brings up the session's supervision infrastructure: the
// trace store, the recovered scope tree, the steering channel, the supervision
// bridge, the root scope, the accept seam's effect sink and the advisory-rules
// service. It returns retainCapable: whether the root scope can actually
// capture workspace state, and therefore whether the retained lane can be live.
//
// Everything it creates that outlives the build lands on st as it is created —
// scopeMgr, traceStore, traceBus, runs, rootScopeID and supervisorSvc — so the
// guard can close the store and the service on a failed startup. Tracing
// degrades rather than fails, so there is no error return: a session without
// supervision is a supported session.
func buildShepherdInfra(st *loopInfraStage, cfg *config.Config, ws *workspaceStage, tr *toolRegistryStage) bool {
	// Shepherd infrastructure: session-shared trace store, effect bus,
	// and scope manager. Initialized before the task tool is built — the
	// session's root scope (shepherd-adoption plan D1) must exist when
	// the runner closure is constructed, since every sub-agent scope
	// forks from it and the supervisor's tree should mirror the dispatch
	// tree. The orchestrator and sub-agent pipelines share this store, so
	// this is the single initialization point for the supervisor tools
	// and both trace middlewares.
	// retainCapable reports whether the root scope can actually capture
	// workspace state. CreateRootScope demotes an uncapturable directory
	// (e.g. not a git repo) to pure-causal — it still returns a root with
	// an ID, but with a nil sandbox, and a retained dispatch would then
	// fail at CaptureWorkspace. The retained lane is only live when both
	// the root exists and it has a sandbox.
	retainCapable := false
	// The session's steering channel: the REPL's steer source and, from
	// A2 on, the supervisor's injection target for the root scope. Sized
	// with the registry's exported buffer so every channel the registry
	// borrows has the same capacity.
	st.steerCh = make(chan string, supervision.SteerBuffer)
	// Tracing is integral: the session store always initializes. A
	// store-open failure is the runtime safety net, not a config switch —
	// warn and continue without a root scope.
	{
		store, bus, mgr, serr := pipeline.InitShepherdInfrastructure(cfg.Agent.Default.TracePath(), 0)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "warning: shepherd trace store unavailable, continuing without a root scope: %v\n", serr)
		}
		// Scope recovery (adoption plan R1, decision D5): the trace is
		// the registry — rebuild the scope tree, checkpoints, and
		// sealed outputs from the durable records so a restarted
		// process remembers its supervision state. The recovered
		// manager replaces the fresh one. A recovery failure fails
		// loudly (half-adopted sandboxes are worse than none) by
		// disabling the ENTIRE tracing block below — root scope,
		// bridge, and supervisor all stay down rather than
		// half-running against a closed store.
		tracingReady := serr == nil
		if tracingReady {
			repoPath := cfg.Agent.Default.RetainedRepoPath
			if repoPath == "" {
				repoPath, _ = os.Getwd()
			}
			// adoptRoots: an isolated session keeps roots causal (D2) —
			// recovery must not attach a host git sandbox where creation
			// refused one.
			// Plan 07 H5: the kernel's scan stats ride the recovery —
			// the scan is O(history), and the cost stays visible long
			// before it matters.
			var stats goatherd.RecoveryStats
			recovered, report, rerr := recovery.Recover(context.Background(), store, repoPath, ws.sessionSandbox == nil, &stats)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: shepherd tracing disabled: scope recovery failed: %v\n", rerr)
				store.Close(context.Background())
				tracingReady = false
			} else {
				mgr = recovered
				// Resumed scopes (yaah-95ri.7): recovery runs before this
				// process forks anything, so every ACTIVE scope it handed
				// back was re-admitted from the trace and has no loop
				// behind it. The set rides the stage to the two consumers
				// that must not mistake "the trace says active" for
				// "something is running": the dispatch cap (a trace full of
				// crashed dispatches must not refuse every new dispatch)
				// and the scope reaper (they are past grace from the
				// start).
				st.resumedScopes = recovery.ResumedIDs(report.Resumed)
				printRecoveryReport(report)
				printRecoveryStats(&stats)
			}
		}
		if tracingReady {
			st.scopeMgr = mgr
			// Session-owned copies for the clean-exit close (plan 07,
			// H2): the session's close() closes the infrastructure the
			// wiring created — see agentSession.traceStore. The manager
			// and store reach the tools and the runner by injection
			// (plan 07, H4), never through package globals.
			st.traceStore, st.traceBus = store, bus

			// Supervision bridge (adoption plan A2): scope IDs mapped
			// to running loops' steering channels. Session-owned and
			// injected into the task runner, the supervisor tool, and
			// the session teardown — never a process global. The
			// runner registers each sub-agent dispatch; the root
			// registers against the session's own steering channel,
			// so an inject into the root steers the session itself.
			st.runs = supervision.NewRegistry()

			// Root scope (adoption plan D1): local mode shares the
			// in-place git sandbox — never destroyed, the kernel's
			// materializer contract; an isolated session keeps the
			// root pure-causal (the container is the substrate;
			// host-git capture would lie about the tree, isolation
			// plan D2). A failure leaves sub-agent scopes on the
			// legacy flat path rather than bricking tracing.
			if root, rerr := runner.CreateRootScope(mgr, st.sessionID, cfg.Agent.Default.RetainedRepoPath, ws.sessionSandbox != nil); rerr != nil {
				if errors.Is(rerr, runner.ErrRootTerminal) {
					// The explicit degraded mode (R2 review): the
					// resumed session's root already ended, so its
					// scope-tree supervision is over — sub-agent
					// scopes run flat and unsupervised for this
					// session. Name it; do not bury it in the
					// generic no-root warning.
					fmt.Fprintf(os.Stderr, "warning: %v — sub-agent scopes run unsupervised (flat) for this session\n", rerr)
				} else {
					fmt.Fprintf(os.Stderr, "warning: no shepherd root scope — sub-agent scopes fall back to flat creation: %v\n", rerr)
				}
			} else {
				st.rootScopeID = root.ID()
				retainCapable = root.Sandbox() != nil

				// Late-bind the accept seam's effect sink, now that the
				// scope it records against exists. The root scope is the
				// FactAppender (it carries AppendFact and OwnerID), so an
				// accept records its workspace effects on the session's own
				// owner path rather than on a synthetic one.
				//
				// Attached only when a root scope was created: without one
				// there is no owner path to append to, and the tool's nil
				// sink makes recording a silent no-op instead of an error
				// on every accept.
				if tr.applyOutTool != nil {
					tr.applyOutTool.SetEffects(&tools.EffectSink{
						Appender: root,
						Bus:      bus,
						OpID:     st.rootScopeID,
					})
				}

				// Same late binding for the toolbelt's mount fact
				// (yaah-6mi9.5): a mount appends yaah.tool.mounted.v1
				// on the session's own owner path. Nil sink without a
				// root scope makes recording a silent no-op — the
				// per-turn session.context records still carry the
				// effective belt as the diff trail.
				if tr.toolbeltTool != nil {
					tr.toolbeltTool.SetSink(&tools.MountSink{
						Appender: root,
						OpID:     st.rootScopeID,
					})
				}
			}

			// Advisory rules engine (adoption plan B1): the kernel
			// supervisor watches the effect bus the trace store
			// publishes to, and a service drains its interventions
			// into the A2 bridge above — a rule firing becomes a
			// delivered steering message, recorded in the trace.
			// Constructed after the root scope so rule firings
			// against it can be dropped: the parent's own calls
			// already passed the approval middleware, and the exit
			// criterion is explicit — the parent session stays
			// untouched. The bus is otherwise inert (its first
			// consumer, per the adoption audit); disabled by config
			// only, since tracing is already the opt-in.
			supervisorCfg := cfg.Agent.Default.Supervisor
			rules := supervision.AdvisoryRules{
				DestructiveTool:      supervisorCfg.DestructiveRuleEnabled(),
				HighErrorRate:        supervisorCfg.HighErrorRateEnabled(),
				ErrorRateThreshold:   supervisorCfg.ErrorRateThreshold(),
				ErrorRateWindow:      supervisorCfg.ErrorRateWindow(),
				StuckDetection:       supervisorCfg.StuckDetectionEnabled(),
				StuckRepeatThreshold: supervisorCfg.StuckRepeatThreshold(),
			}
			// The blocking shell guard (adoption plan B2) rides the
			// same service: the engine gate covers it, and its check
			// is the synchronous pre-dispatch seam the sub-agent
			// pipeline consults.
			guardEnabled := supervisorCfg.DestructiveGuardEnabled()
			if supervisorCfg.EngineEnabled() && (rules.Any() || guardEnabled) {
				// haltRoot is the B3 gate: a rule-driven halt of the session root
				// (the ctrl-C equivalent) dispatches only under the
				// supervisor.halt_root opt-in.
				st.supervisorSvc = supervision.NewService(mgr, bus, st.runs, rules, guardEnabled, cfg.Agent.Default.HaltRootEnabled(), st.rootScopeID)
				st.supervisorSvc.Start("yaah-supervisor-" + st.sessionID)
			}
		}
	}

	return retainCapable
}

// dispatchPosture is what wireDispatchPosture produces: the workspace and spill
// directory a sub-agent dispatch runs with, the confinement policy it carries,
// the per-dispatch container worlds, and whether the retained lane is live.
type dispatchPosture struct {
	workspace    tools.Workspace
	spillDir     string
	network      confinement.NetworkPolicy
	worlds       *dispatch.DispatchWorlds
	retainedLane bool
}

// wireDispatchPosture resolves the world a dispatch would actually get: the
// sub-agent workspace and spill directory, the sub-agent egress posture (which
// starts the session's broker), the per-dispatch container worlds, the retained
// lane, and the session-level world status every UI renders.
//
// The egress broker is a startup resource: it is assigned to st.egressBroker
// before the error return, so the guard stops it on a failed startup and the
// session stops it on close.
func wireDispatchPosture(st *loopInfraStage, cfg *config.Config, opts SessionOptions, ws *workspaceStage, retainCapable bool) (dispatchPosture, error) {
	var posture dispatchPosture

	// Isolated session (plan §1.3, decision D2): sub-agents inherit the
	// session's one sandbox, so the DelegateTool gets the same sandbox
	// workspace the main registry runs on — a sub-agent's file tools and
	// shell dispatch inside the container, never on the host. Nil in
	// local mode, where the path validator route below applies instead.
	// Sub-agents also get no host spill dir: a truncation hint pointing
	// at a host file is unreadable through their sandboxed read tool,
	// so the loop falls back to the bare truncation marker instead.
	if ws.sessionSandbox == nil {
		posture.spillDir = toolSpillDir()
	} else {
		posture.workspace = ws.sessionSandbox.Workspace()
	}

	// Egress (Plan 7 Phase 3): agents.subagent.network selects the confined
	// dispatch's posture. "broker" needs a live broker, so one is started for the
	// session and its port pinned — the same object the guard above stops on a
	// failed startup and the session stops on close.
	network, broker, nerr := wireSubAgentNetwork(cfg.Agent.SubAgent)
	if nerr != nil {
		return posture, nerr
	}
	posture.network = network
	st.egressBroker = broker

	// Per-dispatch container worlds (yaah-ihfh.3, yaah-a7ks): a Linux host with
	// a container image can give a REPORT dispatch its own sibling container.
	// In a host session the sibling copies the host repository in; in a
	// container session it is seeded from the session container's captured
	// state. In a container session both report and proposal lanes use a
	// sibling; a host session keeps the host-settled GitDelta path.
	if runtime.GOOS == "linux" && cfg.Workspace.Containerd.Image != "" {
		posture.worlds = &dispatch.DispatchWorlds{
			Config: cfg.Workspace,
			Opener: tools.OpenContainerdWorkspaceSandbox,
			Deny:   ws.deny,
		}
		if ws.sessionSandbox != nil {
			posture.worlds.SessionSandbox = ws.sessionSandbox
		}
	}
	containerSession := posture.worlds != nil && posture.worlds.SessionSandbox != nil
	posture.retainedLane = retainedLaneActive(cfg.Agent.SubAgent.Mode, retainCapable, containerSession)

	// Session-level posture for every UI (TUI info pane, REPL banner, web
	// header, ACP _meta). One derivation shared with doctor, computed after
	// the containment switch so the sandbox flag is final: the surfaces must
	// describe the world a dispatch would actually get.
	st.worldStatus = worldinfo.Derive(cfg, opts.WorkspaceRoot, ws.sessionSandbox != nil)

	return posture, nil
}

// buildDelegateTool constructs the delegate tool with the session's provider,
// supervision handles, workspace posture and sub-agent lane. It is not
// fallible: a store that would not open or a root scope that could not be
// created already disabled the pieces this tool would have carried.
func buildDelegateTool(st *loopInfraStage, cfg *config.Config, lc *sessionConfig, ws *workspaceStage, tr *toolRegistryStage, posture dispatchPosture, subs subAgentDefaults) {
	st.taskTool = runner.NewDelegateTool(runner.DelegateToolOpts{
		Provider:              lc.provider,
		ProviderName:          lc.providerName,
		SystemPrompt:          buildSubAgentBasePrompt(ws.cwd, tr.db),
		ModelName:             lc.modelName,
		DB:                    tr.db,
		SessionID:             st.sessionID,
		SubAgentProvider:      subs.provider,
		SubAgentModel:         subs.model,
		SubCfg:                cfg.Agent.SubAgent,
		RoleNames:             subagent.Roles(),
		OtelEnabled:           cfg.Observability.Otel.Enabled,
		OtelVerbose:           cfg.Observability.Otel.Verbose,
		Tracker:               st.tracker,
		EstimateFactor:        cfg.Agent.Default.EstimateFactor,
		SubContextWindow:      subs.contextWindow,
		OutputLimit:           cfg.Agent.SubAgent.OutputLimit,
		ProviderMap:           cfg.Providers,
		Defaults:              cfg.Agent.Default,
		PipelineDisabled:      cfg.Agent.Middleware.Disabled,
		ToolSpillDir:          posture.spillDir,
		PathValidator:         ws.pathValidator,
		Workspace:             posture.workspace,
		RootScopeID:           st.rootScopeID,
		Runs:                  st.runs,
		ScopeManager:          st.scopeMgr,
		ResumedScopes:         st.resumedScopes,
		TraceStore:            pipeline.TraceSinkOf(st.traceStore),
		GuardCheck:            supervisorSvcGuardCheck(st.supervisorSvc),
		ResolveProviderByName: resolveProviderByName,

		// Retained lane (Plan 7 Phase 2): default on in local mode, where a
		// sub-agent's work comes back as a proposal. A container session builds
		// the runtime too, for report-lane sibling worlds; its proposal lane is
		// routed shared below because there is no host worktree to settle into.
		// agents.subagent.mode: shared remains the opt-out.
		SubNetwork:           posture.network,
		Retain:               posture.retainedLane,
		RetainedRepoPath:     cfg.Agent.Default.RetainedRepoPath,
		RetainedWorktreeRoot: cfg.Agent.Default.RetainedWorktreeRoot,
		RetainedBootstrap:    cfg.Agent.Default.RetainedWorktreeBootstrap,
		DispatchWorlds:       posture.worlds,
	})

	// RoleResolver provides a live role-name lookup so the delegate
	// tool sees roles created via the role tool without a restart.
	st.taskTool.RoleResolver = func() []string { return subagent.DefaultRegistry().Names() }
}

// startScopeReaper starts the session's scope reaper, the defense-in-depth
// sweep behind the fork-site teardowns and the dispatch cap. The reaper is a
// startup resource the guard closes, so it is on st before it is started.
func startScopeReaper(st *loopInfraStage) {
	// Scope reaper (yaah-95ri.4): sweeps active dispatch scopes that no
	// longer have a bridged run or a pending retained proposal past the
	// grace period. Defense in depth behind the fork-site teardowns and
	// the dispatch cap; started with the session, stopped in the session
	// close.
	st.reaper = supervision.NewReaper(st.scopeMgr, st.runs, st.rootScopeID, 0)
	st.reaper.SetPendingSource(st.taskTool.LiveDispatchScopeIDs)
	// Resumed scopes (yaah-95ri.7): recovery re-admitted them from the
	// trace, nothing in this process runs under them, and their unbridged
	// clock predates the process — so the reaper's startup pass retires
	// them instead of granting a grace window that would leave the
	// dispatch cap holding dead work. Two exemptions survive the resumed
	// entry: a scope the pending source reports live (a rehydrated
	// proposal awaiting settlement), and a scope that carries a
	// re-adopted sandbox (yaah-dbik) — Discard destroys an owned sandbox,
	// and a worktree recovery just re-adopted is live work, not dead
	// weight.
	if len(st.resumedScopes) > 0 {
		ids := make([]string, 0, len(st.resumedScopes))
		for id := range st.resumedScopes {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		st.reaper.SetResumed(ids)
	}
	st.reaper.Start()
}

// wireLaneRouting installs the live role-contract lane lookup on the delegate
// tool, so every dispatch is routed by the role's declared output contract and
// a role created mid-session routes immediately.
func wireLaneRouting(st *loopInfraStage) {
	// LaneForRole routes each dispatch by the role's declared output
	// contract (yaah-7yf): report roles get their text back with the
	// worktree torn down (or run shared when they cannot mutate), proposal
	// roles keep the sealed-delta flow. The registry lookup is live, so
	// roles created via the role tool route immediately too.
	st.taskTool.LaneForRole = func(role string) tools.DelegateLane {
		switch subagent.DispatchLaneFor(subagent.SubAgentRole(role)) {
		case subagent.LaneRetainedReport:
			return tools.LaneReport
		case subagent.LaneShared:
			return tools.LaneShared
		default:
			return tools.LaneProposal
		}
	}
}

// wireBackgroundJobs hands the job manager to the delegate tool, delivers
// completed background results as follow-up messages, and registers the
// delegate and sub-agent-job tools.
func wireBackgroundJobs(st *loopInfraStage, tr *toolRegistryStage) {
	// Hand the manager to the task tool so background:true dispatches go
	// through it instead of the broken inline goroutine.
	st.taskTool.BackgroundJobs = st.backgroundJobs

	// Deliver completed background results as follow-up messages. This
	// is a BLOCKING send (guarded by the manager's Done channel) so
	// results are never dropped: a job whose result cannot be queued yet
	// blocks in its own goroutine until the next turn drains the channel
	// or the session closes.
	bgDone := st.backgroundJobs.Done()
	st.backgroundJobs.Deliver = func(job jobs.BackgroundJobStatus, result string, err error) {
		// One completion shape for every lane (yaah-was9.1): the background
		// result is delivered as the same envelope the foreground lanes
		// return, so the orchestrator never has to parse a prefixed-prose
		// follow-up out of model output.
		data, mErr := json.Marshal(dispatch.BackgroundCompletionEnvelope(job, result, err))
		if mErr != nil {
			data = []byte(fmt.Sprintf(`{"status":"error","dispatch_id":%q,"error":%q}`, job.ID, mErr.Error()))
		}
		select {
		case st.followupCh <- string(data):
		case <-bgDone:
		}
	}

	tr.reg.Register(st.taskTool)

	tr.reg.Register(&tools.SubAgentJobsTool{Jobs: st.backgroundJobs})
}

// registerWakeTools gates the wake pair to sessions that can service a
// wake (yaah-6mi9.2). A one-shot run is a single non-interactive turn:
// a watch armed in it has no later turn in this process to wake, and
// the recorded bench history shows zero wake calls — so the two
// schemas (~1.5K tokens of floor) bought a failed call at best. The
// gate is the memory-tool seams' pattern: refused-by-shape tools are
// named on the belt (an allowlist naming one is not a typo) and
// recorded in omittedTools (the prompt delta says "couldn't", not
// "didn't"). Interactive sessions — REPL, TUI, ACP, serve — keep full
// function: serve has no idle turn of its own, but its Start re-arms
// durable watches and delivers fires as follow-ups on the next
// prompt, so a wake there is serviceable.
func registerWakeTools(st *loopInfraStage, tr *toolRegistryStage, oneShot bool) {
	if oneShot {
		tr.belt.Know("wait_for", "watch")
		tr.omittedTools = append(tr.omittedTools, "wait_for", "watch")
		return
	}
	// Event-driven waiting: wait_for blocks inside the turn; watch arms a
	// predicate that outlives it and delivers a wake follow-up on fire.
	tr.reg.Register(&tools.WaitForTool{Manager: st.wakeMgr})
	tr.reg.Register(&tools.WatchTool{Manager: st.wakeMgr})
}

// registerSubAgentTools registers the tools that ride the infrastructure above:
// the supervisor and proposal tools when supervision came up, and
// list_sub_agents. A tool whose infrastructure stayed down is named on the belt
// instead, so an allowlist mentioning it is not reported as a typo.
func registerSubAgentTools(st *loopInfraStage, cfg *config.Config, tr *toolRegistryStage) {
	// Shepherd-dependent tool surface: the supervisor and proposal tools
	// register only when the infrastructure above came up.
	if st.scopeMgr != nil {
		// Supervisor tool for sub-agent supervision (list_scopes/inject/halt/status).
		tr.reg.Register(&tools.SupervisorTool{
			TraceDir:     cfg.Agent.Default.TracePath(),
			RootScopeID:  st.rootScopeID,
			ScopeManager: st.scopeMgr,
			Runs:         st.runs,
			// Svc carries the session's advisory rules service so
			// status can list rule firings (adoption plan B4); nil
			// when the supervisor stanza is not live.
			Svc: st.supervisorSvc,
			// supervisor.halt_root (adoption plan A3): root halt maps to
			// cancelling the session's current run — a big hammer that
			// needs the explicit opt-in, off by default. HaltRootEnabled
			// resolves the stanza over the legacy flat key.
			HaltRoot: cfg.Agent.Default.HaltRootEnabled(),
		})

		// Proposal tool (Plan 7 Phase 2): settles the sealed proposals the
		// retained lane produces. Registered only when that lane is live — a
		// shared lane or an isolated session produces none. The root-scope
		// check is a runtime guard, not a config gate: the store can fail to
		// open and the root scope can fail to be created independently of
		// the lane, and startRetainedRun refuses without a root scope.
		// taskTool.Retained is the single source of truth for "the retained
		// lane is live": it is non-nil only when the lane is enabled, the
		// scope manager initialized, and the root scope can capture. Gating
		// both the tool and the prompt on it keeps the advertised settlement
		// flow exactly in step with the registered capability.
		if st.taskTool.Retained != nil {
			// The orchestrator is the gate: it reads the proposal's diff and
			// settles it. There is no reviewer dispatch — the judgement lives on
			// this side of the boundary, as it does in shepherd's `gate(task,
			// predicate)`, so no verdict protocol or review round exists.
			//
			// One registry per session (yaah-yox): runner.NewDelegateTool
			// creates it with the retained runtime, and the dispatch records
			// a sealed proposal into it while the proposal tool settles it
			// back out. Passing that same registry here keeps the two halves
			// on one session, so under `web`/`acp-serve` a session can
			// neither see nor settle another session's proposals.
			// Pass the runtime too: continue/fork re-run a proposal through
			// the same session runner the dispatch used.
			tr.reg.Register(&dispatch.ProposalTool{
				Proposals: st.taskTool.Retained.Proposals,
				Runtime:   st.taskTool.Retained,
			})
		} else {
			tr.belt.Know("proposal")
		}
	} else {
		tr.belt.Know("supervisor")

	}

	tr.reg.Register(&tools.ListSubAgentsTool{
		Lister: func() []tools.SubAgentInfo {
			r := subagent.DefaultRegistry()
			if r == nil {
				return nil
			}
			defs := r.List()
			infos := make([]tools.SubAgentInfo, 0, len(defs))
			for name, def := range defs {
				desc := def.Description
				if desc == "" {
					desc = def.Body
					if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
						desc = desc[:idx]
					}
				}

				// Effective budget for a dispatch without per-call
				// overrides — resolved exactly like makeTaskRunner
				// resolves it, so the orchestrator sees real budgets
				// instead of guessing (subagent-turn-budget-floors §4.7).
				rc := cfg.Agent.SubAgent.Roles[string(name)]
				res := budget.Resolve(budget.Spec{
					RoleMaxIterations: def.MaxLoopCycles,
					RoleMinIterations: def.MinLoopCycles,
					RoleMaxTurns:      def.MaxToolTurns,
					RoleMinTurns:      def.MinToolTurns,
					CfgMaxIterations:  rc.MaxLoopCycles,
					CfgMinIterations:  rc.MinLoopCycles,
					CfgMaxTurns:       rc.MaxToolTurns,
					CfgMinTurns:       rc.MinToolTurns,
					DefaultTurns:      cfg.Agent.SubAgent.DefaultMaxToolTurns,
					DefaultMinTurns:   cfg.Agent.SubAgent.DefaultMinToolTurns,
					HardCeiling:       budget.SchemaMaxIterations,
				})

				infos = append(infos, tools.SubAgentInfo{
					Role:        string(name),
					DisplayName: def.DisplayName,
					Specialty:   def.Specialty,
					Contract: tools.SubAgentContract{
						Heading: def.Contract.Heading,
						Fields:  runner.ConvertContractFields(def.Contract.Fields),
					},
					Description: desc,
					Tools:       def.Tools,

					Iterations:    res.Iterations,
					Turns:         res.Turns,
					MinIterations: budget.FirstPositive(rc.MinLoopCycles, def.MinLoopCycles),
					MinTurns:      budget.FirstPositive(rc.MinToolTurns, def.MinToolTurns, cfg.Agent.SubAgent.DefaultMinToolTurns),
				})
			}
			return infos
		},
	})
}

// assemble seals the registry against the workspace, finishes the prompts,
// records the session, and builds the agentSession. It owns the last fallible
// step of a session build — the SetWorkspace gate — so every error it can
// return is a startup failure the caller's guards must release. It sequences
// the concern split (yaah-mp04) rather than performing it: isolated-workspace
// enforcement, prompt assembly, the D4 session-record write, the embedding
// backfill launch, session construction, and hook wiring (wiring_assemble.go).
// The ordering is a contract — the D4 write must follow the SetWorkspace gate,
// and the backfill must launch only after the last fallible step — so it stays
// visible here rather than hidden inside any one concern.
func assemble(lc *sessionConfig, opts SessionOptions, ws *workspaceStage, tr *toolRegistryStage, mcps *mcpSkillsStage, loop *loopInfraStage) (*agentSession, error) {
	if err := enforceIsolatedWorkspace(ws, tr); err != nil {
		return nil, err
	}
	// The belt's refusal log is complete now: every tool the session was
	// configured to have but does not. It feeds both the prompt (the model
	// is told which are missing) and the session record (D4).
	beltRemoved := tr.belt.Refused()
	bare := loop.mainPrompt
	mainPrompt := appendToolGuidance(opts, ws, tr, mcps, loop, beltRemoved)
	// Retain everything appendToolGuidance appended so a mid-session
	// persona switch can rebind the prompt body without losing the
	// tool-guidance blocks (delegate/retained/belt/isolated/one-shot).
	guidance := ""
	if len(mainPrompt) > len(bare) {
		guidance = mainPrompt[len(bare):]
	}
	// A session that STARTS under a narrowed persona needs the note a switch
	// appends (personaBeltNote): the guidance names only the configured
	// belt's removals, while the persona's allowlist is applied at the
	// per-run seam, so without it the construction-time prompt would keep
	// advertising the tools that seam withholds. It sits BEFORE the guidance
	// — the order rebuildPromptsLocked uses — so the retained suffix stays a
	// faithful tail of the prompt.
	if note := personaBeltNote(lc.persona.Persona); note != "" {
		mainPrompt = bare + note + guidance
	}
	recordSessionWorkspace(lc, tr, mcps, loop, beltRemoved)
	launchEmbeddingBackfill(tr)
	sess := newSessionFromStages(lc, opts, ws, tr, mcps, loop, mainPrompt, guidance)
	bindToolbeltLiveAdmits(sess, tr)
	wireSessionHooks(lc, opts, ws, loop, sess)
	return sess, nil
}
