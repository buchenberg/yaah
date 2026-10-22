#!/usr/bin/env bash
#
# Run the ACP Test Compatibility Kit (agentclientprotocol/acp-tck) against the
# local yaah binary. The TCK clones into .scratch/acp-tck (AGENTS.md scratch
# space, gitignored) at a pinned commit so runs are reproducible across the
# team; upgrade deliberately by bumping tck_ref below.
#
# Requirements: uv (https://astral.sh/uv), Go (for the default auto-build).
#
# Usage:
#   scripts/acp-tck.sh                     # full v1 conformance suite
#   scripts/acp-tck.sh -k "session"        # extra args pass through to acp-tck
#   scripts/acp-tck.sh --protocol-version 2
#   AGENT_BIN=/path/to/other-yaah scripts/acp-tck.sh
#
# Environment:
#   AGENT_BIN           agent command to drive (default: ./yaah, auto-built)
#   ACP_TCK_AGENT_CWD   scratch working dir handed to the agent process
#                       (default: /tmp/yaah-acp-tck-wd; reused across runs,
#                       sessions are wiped between tests by the TCK itself)
#   ACP_TCK_REPORT      JSON report path (default: .scratch/acp-tck-report.json)
set -euo pipefail

root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

tck_repo=https://github.com/agentclientprotocol/acp-tck
tck_ref=b29ea51328fb63d20791630d00f1ddd313993546
tck_dir=.scratch/acp-tck
agent_bin=${AGENT_BIN:-./yaah}
agent_cwd=${ACP_TCK_AGENT_CWD:-/tmp/yaah-acp-tck-wd}
report=${ACP_TCK_REPORT:-.scratch/acp-tck-report.json}
case $report in
/*) report_abs=$report ;;
*)  report_abs=$root/$report ;;
esac

mkdir -p "$(dirname "$report_abs")" "$agent_cwd"

if [[ ! -d $tck_dir ]]; then
	git clone --quiet "$tck_repo" "$tck_dir"
fi
git -C "$tck_dir" fetch --quiet origin
git -C "$tck_dir" checkout --quiet --force "$tck_ref"

# Build the default agent on demand; an explicit AGENT_BIN is used verbatim
# (absolute paths and bare executables both work).
case $agent_bin in
/*) agent_cmd=$agent_bin ;;
*) agent_cmd=$root/$agent_bin ;;
esac
if [[ -z ${AGENT_BIN:-} && ! -x $agent_cmd ]]; then
	go build -trimpath -ldflags '-s -w' -o yaah .
fi

# Enter the TCK project: uv resolves the acp-tck entry point from the
# pyproject/uv.lock in this clone, so the run must happen from there.
cd "$tck_dir"
exec uv run acp-tck \
	--agent-cwd "$agent_cwd" \
	--timeout 90 \
	--test-timeout 300 \
	--report-json "$report_abs" \
	"$@" \
	-- "$agent_cmd" acp-serve
