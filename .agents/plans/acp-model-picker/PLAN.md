---
name: acp-model-picker
description: Implement ACP model picker via configOptions and session/set_config_option
status: approved
---

## Objective
Implement a model picker in the ACP protocol that allows Zed and other ACP clients to:
1. See available models via `configOptions` in `session/new` and `session/load`
2. Switch models live with `session/set_config_option`
3. Have model selection persist across `session/load` after restart

## Constraints
- internal/acp must remain config-free (dependency injection only)
- Interface segregation: only add what the protocol layer needs
- Fail closed on invalid input
- Never fail/stall session/new or session/load because of provider discovery

## Implementation Tasks
1. Add wire types to protocol.go (SessionConfigOption, SessionConfigSelectOption)
2. Widen acp.Session interface with Model() and SetModel()
3. Implement ModelCatalog interface for model discovery
4. Build model catalog in composition root (cmd/yaah/acp_cmd.go)
5. Add configOptions to SessionNewResult and session/load result
6. Implement session/set_config_option handler in server.go
7. Extend tests in server_test.go and protocol_test.go
8. Update docs/acp.md
9. Handle persistence across session/load (MVP: report live model as currentValue)
10. Run quality gates: gofmt, vet, staticcheck, tests

## Acceptance Criteria (from bead)
- [ ] Zed shows a model selector; picking applies to next prompt
- [ ] session/new and session/load return configOptions with model select option
- [ ] session/set_config_option switches model, echoes full configOptions
- [ ] In-flight turn finishes on old model
- [ ] session/load after restart preserves model (or gap documented)
- [ ] Unknown configId/value → invalid params, state unchanged
- [ ] nil/empty catalog → no configOptions
- [ ] No internal/config or internal/providers imported into internal/acp
- [ ] Tests extended, all quality gates pass
- [ ] docs/acp.md updated
