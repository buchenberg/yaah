# Workflows: security model and threat surface

A workflow file is untrusted input. This document is the threat model the
adversarial security suite (`yaah-0u0v.26`) enforces; §9 of
[workflow-dsl-plan.md](workflow-dsl-plan.md) is the design source and this
is its operational statement. The inventory below stands alone — each row
names the threat, the rule that refuses it, and the test that pins it. The user-facing surface
guide (workflow YAML, the `validate`/`explain`/`run` commands, bench tasks)
is `.27`'s scope and will live here too; today this file covers security.

## Positions

- **The spec proposes; the runtime disposes.** A document is a reviewed
  artifact. It cannot read files, run shell, or call arbitrary tools.
  Its only verbs are dispatch declared roles, emit values, settle
  proposals, and call registry subworkflows.
- **Fail closed.** Every attack in the inventory below either never
  decodes, never resolves to a graph, or produces a diagnostic that names
  the rule (stable `WFxxx` code) and the node path it anchored to. There is
  no code path from a hostile artifact into the checklist.
- **Warnings are host-dependent facts, not verdicts.** Authority and cost
  findings (WF030–WF033, WF035) are printed as warnings by default because
  their truth depends on the host's declared ceilings; `--strict` promotes
  them to refusals. The runtime still enforces the document's *own* declared
  budgets at execution — a spec that runs despite a warning cannot exceed
  the budget it authored.

## Threat inventory

| # | Threat | Defense | Pinned by |
|---|--------|---------|-----------|
| 1 | Oversized spec, node flood, prompt-size bomb, template-depth bomb | decode limits: 1 MiB spec, 256 nodes, 64 KiB prompt, depth 8 | `spec.TestAdvSpec*` |
| 2 | Integer overflow in control fields (`max_iterations`…) | `intValue` range check: out-of-range integers are refused, never truncated (`int64` of an out-of-range float was implementation-defined garbage) | `spec.TestAdvIntegerOverflowIsRefused`, `spec.FuzzParse` |
| 3 | YAML alias-expansion bombs (billion laughs), structural nesting cliffs | yaml.v3's excessive-aliasing and depth limits; pinned so a dependency upgrade cannot silently lose them | `spec.TestAdvSpecAliasExpansionBomb`, `spec.TestAdvSpecStructuralNestingBomb` |
| 4 | Unknown fields, duplicate keys, trailing data | strict decode: unknown field refusal at the exact key | existing decoder tests; fuzz |
| 5 | Role escalation, imposture (ghost role, primary-only persona, path-shaped role names) | WF030 against the host's role registry | `validate.TestAdvRoleEscalation` |
| 6 | World escape: undeclared world, grants that climb out (`..`), absolute paths, drive prefixes, overlapping grants, emit artifact escapes | WF031; world-relative is a fact about path shape and holds without host config | `validate.TestAdvWorldEscape`, `TestAdvGrantEscape`, `TestAdvEmitArtifactEscape` |
| 7 | Network widening: mode more permissive than the host ceiling, broker with no hosts | WF032 | `validate.TestAdvNetworkWidening` |
| 8 | Cost bombs: budget widened past the host, loops × loops over budget, unbounded loops, unbounded fanout, turns/tokens widened past caps | WF033/WF034/WF035; at runtime the declared `max_dispatches` budget is enforced by the scheduler | `validate.TestAdvCostBombs`, `cmd.TestWorkflowRunRefusesCostBombAndEscapes` |
| 9 | Call traversal (`../outside`, `/etc/passwd`, `sub/dir/x`, file extensions) and unregistered names | call names are a registry charset (letters, digits, underscore, hyphen), resolved under `--workflow-root` only; WF002 | `validate.TestAdvCallTraversal` |
| 10 | Invented assert vocabulary (gate `assert` smuggling) | WF037: asserts are a fixed registry, not free-form names | `validate.TestAdvGateInventedAssert` |
| 11 | Inline secrets in spec text | WF036 markers: private keys, bearer lines, AWS `AKIA`, GitHub, GitLab, Slack, npm, Google `ya29.`, `sk-` keys, JWTs | `validate.TestAdvInlineSecretsBattery` |
| 12 | Prompt injection in spec text and interpolated report values | Spec text is **data**: hostile prose validates clean (defence is the dispatch boundary — dedicated roles, belts, jails), but a template reference to an undeclared step never resolves, so injection cannot exfiltrate out-of-scope values | `validate.TestAdvInjectionPayloadIsData` |
| 13 | Decoder/validator panics or hangs on hostile bytes | standing fuzz harnesses: `spec.FuzzParse` (accepted doc must satisfy its own limits and canonicalize), `validate.FuzzValidate` (every finding carries a cataloged code; nil graph ⇒ exactly one WF001) | both fuzz targets, seeds derived from the attack corpus |

## Self-modification gate

When an orchestrator emits a spec, it must travel the same road a human
author's does: `validate` → `explain` → dry-run → approval (or policy) →
`run`. The commands enforce the gate mechanically — `run` runs the same
analysis as `validate` and refuses before any dispatch when the analysis
fails (under `--strict`, on the first warning). There is no API surface that
executes a document the analysis refused.

## Ambulance list (accepted risks)

- Warning-class findings default to proceed: a host that runs a warningful
  document without `--strict` is trusting its own configuration to make the
  warning false. The declared-budget runtime enforcement is the backstop.
- The secret markers are shape heuristics: a credential formatting itself
  outside every marker (e.g., a base64 blob with no shape) is not caught by
  WF036; the emission gate and role confidentiality are the remaining lines.
- yaml.v3 library limits (aliasing, depth) are external: pinned by tests
  today, but a future parser swap must re-earn both pins.
