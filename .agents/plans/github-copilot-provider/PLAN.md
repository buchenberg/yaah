---
name: github-copilot-provider
description: Add GitHub Copilot as a recognized LLM provider in yaah
status: approved
---

## Summary
GitHub Copilot's Chat API is OpenAI-compatible — same `/chat/completions` endpoint, same `Bearer` auth, same SSE streaming. Most of the work is just recognizing `"copilot"` as an API type in config validation.

## Files to change

### 1. `internal/config/load.go`
- Add `"copilot"` to the recognized API values in the validation block
- Add a default base URL constant for Copilot

### 2. `cmd/yaah/provider_resolve.go`  
- Add `case "copilot"` branch (routes to OpenAIClient, same as default, but explicit for clarity and future hooks)

### 3. `docs/configuration.md`
- Document the new `copilot` provider type with example config

## What does NOT need to change
- `internal/providers/providers.go` — OpenAIClient already handles OpenAI-compatible APIs
- `internal/providers/wire.go` — wire format is identical
- `internal/providers/stream.go` — SSE streaming is identical
- Auth — Copilot uses `Authorization: Bearer <github_token>`, already supported

## Config example
```yaml
providers:
  - name: copilot
    api: copilot
    base_url: https://api.githubcopilot.com
    api_key: ${GITHUB_TOKEN}
    models:
      - gpt-4o
      - claude-sonnet-4
```
