# Decision: HTTP predicates run from the yaah process

**Status:** Accepted
**Bead:** `yaah-95ri.14`
**Date:** 2026-11-05

## Question

`command:` and `file:` predicates are evaluated through the session workspace
(the sandbox when the session is isolated). `http:` currently runs in-process
through the SSRF-guarded HTTP client. Should an `http:` predicate instead be
routed through the materialized workspace/container, or be disabled when the
session is isolated?

## Context

- The orchestrator's existing `http` and `webfetch` tools already execute in
  the yaah process. Their requests are not covered by a container's network
  namespace; the SSRF guard and URL validation are the control.
- A wait predicate is a control-plane probe: "is the CI status green?", "did
  the health endpoint come up?" It is usually aimed at a host-reachable
  service, not a service inside the workload's network.
- Routing the probe through the container would require an in-container
  client (curl/wget) or a workspace exec, making the wait depend on the
  workload image containing that tool and on the container's egress policy.
  A container that intentionally has no network would then be unable to report
  its own health through the orchestrator.
- `command:` predicates already provide the in-workload option: a user who
  needs an endpoint reachable only inside the sandbox can wait on
  `command:curl -fsS http://localhost:8080/health`.

## Options

| Option | Decision | Reason |
|---|---|---|
| Route `http:` through the workspace/container | **No** | Requires an in-image client or exec path, inherits the workload's egress policy, and makes a control-plane check depend on the workload network. |
| Disable `http:` in isolated sessions | **No** | Breaks the useful host-reachable CI/health check the orchestrator needs, and is inconsistent with the existing `http`/`webfetch` tools. |
| In-process with the SSRF guard | **Adopt** | Matches the existing network tools, keeps the probe independent of the workload image, and preserves one control for URL safety. |
| Both (route + in-process) | **No** | Two implementations and two failure modes for one predicate. |

## Decision

`http:` predicates run in the yaah process, exactly like the `http` and
`webfetch` tools:

1. Only `GET` is issued; no request body, credentials or arbitrary headers.
2. The URL must be `http`/`https` with a host. The SSRF guard validates
   literal and redirect targets and the dialer refuses blocked IP ranges.
3. The response body is read up to 256 KiB; the evidence keeps a bounded tail.
4. The predicate target is never recorded in a hook event or span attribute;
   only the kind (`http`) and outcome are.
5. A session that needs a workload-internal endpoint uses a `command:`
   predicate (for example `command:curl -fsS http://localhost:8080/health`)
   so the probe runs through the workspace.

This is an explicit exception to the workspace-routing rule, not an accident:
the other two network-facing tools already behave this way, and the predicate
is a control-plane observation rather than workload I/O.

## Revisit criteria

Revisit if any of the following becomes true:

- an isolated session's threat model requires host egress to be denied even
  for the orchestrator's own tools (that is a broader policy change than this
  predicate);
- a wait must observe an endpoint that is reachable only from inside the
  workload and a `command:` probe is not acceptable;
- the SSRF guard gains a per-session egress policy and `http`/`webfetch` are
  moved behind it — `http:` predicates should follow the same policy.
