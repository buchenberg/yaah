# yaah agent adapter for Terminal-Bench (installed-agent form).
#
# The agent is installed into the task container and driven through the
# tmux session: the prebuilt static yaah binary is copied in, a minimal
# config (provider + model + trace dir) is written inside the container,
# and the task instruction is run through yaah's one-shot mode.
#
# Import path: yaah_agent:YaahAgent (put this directory on PYTHONPATH and
# pass --agent-import-path to `tb run`).
#
# Required environment/kwargs:
#   - api_key:    the provider API key (--agent-kwarg api_key=..., or
#                 DEEPSEEK_API_KEY in the environment)
#   - binary_path: path to the prebuilt static yaah binary
#                 (--agent-kwarg binary_path=..., or YAAH_BIN)
#
# Optional kwargs (defaults in parentheses):
#   - model (deepseek-flash), base_url (https://api.deepseek.com/v1),
#     trace_dir (/agent-logs/yaah-traces — mounted to the host run dir,
#     so the shepherd trace store survives the container and token
#     counts are readable after the run)

import os
import shlex
from pathlib import Path

from terminal_bench.agents.installed_agents.abstract_installed_agent import (
    AbstractInstalledAgent,
)
from terminal_bench.terminal.models import TerminalCommand


class YaahAgent(AbstractInstalledAgent):
    @staticmethod
    def name() -> str:
        return "yaah"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

        self._api_key = kwargs.get("api_key") or os.environ.get("DEEPSEEK_API_KEY")
        if not self._api_key:
            raise ValueError(
                "an API key is required: pass --agent-kwarg api_key=<key> "
                "or set DEEPSEEK_API_KEY"
            )

        binary = kwargs.get("binary_path") or os.environ.get("YAAH_BIN")
        if not binary:
            raise ValueError(
                "the prebuilt yaah binary is required: pass "
                "--agent-kwarg binary_path=<path> or set YAAH_BIN"
            )
        self._binary_path = Path(binary)
        if not self._binary_path.is_file():
            raise ValueError(f"yaah binary not found at {self._binary_path}")

        self._model = kwargs.get("model", os.environ.get("YAAH_MODEL", "deepseek-flash"))
        self._base_url = kwargs.get(
            "base_url", os.environ.get("YAAH_BASE_URL", "https://api.deepseek.com/v1")
        )
        self._trace_dir = kwargs.get("trace_dir", "/agent-logs/yaah-traces")

    @property
    def _env(self) -> dict[str, str]:
        # These are written to /installed-agent/setup-env.sh inside the
        # container (never typed into the pane), the same mechanism every
        # installed CLI agent uses for its API key.
        return {
            "DEEPSEEK_API_KEY": self._api_key,
            "YAAH_MODEL": self._model,
            "YAAH_BASE_URL": self._base_url,
            "YAAH_TRACE_DIR": self._trace_dir,
            # The whole yaah home lands under the mounted agent-logs path,
            # so state.db (sessions.tokens_in/out) survives the container
            # and token counts are readable from the host run dir.
            "YAAH_HOME": "/agent-logs/yaah-home",
        }

    @property
    def _install_agent_script_path(self) -> Path:
        return self._get_templated_script_path("yaah-setup.sh.j2")

    def perform_task(self, instruction, session, logging_dir=None):
        # The binary is too large for the tmux paste the install script
        # arrives by, so it is copied into the container directly; the
        # install script makes it executable and puts it on PATH.
        session.copy_to_container(
            self._binary_path,
            container_dir="/installed-agent",
            container_filename="yaah",
        )
        return super().perform_task(instruction, session, logging_dir)

    def _run_agent_commands(self, instruction: str) -> list[TerminalCommand]:
        return [
            # One-shot mode: full wiring, runs the task, exits when done.
            TerminalCommand(
                command=f"yaah {shlex.quote(instruction)}",
                max_timeout_sec=float("inf"),
                block=True,
            ),
            # Token accounting: the trace store lives under the mounted
            # agent-logs path, so the profile survives the container and
            # is readable from the host run directory afterwards.
            TerminalCommand(
                command=(
                    "yaah shepherd-trace profile "
                    "> /agent-logs/yaah-profile.txt 2>&1 || true"
                ),
                max_timeout_sec=60.0,
                block=True,
            ),
        ]
