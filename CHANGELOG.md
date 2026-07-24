# Changelog

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
