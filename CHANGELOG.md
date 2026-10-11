# Changelog

## [0.64.0](https://github.com/buchenberg/yaah/compare/v0.63.0...v0.64.0) (2026-10-11)


### Features

* **shepherd:** A1 -- session root scope; sub-agent scopes fork it ([0823458](https://github.com/buchenberg/yaah/commit/08234587b817d8e19244a40b5bd4ddac1fc03f79))
* **shepherd:** A1 -- session root scope; sub-agent scopes fork it ([1638347](https://github.com/buchenberg/yaah/commit/1638347db0376bc46187bc816e4b0a71826905fb))
* **shepherd:** A2 -- the inject bridge: supervisor guidance reaches running loops ([17604ae](https://github.com/buchenberg/yaah/commit/17604aee1f930df4cfe96ed33166eb2ac9666c07))
* **shepherd:** A2 -- the inject bridge: supervisor guidance reaches running loops ([672e328](https://github.com/buchenberg/yaah/commit/672e328b9e9155d8b29edb23f3d3e82773ba8a4c))
* **shepherd:** A3 -- the halt bridge: supervisor halt cancels running loops ([472c93f](https://github.com/buchenberg/yaah/commit/472c93f18614cd8f0fc294e2697dd240899dfe02))
* **shepherd:** A3 -- the halt bridge: supervisor halt cancels running loops ([b415fa1](https://github.com/buchenberg/yaah/commit/b415fa193e64ad273fd62be215886a074c5a7c38))


### Bug Fixes

* **shepherd:** A1 review round 1 -- root halt guard, capture probe, empty-repo guard ([227150d](https://github.com/buchenberg/yaah/commit/227150d7c026a20ddf740efbe3ce2b94e1bc03bd))
* **shepherd:** A2 review round 1 -- session-owned registry, honest outcomes, tight cut ([f340957](https://github.com/buchenberg/yaah/commit/f34095741ce4a49b5660be95a11641829f19b322))
* **shepherd:** A3 review round 1 -- halt waits for the run to quiesce before teardown ([eea5d36](https://github.com/buchenberg/yaah/commit/eea5d36b8db49171d739305791ffcf1e4950d97a))


### Documentation

* **plan:** review round 1 -- step IDs, config location, defaults, exit wording ([db80172](https://github.com/buchenberg/yaah/commit/db80172a72bc8dedc3b1641b4c1ef689cb906758))
* **plan:** shepherd kernel adoption -- audit, decisions, phased execution ([9fd6796](https://github.com/buchenberg/yaah/commit/9fd6796e3f8e00e90bb7acb5a2a5b5fb8784dd56))
* **plan:** shepherd kernel adoption -- audit, decisions, phased execution ([590abca](https://github.com/buchenberg/yaah/commit/590abca1ea500c4d0a888e85aac4ec5839d1dd6e))
* review round 1 -- active docs follow the sandbox component move ([376cf87](https://github.com/buchenberg/yaah/commit/376cf87f8f6eb06f2a2a7e13611e8793bcd02ce7))

## [0.63.0](https://github.com/buchenberg/yaah/compare/v0.62.0...v0.63.0) (2026-10-11)


### Features

* **workspace:** step 9 -- sub-agent sandbox inheritance (D2), test matrix, docs ([2f673ee](https://github.com/buchenberg/yaah/commit/2f673eecc2a585b2971079da8862e44a7fa1fa2c))


### Bug Fixes

* **test:** make the local-route workdir assertion platform-portable ([6c24735](https://github.com/buchenberg/yaah/commit/6c24735b1eddf96ccb5492ac3f4f5710e599d3c6))
* **workspace:** review round 1 -- gate ordering, env header, spill, prompt wording ([2392c26](https://github.com/buchenberg/yaah/commit/2392c2654475cf0dc6b20538cc20e68c6fec38e0))
* **workspace:** review round 2 -- shell guidance agrees with the header, single spill predicate, honest test ([d6a6a2d](https://github.com/buchenberg/yaah/commit/d6a6a2da4a2a0239641e696859afd8b5347c2375))

## [0.62.0](https://github.com/buchenberg/yaah/compare/v0.61.0...v0.62.0) (2026-10-11)


### Features

* **doctor:** step-8 activation visibility -- workspace check + live E2E harness (plan §2.4) ([9aac5fb](https://github.com/buchenberg/yaah/commit/9aac5fb639dd19950b0528c2fc61904e449eb4a5))
* **doctor:** step-8 activation visibility -- workspace check + live E2E harness (plan §2.4) ([fc10c7b](https://github.com/buchenberg/yaah/commit/fc10c7b0570906950bdfa47acc8e053f4be48bea))


### Bug Fixes

* **workspace:** rg prints no filename for a single-file search ([ab16c61](https://github.com/buchenberg/yaah/commit/ab16c611a7eb8277170abec2fd06921948c65575))
* **workspace:** second review round -- the chunk bound respects MAX_ARG_STRLEN ([cd97ad3](https://github.com/buchenberg/yaah/commit/cd97ad390974f9fcfd9c93dc18312ae669df8f5b))
* **workspace:** step-8 review findings -- doctor validates the stanza, the live harness dispatches through the registry ([6510463](https://github.com/buchenberg/yaah/commit/6510463fb164abd07b9735944037ae342749ec4f))
* **workspace:** the sandbox write must carry content in argv, not stdin ([6addeec](https://github.com/buchenberg/yaah/commit/6addeec543631e56293a06d1ba08d081e0cea77c))


### Documentation

* **plan:** step 8's live verdict is recorded -- MET, end to end ([3f3bf48](https://github.com/buchenberg/yaah/commit/3f3bf481b65fc8cdda1785935cbb0ffc2551990c))
* **plan:** the live verdict stands on the strengthened harness ([3b29f95](https://github.com/buchenberg/yaah/commit/3b29f95c0f251d475e2a53f958b2ea15204d3167))
* podman save refuses to overwrite an existing tarball ([b2e9a4c](https://github.com/buchenberg/yaah/commit/b2e9a4c6bd07560435f59c5c4922a1c0fb386655))
* runbook for the live isolated-workspace activation test ([4e72d16](https://github.com/buchenberg/yaah/commit/4e72d169e52a82faf9fee87c0bb0b95baf2c4cfc))
* the D5 image needs go on the default PATH, not the image ENV ([2f927e5](https://github.com/buchenberg/yaah/commit/2f927e548aaa26271930f14321cc119d62332cf8))
* the live runbook's run section is one copy-paste block ([f1ce647](https://github.com/buchenberg/yaah/commit/f1ce6473e02687e4b9bcfab6cc3e02b6b82df886))
* the live-test image import needs containerd's -n namespace ([c2c423b](https://github.com/buchenberg/yaah/commit/c2c423bd080be6b547c0a6df81d1c9b69e74ef92))
* the live-test image must be unpacked, not just imported ([08a0708](https://github.com/buchenberg/yaah/commit/08a0708e4beb0cd3d1e7595e5ce6ff0054999683))
* **workspace:** the live harness needs sudo env, not sudo -E ([a02307a](https://github.com/buchenberg/yaah/commit/a02307a28cca9b14ba47402557d83fb6d8b694de))

## [0.61.0](https://github.com/buchenberg/yaah/compare/v0.60.0...v0.61.0) (2026-10-11)


### Features

* **workspace:** the step-7 tool surface -- curated registry, apply_out, prompt delta, D4 record ([85ad98f](https://github.com/buchenberg/yaah/commit/85ad98f91a80b3cd40abe5b445b4376792dfecd0))
* **workspace:** the step-7 tool surface -- curated registry, apply_out, prompt delta, D4 record (step 7, plan §2.1/§2.2/§3) ([b89eada](https://github.com/buchenberg/yaah/commit/b89eadaf424a780f1483f006667e25979be49372))


### Bug Fixes

* **workspace:** close the step-7 review findings -- host-mutation paths, honest apply-out approval ([79960dd](https://github.com/buchenberg/yaah/commit/79960ddbd66034bf575080c35e9e10693d974c62))

## [0.60.0](https://github.com/buchenberg/yaah/compare/v0.59.2...v0.60.0) (2026-10-11)


### Features

* **workspace:** the D1 bootstrap -- copy-in, baseline capture, apply-out (step 6, plan §2.3) ([d2a8793](https://github.com/buchenberg/yaah/commit/d2a879331e63ce1a4817be3f1110bced49f45262))


### Bug Fixes

* **workspace:** harden the D1 bootstrap -- CI failures and PR review findings ([a2ef845](https://github.com/buchenberg/yaah/commit/a2ef8453357079f889fc75907469fa6d38176b7c))

## [0.59.2](https://github.com/buchenberg/yaah/compare/v0.59.1...v0.59.2) (2026-10-11)


### Bug Fixes

* **workspace:** bound the failed-start sandbox teardown -- the third destroy site ([988fc2a](https://github.com/buchenberg/yaah/commit/988fc2a90401d89ec0728589b2caf1def9ada3de))
* **workspace:** bound the failed-start sandbox teardown -- the third destroy site ([481e7d0](https://github.com/buchenberg/yaah/commit/481e7d095a2dd35f2a002ac5f2d53f4c6a93d070))

## [0.59.1](https://github.com/buchenberg/yaah/compare/v0.59.0...v0.59.1) (2026-10-11)


### Bug Fixes

* **llm:** address the PR [#235](https://github.com/buchenberg/yaah/issues/235) review -- overflow-first 400 ordering, the promised table cases, loud drop, honest comment ([7021f46](https://github.com/buchenberg/yaah/commit/7021f46e64816684d80320039c04585e9f4aeaf6))
* **llm:** drop response_format and replay when a provider rejects it ([8c414ee](https://github.com/buchenberg/yaah/commit/8c414ee9a10c619e0c494ecd42e6d5dbf1104066))
* **llm:** the drop warning carries the provider's error, not the canned label ([4b0c43e](https://github.com/buchenberg/yaah/commit/4b0c43ef54a10eda1babdd0fefdf0c0f84293cf9))

## [0.59.0](https://github.com/buchenberg/yaah/compare/v0.58.0...v0.59.0) (2026-10-11)


### Features

* **workspace:** sandbox construction + lifecycle -- step 5 (plan §1.2) ([5ae4a32](https://github.com/buchenberg/yaah/commit/5ae4a3290aec076d2496cfd6f72fcc9b2c9aec12))


### Bug Fixes

* **workspace:** address the PR [#233](https://github.com/buchenberg/yaah/issues/233) re-review -- followup-channel ordering, backfill vs the startup guard ([3e62093](https://github.com/buchenberg/yaah/commit/3e620932d49ed482d2233c3a9120711ee56e8079))
* **workspace:** address the PR [#233](https://github.com/buchenberg/yaah/issues/233) review -- probe leaks, teardown races, startup resource ownership ([05dbd8a](https://github.com/buchenberg/yaah/commit/05dbd8a9cb839d1989f4f0fec8c4712dfd38d9ef))

## [0.58.0](https://github.com/buchenberg/yaah/compare/v0.57.0...v0.58.0) (2026-10-11)


### Features

* **workspace:** the workspace.backend config surface — step 4 (plan §1.1 + D5) ([380cd59](https://github.com/buchenberg/yaah/commit/380cd5918778b45e889bdc6daf12ac8bbf94fb67))


### Bug Fixes

* **config:** the workspace root is a POSIX path, not a host path ([58d429d](https://github.com/buchenberg/yaah/commit/58d429ddbf693b3fcaf27c0dd335fd4d2e05c2f5))
* **workspace:** address the PR [#231](https://github.com/buchenberg/yaah/issues/231) review -- POSIX roots cleaned, probe teardown surfaced, toolchain floor honest ([b156610](https://github.com/buchenberg/yaah/commit/b156610f05baf8c80769fa01215eca88bd7c74da))

## [0.57.0](https://github.com/buchenberg/yaah/compare/v0.56.3...v0.57.0) (2026-10-10)


### Features

* **kernel:** pin shepherd-kernel-go v0.10.0 -- ctx-first store sweep (step 3c) ([774739d](https://github.com/buchenberg/yaah/commit/774739dabb4f636616a38e5a0d2f7e1156733550))
* **kernel:** pin shepherd-kernel-go v0.10.0 — ctx-first store sweep (step 3c) ([c8d691e](https://github.com/buchenberg/yaah/commit/c8d691eafd23494ae0349fa25ed7192522b63c03))


### Bug Fixes

* **kernel:** address the PR [#229](https://github.com/buchenberg/yaah/issues/229) review -- the cancel-free rule, applied consistently ([4300ff3](https://github.com/buchenberg/yaah/commit/4300ff3ef362f9f403d5013c4964409ad61a600b))

## [0.56.3](https://github.com/buchenberg/yaah/compare/v0.56.2...v0.56.3) (2026-10-10)


### Documentation

* fresh frameworks comparison (2026-10-07) + README positioning section ([eee5819](https://github.com/buchenberg/yaah/commit/eee5819fb7ea947e7e866a4524a104619f3e42ca))

## [0.56.2](https://github.com/buchenberg/yaah/compare/v0.56.1...v0.56.2) (2026-10-10)


### Documentation

* **plans:** refresh the isolated-workspace plan against kernel v0.4.1→v0.10.0 ([a0455c1](https://github.com/buchenberg/yaah/commit/a0455c12b09a1ce42a84ec1f0a7d565d3a24dffb))
* **plans:** refresh the isolated-workspace plan against kernel v0.4.1→v0.10.0 ([01baeca](https://github.com/buchenberg/yaah/commit/01baeca70aeca6aa542e6d5182692c44caf038f4))

## [0.56.1](https://github.com/buchenberg/yaah/compare/v0.56.0...v0.56.1) (2026-10-07)


### Documentation

* markdown accuracy audit — fix stale commands, broken links, drift ([#223](https://github.com/buchenberg/yaah/issues/223)) ([4553485](https://github.com/buchenberg/yaah/commit/45534854b869c9a4d0b0889a057d942cf8f57a9c))

## [0.56.0](https://github.com/buchenberg/yaah/compare/v0.55.2...v0.56.0) (2026-10-07)


### Features

* **subagent:** give sub-agents a lean base prompt ([#221](https://github.com/buchenberg/yaah/issues/221)) ([4203df7](https://github.com/buchenberg/yaah/commit/4203df7b8e41073e31f64a72e3313cde35de5c32))

## [0.55.2](https://github.com/buchenberg/yaah/compare/v0.55.1...v0.55.2) (2026-10-07)


### Bug Fixes

* **tools:** add Workspace.Lstat and stop embedding sandbox exec output in errors ([#216](https://github.com/buchenberg/yaah/issues/216)) ([22f913f](https://github.com/buchenberg/yaah/commit/22f913fdd4103afb55b773ddc9d25c6f2a081537))


### Documentation

* **plans:** advance the isolated-workspace plan to step 4 ([#219](https://github.com/buchenberg/yaah/issues/219)) ([4c1e099](https://github.com/buchenberg/yaah/commit/4c1e099e9f057a60086de27c90eff76968775914))

## [0.55.1](https://github.com/buchenberg/yaah/compare/v0.55.0...v0.55.1) (2026-09-01)


### Features

* Add sub-agent turn budget floors (min_turns / min_iterations) ([#213](https://github.com/buchenberg/yaah/issues/213)) ([d446099](https://github.com/buchenberg/yaah/commit/d446099edc0c6b4bf984b3abb0281686e3ab531a))

## [0.55.0](https://github.com/buchenberg/yaah/compare/v0.54.0...v0.55.0) (2026-08-23)


### Features

* **persistence:** cross-link sessions, OTel traces, and Shepherd fac… ([#201](https://github.com/buchenberg/yaah/issues/201)) ([c18263c](https://github.com/buchenberg/yaah/commit/c18263c2f477422898feb65064d1d5c2821e10ab))

## [0.54.0](https://github.com/buchenberg/yaah/compare/v0.53.0...v0.54.0) (2026-08-20)


### Features

* harden SQLite persistence ([#198](https://github.com/buchenberg/yaah/issues/198)) ([fff8aca](https://github.com/buchenberg/yaah/commit/fff8aca96ec9e280b35f54a16553fcc57a9532b3))

## [0.53.0](https://github.com/buchenberg/yaah/compare/v0.52.1...v0.53.0) (2026-08-10)


### Features

* add SupervisorTool for sub-agent supervision ([7f4bcd4](https://github.com/buchenberg/yaah/commit/7f4bcd444e3ecd4ec0434cae006f8e934f731961))

## [0.52.1](https://github.com/buchenberg/yaah/compare/v0.52.0...v0.52.1) (2026-08-09)


### Bug Fixes

* ACP bugs [#179](https://github.com/buchenberg/yaah/issues/179)-[#182](https://github.com/buchenberg/yaah/issues/182), add semantic skill search, update prompt descriptions ([#189](https://github.com/buchenberg/yaah/issues/189)) ([de501d9](https://github.com/buchenberg/yaah/commit/de501d94b8a92d10da2253660ee8f514bfef6910))

## [0.52.0](https://github.com/buchenberg/yaah/compare/v0.51.2...v0.52.0) (2026-08-07)


### Features

* extract tviewmd markdown renderer and wire into tui2 ([#183](https://github.com/buchenberg/yaah/issues/183)) ([563b032](https://github.com/buchenberg/yaah/commit/563b03298aee467f1efad2c0c20ad00b5ff86607))

## [0.51.2](https://github.com/buchenberg/yaah/compare/v0.51.1...v0.51.2) (2026-08-07)


### Code Refactoring

* break tools→agent/subagent cycle, extract control types, unify compaction ([#176](https://github.com/buchenberg/yaah/issues/176)) ([bdd9122](https://github.com/buchenberg/yaah/commit/bdd9122f90395bad98295f4c2c30be189c5e006b))

## [0.51.1](https://github.com/buchenberg/yaah/compare/v0.51.0...v0.51.1) (2026-08-07)


### Code Refactoring

* architectural hardening — slog, sentinel errors, prompts init ([#171](https://github.com/buchenberg/yaah/issues/171)) ([a261b38](https://github.com/buchenberg/yaah/commit/a261b385e35c59d966c7188a308f6f6d5a7e26f8))

## [0.51.0](https://github.com/buchenberg/yaah/compare/v0.50.0...v0.51.0) (2026-08-06)


### Features

* **mcp:** load servers from config.yaml, add headers, remove legacy manifests ([#164](https://github.com/buchenberg/yaah/issues/164)) ([1d84e92](https://github.com/buchenberg/yaah/commit/1d84e92129e9235829e5f3111f8388565ab12129))
* **tools:** workspace containment with ask fallback ([#163](https://github.com/buchenberg/yaah/issues/163)) ([b8f991a](https://github.com/buchenberg/yaah/commit/b8f991ab3043b16c1cafea263e090c59d20f6bd7))


### Bug Fixes

* **tui:** always show tool results collapsed when verbose is off ([#166](https://github.com/buchenberg/yaah/issues/166)) ([76e078d](https://github.com/buchenberg/yaah/commit/76e078d38976007ab2bf66400b68f4a83598d4aa))
* **tui:** register mouse zones for tools even when verbose is off ([#167](https://github.com/buchenberg/yaah/issues/167)) ([aa570f7](https://github.com/buchenberg/yaah/commit/aa570f783adecaa95a92c56ced776c804ebe53f6))
* update deepseek-v4 context window to 1M tokens ([#165](https://github.com/buchenberg/yaah/issues/165)) ([f03df31](https://github.com/buchenberg/yaah/commit/f03df315089ecf7e4a2def97bdd079334a676a1a))


### Tests

* add coverage for banner, observability, process, toolfmt ([#168](https://github.com/buchenberg/yaah/issues/168)) ([ec9f8a9](https://github.com/buchenberg/yaah/commit/ec9f8a9042d7f2225f17659f8052be7be58d09c6))

## [0.50.0](https://github.com/buchenberg/yaah/compare/v0.49.0...v0.50.0) (2026-08-06)


### Features

* **tui2:** separate task pane, move provider info to InfoPane ([#160](https://github.com/buchenberg/yaah/issues/160)) ([f28736b](https://github.com/buchenberg/yaah/commit/f28736b992d6782e1ecf935796ee328955b7fc12))
* **web:** colon-commands, reasoning toggle, search, UX polish ([#162](https://github.com/buchenberg/yaah/issues/162)) ([abcea76](https://github.com/buchenberg/yaah/commit/abcea76c90d03b078ba9103e402ac46c08eeeea8))

## [0.49.0](https://github.com/buchenberg/yaah/compare/v0.48.1...v0.49.0) (2026-08-06)


### Features

* TUI MCP bridge and quiet mode ([#159](https://github.com/buchenberg/yaah/issues/159)) ([c56af0c](https://github.com/buchenberg/yaah/commit/c56af0c87f4608bb6be5c706f49ec002f858d45e))


### Code Refactoring

* **agent:** extract event types into events/ sub-package ([#157](https://github.com/buchenberg/yaah/issues/157)) ([8f47ce6](https://github.com/buchenberg/yaah/commit/8f47ce6d9cd8fb549446e3a43f791c50f9d9fa5c))

## [0.48.1](https://github.com/buchenberg/yaah/compare/v0.48.0...v0.48.1) (2026-08-05)


### Code Refactoring

* **tools:** split task.go into focused files ([#155](https://github.com/buchenberg/yaah/issues/155)) ([07ea13d](https://github.com/buchenberg/yaah/commit/07ea13d8f5bbbe3cf1031ef8fd7a608dcd401865))

## [0.48.0](https://github.com/buchenberg/yaah/compare/v0.47.3...v0.48.0) (2026-08-05)


### Features

* **tui:** single-line role-colored sub-agents with result expand toggle ([#153](https://github.com/buchenberg/yaah/issues/153)) ([1f5a9ff](https://github.com/buchenberg/yaah/commit/1f5a9ff4d5de71dd7fce03a0ffe3246a597cc0f7))


### Code Refactoring

* **tui:** break up tui.go god file into focused files ([#152](https://github.com/buchenberg/yaah/issues/152)) ([1e04f6c](https://github.com/buchenberg/yaah/commit/1e04f6c0c99acf4a22cf45eea10240125afbae1b))

## [0.47.3](https://github.com/buchenberg/yaah/compare/v0.47.2...v0.47.3) (2026-08-05)


### Code Refactoring

* break up agent_frame.go into focused files ([#149](https://github.com/buchenberg/yaah/issues/149)) ([d9c68e8](https://github.com/buchenberg/yaah/commit/d9c68e80a016fd02c1ae4b9f2579c29ace39deca))

## [0.47.2](https://github.com/buchenberg/yaah/compare/v0.47.1...v0.47.2) (2026-08-05)


### Code Refactoring

* extract LLM compaction from Loop into ContextManager ([#147](https://github.com/buchenberg/yaah/issues/147)) ([e43f55b](https://github.com/buchenberg/yaah/commit/e43f55b0522a8fb194af84780d4b218295370608))

## [0.47.1](https://github.com/buchenberg/yaah/compare/v0.47.0...v0.47.1) (2026-08-05)


### Bug Fixes

* compaction effectiveness, OTel wiring, and context % accuracy across all views ([#145](https://github.com/buchenberg/yaah/issues/145)) ([55b099c](https://github.com/buchenberg/yaah/commit/55b099cb4af4b2b0b0a7e5625621eafd072405c5))

## [0.47.0](https://github.com/buchenberg/yaah/compare/v0.46.0...v0.47.0) (2026-08-04)


### Features

* **tui2:** wire to agent loop, break up god file, add OTel metrics ([#143](https://github.com/buchenberg/yaah/issues/143)) ([496d422](https://github.com/buchenberg/yaah/commit/496d422b519f8c3461fcbf864ba3b3ff3ca34352))

## [0.46.0](https://github.com/buchenberg/yaah/compare/v0.45.5...v0.46.0) (2026-08-04)


### Features

* **tui2:** create message-type components as sub-packages under messages/ ([#139](https://github.com/buchenberg/yaah/issues/139)) ([db15b2a](https://github.com/buchenberg/yaah/commit/db15b2a8e5254a3fe2942dae3f7c5574a401fe91))
* **tui2:** wire todo list into right panel as dynamic component ([#141](https://github.com/buchenberg/yaah/issues/141)) ([f167add](https://github.com/buchenberg/yaah/commit/f167add1bab27654f7e9ecc6909e3a1f5abdc5f0))

## [0.45.5](https://github.com/buchenberg/yaah/compare/v0.45.4...v0.45.5) (2026-08-04)


### Code Refactoring

* **pipeline:** remove sub-agent depth-limit middleware ([#137](https://github.com/buchenberg/yaah/issues/137)) ([31b572f](https://github.com/buchenberg/yaah/commit/31b572ff56f18ea57b10d464db192810d2fd3818))

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
