# Isolated workspace: live activation testing

The isolated-workspace activation plan's step 8 (`docs/plans/isolated-workspace-activation.md`
§2.4) requires proof that an isolated session runs a real tool-driven task end to
end — against a real containerd daemon, not the in-memory fake. `TestLive_ActivatedSession`
(`internal/tools/workspace_activation_live_test.go`) is that proof, and this document
is the runbook for executing it.

The harness is inert by default: it skips unless its environment knobs are set and
the process runs as root (the containerd socket requires it). A green `ok` with a
run time under a second means it skipped — run with `-v` to see the skip reason.

## Daemon socket access (one-time host setup)

The containerd socket is `root:root` mode 0660 by default, so a normal user
— including a plain `yaah tui` with `workspace.backend: containerd` — gets
`connect: permission denied`. The durable fix is a `containerd` group that
the daemon sets its socket's group to:

```bash
# 1. A group for socket access, and put yourself in it
sudo groupadd --system containerd
sudo usermod -aG containerd $USER

# 2. Give containerd a config that sets the socket's group.
# [grpc].gid is an INTEGER, not a name -- resolve the group to its number.
# (The gid lines are indented and there are three of them: [grpc], [ttrpc],
# [debug] -- anchor on the section, not the line start.)
sudo mkdir -p /etc/containerd
sudo containerd config default | sudo tee /etc/containerd/config.toml >/dev/null
sudo sed -i "/^\[grpc\]/,/^\[ttrpc\]/ s/gid = 0/gid = $(getent group containerd | cut -d: -f3)/" /etc/containerd/config.toml

# 3. Restart the daemon and pick up the group (newgrp, or log out and back in)
sudo systemctl restart containerd
newgrp containerd

# 4. Verify: the socket should now be root:containerd, mode 0660
ls -l /run/containerd/containerd.sock
```

That is the whole host setup. Two further root-owned paths exist on the
client side, but both are handled in code, not on the host:

- **The snapshot store.** `oci.WithImageConfig` — what a naive adapter would
  use to build the init spec — resolves the image user's uid/gid and
  supplemental groups by reading `/etc/passwd` and `/etc/group` out of the
  unpacked rootfs *in the client process* (containerd 2.3+ opens the overlay
  snapshot directories directly through `fsview`). The daemon creates every
  snapshot directory `0700 root:root` (MkdirTemp + rename), so no one-time
  chmod can fix an unprivileged client: each new active snapshot would be
  root-only again. The kernel adapter therefore does not use
  `oci.WithImageConfig`; it derives the init spec from the image config blob,
  which is read through the daemon's content store — something socket access
  already authorizes. On adapter versions without this fix, an
  unprivileged session fails its startup probe with
  `create container ...: open /var/lib/containerd/.../snapshots/N/fs: permission denied`.
- **The client FIFO directory.** The containerd *client* creates the task/exec
  stdio FIFOs, and containerd's default (`/run/containerd/fifo`) is
  root-only. yaah normalizes an unset `workspace.containerd.fifo_dir` to
  `$TMPDIR/yaah-fifo-<uid>` — see `docs/configuration.md`. No host setup
  needed.

Notes:

Notes:

- The hand-edit form of step 2 is a single line: in the `[grpc]` section of
  `/etc/containerd/config.toml`, change `gid = 0` to the containerd group's
  number (`getent group containerd | cut -d: -f3`) — the field is an
  integer, so a name string makes the daemon exit on startup.
- Namespaces (`yaah-live` included) live under `/var/lib/containerd` and
  survive the restart; only the socket is recreated.
- Without `newgrp` (or a fresh login), the group membership is not yet in
  your process credentials — the permission error will look like it
  "didn't work" until then.
- A quicker but temporary alternative is
  `sudo setfacl -m u:$USER:rw /run/containerd/containerd.sock`: it works
  immediately but is lost whenever the daemon restarts, because the socket
  is recreated. Prefer the group.
- Membership of the `containerd` group is root-equivalent: anyone who can
  connect to the socket can run a root container. The socket grant above is
  therefore the only privilege yaah's unprivileged sessions need and receive.

## Prerequisites

- A containerd daemon (`/run/containerd/containerd.sock` unless overridden).
- `sudo` — the daemon socket needs root. **Use `sudo env VAR=...`, not `sudo -E`**:
  many sudoers policies refuse to preserve the environment, the knobs vanish, and
  the harness skips silently.
- A workspace image satisfying the D5 contract. Nothing stock carries all of it:
  the contract needs `sh`, GNU `stat -c`, GNU `find -printf`, `git`, `rg`,
  `base64`, and a Go toolchain — `alpine/git`, for instance, fails on BusyBox
  `find` (no `-printf`) and has no `rg` or `go`.

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
    && rm -rf /var/lib/apt/lists/* \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
EOF

podman build -f /tmp/Dockerfile.yaah-workspace -t docker.io/library/yaah-workspace:latest /tmp
rm -f /tmp/yaah-workspace.tar   # podman save refuses to overwrite an existing tarball
podman save -o /tmp/yaah-workspace.tar docker.io/library/yaah-workspace:latest
sudo ctr -n yaah-live images import /tmp/yaah-workspace.tar
```

The symlinks are load-bearing, not cosmetic: raw containerd applies no image
config `ENV` to `Task.Exec`, so the golang image's `PATH=/usr/local/go/bin` never
reaches the session's execs — the probe (and later `go_test`/`go_mod`) sees
`/usr/bin` but not the toolchain at `/usr/local/go/bin`. Symlinking `go` and
`gofmt` into `/usr/local/bin` puts them on the default PATH.

The `-n` matters: image references are namespaced resources in containerd, and the
import must land in the namespace the session reads — the harness default
(`yaah-live`), or whatever `YAAH_LIVE_CONTAINERD_NAMESPACE` is set to. An image
imported into `default` is invisible to a session in `yaah-live`, and the
backend's resolve falls through to a registry pull, which fails with
`insufficient_scope` against Docker Hub for a locally-built tag.

Do not pass `--no-unpack` either: the backend uses a local image only when it is
present *and* unpacked in the session's snapshotter (`GetImage` + `IsUnpacked`),
so an imported-but-unpacked image also falls through to a registry pull.
`ctr images import` unpacks by default, which is what the runbook relies on.

## Running the harness

One block, in order — the import lands the image in the namespace the session
reads, then the run follows it:

```bash
sudo ctr -n yaah-live images import /tmp/yaah-workspace.tar && \
sudo env YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock \
     YAAH_LIVE_CONTAINERD_IMAGE=docker.io/library/yaah-workspace:latest \
     go test ./internal/tools/ -run TestLive_ActivatedSession -count=1 -timeout 30m -v
```

The import only needs re-running after rebuilding the image; the test line alone
is enough for subsequent runs.

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
