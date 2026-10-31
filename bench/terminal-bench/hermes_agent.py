# hermes agent adapter for Terminal-Bench (installed-agent form).
#
# Hermes (NousResearch/hermes-agent) runs in the task container from a
# prebuilt, self-contained package directory — the same immunity the
# yaah and opencode static binaries have to containers that break
# their Python packaging on purpose (broken-python's fixture deletes
# pip/setuptools, so a pip install fails by design). The package is
# driven through hermes' one-shot mode (`hermes -z`): approvals are
# auto-bypassed, and --usage-file writes a JSON token/cost report to
# the mounted agent-logs path so the accounting reaches the host run
# directory (input/output/cache_read/cache_write/api_calls — a
# token-comparable CLI, unlike opencode's installed-agent path which
# reports nothing).
#
# Since hermes 0.20.x the project treats Python <3.14 as an
# update-bridge only (every core dependency in pyproject carries a
# `python_version >= '3.14'` marker), and 0.22.x stopped publishing to
# PyPI entirely (installer/Docker/Nix channels only; the wheel build
# is guarded). The tarball therefore bundles its own runtime: a
# CPython 3.14 interpreter, the hermes 0.22.5 source checkout, the
# locked dependency tree (uv export from their uv.lock — plain pip
# cannot resolve it), and the shared libraries the interpreter links
# outside its tree (libsqlite3/libssl/etc., minus the glibc family —
# the build base is the oldest glibc any task image uses, so system
# glibc always wins). One tarball, no per-minor split.
#
# Build the package tarball once (host, or any linux container):
#   git clone --depth 1 --branch v0.22.5 \
#     https://github.com/NousResearch/hermes-agent /tmp/hermes-repo
#   COMMIT=$(git -C /tmp/hermes-repo rev-parse HEAD)
#   podman run --rm -e COMMIT="$COMMIT" \
#     -v /tmp/hermes-repo:/src/repo:ro,Z -v /tmp/hermes-build:/out:Z \
#     python:3.14-slim-bookworm bash -euo pipefail -c '
#     export PATH="/root/.local/bin:$PATH"; python3 -m pip install -q uv
#     mkdir -p /opt/hermes
#     cp -a /usr/local /opt/hermes/python            # relocatable docker-python
#     cp -a /src/repo /opt/hermes/repo               # editable layout, fixed path
#     rm -rf /opt/hermes/repo/{tests,apps,website,.github}
#     printf "{\n  \"commit\": \"$COMMIT\",\n  \"updateMechanism\": \"external\",\n  \"baseVersion\": \"0.22.5\",\n  \"displayVersion\": \"0.22.5 (terminal-bench repack)\",\n  \"source\": \"local\",\n  \"branch\": \"v0.22.5\"\n}\n" > /opt/hermes/repo/install-stamp.json
#     (cd /opt/hermes/repo && uv export --frozen --no-dev --no-emit-project -o /tmp/reqs.txt 2>/dev/null)
#     uv pip install -q --python /opt/hermes/python/bin/python3 -r /tmp/reqs.txt
#     uv pip install -q --python /opt/hermes/python/bin/python3 --no-deps -e /opt/hermes/repo
#     mkdir -p /opt/hermes/libs && cd /opt/hermes/python
#     find bin lib -type f \( -name "*.so" -o -name "*.so.*" -o -name "python3.*" \) \
#       | while read -r f; do ldd "$f" 2>/dev/null; done | grep "=>" | cut -d" " -f3 \
#       | grep -E "^/" | grep -v "^/opt/hermes/python" | sort -u \
#       | grep -vE "lib(c|m|dl|rt|pthread|mvec)[^/]*\.so" \
#       | while read -r l; do cp -L "$l" /opt/hermes/libs/; done
#     tar czf /out/hermes-pkg-py314.tar.gz -C /opt hermes'
# (python:3.14-slim-bookworm is deliberate: glibc 2.36 is the oldest
# any task image at the pin uses, so the bundled binaries run on all
# of them; a trixie build would not run on ubuntu-24-04.)
#
# Import path: hermes_agent:HermesAgent (put this directory on
# PYTHONPATH and pass --agent-import-path to `tb run`).
#
# Required environment/kwargs:
#   - api_key:  the provider API key (--agent-kwarg api_key=..., or
#               DEEPSEEK_API_KEY in the environment)
#   - pkg_dir:  the directory holding hermes-pkg-py314.tar.gz
#               (--agent-kwarg pkg_dir=..., or HERMES_PKG_DIR;
#               default /tmp/hermes-build)
#
# Optional kwargs (defaults in parentheses):
#   - model (deepseek-flash — hermes resolves it via its deepseek
#     provider; the wire alias is deepseek-chat, the flash tier)

import os
import shlex
from pathlib import Path

from terminal_bench.agents.installed_agents.abstract_installed_agent import (
    AbstractInstalledAgent,
)
from terminal_bench.terminal.models import TerminalCommand


class HermesAgent(AbstractInstalledAgent):
    @staticmethod
    def name() -> str:
        return "hermes"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

        self._api_key = kwargs.get("api_key") or os.environ.get("DEEPSEEK_API_KEY")
        if not self._api_key:
            raise ValueError(
                "an API key is required: pass --agent-kwarg api_key=<key> "
                "or set DEEPSEEK_API_KEY"
            )

        pkg_dir = kwargs.get("pkg_dir") or os.environ.get(
            "HERMES_PKG_DIR", "/tmp/hermes-build"
        )
        self._pkg_dir = Path(pkg_dir)
        self._tarball = self._pkg_dir / "hermes-pkg-py314.tar.gz"
        if not self._tarball.is_file():
            raise ValueError(
                f"hermes package tarball not found: {self._tarball} — "
                "build it per the module docstring and pass "
                "--agent-kwarg pkg_dir=<dir> or set HERMES_PKG_DIR"
            )

        self._model = kwargs.get(
            "model", os.environ.get("HERMES_MODEL", "deepseek-flash")
        )
        self._usage_file = "/agent-logs/hermes-usage.json"

    @property
    def _env(self) -> dict[str, str]:
        # Written to /installed-agent/setup-env.sh inside the container
        # (never typed into the pane), the same mechanism every
        # installed CLI agent uses for its API key.
        return {
            "DEEPSEEK_API_KEY": self._api_key,
            "HERMES_MODEL": self._model,
        }

    @property
    def _install_agent_script_path(self) -> Path:
        return self._get_templated_script_path("hermes-setup.sh.j2")

    def perform_task(self, instruction, session, logging_dir=None):
        # The tarball is too large for the tmux paste the install
        # script arrives by, so it is copied into the container
        # directly (same mechanism as the yaah binary); the install
        # script unpacks it. It carries its own CPython 3.14 runtime
        # (hermes 0.22.x requires it; see the module docstring), so
        # the container's own Python version is irrelevant.
        session.copy_to_container(
            self._tarball,
            container_dir="/installed-agent",
            container_filename=self._tarball.name,
        )
        return super().perform_task(instruction, session, logging_dir)

    def _run_agent_commands(self, instruction: str) -> list[TerminalCommand]:
        return [
            # One-shot mode: approvals auto-bypassed, runs the task,
            # exits when done; the usage report lands under the
            # mounted agent-logs path so token counts are readable
            # from the host run directory afterwards.
            TerminalCommand(
                command=(
                    f"hermes -z {shlex.quote(instruction)} "
                    f"-m {shlex.quote(self._model)} "
                    f"--usage-file {shlex.quote(self._usage_file)} --yolo"
                ),
                max_timeout_sec=float("inf"),
                block=True,
            ),
        ]
