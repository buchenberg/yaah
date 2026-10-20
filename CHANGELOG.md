# Changelog

## [0.87.3](https://github.com/buchenberg/yaah/compare/v0.87.2...v0.87.3) (2026-10-20)


### Bug Fixes

* **calculate:** reject non-finite and out-of-range int64 operands ([#433](https://github.com/buchenberg/yaah/issues/433)) ([468faab](https://github.com/buchenberg/yaah/commit/468faab300ca1d5ccb6f9bfe82258babd051ffe0))
* **calculate:** validate the shift count conversion too ([#435](https://github.com/buchenberg/yaah/issues/435)) ([4a41c6a](https://github.com/buchenberg/yaah/commit/4a41c6a429f6f4594f3dbcdbfcb7e5bac389cf67))

## [0.87.2](https://github.com/buchenberg/yaah/compare/v0.87.1...v0.87.2) (2026-10-20)


### Documentation

* **prompts:** state that shell commands already start in the cwd ([#430](https://github.com/buchenberg/yaah/issues/430)) ([9acd085](https://github.com/buchenberg/yaah/commit/9acd085703ade2f50d2ce615d7067fad3da0336b))

## [0.87.1](https://github.com/buchenberg/yaah/compare/v0.87.0...v0.87.1) (2026-10-20)


### Bug Fixes

* make context compaction fire and persist in one-shot runs ([#428](https://github.com/buchenberg/yaah/issues/428)) ([71717ef](https://github.com/buchenberg/yaah/commit/71717ef09357f71618ca80e0b2e0832da63409d4))

## [0.87.0](https://github.com/buchenberg/yaah/compare/v0.86.2...v0.87.0) (2026-10-20)


### Features

* add evidence guard for stale exploration ([#425](https://github.com/buchenberg/yaah/issues/425)) ([6852c07](https://github.com/buchenberg/yaah/commit/6852c07e1103fe25ca98caaf333a5533f5a2e0ff))

## [0.86.2](https://github.com/buchenberg/yaah/compare/v0.86.1...v0.86.2) (2026-10-20)


### Bug Fixes

* display cache-inclusive context usage for Anthropic-shaped providers ([#419](https://github.com/buchenberg/yaah/issues/419)) ([842ee62](https://github.com/buchenberg/yaah/commit/842ee622ca29a96c5236bd0de8fc45c2a2532f7c))
* guard process status reads with a mutex ([#421](https://github.com/buchenberg/yaah/issues/421)) ([ef4703d](https://github.com/buchenberg/yaah/commit/ef4703d0d0418f6dd88def00678c758b8effd4a5))
* mint unique process ids under concurrent starts ([#422](https://github.com/buchenberg/yaah/issues/422)) ([217f656](https://github.com/buchenberg/yaah/commit/217f6568e754e6513494d9195394f213236944e7))
* preserve unknown role frontmatter keys on edit ([#423](https://github.com/buchenberg/yaah/issues/423)) ([a3bc524](https://github.com/buchenberg/yaah/commit/a3bc52459ccfa83e6a8a2ca5e4a16e76917deb53))
* reject path traversal in plan names ([#420](https://github.com/buchenberg/yaah/issues/420)) ([e241d87](https://github.com/buchenberg/yaah/commit/e241d876d24210b65f1c1c39f0a1ccb344223b47))

## [0.86.1](https://github.com/buchenberg/yaah/compare/v0.86.0...v0.86.1) (2026-10-20)


### Bug Fixes

* **web:** improve accessibility, markdown spacing, and theme consistency ([#407](https://github.com/buchenberg/yaah/issues/407)) ([41e881d](https://github.com/buchenberg/yaah/commit/41e881d6e0740586a3a173c63e2ef4a3bf9a2b65))

## [0.86.0](https://github.com/buchenberg/yaah/compare/v0.85.2...v0.86.0) (2026-10-20)


### Features

* **tui:** bump tviewmd to v0.3.1 ([#408](https://github.com/buchenberg/yaah/issues/408)) ([97019b4](https://github.com/buchenberg/yaah/commit/97019b4b37db971e2faa3e76a6fa86ea431bc81f))

## [0.85.2](https://github.com/buchenberg/yaah/compare/v0.85.1...v0.85.2) (2026-10-20)


### Bug Fixes

* **tui:** bump tviewmd to v0.3.0 ([#404](https://github.com/buchenberg/yaah/issues/404)) ([86f0703](https://github.com/buchenberg/yaah/commit/86f07034e0a610654a27ded7dea60a318533da82))

## [0.85.1](https://github.com/buchenberg/yaah/compare/v0.85.0...v0.85.1) (2026-10-20)


### Bug Fixes

* **llm:** retry output-budget exhaustion with a doubled cap ([#400](https://github.com/buchenberg/yaah/issues/400)) ([9eeab58](https://github.com/buchenberg/yaah/commit/9eeab58b12cbf78ad1d5f8e06808ee266f9c7da5))
* **tui:** make between-run subagent delivery view-safe ([#403](https://github.com/buchenberg/yaah/issues/403)) ([0bc68c8](https://github.com/buchenberg/yaah/commit/0bc68c822296a06530e8d3f062edb9b2af2c1485))

## [0.85.0](https://github.com/buchenberg/yaah/compare/v0.84.0...v0.85.0) (2026-10-20)


### Features

* **tui:** show subagents only in the dedicated pane ([#401](https://github.com/buchenberg/yaah/issues/401)) ([893335d](https://github.com/buchenberg/yaah/commit/893335de43f42ac8acca639ffdb2bd61cc194313))

## [0.84.0](https://github.com/buchenberg/yaah/compare/v0.83.2...v0.84.0) (2026-10-20)


### Features

* **tui:** single-rune wide emoji for tool icons ([#397](https://github.com/buchenberg/yaah/issues/397)) ([a57f3eb](https://github.com/buchenberg/yaah/commit/a57f3eb06464e1522fc73d7e02ba5a0fc1bad3e9))

## [0.83.2](https://github.com/buchenberg/yaah/compare/v0.83.1...v0.83.2) (2026-10-19)


### Tests

* **tools:** give each speculation attempt distinct content ([#395](https://github.com/buchenberg/yaah/issues/395)) ([12219a1](https://github.com/buchenberg/yaah/commit/12219a1c862bda91e7f630f0cafbbd7c9111c7e2))

## [0.83.1](https://github.com/buchenberg/yaah/compare/v0.83.0...v0.83.1) (2026-10-19)


### Bug Fixes

* **providers:** make thinking mode config-authoritative and stop stripping it ([#392](https://github.com/buchenberg/yaah/issues/392)) ([46b350f](https://github.com/buchenberg/yaah/commit/46b350f5a9430f6796d0f8b9189870d36875f5c8))

## [0.83.0](https://github.com/buchenberg/yaah/compare/v0.82.2...v0.83.0) (2026-10-19)


### Features

* make shepherd tracing integral ([#390](https://github.com/buchenberg/yaah/issues/390)) ([ec4b639](https://github.com/buchenberg/yaah/commit/ec4b639c2bbb5ccd67a866839d2cd43734d1893b))

## [0.82.2](https://github.com/buchenberg/yaah/compare/v0.82.1...v0.82.2) (2026-10-19)


### Bug Fixes

* **tools:** psExecutable returns "" when no PowerShell is installed ([#387](https://github.com/buchenberg/yaah/issues/387)) ([9960736](https://github.com/buchenberg/yaah/commit/9960736026353e12e636d5405ea421e58504f484))

## [0.82.1](https://github.com/buchenberg/yaah/compare/v0.82.0...v0.82.1) (2026-10-19)


### Documentation

* **bench:** record Hermes coding-suite comparison ([592f802](https://github.com/buchenberg/yaah/commit/592f802d2008b2e18fee49309587795590ab0ee9))
* **bench:** record Hermes coding-suite comparison ([88485d6](https://github.com/buchenberg/yaah/commit/88485d678c8aa708d23ccc40645c912175f1cab4))

## [0.82.0](https://github.com/buchenberg/yaah/compare/v0.81.2...v0.82.0) (2026-10-17)


### Features

* **bench:** add orchestration Terminal-Bench suite and token splits ([08f5091](https://github.com/buchenberg/yaah/commit/08f5091c63ba644e6bcd4e2d8227c6af26b24841))
* **bench:** add orchestration Terminal-Bench suite and token splits ([aaa8958](https://github.com/buchenberg/yaah/commit/aaa8958f9a19b36fda823c75dbfabef0264619ee))


### Bug Fixes

* **bench:** address PR review on orchestration verifiers and usage accounting ([13b634c](https://github.com/buchenberg/yaah/commit/13b634c6f906aef60a11a466cbc974f9802007a9))
* **bench:** close remaining review nits ([93bd230](https://github.com/buchenberg/yaah/commit/93bd230a82ac48e3d5f0ce02a1730e4d1c9f7668))
* **bench:** require enforced confinement in orch-confinement ([5d0b51c](https://github.com/buchenberg/yaah/commit/5d0b51ce38f211628978604f5a8c1d6b64c21283))


### Code Refactoring

* **bench:** improve orchestration verifier security and accuracy ([6f601bb](https://github.com/buchenberg/yaah/commit/6f601bb92450cf9d8f0c8330a39e01441ab430b1))


### Documentation

* correct stale commands, defaults, and behavior claims ([6910359](https://github.com/buchenberg/yaah/commit/69103591429db4214335511a4e6661c35a60d521))
* correct who reviews sealed proposal diffs ([ef8ce9f](https://github.com/buchenberg/yaah/commit/ef8ce9fa04279b45414d56f195a3138e3ea9e4cd))
* update README formatting and orchestration descriptions ([2797796](https://github.com/buchenberg/yaah/commit/2797796447761deae935401c7216e706c7a6a8f4))


### Tests

* **bench:** fix orchestration task defects and record full-suite results ([ba41357](https://github.com/buchenberg/yaah/commit/ba41357f91e86ee904c7377c21989f4242b136a5))

## [0.81.2](https://github.com/buchenberg/yaah/compare/v0.81.1...v0.81.2) (2026-10-17)


### Bug Fixes

* attribute run timeouts and let compaction use the provider cap ([911e6b6](https://github.com/buchenberg/yaah/commit/911e6b685dc5616e90b29636382878b6076a4c3b))
* attribute timeouts to the dispatch budget ([ebda825](https://github.com/buchenberg/yaah/commit/ebda8254583d3ee484ea2b94bf10187b1fbf37c3))

## [0.81.1](https://github.com/buchenberg/yaah/compare/v0.81.0...v0.81.1) (2026-10-17)


### Bug Fixes

* default the OTLP endpoint to the HTTP port ([ec56785](https://github.com/buchenberg/yaah/commit/ec56785e0b1da0710f52506debd54abf0eee8e5c))
* strip quotes from plan frontmatter values ([c10b686](https://github.com/buchenberg/yaah/commit/c10b68666ed0ed7be0f9d6c3ef35e259b5362583))


### Documentation

* address review feedback on plans and runbooks ([f1c283f](https://github.com/buchenberg/yaah/commit/f1c283f9dc0924186fca2167a1e2e886e2579ab2))
* address the second review pass ([88be933](https://github.com/buchenberg/yaah/commit/88be933ada4250c31fa4b1811a856f1558656670))
* prepare documentation for 1.0 ([8f7ac3c](https://github.com/buchenberg/yaah/commit/8f7ac3c93006b1f237c74eaad7540485154fd078))
* remove completed plans and historical records ([a4b705e](https://github.com/buchenberg/yaah/commit/a4b705e2d54af4425db8710b73fe32976e692d71))
* rewrite README and audit reference docs for 1.0 ([af54589](https://github.com/buchenberg/yaah/commit/af54589d156f68e4624a2c1689ce8cc34ecfe5d5))

## [0.81.0](https://github.com/buchenberg/yaah/compare/v0.80.0...v0.81.0) (2026-10-17)


### Features

* surface sub-agent dispatch identity in prompts and errors ([b49f5a0](https://github.com/buchenberg/yaah/commit/b49f5a0f30db69e3d62d9e0dae73dd2a46af1038))


### Bug Fixes

* raise the output-token budget and attribute provider failures ([f8c3c51](https://github.com/buchenberg/yaah/commit/f8c3c51325886211e8f3c82eb6ad56bcdc3545e9))
* raise the output-token budget and classify exhaustion ([24ba883](https://github.com/buchenberg/yaah/commit/24ba8835f0d3ba5778e85869f090c8f59a11b68d))

## [0.80.0](https://github.com/buchenberg/yaah/compare/v0.79.13...v0.80.0) (2026-10-17)


### Features

* decode double-escaped edit arguments and explain snap-confine failures ([5f1a3d2](https://github.com/buchenberg/yaah/commit/5f1a3d236a705fb11971b5a5f34d0d06a6178753))
* decode double-escaped edit arguments and explain snap-confine failures ([1938d2e](https://github.com/buchenberg/yaah/commit/1938d2e38195c991fc794c55e9dfd90914d6c1e0))
* keep a timed-out sub-agent's partial output ([daa856d](https://github.com/buchenberg/yaah/commit/daa856ded8ae22e6e816bb556743efb38cc4f01a))
* keep a timed-out sub-agent's partial output ([3095adc](https://github.com/buchenberg/yaah/commit/3095adc38f76926c2335403dd7c1a6c2e45146e2))
* print memory entry IDs in the CLI ([281f372](https://github.com/buchenberg/yaah/commit/281f372452ea2cc3206a04ee71494957f9e3d7ad))
* print memory entry IDs in the CLI ([633667c](https://github.com/buchenberg/yaah/commit/633667c8eb6b6b81842ec15a1433d8b63e7788a0))
* report run status instead of a false green in go_test/staticcheck ([7075730](https://github.com/buchenberg/yaah/commit/7075730f30c1825ff1d3feb0e0fcc225151b86c0))
* report run status instead of a false green in go_test/staticcheck ([1795cac](https://github.com/buchenberg/yaah/commit/1795cac7402f191787b70f57e6a03f2c4bdcc6b8))

## [0.79.13](https://github.com/buchenberg/yaah/compare/v0.79.12...v0.79.13) (2026-10-17)


### Documentation

* finish the wiring-chain rename in architecture.md ([3592a75](https://github.com/buchenberg/yaah/commit/3592a75d1fbe51ae2779e37631c6d95e8c7546dd))
* point the dynamic delegate schema at delegate_schema.go ([60da262](https://github.com/buchenberg/yaah/commit/60da262b5fcfd645e9eaf6ba8787becd4c9d90b5))
* point the dynamic delegate schema at delegate_schema.go ([2feb13a](https://github.com/buchenberg/yaah/commit/2feb13a71f18bdd468c5f018aee4eecde45fd0ef))

## [0.79.12](https://github.com/buchenberg/yaah/compare/v0.79.11...v0.79.12) (2026-10-17)


### Documentation

* speculation is next, not in progress ([027758a](https://github.com/buchenberg/yaah/commit/027758a8ea776b926be1fcfbaa8b4fe9b71066de))
* speculation is the next item, not in progress ([cb808e2](https://github.com/buchenberg/yaah/commit/cb808e2a2ad2782e8b4753b9a68ad278b86dc3e1))

## [0.79.11](https://github.com/buchenberg/yaah/compare/v0.79.10...v0.79.11) (2026-10-17)


### Tests

* name the dispatch accounting test for what it now asserts ([9170a25](https://github.com/buchenberg/yaah/commit/9170a25c0125240d09524fea411ff96af1edd977))

## [0.79.10](https://github.com/buchenberg/yaah/compare/v0.79.9...v0.79.10) (2026-10-17)


### Bug Fixes

* an unset network policy is the config default, not a refusal ([2acaa76](https://github.com/buchenberg/yaah/commit/2acaa76fa9cf220590bc3118d41711675fa8ddcc))

## [0.79.9](https://github.com/buchenberg/yaah/compare/v0.79.8...v0.79.9) (2026-10-16)


### Bug Fixes

* **tests:** assert the shim against the escaper, and make escaping byte-wise ([2e4857d](https://github.com/buchenberg/yaah/commit/2e4857df2ff972a60496732d2889c0d674e101b1))
* **tests:** resolve the temp workdir the way the validator does ([5906f96](https://github.com/buchenberg/yaah/commit/5906f9646505de678ac57821ae5924869c01c0da))

## [0.79.8](https://github.com/buchenberg/yaah/compare/v0.79.7...v0.79.8) (2026-10-16)


### Bug Fixes

* **llm:** a closed errs must not discard a buffered finish chunk ([71f69a7](https://github.com/buchenberg/yaah/commit/71f69a77772a32e4494557f7e49cb31095a4f901))


### Documentation

* **llm:** note the end-of-stream contract on the closed-errs path ([1e4314b](https://github.com/buchenberg/yaah/commit/1e4314bf45f3940266983ee439e24cafa0fb13c5))

## [0.79.7](https://github.com/buchenberg/yaah/compare/v0.79.6...v0.79.7) (2026-10-16)


### Bug Fixes

* make the Phase 3 tests OS-portable ([ba7da8f](https://github.com/buchenberg/yaah/commit/ba7da8fd4b5d380c9baf4c43e34d62edf90f0cf4))

## [0.79.6](https://github.com/buchenberg/yaah/compare/v0.79.5...v0.79.6) (2026-10-16)


### Bug Fixes

* **llm,prompts:** recover attribute-style tool-call dialects, guard markup-only reports, gate prompts on real tool sets ([23db4c8](https://github.com/buchenberg/yaah/commit/23db4c8947012f924894a2d907915679e0f28517))

## [0.79.5](https://github.com/buchenberg/yaah/compare/v0.79.4...v0.79.5) (2026-10-16)


### Code Refactoring

* **memory:** ErrMemoryNotFound sentinel for not-found deletes/updates ([317a109](https://github.com/buchenberg/yaah/commit/317a109e0721cc133c4fb7ca73d681cf425ea57e))


### Tests

* **memory:** assert UpdateMemory not-found wraps ErrMemoryNotFound ([052aa44](https://github.com/buchenberg/yaah/commit/052aa44388948d8c4f765261091f23917c3d56ce))

## [0.79.4](https://github.com/buchenberg/yaah/compare/v0.79.3...v0.79.4) (2026-10-16)


### Bug Fixes

* **memory:** deletes that match nothing must not report success; full IDs everywhere ([9d4b549](https://github.com/buchenberg/yaah/commit/9d4b54990e2a03963512742e62eb3338b3374311)), closes [#328](https://github.com/buchenberg/yaah/issues/328)
* **memory:** stop truncating memory IDs; deletes that match nothing report not-found ([bf2a9b1](https://github.com/buchenberg/yaah/commit/bf2a9b1760e36ff5f2268e16e61e60cf499b8213))
* **tools:** stop truncating memory IDs to 12 chars ([b632c4e](https://github.com/buchenberg/yaah/commit/b632c4ecc55226c2cf9eea05dc59629e9a430c69))

## [0.79.3](https://github.com/buchenberg/yaah/compare/v0.79.2...v0.79.3) (2026-10-16)


### Bug Fixes

* **supervision:** address review — drift notes never pre-empt real errors, warnings channel for informational notes ([55162d6](https://github.com/buchenberg/yaah/commit/55162d6d9214e16d2fa01df7bf14b104472fb3da))
* **supervision:** address review — lossless-or-loud preservation, symlink-safe restores, empty dirs, quarantine denied creates ([7963a22](https://github.com/buchenberg/yaah/commit/7963a22df69f21a42c287c6c7699aee24c1ad0de))
* **supervision:** root workdir dispatches at their worktree; rewinds preserve uncaptured untracked files ([77893b2](https://github.com/buchenberg/yaah/commit/77893b2f9ad3be4e59cd76bb0aec38b6a20cb008))
* **supervision:** root workdir dispatches at their worktree; rewinds preserve uncaptured untracked files ([5c6c25a](https://github.com/buchenberg/yaah/commit/5c6c25a1d5b1cb3a9d1514cc85e18a53775c3304))

## [0.79.2](https://github.com/buchenberg/yaah/compare/v0.79.1...v0.79.2) (2026-10-15)


### Bug Fixes

* **memory,observability:** address review suggestions — limiter lock scope, test poll snapshots, counter test isolation ([224c652](https://github.com/buchenberg/yaah/commit/224c65231a57936a40dcdeb4d8ab1f8eef49e8c0))
* **observability:** mirror accumulated stream usage; normalize Anthropic cache accounting for gen_ai input tokens ([78bbcce](https://github.com/buchenberg/yaah/commit/78bbccef4f0e33b13528788bcfa18c0113decc71))

## [0.79.1](https://github.com/buchenberg/yaah/compare/v0.79.0...v0.79.1) (2026-10-15)


### Bug Fixes

* **agent:** keep sub-agents alive during silent LLM phases; preserve partial output on watchdog kill ([6819ec5](https://github.com/buchenberg/yaah/commit/6819ec545bd30b7807f7077f0259254e520c5023))
* **agent:** keep sub-agents alive during silent LLM phases; preserve partial output on watchdog kill ([fefcd19](https://github.com/buchenberg/yaah/commit/fefcd19dd764b2c8146e46346477b52567bdfbf9)), closes [#309](https://github.com/buchenberg/yaah/issues/309)
* **llm:** ticker heartbeats during silent waits (TTFT, blocking send, retry backoff) ([a332724](https://github.com/buchenberg/yaah/commit/a3327248bf67f947810bb280bcca464e4a6d9d6c))
* **tools,cli:** address review — embedding enablement condition in config show; sandbox write creates parent dirs ([daa8f14](https://github.com/buchenberg/yaah/commit/daa8f14645d5391f20da1831948beaf3bb608651))
* **tools,cli:** create parent dirs in atomic write; reject invalid calculate chars; show embedding config ([5e2a218](https://github.com/buchenberg/yaah/commit/5e2a218418e3a5de6bdab0b32eae8997efbea94a))
* **tools,cli:** create parent dirs in atomic write; reject invalid calculate chars; show embedding config ([ec342b6](https://github.com/buchenberg/yaah/commit/ec342b62ab7737356cd7fab38610522681e7072e))

## [0.79.0](https://github.com/buchenberg/yaah/compare/v0.78.2...v0.79.0) (2026-10-15)


### Features

* **providers:** adopt anthropic-sdk-go for the native Anthropic path ([a1ed879](https://github.com/buchenberg/yaah/commit/a1ed87929c48f4dc090a79f5812dcc3f61936e59))
* **providers:** adopt anthropic-sdk-go for the native Anthropic path ([0ba965b](https://github.com/buchenberg/yaah/commit/0ba965b5c9a9e4e5fb64ffd7af392095b3df160a))


### Bug Fixes

* **llm:** drain stream errors before synthesizing premature close ([65b066f](https://github.com/buchenberg/yaah/commit/65b066fd48730445b69abffc025d030177d4cc8c))
* **providers:** address Kilo re-review on the review-fix commit ([6611fda](https://github.com/buchenberg/yaah/commit/6611fda5362098634ba608274862f9674d2b9dab))
* **providers:** address review findings on the anthropic-sdk path ([8f3470a](https://github.com/buchenberg/yaah/commit/8f3470a7ef8165e15bb164da9c187bb47baaeb4d))

## [0.78.2](https://github.com/buchenberg/yaah/compare/v0.78.1...v0.78.2) (2026-10-13)


### Bug Fixes

* **review:** address PR [#303](https://github.com/buchenberg/yaah/issues/303) findings -- direct-construction docs, nil guard ([89b25d5](https://github.com/buchenberg/yaah/commit/89b25d5d8fb097e100529369d627755f0fd78c91))


### Code Refactoring

* **workspace:** swap the containerd sandbox backend to croft ([7cd6b11](https://github.com/buchenberg/yaah/commit/7cd6b11b62a08e0998e939955400c26959df2fa9))
* **workspace:** swap the containerd sandbox backend to croft ([a7a81d1](https://github.com/buchenberg/yaah/commit/a7a81d1609c20f3a554ecb959a10ed7130775a29))

## [0.78.1](https://github.com/buchenberg/yaah/compare/v0.78.0...v0.78.1) (2026-10-12)


### Documentation

* **bench:** per-task metrics in TB comparison; metrics extractor ([732cadd](https://github.com/buchenberg/yaah/commit/732caddd4ff58df682746636069ef82e470caa2f))
* **bench:** split TB results into RESULTS.md ([e4dd972](https://github.com/buchenberg/yaah/commit/e4dd9722a0d315f9fed259c7e8a02c3310e71f7d))
* **bench:** split TB results into RESULTS.md ([fad1595](https://github.com/buchenberg/yaah/commit/fad1595f1996f519c04974a0958465f5d4cb76d4))


### CI / Build

* **release:** assert tag_name non-empty; clarify fallback comment ([07ea34a](https://github.com/buchenberg/yaah/commit/07ea34a2c6034d9cdf0f59d7524ebd5e4f2e4731))
* **release:** stamp version/commit/date via ldflags ([4ea7e83](https://github.com/buchenberg/yaah/commit/4ea7e830b14a072d5e72b2c49df3132e0791a560))

## [0.78.0](https://github.com/buchenberg/yaah/compare/v0.77.0...v0.78.0) (2026-10-12)


### Features

* benchmark-driven improvements — one-shot non-interactivity, incremental usage flush, cache + sub-agent attribution ([335091a](https://github.com/buchenberg/yaah/commit/335091a7e908392e7c07e7fb845a06a133998c5e))
* benchmark-driven improvements — one-shot non-interactivity, usage flush, cache + sub-agent attribution ([dd038a7](https://github.com/buchenberg/yaah/commit/dd038a7d9c8e7b30d29f224b101ade2837518d7b))


### Bug Fixes

* benchmark improvements review fixes — cache-flow completeness, monotonic flush, serve-mode hooks ([0b58ea7](https://github.com/buchenberg/yaah/commit/0b58ea70a1f8df805c8fdb4f11a48399a30f9357))


### Documentation

* benchmark improvements — orphaned comment, CacheTokens precedence ([47d4186](https://github.com/buchenberg/yaah/commit/47d4186fd5822f75e682733df335b6b85b5c77e6))

## [0.77.0](https://github.com/buchenberg/yaah/compare/v0.76.0...v0.77.0) (2026-10-12)


### Features

* plan 07 phase H4 — retire the shared-global seam ([39cd251](https://github.com/buchenberg/yaah/commit/39cd2518bcb515feb9cbe53e71314e016c5ec02f))
* plan 07 phase H4 — retire the shared-global seam ([876235a](https://github.com/buchenberg/yaah/commit/876235aaa0f9f44488367723a3bf92c969d586ea))
* **runner:** plan 07 phase H6 -- post-halt dispatches refuse loudly ([bbee345](https://github.com/buchenberg/yaah/commit/bbee3457b5fc405f331eea579e07627e883fa29f))
* **runner:** plan 07 phase H6 — post-halt dispatches refuse loudly ([241ee5c](https://github.com/buchenberg/yaah/commit/241ee5c57fc2eedc81172180cc0a4c2cd0955b5e))


### Bug Fixes

* **runner:** H6 -- nil-root guard on the TOCTOU re-checks (review) ([ed0558e](https://github.com/buchenberg/yaah/commit/ed0558e055af0ace66beaf726f0c120f71d31b14))
* **runner:** H6 review -- close the halt TOCTOU on both fork paths; log capture in the test ([b99ba6e](https://github.com/buchenberg/yaah/commit/b99ba6ef3a29ad62d5538a87f166f52af2f595b7))


### Documentation

* agent-instructions review fixes ([e1052db](https://github.com/buchenberg/yaah/commit/e1052dbfcc585d6b71e1ec88e992afd1d3dc3e88))
* plan 07 shipped ([2fbbb82](https://github.com/buchenberg/yaah/commit/2fbbb82b787af868f7ec44f7711d7e7e1c0fee43))
* plan 07 shipped ([01eef09](https://github.com/buchenberg/yaah/commit/01eef09f6ac67cf30daab7b0c249719748419faf))
* plan 07 shipped — fix the PR count (review) ([506cd07](https://github.com/buchenberg/yaah/commit/506cd0723d4335c49415f779ba23471839c4899a))
* shepherd benchmark — complete the S7 redesign ([d1fa113](https://github.com/buchenberg/yaah/commit/d1fa113c27ffa251d03b9283000248247d679264))
* shepherd benchmark — Kilo review fixes ([c974998](https://github.com/buchenberg/yaah/commit/c974998e1d0d2f2c62706eec698083b6d478db37))
* shepherd benchmark — test prompts and an effectiveness harness ([16927ff](https://github.com/buchenberg/yaah/commit/16927ff54c5c75ed85de8594f05b591fac1e92b5))
* shepherd benchmark — test prompts and an effectiveness harness ([11e0706](https://github.com/buchenberg/yaah/commit/11e0706896cdc4dc92e7554f7d2e142818247c72))
* shepherd benchmark — verifier covers untracked files ([aad3acf](https://github.com/buchenberg/yaah/commit/aad3acf8a1586659716c8f10e32c21b0ca2f5917))
* shepherd benchmark review fixes ([41990c0](https://github.com/buchenberg/yaah/commit/41990c016e1c31464d1f031365dc014e438614d3))
* split the isolated-mode delta rows (Kilo review) ([8630741](https://github.com/buchenberg/yaah/commit/8630741d66a6883b15c6fa9ab5fe7bb1056ed55c))
* write up the agent instructions for the supervision surface ([b36d7cc](https://github.com/buchenberg/yaah/commit/b36d7cc158afd469be5119c988b830fbed38f417))
* write up the agent instructions for the supervision surface ([8ad3ad6](https://github.com/buchenberg/yaah/commit/8ad3ad6dd2fb8e3c2d4ea1edb4db19158c715cb1))

## [0.76.0](https://github.com/buchenberg/yaah/compare/v0.75.0...v0.76.0) (2026-10-12)


### Features

* **session:** plan 07 phase H2 -- close the shepherd infrastructure on a clean exit ([a57d5c1](https://github.com/buchenberg/yaah/commit/a57d5c1a2fcd1ec764104e5f1183cb465d50d7d3))
* **session:** plan 07 phase H2 — close the shepherd infrastructure on a clean exit ([2b19c9b](https://github.com/buchenberg/yaah/commit/2b19c9b75667b7d0eb45c5c998553a58b5e3373e))
* **shepherd:** plan 07 phases H3+H5 -- consume the typed partial settlement; log the recovery scan ([4fdbfec](https://github.com/buchenberg/yaah/commit/4fdbfec6f4c8071922bb873be696aa6236447c67))
* **shepherd:** plan 07 phases H3+H5 — typed partial settlement; recovery scan stats ([eea5a7f](https://github.com/buchenberg/yaah/commit/eea5a7f4835f877fdb2f8badd26da29b680d3f7a))


### Bug Fixes

* **session:** H2 -- drain warning reports the effective timeout (review) ([2ff4ff1](https://github.com/buchenberg/yaah/commit/2ff4ff10025c4b5e34c7143446eb8fda00ab52ab))
* **session:** H2 review -- session-owned drain seam, bus probe, global clear, silent idempotence ([9d21db0](https://github.com/buchenberg/yaah/commit/9d21db0cccdaf6f1392ec6e033fee17cdfb2df46))
* **shepherd:** H3+H5 review -- stage-aware ending, stats test, partial-settle test ([076a038](https://github.com/buchenberg/yaah/commit/076a038672f6662e6174dacc16fdc4b28b2cb7c4))


### Documentation

* H1 -- prune scoped to orphans; doctor described as a fresh scan (review) ([782e85b](https://github.com/buchenberg/yaah/commit/782e85b4a5c3700853e9e15bd3b83837d24345c5))
* plan 07 -- address review findings (three fallbacks, warn-only teardown, bus close, kernel stats) ([2ed124e](https://github.com/buchenberg/yaah/commit/2ed124e5534f2e529bba53e74be0116839d64623))
* plan 07 -- ErrRootTerminal cited at runner.go:719 (review) ([c95c541](https://github.com/buchenberg/yaah/commit/c95c54167c1e42ce1514d393320e5eb2564362df))
* plan 07 -- post-adoption hardening (architectural findings F1-F7) ([5529e5d](https://github.com/buchenberg/yaah/commit/5529e5d197ef54cd0fe8349fd1a6e694509ab471))
* plan 07 — post-adoption hardening (F1–F7) ([b0bfe6b](https://github.com/buchenberg/yaah/commit/b0bfe6b38d0d436019203b83746dd5f217da961d))
* plan 07 phase H1 -- freeze the adoption audit; document the supervision lifecycle ([974a2c8](https://github.com/buchenberg/yaah/commit/974a2c88fa7a7158b291cd5a3dcb204a1f91aae7))
* plan 07 phase H1 — freeze the adoption audit; document the supervision lifecycle ([2a0b43e](https://github.com/buchenberg/yaah/commit/2a0b43ed9feb84b2bd34f4224c45f2b2ec60a303))


### Tests

* **recovery:** H5 -- no positive-duration assert on a sub-tick scan (windows ci) ([43687d5](https://github.com/buchenberg/yaah/commit/43687d577e7f583d03db74718bb44adb2d101222))
* **session:** H2 survivor test closes the store before TempDir cleanup (windows ci) ([2dcf9ac](https://github.com/buchenberg/yaah/commit/2dcf9ac5d1a867124dda49c511d6b3cc9ccf228e))
* **session:** H3 -- privileged-runner guard on the teardown-failure injection (review) ([9358d0e](https://github.com/buchenberg/yaah/commit/9358d0edc80e7b90b45ae7a8400fed83e58d382a))
* **session:** H3 -- probe written before the read-only chmod (review follow-up) ([99f97e6](https://github.com/buchenberg/yaah/commit/99f97e6f5adee4ba90d5e51b780fca5ccec5ad4b))

## [0.75.0](https://github.com/buchenberg/yaah/compare/v0.74.0...v0.75.0) (2026-10-11)


### Features

* **shepherd:** plan 06 phase D -- re-pin kernel v0.14.0, retire the worktree record ([a21798f](https://github.com/buchenberg/yaah/commit/a21798faa6b6564346ce0c49fac1750de2b5519d))
* **shepherd:** plan 06 phase D — re-pin kernel v0.14.0, retire the worktree record ([25656a0](https://github.com/buchenberg/yaah/commit/25656a0fcf086b21635d5ade55704f4abd28ef1c))


### Bug Fixes

* **recovery:** trusted repo, not the occupant's; genuine legacy-fallback coverage (review) ([a2d38d6](https://github.com/buchenberg/yaah/commit/a2d38d67a8b268789390ea46aaa91b53471cc800))

## [0.74.0](https://github.com/buchenberg/yaah/compare/v0.73.0...v0.74.0) (2026-10-11)


### Features

* **shepherd:** R3 —  list +  verb ([82bd5b4](https://github.com/buchenberg/yaah/commit/82bd5b46120aef939e6a902bb6fe0c51932a7eb3))


### Bug Fixes

* **shepherd:** R3 -- plain-quoted prune hint ([f02bb51](https://github.com/buchenberg/yaah/commit/f02bb5191d8656c593d8bd754589c20f8221d789))
* **shepherd:** R3 -- symlink-aware worktree ownership check ([2109f50](https://github.com/buchenberg/yaah/commit/2109f50aca6b33a959763d9b332a884c82337641))
* **shepherd:** R3 review round 1 -- ownership verification before deletion ([c3cf354](https://github.com/buchenberg/yaah/commit/c3cf354df5de67cc635f14600aa78204ac34fd9a))

## [0.73.0](https://github.com/buchenberg/yaah/compare/v0.72.0...v0.73.0) (2026-10-11)


### Features

* **shepherd:** R2 -- resume reattach, PruneTerminal on close, doctor check ([d0a8ab9](https://github.com/buchenberg/yaah/commit/d0a8ab97c30c6f48eca20617424e686bb08b3bd5))
* **shepherd:** R2 — resume reattach, PruneTerminal on close, doctor check ([d2a5241](https://github.com/buchenberg/yaah/commit/d2a5241b506f8ef6591455809f41b4d370b4161a))


### Bug Fixes

* **shepherd:** R2 -- restore CreateRootScope's doc comment ([b7bc668](https://github.com/buchenberg/yaah/commit/b7bc6681f13fdd71e7871c4881184d61a8f00d6c))
* **shepherd:** R2 review round 1 -- doctor auto-heal parity, explicit degraded mode, prune after drain ([abdf0eb](https://github.com/buchenberg/yaah/commit/abdf0eba71818c8e15a35004bb5f3ab9c870e9d9))

## [0.72.0](https://github.com/buchenberg/yaah/compare/v0.71.0...v0.72.0) (2026-10-11)


### Features

* **shepherd:** R1 -- RecoverScopes at startup with warn-only orphans ([cfb17e1](https://github.com/buchenberg/yaah/commit/cfb17e106e0455e1e9c5c2d3e771a07c692d2846))
* **shepherd:** R1 — RecoverScopes at startup, warn-only orphans ([870a7c2](https://github.com/buchenberg/yaah/commit/870a7c2d8429b84d9e8248bb2e2083f8f6eb51ea))


### Bug Fixes

* **shepherd:** R1 -- delivery-assertion race in the call-cancellation jobs test ([69f6ef7](https://github.com/buchenberg/yaah/commit/69f6ef7fc325d0182f0704340bf9527053dbd61e))
* **shepherd:** R1 -- same-file worktree matching; usage-attribution test race ([5b0a722](https://github.com/buchenberg/yaah/commit/5b0a7225a50f345415eb6c162c8528a2403fe15d))
* **shepherd:** R1 -- settle the last two jobs-test completion races ([c3921a9](https://github.com/buchenberg/yaah/commit/c3921a98b552fe96774bcd085854750c7bc00550))
* **shepherd:** R1 review round 1 -- strict adoption, inert orphans, loud disable ([4b769f2](https://github.com/buchenberg/yaah/commit/4b769f2d6d38019d790270fcfcc57c6198259d25))
* **shepherd:** R1 review round 2 -- root-mode gate, unadopted bucket, teardown guard ([469a3d2](https://github.com/buchenberg/yaah/commit/469a3d2d4a2771e554de7332c9f5eba8c8b17a10))

## [0.71.0](https://github.com/buchenberg/yaah/compare/v0.70.0...v0.71.0) (2026-10-11)


### Features

* **shepherd:** C2 -- fork/choose via Seal/Settle ([1780c67](https://github.com/buchenberg/yaah/commit/1780c6771d2a5908448a436ee3d95ba2d75808b9))
* **shepherd:** C2 — fork/choose via Seal/Settle (local mode) ([355e9cb](https://github.com/buchenberg/yaah/commit/355e9cb7eb9c692f00205514198ef3d2c371e4ee))


### Bug Fixes

* **shepherd:** C1 review follow-ups — denial completeness and cumulative rollback ([88902b0](https://github.com/buchenberg/yaah/commit/88902b0a6cc39552c7b3226942a5492a93ec4244))
* **shepherd:** C2 -- dirty-fork choose test tolerates Windows capture drift ([b4a3da2](https://github.com/buchenberg/yaah/commit/b4a3da2513df75a87f62501f773ff068d0dff252))
* **shepherd:** C2 -- drop the dirty-fork settle test; document the kernel gap ([0e62d09](https://github.com/buchenberg/yaah/commit/0e62d09e7527610be4b390dfcacb754919987cae))
* **shepherd:** C2 review round 1 -- settlement ordering, baseline parity, digests ([fc70493](https://github.com/buchenberg/yaah/commit/fc70493a392d3d77cc14aa5d958989435a20adbc))
* **shepherd:** C2 review round 2 -- cancel-free recovery, Windows dirty-fork pin ([e01d53a](https://github.com/buchenberg/yaah/commit/e01d53a40477b403e8d1e48efea186d5e5fa9118))

## [0.70.0](https://github.com/buchenberg/yaah/compare/v0.69.0...v0.70.0) (2026-10-11)


### Features

* **shepherd:** C1 -- accept via CommitMerge ([1cca81c](https://github.com/buchenberg/yaah/commit/1cca81cac7131862209130f4ea9a410f70e2b1b8))
* **shepherd:** C1 — accept via CommitMerge (local mode) ([f9493c0](https://github.com/buchenberg/yaah/commit/f9493c0e1bf311d5eec82d17a670534f98713c63))

## [0.69.0](https://github.com/buchenberg/yaah/compare/v0.68.0...v0.69.0) (2026-10-11)


### Features

* **shepherd:** B4 — supervisor status lists rule firings ([3f88b8d](https://github.com/buchenberg/yaah/commit/3f88b8d941a923f47590a5a760fa23b03d649e3c))

## [0.68.0](https://github.com/buchenberg/yaah/compare/v0.67.0...v0.68.0) (2026-10-11)


### Features

* **shepherd:** B3 -- intervention dispatch end-to-end ([2092ca9](https://github.com/buchenberg/yaah/commit/2092ca914f8bac50ac9c324878ffa05f57410396))
* **shepherd:** B3 — intervention dispatch end-to-end ([814084d](https://github.com/buchenberg/yaah/commit/814084d0681011b1936e711e2af05cf5db909c5d))


### Bug Fixes

* **shepherd:** B3 review round 1 -- halt safety, event-named guidance, race-free tests ([6ddc140](https://github.com/buchenberg/yaah/commit/6ddc1406d7d010831dcaefc2d45bd9358c3fd26b))

## [0.67.0](https://github.com/buchenberg/yaah/compare/v0.66.0...v0.67.0) (2026-10-11)


### Features

* **shepherd:** B2 — the blocking destructive guard at sub-agent dispatch ([50d86b8](https://github.com/buchenberg/yaah/commit/50d86b8ea49b9df4171997e90c66a88013230108))


### Bug Fixes

* **shepherd:** B2 review round 1 -- fail closed, background_process, doc fix ([31f5a68](https://github.com/buchenberg/yaah/commit/31f5a685787e4122f63f3d1808f282d47477c0a6))
* **shepherd:** B2 review round 2 -- empty payload fails closed, read-only process actions pass ([105d1f9](https://github.com/buchenberg/yaah/commit/105d1f96c60f53014edaa668bdc9e88afe1ae50d))

## [0.66.0](https://github.com/buchenberg/yaah/compare/v0.65.1...v0.66.0) (2026-10-11)


### Features

* **shepherd:** B1 -- the advisory rules engine, bridged to execution ([316ea63](https://github.com/buchenberg/yaah/commit/316ea6308130882ce35279db3a3086f5a4b97995))
* **shepherd:** B1 — the advisory rules engine, bridged to execution ([a54ae10](https://github.com/buchenberg/yaah/commit/a54ae10e701a50a832194012b7282802f2289cbe))


### Bug Fixes

* **config:** B1 review round 2 -- supervisor validation follows the stanza's contract ([0d7e423](https://github.com/buchenberg/yaah/commit/0d7e423c95950b33df54dcab90ac17d63cb9c495))
* **shepherd:** B1 review round 1 -- lifecycle, precedence, validation, windows ([f9edda2](https://github.com/buchenberg/yaah/commit/f9edda20d4af05390e21bb7b32134b47819ebbba))

## [0.65.1](https://github.com/buchenberg/yaah/compare/v0.65.0...v0.65.1) (2026-10-11)


### Bug Fixes

* **config:** review round 3 -- refuse an empty fifo_dir with the cause named ([cbc22b4](https://github.com/buchenberg/yaah/commit/cbc22b48f96d0c27efe85f90ea7b1ca5d2a057cd))
* **config:** review round 4 -- host-absolute fifo paths in tests ([3c85129](https://github.com/buchenberg/yaah/commit/3c851296780d7fbf434f2e758702f7265d7d64e4))
* **config:** review round 5 -- absolutize the test fifo paths ([dc0271e](https://github.com/buchenberg/yaah/commit/dc0271e02bc554f14715bc5c8ff2d57515f98cab))
* **jobs:** wait for delivery, not just terminal status, in RunnerError test ([fe5d5d1](https://github.com/buchenberg/yaah/commit/fe5d5d175ce6515e368e7ffa12139ed89801429c))
* **workspace:** review round 2 -- home fallback guard and Image passthrough ([27f10f1](https://github.com/buchenberg/yaah/commit/27f10f1a893436a7dff678720bd409392874bd2d))
* **workspace:** review round 2 -- home fallback guard and Image passthrough ([6c2ee3c](https://github.com/buchenberg/yaah/commit/6c2ee3c6e3e6c81209b9a6dedb0f7a6881fa9f0b))

## [0.65.0](https://github.com/buchenberg/yaah/compare/v0.64.0...v0.65.0) (2026-10-11)


### Features

* **workspace:** run isolated sessions unprivileged (fifo_dir + adapter v0.2.1) ([67e741d](https://github.com/buchenberg/yaah/commit/67e741d42ff60cd0a8ee753efa915abb6efaf7fe))
* **workspace:** run isolated sessions unprivileged (fifo_dir + adapter v0.2.1) ([5d260bb](https://github.com/buchenberg/yaah/commit/5d260bb61e0a4fc3664cabe1cf9262e05eae6233))


### Bug Fixes

* **config:** pass a configured fifo_dir through as-is ([84a37d9](https://github.com/buchenberg/yaah/commit/84a37d97b8e4c74c11e65af64c4bccff803f6f45))
* **workspace:** review round 1 -- private fifo default, passthrough test, doc accuracy ([56f0b0c](https://github.com/buchenberg/yaah/commit/56f0b0c82a9ef357eceebc8f3ffdfd0465313eac))

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
