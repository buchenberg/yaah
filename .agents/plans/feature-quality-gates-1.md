# Plan: Quality Gates + Session Directives

## Goal

Add automatic post-completion validation (quality gates) and runtime
behavior control (directives) so sub-agent output is verified before
reaching the user, and users can adjust agent behavior per-session
without editing config files.

## Scope — One PR

| Component | Lines (est.) | Files |
|-----------|-------------|-------|
| Config: `quality_gates` + `directives` | ~25 | `internal/config/load.go`, `create.go` |
| CLI: `--directive` / `-d` flag | ~10 | `cmd/yaah/root.go` |
| Directives → sub-agent prompts | ~15 | `cmd/yaah/subagent_runner.go` |
| Directives → orchestrator prompt | ~10 | `internal/prompts/` assembly |
| Quality gate dispatch logic | ~80 | `internal/agent/agent_tools.go` |
| Gate result annotation | ~20 | `internal/agent/agent_tools.go` |
| Tests | ~100 | `agent_tools_test.go`, `config` test |
| **Total** | **~260** | **6–7 files** |

---

## Step 1: Config schema

### `internal/config/load.go`

Add to `Defaults` struct:

```go
Directives []string `yaml:"directives"` // injected into all agent prompts
```

Add new top-level config section:

```go
type QualityGates map[string][]string // role → validator roles
```

Add to `AgentConfig`:

```go
QualityGates QualityGates `yaml:"quality_gates"`
```

### Example config

```yaml
agents:
  default:
    directives:
      - "always run tests after implementation"
  quality_gates:
    developer: [tester]
    analyst: []
    reviewer: []
```

---

## Step 2: CLI flag

### `cmd/yaah/root.go`

```go
rootCmd.PersistentFlags().StringArrayVarP(&directives, "directive", "d", nil,
    "session directive injected into all agent prompts (repeatable)")
```

Directives from CLI override (prepend to) config directives.

---

## Step 3: Directives injection

### Sub-agent prompts (`cmd/yaah/subagent_runner.go`)

After the escalation block (~line 276), inject directives:

```go
if len(directives) > 0 {
    sysPrompt += "\n## Session directives\n\n"
    for _, d := range directives {
        sysPrompt += "- " + d + "\n"
    }
    sysPrompt += "\nThese directives apply to this session. Follow them.\n"
}
```

### Orchestrator prompt

Inject into the system prompt assembly (wherever `identity.md` is
concatenated with project context). Same format, appended after identity.

---

## Step 4: Quality gate dispatch

### `internal/agent/agent_tools.go`

After the existing `SubAgentEndEvent` publish and escalation handling
(~line 210), add gate logic:

```go
if isTask && escalation == nil && l.QualityGates != nil {
    if validators, ok := l.QualityGates[taskRole]; ok && len(validators) > 0 {
        for _, validatorRole := range validators {
            gatePrompt := fmt.Sprintf(
                "Validate the following sub-agent output. Run relevant tests, "+
                "check for errors, and report pass/fail.\n\n"+
                "## Sub-agent output (role: %s)\n\n%s",
                taskRole, truncateForGate(res, 8000),
            )
            gateRes, gateErr := l.Registry.Execute(ctx, "spawn_subagent",
                fmt.Sprintf(`{"prompt":%q,"role":%q,"description":"quality gate: %s"}`,
                    gatePrompt, validatorRole, taskRole))
            if gateErr == nil && strings.Contains(gateRes, "FAIL") {
                res += fmt.Sprintf(
                    "\n\n[quality-gate:FAIL] Validator %s found issues:\n%s",
                    validatorRole, truncateForGate(gateRes, 2000))
            }
        }
    }
}
```

### Key design decisions

- Gates run **synchronously** after the sub-agent completes, before the
  result reaches the orchestrator. The orchestrator sees the annotated result.
- Gates are **skipped** when the sub-agent escalated (no point validating
  a failed task).
- Gate output is truncated to avoid context bloat (8k input, 2k annotation).
- Gate failures annotate the result with `[quality-gate:FAIL]` — the
  orchestrator sees this and reports to the user.
- Gate dispatch uses the existing `spawn_subagent` tool infrastructure —
  no new execution path needed.

---

## Step 5: Wire config → Loop

### `internal/agent/agent.go`

Add field:

```go
QualityGates map[string][]string
```

### `internal/agent/options.go`

Add to `LoopConfig`:

```go
QualityGates map[string][]string
Directives   []string
```

### `cmd/yaah/agent_frame.go` + `serve.go`

Wire from config to LoopConfig.

---

## Step 6: Tests

| Test | What |
|------|------|
| `TestQualityGate_dispatchesValidator` | Developer role with `[tester]` gate → validator dispatched |
| `TestQualityGate_skippedOnEscalation` | Escalated sub-agent → no gate |
| `TestQualityGate_noGateConfigured` | Role not in gates map → no dispatch |
| `TestQualityGate_failAnnotation` | Validator returns FAIL → result annotated |
| `TestDirectives_injectedInPrompt` | Directives appear in sub-agent system prompt |
| `TestDirectives_CLIOverridesConfig` | CLI `-d` prepends to config directives |

---

## Out of scope

- Async/parallel gate dispatch (gates run sequentially for simplicity)
- Gate configuration per-project (`.agents/quality.yaml`) — config.yaml only
- Gate retry on transient failure
- Directive composition/chaining semantics (directives are plain strings)

---

## Verification

1. `go build ./...` && `go vet ./...` && `gofmt -l .` — clean
2. `go test ./internal/agent/... ./internal/config/... ./cmd/yaah/...` — pass
3. Manual: configure `quality_gates: {developer: [tester]}`, spawn a
   developer sub-agent that writes code, verify tester auto-dispatches
4. Manual: `yaah -d "prefer table-driven tests" "implement X"`, verify
   directive appears in sub-agent prompt (verbose trace)
