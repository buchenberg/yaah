# Changelog

## [0.45.4](https://github.com/buchenberg/yaah/compare/v0.45.3...v0.45.4) (2026-08-04)


### Code Refactoring

* **agent:** split agent.go into focused files ([#134](https://github.com/buchenberg/yaah/issues/134)) ([5e63d37](https://github.com/buchenberg/yaah/commit/5e63d373bcc6bf18b19983ef2cef82fa414c2a31))

## [0.45.3](https://github.com/buchenberg/yaah/compare/v0.45.2...v0.45.3) (2026-08-04)


### Code Refactoring

* complete Loop struct decomposition (P0 [#1](https://github.com/buchenberg/yaah/issues/1)-[#2](https://github.com/buchenberg/yaah/issues/2), P1 [#4](https://github.com/buchenberg/yaah/issues/4), P1 [#6](https://github.com/buchenberg/yaah/issues/6)) ([#131](https://github.com/buchenberg/yaah/issues/131)) ([b26e9e9](https://github.com/buchenberg/yaah/commit/b26e9e97b66ba7691138ade7074453ce856d1522))

## [0.45.2](https://github.com/buchenberg/yaah/compare/v0.45.1...v0.45.2) (2026-08-03)


### Bug Fixes

* **tui:** restore inline todo table rendering from tool args ([#129](https://github.com/buchenberg/yaah/issues/129)) ([d66e312](https://github.com/buchenberg/yaah/commit/d66e312390923615366cbdbcd2e59aa87a053f3d))

## [0.45.1](https://github.com/buchenberg/yaah/compare/v0.45.0...v0.45.1) (2026-08-03)


### Bug Fixes

* use PowerShell helper for self-update on Windows ([#126](https://github.com/buchenberg/yaah/issues/126)) ([83f8ebb](https://github.com/buchenberg/yaah/commit/83f8ebb4c33847eada03dd51d6142511745378a3))

## [0.45.0](https://github.com/buchenberg/yaah/compare/v0.44.0...v0.45.0) (2026-08-03)


### Features

* Copilot provider, OAuth device flow, compaction fixes, sub-agent refactor, and TUI improvements ([#124](https://github.com/buchenberg/yaah/issues/124)) ([e1271e4](https://github.com/buchenberg/yaah/commit/e1271e489989f10b12900a3e740b53f869ca07bd))

## [0.44.0](https://github.com/buchenberg/yaah/compare/v0.43.0...v0.44.0) (2026-07-31)


### Features

* GitHub Copilot provider with OAuth device flow ([#122](https://github.com/buchenberg/yaah/issues/122)) ([830d36b](https://github.com/buchenberg/yaah/commit/830d36b5f81fd1a3b85f0b07a0f7817e223b9ff3))

## [0.43.0](https://github.com/buchenberg/yaah/compare/v0.42.0...v0.43.0) (2026-07-31)


### Features

* add Serina project config + finalize TUI MCP status line removal ([#120](https://github.com/buchenberg/yaah/issues/120)) ([c14d2cf](https://github.com/buchenberg/yaah/commit/c14d2cf4ed05b64bf7f594e8c642279e021bb258))

## [0.42.0](https://github.com/buchenberg/yaah/compare/v0.41.0...v0.42.0) (2026-07-31)


### Features

* web UI session selection, SigNoz observability, 5 new tools, bash Windows fix ([#116](https://github.com/buchenberg/yaah/issues/116)) ([167a35d](https://github.com/buchenberg/yaah/commit/167a35d4d3153164b5cd9fdc7a2daa05777542ee))


### Bug Fixes

* **pubsub:** eliminate race in TestBroker_DefaultBufferSize ([#118](https://github.com/buchenberg/yaah/issues/118)) ([c045984](https://github.com/buchenberg/yaah/commit/c04598465d5cc532e8fdc3af39ee17f774f23ad8))

## [0.41.0](https://github.com/buchenberg/yaah/compare/v0.40.3...v0.41.0) (2026-07-31)


### Features

* web UI command palette + sub-agent orchestration hardening ([#114](https://github.com/buchenberg/yaah/issues/114)) ([3881467](https://github.com/buchenberg/yaah/commit/3881467f1c7ad94365f4e7d1931682f8a78752e6))

## [0.40.3](https://github.com/buchenberg/yaah/compare/v0.40.2...v0.40.3) (2026-07-30)


### Performance Improvements

* promote batching to cardinal rule, halve raw compaction threshold, edit CRLF fix, web UI improvements ([#113](https://github.com/buchenberg/yaah/issues/113)) ([0d0b7df](https://github.com/buchenberg/yaah/commit/0d0b7dfd58dbbd9ca4c20e0c57660dbeb767b397))


### Code Refactoring

* clean messaging layer, fix DeepSeek reasoning_content 400, raise sub-agent defaults ([#111](https://github.com/buchenberg/yaah/issues/111)) ([78b1c08](https://github.com/buchenberg/yaah/commit/78b1c08096958503ed3f1129a4f50bc75f48de3a))

## [0.40.2](https://github.com/buchenberg/yaah/compare/v0.40.1...v0.40.2) (2026-07-30)


### Bug Fixes

* contain error rendering across TUI, REPL, and web UI ([5a37a84](https://github.com/buchenberg/yaah/commit/5a37a8423ca7d5072fd9428635424ebf527fca1a))
* contain error rendering across TUI, REPL, and web UI ([6f94a2e](https://github.com/buchenberg/yaah/commit/6f94a2e535bde80c5354e38efd53229971bdb6bb))
* TUI garbled output, reasoning_content error, tool improvements ([#110](https://github.com/buchenberg/yaah/issues/110)) ([60cbe48](https://github.com/buchenberg/yaah/commit/60cbe480ae51e1a6eb7a36478491a6c355837934))

## [0.40.1](https://github.com/buchenberg/yaah/compare/v0.40.0...v0.40.1) (2026-07-29)


### Bug Fixes

* sync Loop provider/model after LLM client fallback ([91e4729](https://github.com/buchenberg/yaah/commit/91e47291f680401526ea83b3c3d3a398f425f45d))
* sync Loop provider/model after LLM client fallback ([41c2c1c](https://github.com/buchenberg/yaah/commit/41c2c1c1baf9ada7c628b20f131932db8c096967))

## [0.40.0](https://github.com/buchenberg/yaah/compare/v0.39.0...v0.40.0) (2026-07-29)


### Features

* acp-serve command — Agent Communication Protocol server over stdio ([0a6ae17](https://github.com/buchenberg/yaah/commit/0a6ae172113684a9bcf66c223cf1f105145ff060))
* add acp-serve command implementing Agent Communication Protocol ([66a76bd](https://github.com/buchenberg/yaah/commit/66a76bd9ebb6ed3a18c3c213a089eed331246813))


### Bug Fixes

* remove unused types and fields from acp-serve ([a3cb1fc](https://github.com/buchenberg/yaah/commit/a3cb1fc03a2de8b019b8f6862d306bf015a4fbd9))


### Documentation

* add acp-serve to command reference and architecture docs ([d67f3c3](https://github.com/buchenberg/yaah/commit/d67f3c39a1f539c8b72f442f77682499d01269bf))

## [0.39.0](https://github.com/buchenberg/yaah/compare/v0.38.0...v0.39.0) (2026-07-29)


### Features

* variadic min/max in calculate tool ([c9e063e](https://github.com/buchenberg/yaah/commit/c9e063ed7138366b338282cd5dff8210a8a1d390))
* variadic min/max in calculate tool ([a659c05](https://github.com/buchenberg/yaah/commit/a659c05d0aaafc4bba259ec866868a751dda5f14))


### Bug Fixes

* include tool_id in web UI tool.end events for frontend matching ([6a7df0c](https://github.com/buchenberg/yaah/commit/6a7df0cc0c20a4d39bf62bdcedfddac6137d3e83))


### Documentation

* add web UI architecture and event reference ([6ae2e23](https://github.com/buchenberg/yaah/commit/6ae2e23bdad557d8d17c517cc6f73509435fc114))

## [0.38.0](https://github.com/buchenberg/yaah/compare/v0.37.0...v0.38.0) (2026-07-29)


### Features

* add quality gates and session directives ([d78c6e4](https://github.com/buchenberg/yaah/commit/d78c6e4775922fa75289a4a780c95913e8a72b39))

## [0.37.0](https://github.com/buchenberg/yaah/compare/v0.36.3...v0.37.0) (2026-07-28)


### Features

* sub-agent escalation system, compaction fixes, and staleness middleware ([d7958c9](https://github.com/buchenberg/yaah/commit/d7958c9478006847e247fa0ce716acf88a1c29c4))
* sub-agent escalation system, compaction fixes, and staleness middleware ([2b506a0](https://github.com/buchenberg/yaah/commit/2b506a0c93db3c8e3b9538c945444e68a5dabd25))

## [0.36.3](https://github.com/buchenberg/yaah/compare/v0.36.2...v0.36.3) (2026-07-28)


### Bug Fixes

* **pubsub:** eliminate race in TestBroker_DefaultBufferSize ([76a6e3c](https://github.com/buchenberg/yaah/commit/76a6e3cead5bdf1b1be75cb04a5b0711c0f11344))
* **pubsub:** eliminate race in TestBroker_DefaultBufferSize ([9d8410d](https://github.com/buchenberg/yaah/commit/9d8410d4b7a3ea069e9ecaf3a3340894a725afa1))

## [0.36.2](https://github.com/buchenberg/yaah/compare/v0.36.1...v0.36.2) (2026-07-28)


### Bug Fixes

* **tools:** tolerate string-encoded JSON arrays in todowrite args ([631c917](https://github.com/buchenberg/yaah/commit/631c91755216ff5266140faad7eb44839bb0729b))
* **tools:** tolerate string-encoded JSON arrays in todowrite args ([38471d0](https://github.com/buchenberg/yaah/commit/38471d0622b7576c6a0898122a2e6bfa74a907d8))

## [0.36.1](https://github.com/buchenberg/yaah/compare/v0.36.0...v0.36.1) (2026-07-27)


### Bug Fixes

* **agent:** harden reasoning-content preservation for thinking-mode providers ([#89](https://github.com/buchenberg/yaah/issues/89)) ([869a32b](https://github.com/buchenberg/yaah/commit/869a32b626c1d809f5bc416b7d7de560def0d84a))

## [0.36.0](https://github.com/buchenberg/yaah/compare/v0.35.0...v0.36.0) (2026-07-27)


### Features

* **persist:** MCP config consolidation, session token tracking, update automation, session restoration, and repository pattern ([#86](https://github.com/buchenberg/yaah/issues/86)) ([92df0d9](https://github.com/buchenberg/yaah/commit/92df0d9b47f0e2bd545ab21b944650a0ea41045b))
* **subagent:** background delegation, summary budgeting, permission inheritance ([#88](https://github.com/buchenberg/yaah/issues/88)) ([7efa5d8](https://github.com/buchenberg/yaah/commit/7efa5d84aea7982c5a99764b4903cebfcb811f87))

## [0.35.0](https://github.com/buchenberg/yaah/compare/v0.34.0...v0.35.0) (2026-07-27)


### Features

* web UI with Alpine.js + Pico CSS, plus reasoning_content fixes ([#84](https://github.com/buchenberg/yaah/issues/84)) ([adf9d97](https://github.com/buchenberg/yaah/commit/adf9d970817ad21bedaa4ddd2ac1eeb28e394f81))

## [0.34.0](https://github.com/buchenberg/yaah/compare/v0.33.4...v0.34.0) (2026-07-27)


### Features

* **agent:** compaction optimizations — chunked fallback, overflow patterns, events, pruner improvements ([da14394](https://github.com/buchenberg/yaah/commit/da14394f9452b0a2b0b23b847060baf4b8ee60a5))
* **providers:** resolve context window from model metadata with config cap ([af7e4c8](https://github.com/buchenberg/yaah/commit/af7e4c8aa0df07e3ff5aea040f9e52446ad07138))
* **tui:** lolcat thinking, Esc/:stop abort, textarea, reasoning compaction, Session interface, and view layer refactor ([#81](https://github.com/buchenberg/yaah/issues/81)) ([c675b83](https://github.com/buchenberg/yaah/commit/c675b83de749ef688dc3357c54aa7750200150ae))


### Bug Fixes

* **persist:** store and restore reasoning_content across session resume ([c3e0c00](https://github.com/buchenberg/yaah/commit/c3e0c00c3555e3d6451df19c276a160dee2dd48e))

## [0.33.4](https://github.com/buchenberg/yaah/compare/v0.33.3...v0.33.4) (2026-07-26)


### Bug Fixes

* **llm:** DSML ID collision + TUI header/footer redesign ([#80](https://github.com/buchenberg/yaah/issues/80)) ([fa912d2](https://github.com/buchenberg/yaah/commit/fa912d21b82a2ed8ab058f440f7b84b6c27b17b9))
* **llm:** strip DSML markup from streaming tokens in real-time ([#78](https://github.com/buchenberg/yaah/issues/78)) ([35aac20](https://github.com/buchenberg/yaah/commit/35aac2087daa18bb0515bc11d441d20e8de4f60d))

## [0.33.3](https://github.com/buchenberg/yaah/compare/v0.33.2...v0.33.3) (2026-07-24)


### Code Refactoring

* **agent:** engine DI — extract components, add constructor with functional options ([#76](https://github.com/buchenberg/yaah/issues/76)) ([0706c3b](https://github.com/buchenberg/yaah/commit/0706c3b01ba3f46534f1b729838336c63d8fec35))

## [0.33.2](https://github.com/buchenberg/yaah/compare/v0.33.1...v0.33.2) (2026-07-24)


### Bug Fixes

* **tools:** git flags whitelist + OS-aware shell registration ([#74](https://github.com/buchenberg/yaah/issues/74)) ([4e195e1](https://github.com/buchenberg/yaah/commit/4e195e102b2e4019da050c58db83253a23753c15))

## [0.33.1](https://github.com/buchenberg/yaah/compare/v0.33.0...v0.33.1) (2026-07-24)


### Bug Fixes

* **llm:** close DSML parser gaps in non-streaming and mixed-content paths ([#71](https://github.com/buchenberg/yaah/issues/71)) ([9d9a113](https://github.com/buchenberg/yaah/commit/9d9a113d4db3011d2c0faf3d8b825b528bfaa7a3))

## [0.33.0](https://github.com/buchenberg/yaah/compare/v0.32.0...v0.33.0) (2026-07-24)


### Features

* **mcp:** agent-to-agent MCP server with framing auto-detection and OTel tracing ([#68](https://github.com/buchenberg/yaah/issues/68)) ([d8c8ca2](https://github.com/buchenberg/yaah/commit/d8c8ca2e0da7e757f3d3f4a7c418196f15d5a9c3))

## [0.32.0](https://github.com/buchenberg/yaah/compare/v0.31.1...v0.32.0) (2026-07-24)


### Features

* **agent:** convergence steering, context-bloat fixes, stuck-child watchdog ([#67](https://github.com/buchenberg/yaah/issues/67)) ([6b158c5](https://github.com/buchenberg/yaah/commit/6b158c5460d1ea248e1edcbeee7f883cc3d9fde0))
* **agent:** fix progressive context degradation (dual-trigger compaction, reasoning stripping, tool-def cache, payload guard) ([#65](https://github.com/buchenberg/yaah/issues/65)) ([e5f9a3e](https://github.com/buchenberg/yaah/commit/e5f9a3e17dcb4c15a1e4218be7d77f90aa65b3aa))

## [0.31.1](https://github.com/buchenberg/yaah/compare/v0.31.0...v0.31.1) (2026-07-23)


### Code Refactoring

* engine-view separation phases 2-3 ([#62](https://github.com/buchenberg/yaah/issues/62)) ([bc76316](https://github.com/buchenberg/yaah/commit/bc7631624e28ef7a00da24883c286d426e14e28f))

## [0.31.0](https://github.com/buchenberg/yaah/compare/v0.30.0...v0.31.0) (2026-07-23)


### Features

* **agent:** engine-view separation — typed Event interface, View contract, remove callbacks ([#60](https://github.com/buchenberg/yaah/issues/60)) ([469476b](https://github.com/buchenberg/yaah/commit/469476b50e5b63fba7488f7656057ae2f6d71960))

## [0.30.0](https://github.com/buchenberg/yaah/compare/v0.29.0...v0.30.0) (2026-07-23)


### Features

* **agent:** add typed Event interface, View contract, and BrokerView adapter ([#58](https://github.com/buchenberg/yaah/issues/58)) ([de18682](https://github.com/buchenberg/yaah/commit/de1868295cbed76b25a8f2fc672b7e4ac3cbfbf0))

## [0.29.0](https://github.com/buchenberg/yaah/compare/v0.28.0...v0.29.0) (2026-07-23)


### Features

* **agent:** add in-process pub/sub broker for decoupled streaming ([#56](https://github.com/buchenberg/yaah/issues/56)) ([db50e08](https://github.com/buchenberg/yaah/commit/db50e0826e64ab8ec3f8c9e66303261f0c2a8e59))

## [0.28.0](https://github.com/buchenberg/yaah/compare/v0.27.0...v0.28.0) (2026-07-23)


### Features

* framework parity phase 2 — session-affinity headers & wakeup coalescing (4.1, 4.2) ([d27b82e](https://github.com/buchenberg/yaah/commit/d27b82e02e3aff5ea5575b9e4fa4562c8ab0ef63))
* framework parity phase 2 — session-affinity headers & wakeup coalescing (4.1, 4.2) ([d27b82e](https://github.com/buchenberg/yaah/commit/d27b82e02e3aff5ea5575b9e4fa4562c8ab0ef63))

## [0.27.0](https://github.com/buchenberg/yaah/compare/v0.26.0...v0.27.0) (2026-07-23)


### Features

* framework parity phase 1 — correctness & durability ([#52](https://github.com/buchenberg/yaah/issues/52)) ([ccd7a57](https://github.com/buchenberg/yaah/commit/ccd7a570a267ca65925ae4e9057d1893fc900cec))

## [0.26.0](https://github.com/buchenberg/yaah/compare/v0.25.0...v0.26.0) (2026-07-23)


### Features

* add parallel sub-agent preference hint to identity prompt ([985d482](https://github.com/buchenberg/yaah/commit/985d4823e801e9c6348a11044caa04db167698f7))
* per-role overrides, role descriptions in schema, micro-agent roles, trace parity ([f1b48d9](https://github.com/buchenberg/yaah/commit/f1b48d9918fe99cf2a8a85e1cc6a1bcb763be5b4))
* sub-agent efficiency — MaxTurns, ContextWindow, JSONMode, OutputLimit ([11e0da6](https://github.com/buchenberg/yaah/commit/11e0da68c97644feccb6a1e2cd88aef4e808014a))
* sub-agent efficiency — MaxTurns, ContextWindow, JSONMode, OutputLimit ([cc7cf8b](https://github.com/buchenberg/yaah/commit/cc7cf8bd576818f059f415e16fee61a17c5562a7))


### Documentation

* add sub-agent efficiency head-to-head benchmark comparison ([48a8753](https://github.com/buchenberg/yaah/commit/48a87530d3f7185503bcadff873a86a88f64d3ec))

## [0.25.0](https://github.com/buchenberg/yaah/compare/v0.24.0...v0.25.0) (2026-07-22)


### Features

* Phase 2 loop tuning — truncation, skill index, cost propagation, replay recovery, depth hard-code ([1e6ed63](https://github.com/buchenberg/yaah/commit/1e6ed638e292cdfceab8d2295f7ccef651c562e5))
* Phase 2 loop tuning — truncation, skill index, cost propagation, replay recovery, depth hard-code ([a8bb0ff](https://github.com/buchenberg/yaah/commit/a8bb0ffe892b4c82e53de53afb53f80537c37e18))

## [0.24.0](https://github.com/buchenberg/yaah/compare/v0.23.0...v0.24.0) (2026-07-22)


### Features

* token-budgeted compaction survival with boundary alignment ([4ac30fc](https://github.com/buchenberg/yaah/commit/4ac30fc0e9f2583c3363b214c7291d3b162e46dd))
* token-budgeted compaction survival with boundary alignment ([e996b50](https://github.com/buchenberg/yaah/commit/e996b5090a6c32ec42c95b1331c94b75dc7e03f3))

## [0.23.0](https://github.com/buchenberg/yaah/compare/v0.22.0...v0.23.0) (2026-07-22)


### Features

* **agent:** add preflight compaction with configurable estimate factor ([43152bb](https://github.com/buchenberg/yaah/commit/43152bb105bb54f6266b0020ae652dc4be2cc072))
* **agent:** add preflight compaction with configurable estimate factor ([7f86ab5](https://github.com/buchenberg/yaah/commit/7f86ab5783efbcd8eeee4cf77778f3cddba96e75))


### Documentation

* add B4 benchmark results for preflight compaction ([686c237](https://github.com/buchenberg/yaah/commit/686c2371e15d00dfd116ed493d1ce2c6432911b5))

## [0.22.0](https://github.com/buchenberg/yaah/compare/v0.21.0...v0.22.0) (2026-07-22)


### Features

* add soft-prune context management and remove executor cruft ([10296d3](https://github.com/buchenberg/yaah/commit/10296d3464e3d26b9c489dffd509bda8dbbaf9ab))
* add soft-prune context management and remove executor cruft ([c48ce7e](https://github.com/buchenberg/yaah/commit/c48ce7e7c5ed05863c1bd49554a7fc2ed7a6bbea))

## [0.21.0](https://github.com/buchenberg/yaah/compare/v0.20.0...v0.21.0) (2026-07-22)


### Features

* FullTools mode, batching, contract auto-injection, TUI subagent parity ([a740911](https://github.com/buchenberg/yaah/commit/a740911bf4a6bf21c0d29e47046b44bf04a76268))


### Code Refactoring

* **subagents:** rename roles to analyst/developer/tester/reviewer, add custom role discovery ([c98bf61](https://github.com/buchenberg/yaah/commit/c98bf61da9fa1a699020ba75665985df7ca3004b))

## [0.20.0](https://github.com/buchenberg/yaah/compare/v0.19.0...v0.20.0) (2026-07-21)


### Features

* dual-loop executor improvements and agent loop hardening ([41cf6b3](https://github.com/buchenberg/yaah/commit/41cf6b348a41a025869f581d29517595e495c16d))
* dual-loop executor improvements and agent loop hardening ([03b2be3](https://github.com/buchenberg/yaah/commit/03b2be3dce5c50a95e4f6ef88821a5e328999582))

## [0.19.0](https://github.com/buchenberg/yaah/compare/v0.18.0...v0.19.0) (2026-07-21)


### Features

* **agent:** spontaneous delegation, executor self-correction, identity/doc overhaul ([be308b5](https://github.com/buchenberg/yaah/commit/be308b591372710206b04f75bb77fcc9688ff097))
* **agent:** spontaneous delegation, executor self-correction, identity/doc overhaul ([f5da3a6](https://github.com/buchenberg/yaah/commit/f5da3a6f6df51b8d5cd3971979a2100bc576ef33))

## [0.18.0](https://github.com/buchenberg/yaah/compare/v0.17.0...v0.18.0) (2026-07-21)


### Features

* dual-loop execution — inner executor chains tools per outer turn ([deba521](https://github.com/buchenberg/yaah/commit/deba52196412eaf9aaf06de81570be886da1bb24))

## [0.17.0](https://github.com/buchenberg/yaah/compare/v0.16.0...v0.17.0) (2026-07-20)


### Features

* add 'yaah skill create' and 'yaah skill edit' CLI commands ([13faa40](https://github.com/buchenberg/yaah/commit/13faa40ae43b121e2b0cf2eed6a62774aecd59f1))

## [0.16.0](https://github.com/buchenberg/yaah/compare/v0.15.0...v0.16.0) (2026-07-20)


### Features

* add plan tool with CRUD + approval workflow ([3f60250](https://github.com/buchenberg/yaah/commit/3f6025092eb681d2da43dea73e2dd5c5e48b6112))
* **providers:** extract agent session, add configurable provider timeout ([1b58083](https://github.com/buchenberg/yaah/commit/1b5808385a21fbb67f2343f5a357e1a71662faa3))
* **tools:** add http tool for generic REST API calls ([1496c9a](https://github.com/buchenberg/yaah/commit/1496c9a1d36db6564736775aa2cf31f79d38a919))
* **tools:** add replace and json_query tools ([ece1330](https://github.com/buchenberg/yaah/commit/ece13306353f32eed095618d2dc9761eb9d86561))


### Bug Fixes

* **tools:** fix calculate Pratt parser binary operators and right-associativity ([b09d682](https://github.com/buchenberg/yaah/commit/b09d682b752406c18b20c6ca97088d83d76c6914))


### Documentation

* sync tool inventory across identity.md, AGENTS.md, README.md ([a758ed7](https://github.com/buchenberg/yaah/commit/a758ed7547701463aa2672c004e2a542f9f6dc48))
* update tool inventory to include replace, json_query, and http ([b1960d4](https://github.com/buchenberg/yaah/commit/b1960d4ae41f381fdb6cdaf51e8cfe6942bf224b))

## [0.15.0](https://github.com/buchenberg/yaah/compare/v0.14.3...v0.15.0) (2026-07-20)


### Features

* **errorclassify:** structured error classification with provider fallback ([a53f737](https://github.com/buchenberg/yaah/commit/a53f737057d364ba11d5eba7205a7d3701d3f561))
* **errorclassify:** structured error classification with provider fallback ([57b0a53](https://github.com/buchenberg/yaah/commit/57b0a53eee477a2a4f2390e59ed72443948f6d70))


### Bug Fixes

* **errorclassify:** gofmt alignment ([80dfb8f](https://github.com/buchenberg/yaah/commit/80dfb8fc06e91261c27f1d27d22340dd163e50ef))

## [0.14.3](https://github.com/buchenberg/yaah/compare/v0.14.2...v0.14.3) (2026-07-20)


### Bug Fixes

* **ci:** add checkout step to release upload job ([7caa713](https://github.com/buchenberg/yaah/commit/7caa7139a5d3922600abb4fee4f9b70b4ca0aca6))
* **ci:** add checkout to release upload job ([249ca56](https://github.com/buchenberg/yaah/commit/249ca56cc2bd1eaf4f99f5bf390c2150b5344123))

## [0.14.2](https://github.com/buchenberg/yaah/compare/v0.14.1...v0.14.2) (2026-07-20)


### CI / Build

* add cross-compile and upload to release-please workflow ([0165a05](https://github.com/buchenberg/yaah/commit/0165a054e4043f0ce450a0300d965189f338feeb))
* inline build/upload into release-please workflow ([aad2c42](https://github.com/buchenberg/yaah/commit/aad2c4256abf0a1e1e0f07480e036cf9de54336a))

## [0.14.1](https://github.com/buchenberg/yaah/compare/v0.14.0...v0.14.1) (2026-07-20)


### CI / Build

* add .release-please-manifest.json ([cfeb6e7](https://github.com/buchenberg/yaah/commit/cfeb6e7dd56f9d7fc77fd7aca108d982ac255e99))
* add missing .release-please-manifest.json ([9365eb6](https://github.com/buchenberg/yaah/commit/9365eb670a98ae2287c7072262ca84a4d09da7e8))
* add release-please for automated tag and release creation ([1195a73](https://github.com/buchenberg/yaah/commit/1195a7390a54eab66ac74226fe5e98bad4740622))
* add release-please for automated tag and release creation ([a1388f8](https://github.com/buchenberg/yaah/commit/a1388f8459668271383d05d0a7c6c6605897a0e5))
* bump release-please-action to v5 (Node 24) ([97a85b0](https://github.com/buchenberg/yaah/commit/97a85b0484555c1295f77660299e2b115d1c0e8d))
* bump release-please-action to v5 (Node 24) ([3fb78b3](https://github.com/buchenberg/yaah/commit/3fb78b30d600f1105ebb8415441bc56512c4b9de))
