# Isolated workspace live test

`TestLive_ActivatedSession` in
`internal/tools/workspace_activation_live_test.go` exercises a real
containerd-backed session end to end. The harness is inert by default: it
skips unless its environment knobs are set **and the process runs as root**
(the live harness hard-codes that requirement; ordinary yaah sessions do not
need root). A fast `ok` means it skipped; run with `-v` to see why.

## One-time host setup

The containerd socket is `root:root`, mode 0660, by default. The live harness
still runs under `sudo`; this group setup is for unprivileged `yaah` sessions
using `workspace.backend: containerd`:

```bash
sudo groupadd --system containerd
sudo usermod -aG containerd "$USER"

sudo mkdir -p /etc/containerd
sudo containerd config default | sudo tee /etc/containerd/config.toml >/dev/null
sudo sed -i "/^\[grpc\]/,/^\[ttrpc\]/ s/gid = 0/gid = $(getent group containerd | cut -d: -f3)/" /etc/containerd/config.toml
sudo systemctl restart containerd
newgrp containerd
```

Verify the socket is `root:containerd` and mode 0660. If you have a
customized `/etc/containerd/config.toml`, edit only the `[grpc]` `gid` line
instead of regenerating the file. The `gid` value is an integer.

A temporary alternative is
`sudo setfacl -m u:$USER:rw /run/containerd/containerd.sock`, but it is lost
when the daemon restarts. Membership in the containerd group is
root-equivalent on the host; grant it deliberately.

## Build the workspace image

The image needs `sh`, GNU `stat -c`, GNU `find -printf`, `git`, `rg`,
`base64`, a Go toolchain, and standard coreutils/findutils (`mkdir`, `mktemp`,
`printf`, `rm`, `chmod`, `mv`). `golang:bookworm` has most of that; add
ripgrep and put the Go toolchain on the default `PATH`:

```bash
sudo apt-get install -y podman

cat > /tmp/Dockerfile.yaah-workspace <<'EOF'
FROM docker.io/library/golang:bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ripgrep \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
EOF

podman build -f /tmp/Dockerfile.yaah-workspace -t docker.io/library/yaah-workspace:latest /tmp
rm -f /tmp/yaah-workspace.tar
podman save -o /tmp/yaah-workspace.tar docker.io/library/yaah-workspace:latest
sudo ctr -n yaah-live images import /tmp/yaah-workspace.tar
```

The `-n yaah-live` matters: image references are namespaced in containerd,
and the harness reads the `yaah-live` namespace by default. Importing into
`default` makes the backend fall through to a registry pull. Do not pass
`--no-unpack`; the backend uses a local image only when it is present and
unpacked.

## Run the harness

```bash
sudo ctr -n yaah-live images import /tmp/yaah-workspace.tar && \
sudo env YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock \
     YAAH_LIVE_CONTAINERD_IMAGE=docker.io/library/yaah-workspace:latest \
     go test ./internal/tools/ -run TestLive_ActivatedSession -count=1 -timeout 30m -v
```

The import only needs re-running after rebuilding the image. `go test` under
sudo uses root's module cache, so the first run re-downloads modules. The
`-timeout` above is the Go test bound; the session itself is bounded by
`YAAH_LIVE_CONTAINERD_TIMEOUT` (default `10m`).

## Dispatch-container live test

`TestLive_DispatchContainerProposal` exercises the per-dispatch container
proposal lane: resolve `world: container`, create a dispatch container, run the
body inside it, seal a `GitDelta` manifest, destroy the container, and settle the
manifest onto the host with `select`.

Unlike `TestLive_ActivatedSession`, this test does **not** need root. The
invoking user only needs access to the containerd socket (the `containerd`
group, or an ACL). It uses the same environment knobs; the address and image are
still required:

```bash
env YAAH_LIVE_CONTAINERD_ADDR=/run/containerd/containerd.sock \
    YAAH_LIVE_CONTAINERD_IMAGE=docker.io/library/yaah-workspace:latest \
    go test ./internal/tools/ -run TestLive_DispatchContainerProposal \
    -count=1 -timeout 15m -v
```

It proves a real dispatch container world is not fiction: the body runs
`local=false` inside the container, the manifest is persisted under
`.yaah/proposals/<id>/`, the host tree is unchanged before settlement, and
`select` consumes the artifact and lands the post-image.

## Environment knobs

The knobs below apply to both live tests.

| Knob | Required | Default | Meaning |
|---|---|---|---|
| `YAAH_LIVE_CONTAINERD_ADDR` | yes | — | containerd socket/endpoint |
| `YAAH_LIVE_CONTAINERD_IMAGE` | yes | — | Workspace image (must satisfy the tool contract) |
| `YAAH_LIVE_CONTAINERD_NAMESPACE` | no | `yaah-live` | containerd namespace |
| `YAAH_LIVE_CONTAINERD_SNAPSHOTTER` | no | adapter default (`overlayfs`) | Snapshotter plugin |
| `YAAH_LIVE_CONTAINERD_ROOT` | no | `/workspace` | Containment root inside the sandbox |
| `YAAH_LIVE_CONTAINERD_TIMEOUT` | no | `10m` | Whole-session bound (probe, create, bootstrap, tools, apply-out) |

## What it proves

1. `StartSessionSandbox` through the real registry seam: image probe,
   `Create`, and the copy-in/bootstrap/rebind path.
2. The isolated registry accepts the real sandbox workspace.
3. Tool-shaped work runs inside the container (GNU `stat`, `rg`, writes).
4. A three-file modify/add/delete delta is captured.
5. `apply_out` dry-run and apply land the changes on the host tree.
6. Teardown completes under its own bound.
