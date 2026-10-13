# aider agent adapter for Terminal-Bench (installed-agent form).
#
# Aider (Aider-AI/aider) runs in the task container from a prebuilt,
# self-contained package tree — one tarball per Python minor version,
# picked by the container's interpreter (same immunity as the static
# binaries and the hermes tree: broken-python's fixture deletes pip).
# Because aider's usage reporting is display-only, the adapter runs
# its own driver (aider_driver.py) in place of the `aider` console
# script: it drives the same Coder flow as `aider --message` and
# dumps exact token accounting (input tokens include cache hits,
# per deepseek usage semantics) to the mounted agent-logs path.
#
# Build the package tarballs once from an aider checkout (cite its
# commit; the version metadata needs a pretend version without git,
# and the pyproject `include = ["aider"]` package filter must be
# widened to `["aider*"]` in the copy or subpackages like
# aider.coders are dropped from source builds):
#   rsync -a --exclude .git --exclude __pycache__ <aider-checkout>/ /tmp/aider-src/
#   sed -i 's/^include = \["aider"\]$/include = ["aider*"]/' /tmp/aider-src/pyproject.toml
#   for py in 3.12 3.13; do
#     tag="${py/./}"
#     podman run --rm -v /tmp/aider-src:/src:Z -v /tmp/aider-build:/out:Z \
#       "docker.io/library/python:${py}-slim" \
#       sh -c "SETUPTOOLS_SCM_PRETEND_VERSION=0.16.0.dev1 \
#              pip install --quiet --no-input --upgrade pip setuptools wheel \
#              && SETUPTOOLS_SCM_PRETEND_VERSION=0.16.0.dev1 \
#                 pip install --quiet --no-input --target /out/aider-pkg /src \
#              && cd /out && tar czf aider-pkg-py${tag}.tar.gz aider-pkg \
#              && rm -rf aider-pkg"
#   done
#
# Import path: aider_agent:AiderAgent (put this directory on PYTHONPATH
# and pass --agent-import-path to `tb run`).
#
# Required environment/kwargs:
#   - api_key:  the provider API key (--agent-kwarg api_key=..., or
#               DEEPSEEK_API_KEY in the environment)
#   - pkg_dir:  the directory holding aider-pkg-py312.tar.gz and
#               aider-pkg-py313.tar.gz (--agent-kwarg pkg_dir=..., or
#               AIDER_PKG_DIR; default /tmp/aider-build)
#
# Optional kwargs (defaults in parentheses):
#   - model (deepseek/deepseek-chat — the name aider's
#     model-settings.yml curates for this family: edit_format diff,
#     caches_by_default; deepseek-chat is the flash tier's wire
#     alias, the same model the other CLIs bench)

import os
import shlex
from pathlib import Path

from terminal_bench.agents.installed_agents.abstract_installed_agent import (
    AbstractInstalledAgent,
)
from terminal_bench.terminal.models import TerminalCommand


class AiderAgent(AbstractInstalledAgent):
    @staticmethod
    def name() -> str:
        return "aider"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

        self._api_key = kwargs.get("api_key") or os.environ.get("DEEPSEEK_API_KEY")
        if not self._api_key:
            raise ValueError(
                "an API key is required: pass --agent-kwarg api_key=<key> "
                "or set DEEPSEEK_API_KEY"
            )

        pkg_dir = kwargs.get("pkg_dir") or os.environ.get(
            "AIDER_PKG_DIR", "/tmp/aider-build"
        )
        self._pkg_dir = Path(pkg_dir)
        self._tarballs = {
            "3.12": self._pkg_dir / "aider-pkg-py312.tar.gz",
            "3.13": self._pkg_dir / "aider-pkg-py313.tar.gz",
        }
        missing = [str(p) for p in self._tarballs.values() if not p.is_file()]
        if missing:
            raise ValueError(
                f"aider package tarballs not found: {missing} — "
                "build them per the module docstring and pass "
                "--agent-kwarg pkg_dir=<dir> or set AIDER_PKG_DIR"
            )

        self._driver = Path(__file__).resolve().parent / "aider_driver.py"
        if not self._driver.is_file():
            raise ValueError(f"aider driver not found at {self._driver}")

        self._model = kwargs.get(
            "model", os.environ.get("AIDER_MODEL", "deepseek/deepseek-chat")
        )
        self._usage_file = "/agent-logs/aider-usage.json"

    @property
    def _env(self) -> dict[str, str]:
        # Written to /installed-agent/setup-env.sh inside the container
        # (never typed into the pane), the same mechanism every
        # installed CLI agent uses for its API key.
        return {
            "DEEPSEEK_API_KEY": self._api_key,
            "AIDER_MODEL": self._model,
        }

    @property
    def _install_agent_script_path(self) -> Path:
        return self._get_templated_script_path("aider-setup.sh.j2")

    def perform_task(self, instruction, session, logging_dir=None):
        # The tarballs are too large for the tmux paste the install
        # script arrives by, so they are copied into the container
        # directly (same mechanism as the yaah binary and hermes
        # tarballs); the driver script rides along.
        for tarball in self._tarballs.values():
            session.copy_to_container(
                tarball,
                container_dir="/installed-agent",
                container_filename=tarball.name,
            )
        session.copy_to_container(
            self._driver,
            container_dir="/installed-agent",
            container_filename="aider_driver.py",
        )
        return super().perform_task(instruction, session, logging_dir)

    def _run_agent_commands(self, instruction: str) -> list[TerminalCommand]:
        return [
            # The driver runs the same Coder flow as `aider --message`
            # and writes the usage report under the mounted
            # agent-logs path, so token counts are readable from the
            # host run directory afterwards.
            TerminalCommand(
                command=(
                    f"aider-driver --message {shlex.quote(instruction)} "
                    f"--model {shlex.quote(self._model)} "
                    f"--usage-file {shlex.quote(self._usage_file)}"
                ),
                max_timeout_sec=float("inf"),
                block=True,
            ),
        ]
