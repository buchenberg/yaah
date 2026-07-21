# yaah Agent Loop — Mermaid Diagrams

> Reflects the **current code on `feature/dual-loop`** (HEAD `d28003f`
> plus uncommitted working-tree edits in `internal/agent/agent.go`,
> `cmd/yaah/doctor.go`, and `internal/agent/*_test.go`). The
> working-tree state implements the **always-on dual-loop** design
> from `docs/plans/2026-07-20-dual-loop-executor-always-on.md` (v2),
> which **supersedes** the v1 plan in
> `docs/plans/2026-07-20-dual-loop-executor-owns-tools.md` (whose
> Tasks 1-2 already landed at `86834ac` and `d28003f`).
>
> Diagrams 1 and 6 describe the dual-loop architecture as it stands
> in `internal/agent/agent.go` after the v2 pivot. Diagrams 2-5
> (retry classification, streaming, compaction, middleware order) are
> unchanged by the dual-loop work and are kept verbatim from their
> prior versions.
>
> **Plan pointers** (for the why, not the what):
> - `docs/plans/2026-07-20-dual-loop-executor-owns-tools.md` — v1, gated; first two tasks shipped as `86834ac` and `d28003f`
> - `docs/plans/2026-07-20-dual-loop-executor-always-on.md` — v2, always-on; supersedes v1; matches current working tree
> - `docs/plans/2026-07-20-dual-loop-executor-owns-tools-review.md` — industry survey that informed the v2 pivot
>
> **Code anchors** for Diagram 1 + 6:
> - `runMiddleware` entry: `internal/agent/agent.go:429`
> - dual-loop routing in `Run()`: `internal/agent/agent.go:497-625`
> - `splitDelegateCalls` / `parseDelegateCall` / `lastUserMessage` helpers: `internal/agent/agent.go:684-720`
> - `wrapExecutorResult` envelope: `internal/agent/agent.go:722-735`
> - `runExecutor`: `internal/agent/agent.go:777`
> - `resolveExecutor`: `internal/agent/agent.go:1801`
> - `buildPlannerToolDefs`: `internal/agent/agent.go:1839`
>
> Source files: `internal/agent/agent.go`, `middleware.go`,
> `errorclassify/classify.go`. If code changes, regenerate these
> diagrams.

---

## Diagram 1: Main Agent Loop (`runMiddleware`)

```mermaid
flowchart TD
    START([User submits prompt]) --> INIT[applyDefaults<br/>MaxIterations=50, RetryBackoff=1s]

    INIT --> SESSION{Running session?}

    SESSION -->|No| NEW[Build messages:<br/>SystemMsg + UserMsg]
    SESSION -->|Yes| APPEND[Append UserMsg to<br/>existing messages]

    NEW --> BUILD[buildPipeline:<br/>steer → followup → compaction<br/>→ approval → loop_detection]
    APPEND --> BUILD

    BUILD --> LOOP{"iter < MaxIterations?"}

    LOOP -->|Yes| TURN_START[emitHook: TurnStart<br/>StartTurn span]

    TURN_START --> PREPARE[pipeline.RunPrepareStep<br/>compaction: check LastPromptTokens<br/>steer: inject steering messages<br/>followup: drain follow-up queue]

    PREPARE -->|error| DONE_ERROR[return error]
    PREPARE -->|ok| GET_MSG[getAssistantMessage<br/>streaming or non-streaming<br/>with retry + error classification]

    GET_MSG -->|error| DONE_PROVIDER[return provider error]
    GET_MSG -->|ok| APPEND_MSG[append assistant msg<br/>persistMessage]

    APPEND_MSG --> CHECK_TOOLS{"len(msg.ToolCalls) == 0?"}

    CHECK_TOOLS -->|Yes: final response| FLUSH{streamed and<br/>msg.Content not empty?}
    FLUSH -->|Yes| ON_FLUSH[OnFlush callback]
    FLUSH -->|No| DONE_OK
    ON_FLUSH --> DONE_OK([return msg.Content])

    CHECK_TOOLS -->|No: continue| SPLIT["splitDelegateCalls(msg.ToolCalls)<br/>delegate, inline = split"]

    SPLIT --> SPLIT_PROBE{"any delegate calls?<br/>and/or any inline calls?"}

    %% ── DELEGATE PATH (always-on; planner keeps full tool set + delegate) ──
    SPLIT_PROBE -->|"delegate ≥ 1"| DEL_LOOP[for each delegate call:<br/>parseDelegateCall → directive, execType<br/>originalIntent = lastUserMessage]

    DEL_LOOP --> RUN_EXEC["runExecutor(turnCtx, directive,<br/>originalIntent, execType)<br/>resolveExecutor(execType):<br/>ExecutorProvider if set else main Provider<br/>uses executorSystemPrompt<br/>chains tools up to MaxInnerIterations"]

    RUN_EXEC -->|error| EXEC_ERR[summary = 'executor error: …']
    RUN_EXEC -->|exhausted| EXEC_EXH[summary += 'budget exhausted']
    RUN_EXEC -->|ok| EXEC_OK[summary = terse structured text]

    EXEC_ERR --> TRUNC[if len>maxInnerSummaryLen:<br/>truncateRunes, truncated=true]
    EXEC_EXH --> TRUNC
    EXEC_OK --> TRUNC

    TRUNC --> WRAP["wrapExecutorResult(summary, exhausted, err, truncated)<br/>→ &lt;executor_result state='completed|error|exhausted'<br/>       truncated='true|false'&gt;…&lt;/executor_result&gt;"]

    WRAP --> INJECT_TOOL["inject as tool message:<br/>Role=tool, Name=delegate<br/>ToolCallID=delegateCall.ID<br/>persistMessage"]

    INJECT_TOOL --> NEXT_DEL{more delegate<br/>calls?}
    NEXT_DEL -->|Yes| DEL_LOOP
    NEXT_DEL -->|No| MIXED_CHECK{"also have inline calls?"}

    MIXED_CHECK -->|No: delegate-only turn| TURN_END_DEL["if turnSpan: turnSpan.End()<br/>continue → next iteration"]

    %% ── INLINE PATH (existing single-loop, full middleware) ──
    MIXED_CHECK -->|Yes| EXEC_INLINE[executeAndCollect<br/>run all inline calls in parallel<br/>goroutines + toolSem]
    SPLIT_PROBE -->|"inline ≥ 1<br/>(no delegate)"| EXEC_INLINE

    EXEC_INLINE --> CONFLICT{ConflictTracker?}
    CONFLICT -->|yes + conflicts| INJECT[inject conflict report<br/>as user message]
    CONFLICT -->|no| POST_TOOL
    INJECT --> POST_TOOL[pipeline.RunPostTool<br/>compaction: check LastPromptTokens<br/>after tool results added]

    POST_TOOL -->|error| DONE_ERROR
    POST_TOOL -->|ok| PERSIST[persist new tool result<br/>messages to DB]

    PERSIST --> BUMP[iter++]
    BUMP --> LOOP

    LOOP -->|No: exhausted| MAX_ITER([error: max iterations reached])

    %% ── tool-set annotation (planner sees full + delegate; executor sees full) ──
    PLAN_TOOLS["Planner Tools = buildPlannerToolDefs()<br/>= buildToolDefs() + delegateToolDef()<br/>(additive: always; no gate)"]:::note -.-> SPLIT
    EXEC_TOOLS["Executor Tools = buildToolDefs()<br/>(full registry, no delegate<br/>→ structurally cannot delegate)"]:::note -.-> RUN_EXEC

    classDef note fill:#eee,stroke:#888,color:#333,font-size:11px

    style START fill:#4a9,stroke:#262
    style DONE_OK fill:#4a9,stroke:#262
    style DONE_ERROR fill:#e55,stroke:#622
    style DONE_PROVIDER fill:#e55,stroke:#622
    style MAX_ITER fill:#e55,stroke:#622
    style RUN_EXEC fill:#f96,stroke:#862
    style WRAP fill:#f96,stroke:#862
    style INJECT_TOOL fill:#f96,stroke:#862
```

**Notes on the dual-loop shape (v2 always-on):**

- The planner is exposed to **both** the full registry and `delegate` —
  `buildPlannerToolDefs()` appends `delegateToolDef()` to
  `buildToolDefs()` unconditionally. There is no `dualLoopActive()`
  gate; the planner chooses inline vs. delegate per-action. (Same
  pattern as opencode `task`, crush `agent`.)
- A single turn may contain **both** delegate and inline calls.
  Delegates run through `runExecutor` (own provider/model if
  configured, else the main one — `resolveExecutor` default-model
  fallback). Inline calls go through `executeAndCollect` with the
  full middleware pipeline (approval, conflict tracking, loop
  detection).
- The executor's tool set is `buildToolDefs()` — `delegate` is never
  registered there, so the executor **structurally cannot delegate**
  (recursion guard by schema; analogous to kilocode's
  `nestedTask(): false`).
- The executor's summary is wrapped in an
  `<executor_result state="…" truncated="…">…</executor_result>`
  envelope and injected as a `tool` message whose `Name` is
  `delegateToolName` and `ToolCallID` matches the planner's delegate
  call — the honest tool-result framing.
- `DisableInnerLoop` is removed entirely (it was test-only, never set
  by production code). The dual-loop is unconditional; opt-out, if
  ever needed, would come from per-call configuration.

---

## Diagram 2: getAssistantMessage — Retry & Error Classification

```mermaid
flowchart TD
    ENTER(["getAssistantMessage(ctx, req)"]) --> PROVIDER{StreamProvider<br/>+ OnToken set?}

    PROVIDER -->|Yes: stream| STREAM[runStream<br/>SSE chunks → callbacks<br/>assembles msg from stream]
    PROVIDER -->|No: REST| REST[Provider.Send<br/>non-streaming API call]

    STREAM --> CHECK_ERR1{success?}
    REST --> CAPTURE[captureUsage<br/>set LastPromptTokens]
    CAPTURE --> CHECK_CHOICES{any choices?}

    CHECK_CHOICES -->|No| ERR_NO_CHOICES[err: no choices]
    CHECK_CHOICES -->|Yes| CHECK_REFUSAL{has refusal?}
    CHECK_REFUSAL -->|Yes| SURFACE[msg.Content = refusal text]
    CHECK_REFUSAL -->|No| CHECK_FILTER{finish=content_filter,<br/>no content?}
    CHECK_FILTER -->|Yes| ERR_FILTER[err: blocked by content filter]
    CHECK_FILTER -->|No| CHECK_LENGTH{finish=length,<br/>tool calls present?}
    CHECK_LENGTH -->|Yes| ERR_LENGTH[err: truncated, discard tool calls]
    CHECK_LENGTH -->|No| CHECK_EMPTY{no content,<br/>no tool calls?}
    CHECK_EMPTY -->|Yes| ERR_EMPTY[err: produced no content]
    CHECK_EMPTY -->|No| OK_REST

    SURFACE --> CHECK_FILTER

    CHECK_ERR1 -->|Yes| OK[return msg, streamed, nil]
    CHECK_ERR1 -->|No| CLASSIFY

    ERR_NO_CHOICES --> CLASSIFY
    ERR_FILTER --> CLASSIFY
    ERR_LENGTH --> CLASSIFY
    ERR_EMPTY --> CLASSIFY

    CLASSIFY["errorclassify.Classify(err, meta)<br/>maps error → recovery hints"] --> SWITCH{which recovery?}

    SWITCH -->|ShouldCompress<br/>+ compactAttempts < 2| COMPACT[compactContext at 50%<br/>compactAttempts++]
    COMPACT --> SHRANK{Messages shrunk?}
    SHRANK -->|Yes| RETRY_NO_COUNT[attempt--; continue]
    SHRANK -->|No| RETRY_NEXT[continue to next attempt]

    SWITCH -->|ShouldRotateCred<br/>+ FallbackProvider<br/>+ not swapped yet| SWAP[swap to FallbackProvider<br/>swap Model if FallbackModel set<br/>providerSwapped = true]
    SWAP --> RETRY_NO_COUNT

    SWITCH -->|ShouldAbort| ABORT[return last error immediately]

    SWITCH -->|Retryable<br/>+ attempts < MaxRetries| BACKOFF[exponential backoff<br/>backoff * 2^attempt<br/>max MaxRetries attempts]
    BACKOFF --> RETRY_NEXT

    SWITCH -->|non-retryable| ABORT

    RETRY_NO_COUNT --> PROVIDER
    RETRY_NEXT --> PROVIDER

    OK_REST --> OK

    style ENTER fill:#4a9,stroke:#262
    style OK fill:#4a9,stroke:#262
    style COMPACT fill:#f96,stroke:#862
    style SWAP fill:#f96,stroke:#862
    style ABORT fill:#e55,stroke:#622
```

---

## Diagram 3: runStream — SSE Streaming Lifecycle

```mermaid
flowchart TD
    ENTER(["runStream(ctx, sp, req)"]) --> OTEL{OtelEnabled?}
    OTEL -->|Yes| START_SPAN[StartStream span]
    OTEL -->|No| SEND

    START_SPAN --> SEND[sp.SendStream<br/>sets stream_options.include_usage]

    SEND --> SELECT{select}

    SELECT -->|chunk from chunks| PROCESS
    SELECT -->|chunks channel closed| CHAN_DONE[FinishStream<br/>streamSpan.End]
    SELECT -->|err from errs| ERR_CHAN{has error?}
    SELECT -->|context cancelled| CTX_ERR[RecordError<br/>streamSpan.End<br/>return ctx.Err]

    ERR_CHAN -->|Yes| ERR_RECORD[RecordError<br/>streamSpan.End<br/>return error]
    ERR_CHAN -->|No| CHAN_DONE

    CHAN_DONE --> CHECK_TRUNC[checkTruncatedStream<br/>empty-response detection<br/>content_filter check<br/>length + tool_calls check]
    CHECK_TRUNC -->|error| ERR_RETURN[return error]
    CHECK_TRUNC -->|ok| OK_RETURN[return assembled msg]

    PROCESS --> FIRST{first token?}
    FIRST -->|Yes| SET_TTFT[set llm.ttft_ms attribute]
    FIRST -->|No| DELTA
    SET_TTFT --> DELTA

    DELTA["process delta:<br/>ReasoningContent → OnThinking<br/>Content → OnToken + tokenCount++<br/>ToolCalls → toolCallMap"]

    DELTA --> HAS_USAGE{has usage?}
    HAS_USAGE -->|Yes| CAPTURE[setStreamUsageAttrs<br/>captureStreamUsage<br/>set LastPromptTokens]
    HAS_USAGE -->|No| HAS_FINISH

    CAPTURE --> HAS_FINISH{chunk.FinishReason?}

    HAS_FINISH -->|set| FINISH_SET[capture usage from this chunk<br/>drain remaining chunks<br/>for range chunks: capture usage<br/>FinishStream<br/>streamSpan.End]
    FINISH_SET --> CHECK_TRUNC

    HAS_FINISH -->|not set| SELECT

    ERR_RETURN --> ERR_DONE([return error])
    OK_RETURN --> OK_DONE([return msg])
    CTX_ERR --> CTX_DONE([return ctx.Err])

    style ENTER fill:#4a9,stroke:#262
    style OK_DONE fill:#4a9,stroke:#262
    style OK_RETURN fill:#4a9,stroke:#262
    style ERR_DONE fill:#e55,stroke:#622
    style ERR_RETURN fill:#e55,stroke:#622
    style CTX_DONE fill:#e55,stroke:#622
    style FINISH_SET fill:#f96,stroke:#862
```

---

## Diagram 4: Compaction Decision Flow

```mermaid
flowchart TD
    subgraph PROACTIVE["Proactive (middleware)"]
        direction TB
        P1["CompactionMiddleware<br/>PrepareStep / PostTool"] --> P2{"ContextWindow set?"}
        P2 -->|No| P_SKIP[skip]
        P2 -->|Yes| P3["threshold = CompactionThreshold<br/>default: 0.6 (60%)"]
        P3 --> P4{"LastPromptTokens set?"}
        P4 -->|Yes| P5["estimated = LastPromptTokens<br/>(API-reported, accurate)"]
        P4 -->|No| P6["estimated = EstimatedTokens()<br/>(chars/4, fallback)"]
        P5 --> P7{"over threshold?"}
        P6 --> P7
        P7 -->|No| P_SKIP
        P7 -->|Yes| COMPACT[compactContext]
    end

    subgraph REACTIVE["Reactive (retry loop)"]
        direction TB
        R1["getAssistantMessage error"] --> R2["errorclassify.Classify(err)"]
        R2 --> R3{"ShouldCompress?<br/>window set?<br/>under 2 attempts?"}
        R3 -->|Yes| R4["compactContext(ctx, 0.5)<br/>aggressive 50% threshold<br/>compactAttempts++"]
        R3 -->|No| R5[try other recoveries<br/>or backoff]
        R4 --> R6{"Messages shrunk?"}
        R6 -->|Yes| R7["req.Messages = new messages<br/>attempt-- (free retry)"]
        R6 -->|No| R8["continue (attempt counts)"]
    end

    COMPACT --> C1{"Messages count ≤ 4?"}
    C1 -->|Yes| C_SKIP[skip: nothing to compact]
    C1 -->|No| C2["keep last 6 messages<br/>send older to LLM for summary"]

    C2 --> C3["LLM: structured summary<br/>Goal / Completed / Active<br/>Pending / Decisions / Files"]

    C3 -->|success| C4["replace old msgs with<br/>SystemMsg(summary) + recent"]
    C3 -->|failure| C5[trimContext: simple truncation<br/>drop oldest messages<br/>until under 80% of window]

    style COMPACT fill:#f96,stroke:#862
    style C4 fill:#4a9,stroke:#262
    style C5 fill:#f96,stroke:#862
```

---

## Diagram 5: Middleware Pipeline Order

```mermaid
flowchart LR
    subgraph DEFAULT["Default Pipeline"]
        direction LR
        STEER["① steer<br/>injects mid-turn<br/>steering messages"] --> FOLLOWUP
        FOLLOWUP["② followup<br/>drains follow-up<br/>message queue"] --> COMPACTION_MW
        COMPACTION_MW["③ compaction<br/>triggers context<br/>compaction if needed"] --> APPROVAL
        APPROVAL["④ approval<br/>gates dangerous<br/>tool execution"] --> LOOPDETECT
        LOOPDETECT["⑤ loop_detection<br/>SHA-256 hash<br/>5 repeats in 10 steps"]
    end

    OPT["Optional middleware"] -.-> PERMISSION["permission<br/>custom tool access rules"]
    OPT -.-> CONCURRENCY["tool_concurrency<br/>max parallel tools"]
    OPT -.-> SUBAGENT["sub_agent<br/>depth-limited nesting"]
    OPT -.-> PROMPTCACHE["prompt_caching<br/>vendor cache markers"]

    style STEER fill:#69f,stroke:#248
    style FOLLOWUP fill:#69f,stroke:#248
    style COMPACTION_MW fill:#f96,stroke:#862
    style APPROVAL fill:#69f,stroke:#248
    style LOOPDETECT fill:#69f,stroke:#248
```

---

## Diagram 6: Complete Turn Lifecycle (Sequence)

```mermaid
sequenceDiagram
    participant U as User
    participant L as Loop.RunMiddleware()
    participant M as Middleware
    participant P as Provider (planner)
    participant E as Provider (executor)
    participant X as Executor (runExecutor)
    participant T as Tools
    participant C as errorclassify

    U->>L: prompt

    rect rgb(40, 60, 90)
        Note over L: PrepareStep (middleware)
        L->>M: RunPrepareStep
        M-->>L: compacted/steered messages
    end

    rect rgb(40, 70, 60)
        Note over L: getAssistantMessage retry loop
        loop up to MaxRetries
            L->>P: SendStream / Send<br/>(Tools = buildPlannerToolDefs():<br/>full + delegate, always)
            alt streaming
                P-->>L: SSE chunks (content, reasoning, tool calls)
                L-->>U: OnThinking / OnToken callbacks
            else non-streaming
                P-->>L: ChatResponse
            end

            alt success
                L-->>L: capture usage, set LastPromptTokens
                L->>L: checkTruncatedStream
            else error
                L->>C: Classify(err)
                C-->>L: recovery hints
                alt ShouldCompress
                    L->>L: compactContext at 50%
                else ShouldRotateCred
                    L->>L: swap to fallback provider
                else retryable
                    L->>L: exponential backoff
                else abort
                    L-->>U: error
                end
            end
        end
    end

    L->>L: append assistant msg, persistMessage

    alt final response (no tool calls)
        L-->>U: msg.Content
    else has tool calls

        L->>L: splitDelegateCalls(msg.ToolCalls)<br/>→ delegateCalls, inlineCalls

        rect rgb(120, 80, 30)
            Note over L,X: DELEGATE PATH (always-on)
            loop for each delegate call
                L->>L: parseDelegateCall(args)<br/>→ directive, executorType
                L->>L: originalIntent = lastUserMessage(messages)

                rect rgb(150, 100, 40)
                    Note over X: runExecutor(turnCtx, directive, originalIntent, executorType)<br/>Tools = buildToolDefs() (NO delegate — recursion guard)<br/>SystemPrompt = executorSystemPrompt
                    X->>E: SendStream / Send
                    E-->>X: response (may chain tool calls)
                    loop while has tool calls & iter < MaxInnerIterations
                        X->>T: tool call (executor owns selection)
                        T-->>X: result (appended to executor's msg history)
                        X->>E: next iteration
                        E-->>X: response
                    end
                    X-->>L: summary, exhausted, err
                end

                L->>L: wrapExecutorResult(summary, exhausted, err, truncated)<br/>→ &lt;executor_result state="…" truncated="…"&gt;…
                L->>L: inject as tool message<br/>(Role=tool, Name=delegate,<br/>ToolCallID=delegateCall.ID)<br/>persistMessage<br/>RecordInnerSummary (if OtelVerbose)
            end
        end

        alt no inline calls (delegate-only turn)
            L->>L: turnSpan.End(); continue → next iteration
            Note over L: skips PostTool/conflict middleware<br/>(no inline tool results to process)
        else has inline calls
            rect rgb(90, 60, 40)
                Note over L: INLINE PATH (existing single-loop)
                L->>T: parallel goroutines — each inline tool
                T-->>L: tool results
            end

            opt conflict detected
                L->>L: inject conflict report as user msg
            end

            rect rgb(40, 60, 90)
                Note over L: PostTool (middleware)
                L->>M: RunPostTool
                M-->>L: compacted messages
            end

            Note over L: iter++, loop continues
        end
    end
```

**Notes on the dual-loop sequence:**

- The planner's `SendStream`/`Send` always uses
  `buildPlannerToolDefs()` — the planner sees the full registry
  *plus* `delegate` unconditionally. There's no gate.
- Delegate calls run **before** inline calls within a turn, but
  both can coexist. The two paths share the same outer-loop turn
  counter (`iter++`) but otherwise have different observability
  surfaces: delegates produce `inner.loop` spans (executor side),
  inline calls produce the outer `agent.turn` tool spans.
- The executor's tool set is `buildToolDefs()` (full registry only)
  — `delegate` is never present in the executor's `Tools` array, so
  the executor structurally cannot delegate.
- The executor's final summary flows back as a `tool` message whose
  `Name` is `delegateToolName` and whose `ToolCallID` matches the
  planner's original `delegate` call — the honest tool-result
  framing that satisfies the OpenAI requirement that every
  assistant `tool_calls` be followed by matching `tool` messages.
- `wrapExecutorResult` emits a structured XML envelope
  (`<executor_result state="…" truncated="…">…</executor_result>`)
  so downstream consumers (TUI, middleware, composability code) can
  programmatically detect state — mirrors opencode's
  `<task id="…" state="…">` wrapping.
- Delegate-only turns skip the conflict-tracking and PostTool
  middleware (no inline tool results to process). Mixed turns
  (delegate + inline) run both paths and process the inline results
  through the full middleware pipeline.

---

## Error Classification Recovery Hints

```mermaid
flowchart TD
    ERR(["provider error"]) --> STATUS{HTTP status code}

    STATUS -->|401| CRED["ShouldRotateCred"]
    STATUS -->|402| BILLING{transient usage limit?}
    BILLING -->|Yes| CRED
    BILLING -->|No| CRED
    STATUS -->|403| BILLING403{"key limit / spending<br/>limit pattern?"}
    BILLING403 -->|Yes| CRED
    BILLING403 -->|No| CRED
    STATUS -->|404| NOTFOUND{billing pattern?}
    NOTFOUND -->|Yes| CRED
    NOTFOUND -->|policy pattern| ABORT["ShouldAbort"]
    NOTFOUND -->|model not found| CRED
    STATUS -->|413| COMPRESS["ShouldCompress"]
    STATUS -->|429| CRED
    STATUS -->|400| BADREQ{"context overflow<br/>pattern in body?"}
    BADREQ -->|Yes| COMPRESS
    BADREQ -->|policy pattern| ABORT
    BADREQ -->|else| ABORT
    STATUS -->|500/502| SVRERR{"context overflow<br/>pattern in body?"}
    SVRERR -->|Yes| COMPRESS
    SVRERR -->|validation error| ABORT
    SVRERR -->|else| RETRY["Retryable: true"]
    STATUS -->|503/529| RETRY
    STATUS -->|other 4xx| ABORT
    STATUS -->|other 5xx| RETRY

    STATUS -->|no HTTP status code| PATTERNS[pattern matching on error message]

    PATTERNS --> EMPTY{"contains<br/>'produced no content'?"}
    EMPTY -->|Yes| COMPRESS
    EMPTY -->|No| PAT{"other patterns"}
    PAT --> CONTEXT{"context overflow<br/>patterns?"}
    CONTEXT -->|Yes| COMPRESS
    CONTEXT -->|No| BILL{"billing patterns?"}
    BILL -->|Yes| CRED
    BILL -->|No| RATE{"rate limit patterns?"}
    RATE -->|Yes| CRED
    RATE -->|No| AUTH{"auth patterns?"}
    AUTH -->|Yes| CRED
    AUTH -->|No| DISCON{"disconnect,<br/>large session?"}
    DISCON -->|Yes| COMPRESS
    DISCON -->|No| TRANSPORT{"transport error type?"}
    TRANSPORT -->|Yes| RETRY
    TRANSPORT -->|No| UNKNOWN["Retryable: true<br/>ReasonUnknown"]

    style COMPRESS fill:#f96,stroke:#862
    style CRED fill:#f96,stroke:#862
    style ABORT fill:#e55,stroke:#622
    style RETRY fill:#69f,stroke:#248
    style UNKNOWN fill:#69f,stroke:#248
```
