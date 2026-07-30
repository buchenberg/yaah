# Prompt Extraction Plan

Move all hardcoded prompt text out of Go source files and into `internal/prompts/` as embedded `.md` files.

## Already done

| Asset | Location |
|-------|----------|
| Main identity prompt | `internal/prompts/identity.md` |
| Sub-agent role definitions (7) | `internal/prompts/roles/*.md` |

These are already embedded (`//go:embed`) and loaded at startup. No change needed.

---

## Extraction plan (priority order)

### 1. Anchored summary template (~37 lines)

**Source:** `internal/agent/agent_context.go:44-79` — `const summaryTemplate`

**Create:** `internal/prompts/summary_template.md`

```
Output exactly the Markdown structure shown inside <template>...
## Goal / ## Constraints / ## Progress / ## Key Decisions / ...
```

**Affected Go files:**
- `internal/agent/agent_context.go` — delete `summaryTemplate` const, add `//go:embed internal/prompts/summary_template.md` (or read from prompts package)
- `internal/agent/agent_chunked.go:97` — references `summaryTemplate`; update to use embedded version
- `internal/prompts/prompts.go` — add `summary_template.md` to the existing `//go:embed` directive

**Effort:** Trivial. Pure text, no interpolation. Biggest single win.

---

### 2. Contract rules & escalation block (~35 lines)

**Source:** `cmd/yaah/subagent_runner.go:220-263` — contract rules + escalation format injected into sub-agent system prompts

**Create:** `internal/prompts/subagent_contract.md`

```
## Contract Rules
Evidence fields (raw tool output...) ...
Interpretation fields (your synthesis...) ...

## Escalation
If you encounter a blocker...
```escalation
{"severity":"blocker|critical|warning|info",...}
```

**Affected Go files:**
- `cmd/yaah/subagent_runner.go:220-263` — replace `b.WriteString(...)` chain + escalation `sysPrompt += ...` with `prompts.SubAgentContract() string`
- `internal/prompts/prompts.go` — add `subagent_contract.md` to the embed directive, expose a loader function

**Effort:** Low. Pure text with only one minor interpolation point (the `jsonMode` boolean for the JSON contract variant — keep that conditional in Go, load the static text from `.md`).

---

### 3. Chunk summarization preamble (~8 lines)

**Source:** `internal/agent/agent_chunked.go:82-98` — `"Summarize chunk %d/%d..."` + `"You are a chunk summarizer..."`

**Create:** `internal/prompts/chunk_summarizer.md`

```
Summarize chunk {{chunkIdx}}/{{total}} of a longer conversation.

<conversation>
...
</conversation>
```

And a separate system message:

```
You are a chunk summarizer. Extract the key facts from this conversation segment.
```

**Affected Go files:**
- `internal/agent/agent_chunked.go:82-98` — replace inline `sb.WriteString` with `prompts.ChunkSummaryPreamble(chunkIdx, total)` and `prompts.ChunkSummarizerRole()`
- `internal/prompts/prompts.go` — embed the `.md`, expose two loader functions

**Effort:** Low. Two minor `fmt.Sprintf` interpolations (`%d/%d`).

---

### 4. Conversation summarization preamble (~8 lines)

**Source:** `cmd/yaah/agent_frame.go:574-581` — `"Summarize the following conversation excerpt. Keep the structured format below.\n\n## Goal\n## Completed Work\n..."`

**Create:** `internal/prompts/conversation_summary_preamble.md`

```
Summarize the following conversation excerpt. Keep the structured format below.

## Goal
## Completed Work
## Active Work
## Pending Tasks
## Key Decisions
## Files Modified
```

**Affected Go files:**
- `cmd/yaah/agent_frame.go:574-581` — replace inline `sb.WriteString` with `prompts.ConversationSummaryPreamble()`
- `internal/prompts/prompts.go` — embed, expose loader

**Effort:** Trivial. Pure text.

---

### 5. Loop detection steering message (~4 lines)

**Source:** `internal/agent/pipeline/loopdetect.go:135-138` — `"[STEER] You have called the %q tool..."`

**Create:** `internal/prompts/steering_message.md`

```
[STEER] You have called the {{toolName}} tool {{count}} times. This may indicate a loop. Try a different approach or ask the user for guidance.
```

**Affected Go files:**
- `internal/agent/pipeline/loopdetect.go:135-138` — replace `fmt.Sprintf` with `prompts.SteeringMessage(toolName, count)`
- `internal/prompts/prompts.go` — embed, expose loader with `strings.Replace`

**Effort:** Trivial. Two interpolations. Smallest extraction but completes the picture.

---

### 6. Environment header (~4 lines)

**Source:** `cmd/yaah/subagent_runner.go:169-178` — `subagentEnvironmentHeader()` — `"## Environment\nOS: %s/%s. Default shell: %s..."`

**Create:** `internal/prompts/environment_header.md`

```
## Environment
OS: {{os}}/{{arch}}. Default shell: {{shell}}. Use {{shell}} for all shell commands. Working directory: {{cwd}}.
```

**Affected Go files:**
- `cmd/yaah/subagent_runner.go:169-178` — `subagentEnvironmentHeader()` loads from prompts package, applies substitutions
- `internal/prompts/prompts.go` — embed, expose `EnvironmentHeader(os, arch, shell, cwd) string`

**Effort:** Low. Four runtime interpolations. Debatable whether this is worth extracting (it's mostly template text, not prompt content), but included for completeness.

---

## Implementation approach

Add all new `.md` files to the existing `//go:embed` directive in `internal/prompts/prompts.go`, alongside `identity.md` and `roles/*.md`. Expose one accessor function per template. For templates with interpolation, use `strings.Replace` with `{{placeholder}}` syntax (no template engine needed — all interpolations are simple key-value substitutions).

### Go changes (summary)

| File | Change |
|------|--------|
| `internal/prompts/prompts.go` | Add 6 `.md` files to embed; add 6-7 accessor functions |
| `internal/agent/agent_context.go` | Delete `summaryTemplate` const (30 lines); import from `prompts` |
| `internal/agent/agent_chunked.go` | Replace 8 inline prompt lines with `prompts.*` calls |
| `internal/agent/pipeline/loopdetect.go` | Replace 4 inline prompt lines with `prompts.SteeringMessage()` |
| `cmd/yaah/agent_frame.go` | Replace 8 inline prompt lines with `prompts.ConversationSummaryPreamble()` |
| `cmd/yaah/subagent_runner.go` | Replace ~35 inline prompt lines with `prompts.SubAgentContract()` + `prompts.EnvironmentHeader()` |

### New files to create

```
internal/prompts/
├── summary_template.md
├── subagent_contract.md
├── chunk_summarizer.md
├── conversation_summary_preamble.md
├── steering_message.md
└── environment_header.md
```

### Net diff

- **~95 lines of Go prompt text deleted** across 5 source files
- **~6 new .md files** (~95 lines of markdown)
- **~7 new accessor functions** in `prompts/prompts.go` (~30 lines of Go)
- **Net:** roughly neutral on line count, significantly better separation of concerns
