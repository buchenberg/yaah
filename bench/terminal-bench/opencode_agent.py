# OpenCode agent adapter for Terminal-Bench (installed-agent form).
#
# OpenCode is copied into the task container in the same way as the yaah
# binary: the task image is the sandbox, and the adapter must not depend on
# the container's package manager (broken-python deliberately removes pip).
#
# Import path: opencode_agent:OpenCodeAgent (put this directory on
# PYTHONPATH and pass --agent-import-path to `tb run`).
#
# Required environment/kwargs:
#   - api_key:     the provider API key (--agent-kwarg api_key=..., or
#                  DEEPSEEK_API_KEY in the environment)
#   - binary_path: path to the OpenCode binary (--agent-kwarg binary_path=...,
#                  or OPENCODE_BIN; default ~/.opencode/bin/opencode)
#
# Optional kwargs (defaults in parentheses):
#   - model (deepseek/deepseek-flash, or OPENCODE_MODEL)
#
# Each trial gets a private `--standalone` server and a deterministic
# `ses_tb_<uuid>` session id. The JSON event stream, session id, stderr, and
# the XDG state directory all land under /agent-logs, so metrics can be
# extracted after the container exits.

import os
import shlex
import uuid
from pathlib import Path

from terminal_bench.agents.installed_agents.abstract_installed_agent import (
    AbstractInstalledAgent,
)
from terminal_bench.terminal.models import TerminalCommand


class OpenCodeAgent(AbstractInstalledAgent):
    @staticmethod
    def name() -> str:
        return "opencode"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

        self._api_key = kwargs.get("api_key") or os.environ.get("DEEPSEEK_API_KEY")
        if not self._api_key:
            raise ValueError(
                "an API key is required: pass --agent-kwarg api_key=<key> "
                "or set DEEPSEEK_API_KEY"
            )

        binary = (
            kwargs.get("binary_path")
            or os.environ.get("OPENCODE_BIN")
            or os.path.expanduser("~/.opencode/bin/opencode")
        )
        self._binary_path = Path(binary)
        if not self._binary_path.is_file():
            raise ValueError(f"OpenCode binary not found at {self._binary_path}")

        self._model = kwargs.get(
            "model", os.environ.get("OPENCODE_MODEL", "deepseek/deepseek-flash")
        )

    @property
    def _env(self) -> dict[str, str]:
        # These are written to /installed-agent/setup-env.sh inside the
        # container (never typed into the pane).
        return {
            "DEEPSEEK_API_KEY": self._api_key,
            "OPENCODE_MODEL": self._model,
            # Keep session state and config on the mounted agent-logs path so
            # the metrics extractor can read the session database afterwards.
            "XDG_DATA_HOME": "/agent-logs/opencode-data",
            "XDG_CONFIG_HOME": "/agent-logs/opencode-config",
        }

    @property
    def _install_agent_script_path(self) -> Path:
        return self._get_templated_script_path("opencode-setup.sh.j2")

    def perform_task(self, instruction, session, logging_dir=None):
        # The binary is too large for the tmux paste used by the install
        # script, so copy it into the container directly, as the yaah/hermes
        # adapters do.
        session.copy_to_container(
            self._binary_path,
            container_dir="/installed-agent",
            container_filename="opencode",
        )
        return super().perform_task(instruction, session, logging_dir)

    def _run_agent_commands(self, instruction: str) -> list[TerminalCommand]:
        # A session id must start with "ses" for the v2 API. The file is the
        # correlation key for the post-run metrics extractor.
        session_id = "ses_tb_" + uuid.uuid4().hex
        command = (
            f'SID={shlex.quote(session_id)}; '
            'printf \'%s\' "$SID" > /agent-logs/opencode-session.txt; '
            'opencode run --standalone --session "$SID" '
            f"--model {shlex.quote(self._model)} --auto --format json "
            f"{shlex.quote(instruction)} "
            "> /agent-logs/opencode-stdout.jsonl "
            "2> /agent-logs/opencode-stderr.log"
        )
        return [
            TerminalCommand(
                command=command,
                max_timeout_sec=float("inf"),
                block=True,
            )
        ]
