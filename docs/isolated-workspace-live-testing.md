# Isolated workspace: live activation testing

The isolated-workspace activation plan's step 8 (`docs/plans/isolated-workspace-activation.md`
§2.4) requires proof that an isolated session runs a real tool-driven task end to
end — against a real containerd daemon, not the in-memory fake. `TestLive_ActivatedSession`
(`internal/tools/workspace_activation_live_test.go`) is that proof, and this document
is the runbook for executing it.

The harness is inert by default: it skips unless its environment knobs are set and
the process runs as root (the containerd socket requires it). A green `ok` with a
run time under a second means it skipped — run with `-v` to see the skip reason.

## Prerequisites

- A containerd daemon (`/run/containerd/containerd.sock` unless overridden).
- `sudo` — the daemon socket needs root. **Use `sudo env VAR=...`, not `sudo -E`**:
  many sudoers policies refuse to preserve the environment, the knobs vanish, and
  the harness skips silently.
- A workspace image satisfying the D5 contract. Nothing stock carries all of it:
  the contract needs `sh`, GNU `stat -c`, GNU `find -printf`, `git`, `rg`, and a Go
  toolchain — `alpine/git`, for instance, fails on BusyBox `find` (no `-printf`) and
  has no `rg` or `go`.

## Building the D5 image

`golang:bookworm` has the GNU userland, `git`, and `go`; add `ripgrep` and import
the result into containerd:

```bash
# One-time: a builder (any OCI builder that can `save` works; podman is the
# smallest install on a containerd-only host).
sudo apt-get install -y podman

cat > /tmp/Dockerfile.yaah-workspace <<'EOF'
FROM docker.io/library/golang:bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ripgrep \
    && rm -rf /var/lib/apt/lists/*
EOF

podman build -f /tmp/Dockerfile.yaah-workspace -t docker.io/library/yaah-workspace:latest /tmp
podman save -o /tmp/yaah-workspace.tar docker.io/library/yaah-workspace:latest
sudo ctr images import /tmp/yaah-workspace.tar
```

## Running the harness

```bash
sudo env YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock \
     YAAH_LIVE_CONTAINERD_IMAGE=docker.io/library/yaah-workspace:latest \
     go test ./internal/tools/ -run TestLive_ActivatedSession -count=1 -timeout 30m -v
```

Notes:

- `go test` under sudo uses root's module cache; the first run re-downloads
  modules. That is expected.
- The first run also rides a cold image pull inside the test's own timeout; pass a
  matching `go test -timeout` (30m is comfortable).

## Environment knobs

| Knob | Required | Default | Meaning |
|---|---|---|---|
| `YAAH_LIVE_CONTAINERD_ADDR` | yes | — | The containerd socket/endpoint. Without it there is nothing to talk to, and the test skips. |
| `YAAH_LIVE_CONTAINERD_IMAGE` | yes | — | The D5 image. Not defaulted: a stock image that fails the probe would test the probe's refusal, not the activation. |
| `YAAH_LIVE_CONTAINERD_NAMESPACE` | no | `yaah-live` | The containerd namespace the session's containers live in. |
| `YAAH_LIVE_CONTAINERD_SNAPSHOTTER` | no | the adapter's (`overlayfs`) | The snapshotter plugin. |
| `YAAH_LIVE_CONTAINERD_ROOT` | no | `/workspace` | The containment root inside the sandbox. |
| `YAAH_LIVE_CONTAINERD_TIMEOUT` | no | `10m` | The bound for the whole session (probe, create, bootstrap, tool work, apply-out). A cold image pull rides it. |

## What the harness proves

One test, the whole activation story:

1. **Construction** — `StartSessionSandbox` through the real registry seam:
   D5 image probe on a throwaway sandbox, `Create`, and the D1 bootstrap
   (copy-in from a host git work tree, baseline capture, the rebind Apply).
2. **The step-7 gate** — the curated registry accepts the real sandbox workspace
   (`SetWorkspace` with both diagnostic lists empty).
3. **Tool-shaped work in the container** — copy-in fidelity, round-trip writes,
   GNU `stat` and `rg` from the image (not the host).
4. **The delta** — a three-file modify/add/delete against the session baseline,
   and the containerd-only `delta_patch` property asserted on a live capture:
   D1's load-bearing piece.
5. **The accept seam** — `apply_out` dry run, then apply, landing all three
   changes onto the host working tree.
6. **Teardown** — `Destroy` under its own bound.

## Recording the verdict

Paste the result into step 8's entry in
`docs/plans/isolated-workspace-activation.md` — until a green run is recorded
there, step 8's exit criterion is *implemented*, not *met*.
