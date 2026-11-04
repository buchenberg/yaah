---
name: tui-infopane-world-restructure
description: TUI info-pane restructure: dissolve World section, add Subagent Context
status: draft
---

# TUI info-pane restructure: dissolve the World section, add Subagent Context

Planning only — no code changes yet.

## Research findings (current state)

- The World section is rendered by `worldinfo.Format` (`internal/tui/components/worldinfo/worldinfo.go`) from `worldinfo.Status` (`internal/worldinfo/status.go`), derived once in `cmd/yaah/wiring.go:825` (`worldinfo.Derive(cfg, opts.WorkspaceRoot, sessionSandbox != nil)`) and pushed via `app.SetWorld(sess.WorldStatus())` (`cmd/yaah/tui.go:116`).
- **"Dispatch: container → container" explained:** the line is `WorldReq + " → " + ProposalWorld` (plus `· proposal X · report Y` when the proposal/report lanes differ). `WorldReq` is what the config asked for (`world: container`), `ProposalWorld` is what a dispatch actually gets. When request == resolution the arrow form is noise — it only carries information on mismatch/degradation (which already has its own `Degraded` line) or when the two lanes differ.
- **Version** is the build-time `version` var (`cmd/yaah/root.go:18`, ldflags / `debug.ReadBuildInfo` fallback), passed to `tui.New(version, ...)`, and currently shown as a `Version:` row inside the Session section (`sessioninfo.go`).
- **Sub-agent usage already exists but never reaches the TUI:** `agentSession.subUsage types.Usage` (`cmd/yaah/session.go:234`) attributes the sub-agent share of `totalUsage` (blocking via the loop's `OnSubUsage`, background via `onReportUsage`). `SubAgentStart/End` events feed the Subagents pane (`proxy.go`, `blocks.go`); there is no `control.Msg` carrying sub-usage. Sub-agents also have their own context window (`SubContextWindow`/`subCW`, `wiring.go:841`).
- Sections today (order): Session, Agent, Context, World, Usage, MCP, Sub-agents, Embedding, Middleware (`infopane.go:45-58`).
- Shared surfaces that use the same `worldinfo.Status`: REPL banner (`worldBannerLines`, `repl_loop.go:30`) and web header (`worldHeaderOf`, `web_view.go:215`, documented in `docs/web-ui.md`). **This plan is TUI-only**; those keep working untouched. Optional parity follow-up noted below.

## Target layout

```
yaah v0.125.0                              <- new header (bold), replaces Session.Version row
  session abc123 · yaah · started 14:02    <- dim, folds Session section (ID, dir, Started)

Agent
  Provider   deepseek
  Model      deepseek-chat
  Status     Ready
  Workspace  containerd · isolated        <- from World.Backend (World's "Workspace" key)
  Scope      /workspace                    <- from World.Root ("unrestricted host" when empty)

Agent Context                             <- renamed "Context" section
  <context line + notes, unchanged>

Sub-agents
  Enabled / Provider / Model / Concurrency / withScope   (existing rows)
  Mode         retained · dispatch container   <- World.Mode + collapsed Dispatch
  Containment  landlock · strict               <- World rows
  Jail         (root) / not in use
  Network      deny_all · broker proxy

Subagent Context                          <- NEW
  Usage   12,345 prompt (9,876 cached) · 2,345 completion   <- from subUsage
  Window  200,000 tokens                                    <- subCW
  (optional) Dispatches 3 · 1 active                        <- SubAgents pane state

Usage, MCP, Embedding, Middleware         <- tail group, unchanged content
```

Decisions made (defaults where the request was ambiguous):
- **Image row: removed** from the pane. Keep the `Image` field in `worldinfo.Status` (web/doctor still use it) — just stop rendering it in the TUI.
- **Dispatch label collapses** to the resolved world: `container`, `worktree`, or `shared`. When proposal/report lanes differ: `proposal container · report worktree`. `WorldReq` is dropped from the line except where the `Degraded` reason already names it ("requested container, got worktree: …").
- **Scope** is `World.Root` renamed: `/workspace`, or `unrestricted host` for local sessions (same wording the REPL banner uses).
- **Session section disappears** as a section; its rows move to the dim header line. (Alternative if you prefer: keep ID/Started as a row inside Agent.)
- **Subagent Context tracks sub-agent spend separately** (subUsage prompt/completion/cached + sub context window) — the "track that separately" counterpart to Agent Context. The existing `Usage` section keeps showing session totals and stays in the tail. (Alternative: split Usage into agent-only vs sub-agent spend under the two context sections; more invasive, changes numbers people may compare across runs.)
- Version string normalization: render `yaah v0.125.0` — prefix `v` if absent; if the fallback build info yields `(devel)`, render `yaah (devel)`.

## Implementation steps

1. **worldinfo formatting (TUI)** — `internal/tui/components/worldinfo/worldinfo.go`:
   - Rework/dissolve `Format`: no `Image` line; `Dispatch` → collapsed form per above; `Root` rendered as `Scope` with `unrestricted host` wording. Consider splitting `Format` into `AgentRows(Status)` + `SubAgentRows(Status)` consumed by the two sections instead of one monolithic block.
2. **infopane sections** — `internal/tui/components/infopane/infopane.go`:
   - New order: header line(s), Agent, Agent Context, Sub-agents, Subagent Context, Usage, MCP, Embedding, Middleware.
   - New `Subagent Context` section; `Format` signature grows to carry sub-usage + sub window (new small struct, e.g. `SubContext{Usage types.Usage, Window int}`).
3. **Header + session fold** — `internal/tui/components/sessioninfo/sessioninfo.go` (or inline in infopane):
   - `yaah v…` bold header built from `t.version` (normalize `v` prefix); drop the `Version` row; fold `ID`/`Workspace`/`Started` into the dim second line; remove the standalone Session section from `renderInfoPane`.
4. **Sub-usage plumbing** — `internal/control/` + `cmd/yaah/`:
   - New `control.SubUsageInfo{Prompt, Completion, Cached int, Window int}` message; `App.SetSubUsage(...)` in `internal/tui/` (mirrors `SetContext`/`SetUsage`, calls `renderInfoPane`).
   - Emit from `agentSession` where `subUsage` is folded (`cmd/yaah/session.go` OnSubUsage hook ~L391 and the background `onReportUsage` path in `wiring.go:1213`) over the existing `ctrlCh`.
5. **Docs** — `docs/features.md` (info-pane description), `docs/tui-components.md` (infopane section list). `docs/web-ui.md` untouched (web keeps `wireWorldMeta` as is).
6. **Tests** (all red-first where practical):
   - `worldinfo_test.go`: dispatch collapse cases (equal ⇒ single word; lanes differ ⇒ `proposal X · report Y`; degraded line preserved), `unrestricted host` scope wording, no `image` in output.
   - `infopane_test.go`: exact new section order + presence of `Subagent Context`; header line `yaah v…`.
   - `sessioninfo_test.go`: Version row gone; folded session line.
   - New: SetSubUsage → pane shows the numbers (follow existing `SetContext` test pattern).
   - Gate: `go test ./...`, `go vet ./...`, `gofmt -l .` empty, `go run honnef.co/go/tools/cmd/staticcheck@latest ./...`.

## Out of scope / follow-ups (flag, don't do)

- REPL banner (`worldBannerLines`) and web header (`worldHeaderOf`) keep the `world: auto → worktree` arrow wording; a parity pass can adopt the collapsed dispatch wording later (would touch `cmd/yaah/world_ui_test.go`, `web_view_test.go`, `docs/web-ui.md`).
- No change to `worldinfo.Derive`, the derivation in `wiring.go`, or doctor output.

## Open points (defaults chosen, easy to flip)

1. Session ID/Started → dim header line (chosen) vs. rows inside Agent.
2. `Usage` section stays as session totals in the tail (chosen) vs. splitting agent/sub spend across the two context sections.
3. `Dispatches n · m active` row in Subagent Context (optional; the Subagents pane already shows active ones).

