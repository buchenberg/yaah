package yaah

import (
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
)

// The Memory Guidelines bullets, split by which tools they name. A session
// that holds only the readers must not be shown the writer bullets, or the
// prompt advertises tools absent from the request.
var (
	memoryRecallBullets = []string{
		`- Use memory_search to find relevant memories before answering personal/project questions. It uses semantic vector search when an embedding model is configured (understands paraphrases and meaning, not just keywords) and falls back to keyword full-text matching when semantic recall is unavailable, fails, or returns no candidates — each result names the path that ran. A tag filters by category but selects keyword-only matching and withdraws semantic recall, so omit the tag when searching by paraphrase; an empty query lists the most recent entries instead of searching. Results default to cross-session facts; pass scope "session" (or "all") to also reach atoms distilled from earlier conversations.`,
		`- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.`,
	}
	memoryWriteBullets = []string{
		`- Use memory_add to save important facts. Always include a tags array (e.g., ["user_info"], ["preferences"], ["project:yaah"], ["decision"]).`,
		`- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.`,
		`- To correct a fact whose *claim* changed rather than its wording, prefer memory_add with supersedes set to the old atom's ID: the old atom is kept for audit, marked superseded, and drops out of recall.`,
		`- Use memory_promote to move a session atom into the global tier once it proves to outlive the conversation it came from.`,
		`- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag ["session_summary"].`,
	}
	// Names no tool and claims no reason. Writers can be absent because the
	// session is one-shot, or because an agents.default.tools allowlist
	// refused one at registration — the bullet has to be true in both, so it
	// states the boundary without asserting which caused it.
	memoryRecallOnlyBullet = `- This session can recall memories but cannot save any: no memory write tools are registered. Do not attempt to persist findings.`
)

// memoryGuidanceBlock renders the Memory Guidelines section for what the
// session actually registered. Empty when it registered nothing, or when the
// session is a resume (the guidelines were already in the original prompt).
func memoryGuidanceBlock(g prompts.MemoryGuidance, resumeSessionID string) string {
	if resumeSessionID != "" || (!g.Recall && !g.Writers) {
		return ""
	}
	var bullets []string
	if g.Recall {
		bullets = append(bullets, memoryRecallBullets...)
	}
	switch {
	case g.Writers:
		bullets = append(bullets, memoryWriteBullets...)
	case g.Recall:
		bullets = append(bullets, memoryRecallOnlyBullet)
	}
	return "\n## Memory Guidelines\n" + strings.Join(bullets, "\n")
}

// storeHasRecall reports whether db holds anything a read-only memory tool
// could return: a live memory entry, or a past session message
// (memory_search_sessions searches the messages table, not the memory one).
//
// An empty store makes the readers dead weight, and that is exactly the state
// a fresh YAAH_HOME is in — CI, benchmarks, a throwaway container. Gating on
// it keeps the full schema saving there while preserving recall for a one-shot
// run against a populated store (yaah-zx18).
//
// Messages are counted, not session rows. Wiring creates this session's own
// row later on, with no one-shot guard, so counting rows would make the probe
// permanently true for any YAAH_HOME that had ever been used — and a row
// written by a run that produced no messages recalls nothing, because
// memory_search_sessions skips message-less sessions. Counting messages also
// keeps this safe to call before the session's own row exists.
func storeHasRecall(db *memory.DB) bool {
	if db == nil {
		return false
	}
	if n, err := db.CountMemories(memory.ScopeAll); err == nil && n > 0 {
		return true
	}
	n, err := db.CountMessages()
	return err == nil && n > 0
}

// buildSystemPrompt assembles the main-session base system prompt from
// config, environment, instructions, and memory. When db is non-nil,
// stored memories are loaded into the prompt layers. Memory guidelines
// are appended for fresh sessions (resumeSessionID == "") only, and only
// for the tools mem says are actually registered — the caller derives it
// from the registry, so the prompt can never advertise a tool that is not
// in the request.
//
// The returned systemPrompt is the main-session base prompt (clean of
// top-level directives). The caller derives mainPrompt by injecting
// directives. Sub-agents get their own lean base from
// buildSubAgentBasePrompt instead of this one.
func buildSystemPrompt(cfg *config.Config, cwd string, db *memory.DB, resumeSessionID string, mem prompts.MemoryGuidance) string {
	layers := prompts.Layers{
		Identity:               prompts.IdentityPrompt,
		Environment:            prompts.DetectEnvironment(cwd),
		SubAgentDispatch:       subAgentDispatchLine(cfg),
		UserContext:            prompts.LoadUserContext(config.HomeDir()),
		Project:                instructions.FormatForSystem(instructions.Load(cwd, instructions.WorktreeRoot(cwd))),
		MaxSubAgentConcurrency: cfg.Agent.SubAgent.MaxConcurrency,
		Memory:                 memoryLayer(db),
	}

	return prompts.Build(layers) + memoryGuidanceBlock(mem, resumeSessionID)
}

// subAgentDispatchLine describes the resolved sub-agent dispatch defaults
// in the orchestrator's environment block. Without it, any sub-agent
// provider failure sends the orchestrator to stale memory or endpoint
// probing to discover which provider actually served the dispatch.
func subAgentDispatchLine(cfg *config.Config) string {
	sc := cfg.Agent.SubAgent
	provider := sc.Provider
	// A configured-but-unknown or unusable name is ignored at dispatch
	// (resolveSubAgent returns nil and sub-agents inherit the planner),
	// so report what will actually run rather than the dangling name.
	modelOverrideHonored := true
	if provider == "" {
		provider = resolveProviderName(cfg)
	} else if !providerUsable(cfg, provider) {
		provider = resolveProviderName(cfg)
		modelOverrideHonored = false
	}
	model := sc.Model
	if model == "" || !modelOverrideHonored {
		model = resolveModel(cfg)
	}
	if model == "" {
		model = "(unset)"
	}
	line := fmt.Sprintf("Sub-agent dispatch: provider=%s model=%s", provider, model)
	if sc.MaxConcurrency > 0 {
		line += fmt.Sprintf(" max_concurrency=%d", sc.MaxConcurrency)
	}
	if sc.DefaultTimeout > 0 {
		line += fmt.Sprintf(" default_timeout=%ds", sc.DefaultTimeout)
	}
	if p, ok := cfg.Providers[provider]; ok && p.MaxTokens > 0 {
		line += fmt.Sprintf(" max_tokens=%d", p.MaxTokens)
	}
	return line + ". Per-role overrides may change provider or model."
}

// providerUsable mirrors makeProvider's cheap gate: a provider entry can
// actually be constructed when it has OAuth, a real API key, or a base URL.
// This keeps the dispatch line from advertising a dangling or keyless entry.
func providerUsable(cfg *config.Config, name string) bool {
	p, ok := cfg.Providers[name]
	if !ok {
		return false
	}
	r := config.Resolve(p)
	return r.Auth == "oauth" || isRealKey(r.APIKey) || r.BaseURL != ""
}

// buildMainPrompt derives the top-level agent prompt from the system prompt
// by injecting session directives after the identity block. The systemPrompt
// stays clean so child sub-agent prompts never inherit top-level directives.
//
// The old tool quick-reference card is gone (yaah-w8x): it re-rendered every
// registered tool's name, signature, and description that the request's
// tools array already carries — ~3.4K tokens of pure duplication per call.
// The schemas are the single source of tool truth on the wire.
func buildMainPrompt(cfg *config.Config, opts SessionOptions, systemPrompt string) string {
	return prompts.InjectAfterIdentity(systemPrompt, resolveDirectives(cfg, opts))
}

// buildSubAgentBasePrompt assembles the lean base prompt handed to every
// dispatched sub-agent: the sub-agent identity, user context, project
// instructions, and stored memories. It deliberately omits the main
// identity (sub-agent orchestration guidance the sub-agent cannot act
// on), the sub-agent concurrency limit, and memory tool guidelines
// (sub-agent roles carry no memory tools). The runner prepends the
// environment header and appends role guidance, contract, and escalation
// rules on top of this base.
func buildSubAgentBasePrompt(cwd string, db *memory.DB) string {
	layers := prompts.Layers{
		Identity:    prompts.SubAgentIdentityPrompt,
		UserContext: prompts.LoadUserContext(config.HomeDir()),
		Project:     instructions.FormatForSystem(instructions.Load(cwd, instructions.WorktreeRoot(cwd))),
		Memory:      memoryLayer(db),
	}
	return prompts.Build(layers)
}

// memoryLayer loads stored memories (excluding user_info entries) from db
// into a prompt layer. Returns "" when db is nil or nothing is loadable.
func memoryLayer(db *memory.DB) string {
	if db == nil {
		return ""
	}
	entries, err := db.ListMemory(50)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var memLines []string
	for _, entry := range entries {
		if strings.Contains(entry.Tags, `"user_info"`) {
			continue
		}
		memLines = append(memLines, "- "+entry.Text)
	}
	if len(memLines) == 0 {
		return ""
	}
	return "You have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
}
