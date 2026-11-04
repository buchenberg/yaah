# Architecture Audit — yaah · goatherd-runtime · goatherd-kernel · croft

**Date:** 2026-11-02 · **Method:** six parallel deep audits (one per repo, one per yaah layer, one cross-repo) against a SOLID / clean-architecture rubric, with headline claims re-verified by direct inspection (`staticcheck`, import greps, git history). Read-only; nothing was modified.

**Commits audited** (all repos fast-forwarded to latest `main` before auditing):

| Repo | Commit | Note |
|---|---|---|
| goatherd-kernel | `37fcd4f` | v0.18.0 + 2 docs-only commits |
| goatherd-runtime | `da4e06c` | v0.21.0 + 14 commits (docs + `sandbox/croft` nested module, which yaah already pins at this pseudo-version) |
| croft | `5322438` | v0.9.2-pre + 1 docs-only commit |
| yaah | `3927cc5` | in sync with origin/main |

---

## 1. Executive summary

**The system is architecturally sound, and unusually honest about where it isn't.** The four repos form a genuine platform/product stack rather than four codebases that share a brand:

- **Dependency direction is clean at every level.** Kernel depends on nothing but SQLite. Runtime depends only on the kernel. Croft is standalone. yaah depends on all three, and — verified by grep — *no satellite imports yaah*, and yaah's consumer layer (`tui`, `acp`, `web`, `repl`) contains **zero** goatherd imports: satellite types are contained in adapter packages (`tools`, `agent/pipeline`, `agent/runner`, `recovery`, `supervision`, `jail`, `gitsandbox`) and the composition root.
- **The suspected duplications are mostly not duplications.** yaah is a real host *over* the stack: the kernel store is session-owned via `InitShepherdInfrastructure` (`internal/agent/pipeline/scope_init.go:23-57`), scopes/settlement/supervision come from the runtime, croft is reached only through the runtime adapter. What looks like overlap (gitsandbox, jail, recovery, supervision) is thin host-side decoration, and the deliberate divergences are recorded in a test-enforced ledger (`docs/shepherd-parity.md`, guarded by `internal/parity`) — divergence management as engineering, not accident.
- **Documentation matches code.** Every repo's README/ADR claims that were spot-checked held up (croft's exposure inventory, runtime's capability honesty, kernel's determinism claims). This is rare and worth protecting.

**The debt is concentrated, not diffuse.** Each repo has one or two structural pressure points (a god type, an accidental public seam, an unconsumed layer) plus a small number of sharp edges. One genuine correctness bug and one security-posture gap were found; everything else is maintainability economics.

**Findings by severity** (details in §3):

| Repo | P1 | P2/P3 | Headline |
|---|---|---|---|
| goatherd-kernel | 5 | 5 | Accidental public seam that must be resolved **before** the planned v1.0 API freeze |
| goatherd-runtime | 3 | 6 | `Halt`-on-merged corrupts recovery (only data-loss-capable bug found); nested-module pin lag |
| croft | 1 | 8 | No seccomp / `NoNewPrivileges` on user execs (P1, already gated by ADR-0002) |
| yaah — engine | 3 | 8 | `llm` seam not self-owned; `runner.go` god file; concrete kernel store threaded through configs |
| yaah — periphery | 3 | 9 | `internal/tools` is three packages; wiring stage 6 overload; test-guarded duplication |
| cross-repo | 2 | 4 | gitsandbox root-vs-worktree inconsistency; unconsumed task/run layer |

---

## 2. System map

```
goatherd-kernel  (package trace)         content-addressed SQLite trace store, canonical JSON,
  │  no deps except sqlite               effect bus, execution/history projections, traceui, testkit
  ▲
goatherd-runtime                        scope tree, merge gate, settlement, recovery-as-registry,
  │  requires kernel only               Sandbox seam: git (root) + croft/landlock/winacl/seatbelt
  │                                     (nested modules), substrate/materialize, task, run, supervisor
  ▲                         ▲
  │                         │  (nested module sandbox/croft requires croft)
yaah  ──────────────────────┘           the product: agent loop engine, pipeline middleware,
  requires kernel+runtime+croft          providers, tools, memory, TUI/REPL/web/ACP/MCP surfaces
```

yaah's non-test goatherd imports concentrate in: `internal/tools` (13 files), `cmd/yaah` (7), `internal/agent/pipeline` (5), `internal/agent` (4), `internal/sandboxfake` (3), `internal/agent/runner` (2), and one each in `jail`, `gitsandbox`, `doctor`, `agent/supervision`, `agent/recovery`.

**Version graph (resolved):** kernel v0.18.0 everywhere (no diamond), runtime root v0.21.0, `sandbox/croft` at pseudo-version `da4e06c` (= runtime main), croft v0.9.2-pre, landlock v0.1.2, seatbelt v0.1.0, winacl v0.1.1. Unconsumed satellite commits are docs-only — **rolling yaah's pins forward is currently free**. The one real skew is *inside* runtime: nested modules declare runtime v0.19.0/v0.20.0 while MVS resolves them against v0.21.0 (§4.3).

---

## 3. Findings

### 3.1 goatherd-kernel (37fcd4f)

A disciplined port with excellent parity testing; debt concentrates at the runtime-extraction seam and the god store type. `go vet`/`gofmt` clean.

| # | Sev | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| K1 | P1 | **Accidental public seam heading for the v1.0 freeze.** `ScanRecordsBySchema`, the checkpoint-blob and materialization-ledger functions are exported package functions reaching directly into `store.db`, bypassing transactions, the bus, and authorization. Their comments still claim they are `internal/` "invisible to importers" — that layout died when `trace` became the module root. | `store_read.go:150`, `checkpoint_blob.go:48,77,120`, `materialization_ledger.go:90,116,159`; stale comments at `store_read.go:147-150`, `checkpoint_blob.go:11-15` | Restore real encapsulation (unexported accessor or narrow re-export type) or consciously freeze the surface; fix comments either way. **Time-boxed by docs/DEVELOPMENT.md's v1.0 freeze gate — the only finding that gets permanently more expensive by waiting.** |
| K2 | P1 | **Runtime vocabulary leaks into the kernel.** `materialization_intents` table, `OpMaterialize`, and `MaterializationResult` exist solely to serve the runtime's `Materialize`/`Substrate` contract; a read-only open *fails* if the runtime's tables are absent. `WorkspaceState` is defensible (trace-recordable data); the contract types are not. | `store_schema.go:79-84,142-149,17-28`; `types.go:63`; `materialization_types.go:35` vs README claim at `README.md:13` | Keep the tables (frozen ABI), move outcome/result vocabulary to the runtime, expose opaque ledger get/put over `receipt_json`. |
| K3 | P1 | **61-method god store type, split by file not responsibility — and the split is dead.** 15 `…`/`…Tx` twin method bodies exist to support a transactional path that never landed; staticcheck U1000 confirms **14 unused production members** incl. `q()` (built to unify the twins, never wired), `insertFactRow`, `readRecord`, `nextOwnerOrdinal`, `validateCausalParents`. *(Independently re-verified during this audit.)* | `store.go:175-187`; staticcheck output; e.g. `store_context.go:68` vs `:252` | Delete the dead twins (~350 lines), wire or drop `q()`, then extract consumer-side interfaces (`SliceReader`, `Appender`) where consumers pass the concrete store. |
| K4 | P1 | **Silent wrong-answer path in the read path.** `canonicalFactOrder` swallows the query error and can return a valid-looking empty ordering → empty `Slice`, `err == nil`, from a transient DB failure. | `store_read.go:421-424,437-439` | Return `([]pathEntry, error)`. |
| K5 | P1 | **Bus events diverge from retained records.** Appended events' `CausalParents` omit `CausedByLocalRefs` resolution the durable envelope performs; `PublishFrontier` records never publish bus events at all — SSE consumers can't see frontiers. | `store_append.go:87` vs `:434-475`; `store_frontier.go:168` | Publish from the receipt's `CausalEdges`; route frontier facts through the same publish path; pin with a traceui invariant test. |
| K6 | P2 | Trusted contexts are a vocabulary, not a capability: `TrustedAppendContext`/`TrustedReadContext` are mutable exported vars; several reads skip authorization entirely. Good as type decoration, misdocumented as a boundary. | `types.go:232-244`, `store_append.go:601-611`, `store_read.go:482-493`, `store_frontier.go:103-105` | Make them readonly; state plainly in README that enforcement is Python-parity, not security. |
| K7 | P2 | WAL claim not realized: `SetMaxOpenConns(1)` means readers queue behind writers (busy_timeout never engages); `WithBus` mutates `s.bus` unsynchronized (fine in construct-then-attach, racy otherwise). | `store.go:101-107,72-75` | Fix the comment or the pool; document the `WithBus` idiom. |
| K8 | P2 | Error taxonomy bimodal: typed `AppendIntentConflictError`/`UnknownFactError` beside bare `fmt.Errorf` for law violations; `closurePolicy` is a raw string; tests string-match; runtime forges kernel errors via `NewTraceStoreError` ×14 (a sign it needs error codes). | `store.go:21-52`, `store_append.go:404-431`, `store_read.go:497-509`, `laws_test.go:52-60` | Type the policies; add error codes; stop exporting the constructor. |
| K9 | P2 | N+1 read patterns (`buildSlice` per-record queries, BFS per-fact queries, traceui owner re-reads) and small swallowed errors (`buildSlice` drops unreadable contexts; `anchorForFact` converts failure to `HiddenReason:"unknown"`). | `store_read.go:249-260,96-116,324-330,372-375` | Propagate; batch where it matters. |
| K10 | P2 | `VisibleRecord` too anemic — every consumer immediately type-asserts back to `Record` for the payload. | `execution.go:195-197`, `relations.go:111-113`, `history.go:158-160`, `traceui/records.go:134-138` | Add `Payload() (map[string]any, bool)`. |

**Strengths:** `canonical.go` is exemplary (byte-identical CPython JSON in both escape flavors, CPython float repr, cycle detection, documented refusal to fall back to `encoding/json`); determinism engineered at every map-backed read with Python-semantics citations; hash-pinned golden vectors with provenance discipline (`testkit`); pure fold-based projections with an `EnsureProjectionCompatible` contract that fails loudly; idempotent content-addressed appends with a legacy-digest retirement condition; `traceui` is a genuinely pure read-only projection; bus drop-oldest design is well-tested under `-race`.

### 3.2 goatherd-runtime (da4e06c)

The best-documented codebase of the four — every controversial decision carries inline rationale. One real bug, one structural fragility.

| # | Sev | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| R1 | P1 | **`Halt` on a merged scope corrupts the tree and poisons recovery.** `halt()` guards only `ScopeDiscarded`; a merged scope falls through, gets a `supervisor.halt` record and flips to `ScopeDiscarded` — after which `RecoverScopes` fails *entirely* ("recorded both merged and discarded"). Publicly reachable from yaah's supervisor wiring. No test covers halt-after-merge. | `scope.go:494-520` (guard), `recovery.go:372-374` (poison); reached from `supervisor.go:218-225` and yaah `internal/agent/supervision/service.go:515` | Refuse Halt on terminal states; make recovery quarantine an inconsistent owner (skip + count in `RecoveryStats`) instead of failing every scope. **Only finding in the system that can lose data from one API call.** |
| R2 | P1 | **Nested-module pin lag → untested combinations.** Nested `go.mod`s pin runtime v0.19.0/v0.20.0 while the root is v0.21.0 and MVS force-resolves all backends against v0.21.0 — a version root CI never tested (v0.21.0 retired `CommitMerge`). Constants fork by comment discipline (`execCeiling` mirrored in 5 places). There is **no `go.work`** (only a stale `go.work.sum` — verified), so local nested tests also compile against published pins. | `sandbox/landlock/go.mod`, `sandbox/winacl/go.mod` (v0.19.0), `sandbox/seatbelt/go.mod`, `sandbox/croft/go.mod` (v0.20.0); `landlock/backend.go:47-48` ("cannot consume a new root symbol until the runtime is re-tagged"); CI builds nested modules against pins (`ci.yml:57-67`) | Add a root `go.work` (excluded from module zips; publishing unaffected) + make nested-pin bumping a release invariant + a CI job building each nested module against root `main`. |
| R3 | P1 | **No Sandbox-backend conformance suite.** `conformance_test.go` is a *trace-store* suite. The Sandbox contract's load-bearing invariants (Capture/Diff non-mutation, digest stability, `ErrUnsupported` honesty, capability coherence, Destroy safety) are re-verified ad hoc per backend. The interface is the product; LSP is enforced by prose. | `conformance_test.go:41-67`; disjoint suites in `sandbox/git/backend_test.go` vs `sandbox/croft/backend_test.go` | Ship `sandboxtest.RunSandboxConformance(t, factory)`; require it from every backend (kernel `testkit` precedent). |
| R4 | P2 | Sandbox is a 10-method interface with one behavioral variance not machine-queryable: Apply **reusability** (git: reusable reset; croft: single-use per state) — settlement's `SettleSelect` behaves materially differently by backend and callers can't branch. Optional assertion interfaces (`DeltaApplier`, `StateReleaser`, `Addressed`) are a good ISP pattern already. | `sandbox/sandbox.go:133-194,41-58,171`; `settlement.go:436`; `sandbox/croft/backend.go:196-207` | Add `ReusableStates bool` to `SandboxCapabilities`, or split role interfaces. |
| R5 | P2 | Hub APIs take `*trace.SQLiteTraceStore` concretely (DIP seam exists only in tests) and lifecycle appends/fork-baseline use `context.Background()`; `ScopeManager.Fork` takes no ctx — a fork against a wedged git subprocess is uncancellable through the API. | `scope.go:66,201,281`; `scope_manager.go:19`; `task/task.go:445-460` | Accept the store (kernel has one store; an interface would be speculative) but thread ctx through Fork/Merge/Discard/Halt. |
| R6 | P2 | Supervisor's async intervention channel silently drops interventions **including Halts** (drop-oldest, 64 buffer) with no trace/log/counter. | `supervisor/supervisor.go:191-204` | Separate halt-class signals (unbounded/blocking-with-timeout) from advisory notices; count drops. |
| R7 | P2 | `run`/`task`/`materialize`/`substrate` have **zero consumers** (~2.2k LOC of tested public API; yaah's D6 decision explicitly declined `task`). Honest in README/doc.go, but every breaking change taxes the module version while the surface ages without feedback. | grep of yaah: no imports of the four packages; `doc.go:39-42` | Wire one real composition into yaah (the merge-gate/settlement spine is already consumed) or mark them experimental. |
| R8 | P2 | Swallowed `Digest()` errors record empty digests silently on hard-append paths (seal/settlement/checkpoint); git `Capabilities()` discards the `Available()` reason. | `checkpoint.go:147`, `workspace.go:115`, `settlement.go:251-252,557`, `git/backend.go:360` | Propagate on hard paths (silent audit degradation otherwise). |
| R9 | P2 | Toolchain floor asymmetry (root `go 1.21`, nested up to 1.26.3) — an innocent post-1.21 idiom in root breaks the declared floor. | `go.mod` files | Pick one floor; record it. |

**Strengths:** documentation-as-contract (digest-stability contract, two-pass re-adoption ordering, effect's "what it deliberately is not" preamble); honest capability reporting (git in-place `uncontained`, worktree `buffered`, `contained` only when actually enforceable; croft refuses `Addressed` by test); one-way layering actually holds (hub never imports nested modules; `Detect()` lives in each backend so the root stays ignorant); registries are deliberate and testable; recovery-as-registry is real (baselines/sealed states ride in fork/seal payloads); release engineering (no `replace` in tagged modules, tag-resolution verification); >1.2:1 test-to-code ratio.

### 3.3 croft (5322438)

An unusually honest library whose architecture is strained by success — flat layout and a god type are the debt of a fast-growing single concept.

| # | Sev | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| C1 | P1 | **User exec processes run with the kernel's full syscall surface** — no seccomp profile, no `NoNewPrivileges` (the shaping script's `setpriv` covers only the init chain; execs are separate runc processes). croft's own ADR-0002 records this as gap 1 / prerequisite P1 gating the container-lane shell allowance. | `client.go:509-610` (`initSpecOpts`), `client.go:901-906` (exec spec); `docs/adr/0002-lane-aware-shell-authority.md:80-84,140-143` | Default seccomp profile + `NoNewPrivileges=true` in `initSpecOpts` and the exec spec; configurable only *more* restrictive. |
| C2 | P2 | **`ContainerdSandbox` is a god type; flat package at the edge of justification.** 65 methods across five files (lifecycle state machine, lease owner, two write paths + stdin staging, diff orchestrator, delta applier, authority prober, connection manager). The pure leaves have zero containerd deps and would move mechanically. | `sandbox.go` (30 methods, 1627 L), `diff.go` (19), `client.go` (19 + lease policy mixed with adapters), `authority.go` (4 + a lifecycle method) | Split along file-aligned seams, pure leaves first (`textdiff`, delta algebra, tar parsing, broker net, output capping) — no API change needed; then relocate lease policy next to lifecycle. |
| C3 | P2 | `Config` sprawl (17 fields, six concern groups) with the five validators re-listed verbatim at every generation-starting entry (a fourth must remember by discipline; one asymmetry already unlabeled). | `sandbox.go:68-217,654-668,1022-1036`; `diff.go:506-517` | One composite `Config.validate()` (with a reader-profile variant); group posture fields before field 18 arrives. |
| C4 | P2 | Image contract (11 coreutils) unprobed at provisioning — a BusyBox-without-`base64` image fails deep inside `WriteFile` with an opaque shell error *after* provisioning. | `croft.go:53-62`; `authority.go:151-189` (fail-closed probe pattern already exists) | One `command -v` probe exec at `Create`. |
| C5 | P2 | Broker subsystem: process-global address allocator (hidden global, documented leak-on-unclean-teardown), silent override of caller-supplied `HTTP_PROXY`, full veth/iptables/proxy rebuild on **every** Capture under broker mode. | `broker.go:211-239,362-367`; `client.go:798-810,822-826,882-887` | Injectable allocator; only override proxy env when unset (or log); name the future multi-tenant broker something else. |
| C6 | P3 | `ErrNotImplemented` misnomer — it means *daemon connection failed*, but consumers infer a capability gap. | `sandbox.go:56-59` | Rename (`ErrDaemonUnavailable`) with a one-version alias. |
| C7 | P3 | Digest-stability contract degrades silently without a diff service (fast path mints a new key); off-interface extensions (`ReadFilesAt`, `ListPinnedStates`) unreachable through the runtime adapter's interface seam; `s.states` grows unboundedly between Applies. | `sandbox.go:840-875`; `diff.go:677-693`; `client.go:261-289`; `sandbox.go:1145-1157` | Constructor-level degradation signal; decide the optional-interface story before the multi-tenant broker consumes them. |

**Strengths:** the docs honesty is real and the code matches it (argv-exposure table ↔ the two write paths; "background processes die at Capture" pinned by live tests; the adapter never overclaims containment or evidence — `ContainContained` not `ContainFull`, refuses paddock placement with `ErrUnsupported` rather than downgrading); genuine DIP seams (three narrow consumer-side interfaces with production adapters and invariant-enforcing fakes; `NewWithBackend` + `With*` options); locking discipline documented and *tested* (opMu exclusive/shared split, lock ordering, `context.WithoutCancel` teardown); small, correctly wrapped error taxonomy; unit:code ratio >1:1 with a fake snapshotter that *enforces* overlayfs invariants; the repo metabolizes reviews (docs/plans/history shows the prior review's findings executed with pinning tests).

### 3.4 yaah — engine (`internal/agent` + `types`/`prompts`/`control`/`pubsub`)

The engine-view architecture in `docs/architecture.md` is real, not aspirational. Debt concentrates in three places.

| # | Sev | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| E1 | P1 | **The engine's LLM seam is not self-owned.** `llm` imports the concrete `providers` package (its `StreamProvider.SendStream` returns `<-chan providers.StreamChunk` — the abstraction depends on a concrete) and imports `tools` for heartbeat; the loop itself calls `tools.SendHeartbeat`. *(Independently re-verified.)* | `llm/types.go:6,16-19`, `llm/client.go:13-14,152,182`, `llm/stream.go:14`, `loop.go:290` | Move `StreamChunk` to `internal/types`; inject `SessionIDFn`/`Heartbeat` as `Client` fields. Removes the engine's only non-test edges to `providers`/`tools`. |
| E2 | P1 | **`runner.go` is a 2,085-line god file with ~10 responsibilities** (delegate construction + proposal rehydration, role discovery, dispatch closure, output forensics, retained-context stamping, system-prompt assembly, sandbox/scope-tree creation, per-role registry building with confinement/egress, budget adapters, trace facts, confinement placement). 12 internal + 3 goatherd imports. | `internal/agent/runner/runner.go:152-252,482-787,728-748,865-1052,1067-1250,1297-1505,1627-1819,1820-2085` | Mechanical split along section boundaries (confinement/scope/registry/forensics/prompt) — the package's own `checkpoint.go`/`role_resolver.go` prove the pattern. |
| E3 | P1 | **Concrete kernel store types threaded through every config surface.** `LoopConfig.TraceStore *shepherdtrace.SQLiteTraceStore`, `PipelineConfig.TraceStore`, `DelegateToolOpts.TraceStore`, `WithTraceStore` — the "off" switch is a nil concrete pointer through five structs; `NewShepherdTraceStore` is exported *from* pipeline. | `internal/agent/types.go:250,269`; `pipeline/config.go:109,115`; `runner.go:112`; `options.go:64`; `pipeline/trace.go:16` | A small `TraceSink`/`ScopeSource` interface in `agent`/`pipeline`; kernel adapter in `pipeline/scope_init.go` (already the composition point). |
| E4 | P2 | Three delivery mechanisms (broker events / `control.Msg` / loop hooks) — the taxonomy is defensible but edges bleed: `OnTitle` fires *and* `TitleSetEvent` publishes (one fact, two channels); `OnUsage` is re-wrapped into `control.UsageInfo` at the session layer; `ContinueAfterMaxIter` duplicates `control.Continue` as a second query path; architecture.md understates the hook surface 2×. | `persist.go:75-76` + `lifecycle_init.go:224`; `cmd/yaah/session.go:683`; `loop.go:460` vs `control.go:94-100`; `types.go:107-151` vs `architecture.md:1128` | Converge notifications onto events (`UsageEvent`, delete `OnTitle`); keep hooks for must-answer queries only; fix the doc. |
| E5 | P2 | Exhaustive-event enforcement is a regex over consumer *source text* — a comment mentioning an event counts as "handled"; the consumer list is hardcoded to 4 files. The sealed interface + registry-completeness half is good OCP engineering. | `events/exhaustive_test.go:74-84` | Match `case *agent.` clauses; derive consumers from `agent.View` implementers. |
| E6 | P2 | The generic dispatcher hardcodes knowledge of "delegate" (8 branches on `d.isDelegate`); `admitTool` re-asks admission questions the approval middleware also asks (deliberate, documented, but duplicated gating). | `agent_dispatch.go:57-61,105-176,96-100` | Extract a `DispatchObserver` seam the delegate wiring registers. |
| E7 | P2 | Hidden global role registry (service locator): `defaultRoleReg atomic.Pointer` read mid-dispatch; tests must mutate a global. (On AGENTS.md's allowed list, but the runner already carries 20+ injected opts.) | `subagent/role.go:58,93-98`; `runner.go:261,493` | Thread through `taskRunnerOpts`; keep the global only as the CLI install point. |
| E8 | P2 | `PipelineConfig` is a 30-field callback bag (service locator); `Loop` a ~30-field god struct mixing config, state, DI seams, caches, generators. Documented trade-offs — honest, but the SRP pressure point is the struct, not the functions. | `pipeline/config.go:22-139`; `loop.go:124-203`; `types.go:70-168` | Live with the builder compromise; stop growing `Loop` fields. |
| E9 | P2 | String matching survives where the codebase eliminated it elsewhere: `llm.isDegenerateStream` matches error *text*; `llm.Client.Call` prints to stderr bypassing slog. | `llm/client.go:314,473-487` | Typed sentinel; slog. |
| E10 | P3 | Stale boundary claim: the `SubAgentRoleResolver` adapter's stated purpose ("tools never imports agent/subagent") is void — 5 tools files import it for the world/lane vocabulary. | `runner.go:9-11` vs `tools/retained.go:24`, `proposal.go:9`, `dispatch_container.go:12`, `dispatch_authority.go:8`, `git_delta.go:22` | Move the world vocabulary to a neutral package. |

**Strengths:** sealed event contract with broker delivery correctly inverted (`View` is one method; `TryPublishMustDeliver` gives teardown an honest fallback); `errorclassify` is textbook DIP (consumer-side `statusCoder` interface, dependency-free); `recovery` is an adapter, not duplication (delegates to `goatherd.RecoverScopes`, adds only yaah policy); `budget`/`context` subpackages are actually pure; middleware pipeline with documented review-driven semantics; supervision decoupling; tests drive the public seam; comments carry an audit trail of review findings.

### 3.5 yaah — periphery & composition root

Disciplined periphery with excellent boundary interfaces and resource ownership; debt in one overloaded package, one overloaded wiring stage, and a handful of test-guarded duplications. (Note: `acp/server.go` is 647 lines at HEAD — the 1,559-line figure circulating in stale `.kilo/worktrees` copies is not current.)

| # | Sev | Finding | Evidence | Fix direction |
|---|---|---|---|---|
| P1f | P1 | **`internal/tools` is three packages wearing one name**: leaf tool implementations; the `Workspace`/`PathValidator`/`Registry` substrate port; and the retained-dispatch *runtime* (`retained.go` 1564 L, `proposal*`, `git_delta.go` 1236 L — orchestration consuming `jobs`, `supervision`, scope managers). It has 14 internal imports including inversions of the engine's own layering. | `tools.go`, `workspace.go:847+`, `workspace_sandbox.go`, `retained.go:500+` | Split: `tools` (leaves) / `tools/substrate` (port) / `internal/dispatch` (retained lane, beside `agent/runner`). |
| P2f | P1 | **Wiring stage 6 (`buildLoopInfra`, 477 lines) builds ~8 unrelated infrastructures** (session restore, jobs, trace store + recovery + root scope + supervision + egress broker + task tool + world status) inside the 1267-line `wiring_stages.go`. The staged decomposition and ownership guard are exemplary; the granularity is not. | `wiring_stages.go:566-1043`, `assemble` at `:1043-1267` | Extract `shepherdInfra` sub-stage; target ≤200 lines/stage. |
| P3f | P1 | **World-availability policy exists twice, test-enforced instead of factored** — identical reason strings in `worldinfo.Availability()` and `tools.resolveDispatchWorld`; `jailAvailable()` is a verbatim copy of `worldinfo.JailAvailable()`; `dispatch_world_parity_test.go` guards the drift that a single source would prevent. No import cycle prevents sharing. | `worldinfo/worldinfo.go:183-206,275-282`; `tools/retained.go:808-872` | Share the availability builder + jail probe; keep the parity test as armor. |
| P4f | P2 | Dead abstraction: `memory` repository interfaces (17-method `SessionRepository` + 3 more) have zero consumers — everyone uses `*memory.DB` concretely. The *live* ISP seams are `sessions.Store` (5 methods) and `acp.Session`. | `memory/repository.go:6-52` | Inject at one real seam or delete. |
| P5f | P2 | `memory.go` (1368 L) mixes DB lifecycle, a 317-line inline `migrate()`, scope normalization, memory-entry domain logic, and search. | `memory/memory.go:235-612` | Extract `migrations.go` first. |
| P6f | P2 | Encapsulation leaks: `Registry.PathValidator`/`Registry.Workspace` public mutable fields; ACP downcasts `reg.Get("question").(*tools.QuestionTool)` and assigns `qt.Handler` cross-package. | `tools/tools.go:159,165`; `acp/session_lifecycle.go:697-705` | Private fields + setters; a documented `SetHandler` seam. |
| P7f | P2 | Capability detection by concrete downcast at the provider seam (`cacheControlOptedIn` asserts `*providers.OpenAIClient`, unknown types treated as opted-in); `observability → providers` edge exists solely for `StreamChunk`. | `provider_resolve.go:157-160`; `observability/provider.go:9-18,72-90` | `SupportsCacheControl() bool` capability interface; `StreamChunk` to `types` (pairs with E1). |
| P8f | P2 | Two tool-display summarizers with duplicated branch shapes; two parallel local-HTTP auth stacks (`web.go` token/CSRF vs `mcp/http_server.go` Bearer — different Origin policies, both constant-time). | `web_view.go:522` vs `toolfmt`; `web.go:82-104` vs `mcp/http_server.go:93-110` | `toolfmt.StartSummary`; one shared token helper with one Origin policy. |
| P9f | P2 | `registerServeTools` takes 8 parameters dismantling the `serveState` bundle that exists to hold them (incl. `**agentSession`, `*error`). | `serve.go:99-107,137` | Take `*serveState`. |
| P10f | P2 | One-file-one-concern violations vs AGENTS.md's own bar: `retained.go`, `git_delta.go`, `doctor/checks.go` (940 L, 15 checks), `trace.go` (720 L, four subcommands). Error-reporting bimodal (80 stderr `fmt.Fprintf` vs slog in siblings). | see file list | Split; pick one diagnostic sink per layer. |
| P11f | P2 | `agentSession` is a ~50-field, 5-mutex aggregate (tolerable — it lives in the composition root behind two narrow interfaces); `LoopBuilder`'s 30 exported fields are the same shape pushed into the kernel. | `session.go:54-260` | Acceptable at the root; don't let the pattern spread. |

**Strengths:** dependency direction genuinely clean (no internal package imports `cmd/yaah`; `mcp` has zero internal imports; `providers` depends only on `config`+`types`); consumer-defined interfaces at every boundary (`acp.Session`, `sessions.Store`, `observability.LLMProvider`) with factories; the **startup ownership guard** (wiring.go:127-238 — every error path closes partially-built resources; leak discipline rarely seen in CLIs); tool extension via the `leafTools` factory map; `sandboxfake` a contract-honoring double with an `ExecHook` seam; provider null objects; parity tests guarding the intentional duplications.

### 3.6 Cross-repo

**The product/platform boundary is unusually clean.** Verdicts on the duplication suspects:

| Pair | Verdict | Note |
|---|---|---|
| `gitsandbox` vs runtime `sandbox/git` | **Migrating** — a decorator overriding only `Apply` (untracked-file evacuation), retirement tracked as `yaah-bp4` | **But it wraps only the root sandbox — `ForkIsolated` worktree children settle under raw `git clean -fd` semantics: a rewind that spares orchestrator scratch on the root can still destroy it in a child. This inconsistency is the cross-repo P1.** |
| `jail` vs landlock/winacl/seatbelt | **Complementary** — a 3-entry GOOS→`Detect()` table; the intended host composition point | Lowest-risk seam in the system |
| `memory` (state.db) vs kernel store (trace.db) | **Keep-both, deliberately** — mutable read model vs append-only evidence; `docs/storage.md` names the blockers and the `consolidate-persistence` plan exists | Dual writes can diverge; retention/redaction (`yaah-1e6g`) unfunded; trace keeps deleted-session content forever |
| yaah `pubsub`/events vs kernel effect bus | **Complementary** — UI notifications vs durable trace facts; different payloads, lifetimes, consumers | No wiring needed |
| `agent/recovery` vs `RecoverScopes` | **Complementary** — delegates; adds resolver policy, orphan taxonomy, legacy-schema fallbacks (self-dated "removable at next minor") | Schedule the removals |
| runner/jobs vs runtime `task`+`run` | **Keep-both, documented (D6)** — the one true parallel surface; yaah reimplements `run`'s fork→review→seal shape in `tools/retained.go` | Two orchestration engines age in parallel |
| `agent/supervision` vs runtime `supervisor` | **Complementary bridge** — builds two runtime `Supervisor`s, reuses `DestructiveToolRule`/`Guard`, adds yaah tool-vocabulary matchers | Ledger row "Partial" is accurate |
| `sandboxfake` vs runtime fakes | **Forced duplication** — runtime test doubles are `internal/` (unimportable); every future host reimplements a fake | Export a public double from runtime |
| `jobs`/`process` vs satellites | **No overlap** — orchestrator-tier host concepts (D8) | — |
| `parity` | **Guard, not duplication** — pins go.mod ↔ shepherd-parity.md; keeps documented divergence honest | If the ledger rots, divergence becomes invisible |
| `worldinfo` | **Intra-yaah mirror** — display-layer derivation of the dispatch-world ladder (see P3f) | Hoist to one source |

**Nested-module diamond:** sound in design (nested modules consume the root's types by module path — no type duplication), fragile in execution (pin lag, §3.2 R2). **Boundary leaks:** none found in consumer layers (sole hit is a test file).

---

## 4. Cross-cutting themes

These are the system-level patterns; they are worth more than any single finding.

**4.1 One god aggregate per repo, split by file rather than responsibility.** `SQLiteTraceStore` (61 methods / 10 files, with a dead transactional twin layer), `ContainerdSandbox` (65 methods / 5 files), `runner.go` (2,085 lines / ~10 concerns), `internal/tools` (3 domains), `buildLoopInfra` (8 infrastructures), `agentSession` (~50 fields). All five repos grew the same way: cohesion maintained by comments and file splits long past the point where a module/type boundary was cheaper. The consistent fix is also the same: split along the seams that already exist as file/section boundaries (each repo's own newer files — `checkpoint.go`, `role_resolver.go`, repo-shaped `memory/*_repo.go` — prove the pattern works), and extract consumer-side interfaces where concrete substrate types are threaded through configs (K3, E3, R5).

**4.2 Parity tests as a bandage over unfactored single sources.** `dispatch_world_parity_test.go`, `config_parity_test.go`, the kernel golden vectors, and `internal/parity`'s ledger test. Where the two sides live in *different modules* (kernel vs Python reference), the guard is excellent. Where both sides live in the same repo and nothing prevents an import (worldinfo vs tools, `jailAvailable`), a parity test is a smell wearing a bandage — factor the single source, keep the test as regression armor.

**4.3 The nested-module diamond is the system's one structural fragility.** Correct in principle (dependency footprint, per-platform release cadence), lagging in execution: pins at v0.19/v0.20 against a v0.21 root, constants mirrored five ways by comment discipline, no `go.work` so even *local* nested tests compile against published pins. Two mechanical fixes: a root `go.work` (excluded from module zips; publishing unaffected) and nested-pin bumping as a release invariant.

**4.4 Events and notifications: principled taxonomies with leaky edges.** Kernel bus (durable facts) vs yaah broker (lossy UI notifications) vs `control.Msg` (queries) vs loop hooks — each is defensible, but: the kernel's own bus diverges from its records (K5), yaah's hooks/events overlap (E4), and the supervisor can silently drop Halts (R6). The invariant worth writing down across the system: *a fact has exactly one authoritative channel; everything else is a projection of it.*

**4.5 Error taxonomies: strong cores, bimodal edges.** Sentinel + `%w` + `errors.Is` discipline is real everywhere (runtime's `UnknownSandboxBackendError.Is`, kernel's `unknownFrontierError.Is`, croft's three sentinels). The edges: string-matching in `laws_test` and `llm.isDegenerateStream`, swallowed digest/context errors (R8, K4, K9), one misnomer (`ErrNotImplemented`), and the runtime forging kernel errors 14× because it lacks error codes.

**4.6 Unconsumed platform surface is the biggest strategic ambiguity.** runtime's `task`/`run`/`materialize`/`substrate` (~2.2k LOC, zero hosts, D6 decline documented), croft's `ListPinnedStates` (built for an unbuilt broker), yaah's `memory` repository interfaces (zero consumers). Each is honest, none is free: every one taxes version numbers and reviewer attention while aging without feedback. Each needs a decision — consume, label experimental, or delete — rather than continued maintenance-by-default.

---

## 5. Remediation roadmap

### Phase 1 — this week (small diffs, real risk retired)

1. **R1:** Halt guard on terminal scopes + recovery quarantine (data-loss bug).
2. **K4/K9:** `canonicalFactOrder` returns an error; `buildSlice` propagates context-read failures.
3. **K3 (part):** delete the 14 dead store members staticcheck confirms (incl. `q()` or wire it).
4. **Roll yaah's satellite pins forward** — currently free (all unconsumed commits are docs-only); resets the drift baseline.
5. **R2 (part):** add runtime's root `go.work`; add nested-pin bumping to the release checklist.

### Phase 2 — this month (structural, mechanical)

6. **K1/K2:** the kernel's pre-freeze seam decision — encapsulate or consciously freeze the checkpoint-blob/materialization free functions; move `MaterializationResult`/`OpMaterialize` vocabulary to the runtime; fix the stale `internal/` comments. *Must land before v1.0 freeze.*
7. **E2:** split `runner.go` along its documented sections.
8. **P1f:** split `internal/tools` into leaves / substrate port / dispatch runtime.
9. **E1/P7f:** `StreamChunk` → `internal/types`; inject session-affinity + heartbeat; capability interface instead of provider downcast.
10. **E3:** `TraceSink` interface; kernel store becomes an implementation detail of `pipeline/scope_init.go`.
11. **C1:** croft seccomp + `NoNewPrivileges` (gates ADR-0002's lane work).
12. **P2f:** extract `shepherdInfra` from `buildLoopInfra`.
13. **P3f:** single-source the world-availability ladder in `worldinfo`.
14. **R3:** ship `sandboxtest` conformance suite; require it from git + croft backends.

### Phase 3 — this quarter (decisions, not just refactors)

15. **Close `yaah-bp4` from the runtime side:** host-selectable clean/apply policy in `sandbox/git`, then delete yaah's gitsandbox decorator — fixes the root-vs-worktree Apply inconsistency (cross-repo P1) for free.
16. **Fund persistence convergence:** owner-scoped retention/GC API in the kernel is the prerequisite for redaction and any state.db-as-projection future; until then trace.db retains deleted-session content forever.
17. **Decide `task`/`run`'s fate:** adopt `run`'s review-provenance composition in yaah's settlement path (narrow, high-value step toward the stated convergence) or document the packages reference-only.
18. **Export a public sandbox test double from runtime**; collapse `sandboxfake` onto it; delete the self-dated legacy recovery fallbacks.
19. **R4:** make Apply-reusability machine-queryable; **R6:** stop the supervisor dropping Halts silently; **E5:** harden the exhaustive-event test.

---

## 6. What to protect

The audit found real debt, but the assets below are the reason the debt is fixable — they should not be traded away for speed:

- **Documentation-as-contract** (runtime's inline rationales, croft's exposure inventory, yaah's review-finding audit trail in comments). Several findings were *only findable* because the code states its own intent.
- **The parity-ledger discipline** (`docs/shepherd-parity.md` + `internal/parity`): documented divergence, test-enforced against a moving upstream.
- **The startup ownership guard** in yaah's wiring and croft's teardown/locking discipline — leak discipline in both, tested.
- **Consumer-defined narrow interfaces** at every real boundary (`acp.Session`, `sessions.Store`, `observability.LLMProvider`, croft's three service seams, containment's `Detect()` per platform).
- **Test-to-code ratios above 1:1 everywhere**, with tests that pin behavior (digest stability, non-mutation, settlement consume-once) rather than mirror implementation.
