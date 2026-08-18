package yaah

import (
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
)

// buildSystemPrompt assembles the system prompt from config, environment,
// instructions, and memory. When db is non-nil, stored memories are loaded
// into the prompt layers. Memory guidelines are appended for fresh sessions
// (resumeSessionID == "") only.
//
// The returned systemPrompt is the sub-agent base prompt (clean of
// top-level directives). The caller derives mainPrompt by injecting
// directives and the tool quick-reference card.
func buildSystemPrompt(cfg *config.Config, cwd string, db *memory.DB, resumeSessionID string) string {
	layers := prompts.Layers{
		Identity:               prompts.IdentityPrompt,
		Environment:            prompts.DetectEnvironment(cwd),
		UserContext:            prompts.LoadUserContext(config.HomeDir()),
		Project:                instructions.FormatForSystem(instructions.Load(cwd, cwd)),
		MaxSubAgentConcurrency: cfg.Agent.SubAgent.MaxConcurrency,
	}

	if db != nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				if strings.Contains(entry.Tags, `"user_info"`) {
					continue
				}
				memLines = append(memLines, "- "+entry.Text)
			}
			if len(memLines) > 0 {
				layers.Memory = "You have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
			}
		}
	}

	systemPrompt := prompts.Build(layers)
	if db != nil && resumeSessionID == "" {
		systemPrompt += `
## Memory Guidelines
- Use memory_search to find relevant memories before answering personal/project questions. This uses semantic vector search (understands paraphrases and meaning, not just keywords). Pass a tag to filter by category.
- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.
- Use memory_add to save important facts. Always include a tags array (e.g., ["user_info"], ["preferences"], ["project:yaah"], ["decision"]).
- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.
- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag ["session_summary"].`
	}

	return systemPrompt
}

// buildMainPrompt derives the top-level agent prompt from the system prompt
// by injecting session directives after the identity block and appending the
// tool quick-reference card. The systemPrompt stays clean so child sub-agent
// prompts never inherit top-level directives.
func buildMainPrompt(cfg *config.Config, systemPrompt string, toolReg *tools.Registry) string {
	mainPrompt := prompts.InjectAfterIdentity(systemPrompt, resolveDirectives(cfg))
	if quickRef := buildToolQuickRef(toolReg); quickRef != "" {
		mainPrompt += "\n\n" + quickRef
	}
	return mainPrompt
}
