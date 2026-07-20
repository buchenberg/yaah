# yaah Agent Loop — Mermaid Diagrams

> Auto-generated from current code (`internal/agent/agent.go`, `middleware.go`, `errorclassify/classify.go`).
> If code changes, regenerate these diagrams.

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

    CHECK_TOOLS -->|No: continue| POST_MODEL[pipeline.RunPostModel<br/>approval: gate dangerous tools<br/>loop_detection: SHA-256 check]

    POST_MODEL -->|error| DONE_ERROR
    POST_MODEL -->|ok| EXECUTE[executeAndCollect<br/>run all tool calls in parallel<br/>goroutines + toolSem]

    EXECUTE --> CONFLICT{ConflictTracker?}
    CONFLICT -->|yes + conflicts| INJECT[inject conflict report<br/>as user message]
    CONFLICT -->|no| POST_TOOL
    INJECT --> POST_TOOL[pipeline.RunPostTool<br/>compaction: check LastPromptTokens<br/>after tool results added]

    POST_TOOL -->|error| DONE_ERROR
    POST_TOOL -->|ok| PERSIST[persist new tool result<br/>messages to DB]

    PERSIST --> BUMP[iter++]
    BUMP --> LOOP

    LOOP -->|No: exhausted| MAX_ITER([error: max iterations reached])

    style START fill:#4a9,stroke:#262
    style DONE_OK fill:#4a9,stroke:#262
    style DONE_ERROR fill:#e55,stroke:#622
    style DONE_PROVIDER fill:#e55,stroke:#622
    style MAX_ITER fill:#e55,stroke:#622
    style COMPACTION fill:#f96,stroke:#862
```

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
    participant L as Loop.Run()
    participant M as Middleware
    participant P as Provider
    participant E as errorclassify
    participant T as Tools

    U->>L: prompt

    rect rgb(40, 60, 90)
        Note over L: PrepareStep (middleware)
        L->>M: RunPrepareStep
        M-->>L: compacted/steered messages
    end

    rect rgb(40, 70, 60)
        Note over L: getAssistantMessage retry loop<br/>involves Provider + errorclassify
        loop up to MaxRetries
            L->>P: SendStream / Send
            alt streaming
                P-->>L: SSE chunks (content, reasoning, tool calls)
                L-->>U: OnThinking / OnToken callbacks
            else non-streaming
                P-->>L: ChatResponse
            end

            alt success
                L-->>L: capture usage, set LastPromptTokens
                L->>L: checkTruncatedStream (empty? content_filter? length?)
            else error
                L->>E: Classify(err)
                E-->>L: recovery hints
                alt ShouldCompress
                    L->>L: compactContext at 50%
                    Note over L: attempt-- (free retry)
                else ShouldRotateCred
                    L->>L: swap to fallback provider
                    Note over L: attempt-- (free retry)
                else retryable
                    L->>L: exponential backoff
                else abort
                    L-->>U: error
                end
            end
        end
    end

    alt final response (no tool calls)
        L-->>U: msg.Content
    else has tool calls
        rect rgb(90, 60, 40)
            Note over L: PostModel (middleware)
            L->>M: RunPostModel
            M-->>L: approval gating, loop detection
        end

        rect rgb(60, 60, 40)
            Note over L: Tool Execution
            par parallel goroutines
                L->>T: tool A
            and
                L->>T: tool B
            and
                L->>T: tool C
            end
            T-->>L: results
        end

        alt conflict detected
            L->>L: inject conflict report as user msg
        end

        rect rgb(40, 60, 90)
            Note over L: PostTool (middleware)
            L->>M: RunPostTool
            M-->>L: compacted messages
        end

        Note over L: iter++, loop continues
    end
```

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
