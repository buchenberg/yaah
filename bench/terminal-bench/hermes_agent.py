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
# Build the package tarballs once (host, or any linux container), one
# per Python minor version the task images use — the compiled wheels
# (pydantic_core and friends) are ABI-specific:
#   mkdir -p /tmp/hermes-build
#   for py in 3.12 3.13; do
#     podman run --rm -v /tmp/hermes-build:/out:Z python:${py}-slim \
#       sh -c "pip install --quiet --no-input --target /out/hermes-pkg \
#              hermes-agent==0.19.0 \
#              && cd /out && tar czf hermes-pkg-py${py//./}.tar.gz hermes-pkg \
#              && rm -rf hermes-pkg"
#   done
# The task images at the pin use python-3-13 (hello-world, broken-python,
# assign-seats) and ubuntu-24-04 / Python 3.12 (csv-to-parquet,
# jsonl-aggregator, gomoku-planner); the install script picks the
# matching tarball by the container's interpreter.
#
# Import path: hermes_agent:HermesAgent (put this directory on
# PYTHONPATH and pass --agent-import-path to `tb run`).
#
# Required environment/kwargs:
#   - api_key:  the provider API key (--agent-kwarg api_key=..., or
#               DEEPSEEK_API_KEY in the environment)
#   - pkg_dir:  the directory holding hermes-pkg-py312.tar.gz and
#               hermes-pkg-py313.tar.gz (--agent-kwarg pkg_dir=..., or
#               HERMES_PKG_DIR; default /tmp/hermes-build)
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
        self._tarballs = {
            "3.12": self._pkg_dir / "hermes-pkg-py312.tar.gz",
            "3.13": self._pkg_dir / "hermes-pkg-py313.tar.gz",
        }
        missing = [str(p) for p in self._tarballs.values() if not p.is_file()]
        if missing:
            raise ValueError(
                f"hermes package tarballs not found: {missing} — "
                "build them per the module docstring and pass "
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
        # The tarballs are too large for the tmux paste the install
        # script arrives by, so they are copied into the container
        # directly (same mechanism as the yaah binary); the install
        # script unpacks the one matching the container's Python.
        for tarball in self._tarballs.values():
            session.copy_to_container(
                tarball,
                container_dir="/installed-agent",
                container_filename=tarball.name,
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
