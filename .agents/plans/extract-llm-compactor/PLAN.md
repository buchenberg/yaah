---
name: extract-llm-compactor
description: Extract LLM compaction logic from Loop into a standalone LLMCompactor struct in the pipeline package
status: draft
---


## Goal
Extract the LLM summarization logic currently embedded in `Loop` (`compactContext`, `summarizeChunk`, `trimContext`, `applyCompactedSummary`) into a standalone `LLMCompactor` struct that implements the existing `Compactor` interface.

## Steps

1. **Analyze all compaction code** — Read every method on Loop related to compaction, all call sites, all dependencies
2. **Create LLMCompactor struct** in `internal/agent/pipeline/llm_compactor.go` — standalone struct with `*llm.Client`, provider, model, cost model, summary max tokens
3. **Move Compact, TrimContext, SummarizeChunk** from Loop to LLMCompactor
4. **Update Loop** — remove the methods, add an `llmCompactor *LLMCompactor` field, delegate `Compact()` to it
5. **Update all call sites** — pipeline.NewFromConfig, Loop creation, sub-agent runner
6. **Run tests** — verify nothing breaks
7. **Run staticcheck + vet** — ensure clean

