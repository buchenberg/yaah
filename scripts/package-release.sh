#!/usr/bin/env bash
# package-release.sh packages one built yaah binary into the archive shape an
# ACP registry manifest expects (distribution.binary in agent.schema.json).
#
# Two details make this non-obvious, both from the registry's verify_agents.py:
#
#   1. Platform keys are canonical target triples, NOT Go's GOOS/GOARCH
#      spelling: arm64 is "aarch64" and amd64 is "x86_64". A key that does not
#      match exactly fails the client with "no archive for current platform".
#
#   2. The launch probe extracts the archive and resolves the manifest's `cmd`
#      against the EXTRACT ROOT. A tarball nesting everything under
#      yaah-<ver>-<key>/ therefore only works if `cmd` names the nested path.
#      These archives are deliberately FLAT — the binary sits at the top level
#      named "yaah" (or "yaah.exe"), so the manifest can say "./yaah".
#
# Windows archives are .zip and Unix archives .tar.gz, matching the schema's
# convention and the existing entries.
#
# The bare binaries the release already publishes are NOT replaced: `yaah
# update` downloads assets named yaah-<goos>-<goarch>[.exe] (see
# internal/update/update.go:49), so the archives are added alongside them.
#
# Usage:
#   scripts/package-release.sh --goos linux --goarch amd64 \
#     --binary dist/yaah-linux-amd64 --version v0.102.0 --outdir dist
#
# Prints the archive path on stdout and APPENDS "<sha256>  <name>" to
# <outdir>/sha256sums.txt, so one clean outdir accumulates a line per platform.
# Run it against a fresh outdir (the release workflow does) — it does not
# truncate, and re-running it for the same platform adds a second line.
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
usage: package-release.sh --goos <goos> --goarch <goarch> --binary <path>
                          --version <tag> [--outdir <dir>]

Packages one built yaah binary into the flat archive shape an ACP registry
manifest expects, and appends its sha256 to <outdir>/sha256sums.txt.
EOF
}

die() {
	echo "package-release.sh: $*" >&2
	exit 1
}

goos=""
goarch=""
binary=""
version=""
outdir="dist"

while [ $# -gt 0 ]; do
	case "$1" in
	--goos | --goarch | --binary | --version | --outdir)
		[ $# -ge 2 ] || die "$1 needs a value"
		case "$1" in
		--goos) goos=$2 ;;
		--goarch) goarch=$2 ;;
		--binary) binary=$2 ;;
		--version) version=$2 ;;
		--outdir) outdir=$2 ;;
		esac
		shift 2
		;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[ -n "$goos" ] || { usage; die "--goos is required"; }
[ -n "$goarch" ] || { usage; die "--goarch is required"; }
[ -n "$binary" ] || { usage; die "--binary is required"; }
[ -n "$version" ] || { usage; die "--version is required"; }
[ -f "$binary" ] || die "no such binary: $binary"

# platform_key maps Go's GOOS/GOARCH to the registry's canonical key. This
# mapping is the single source of truth; agent.schema.json's propertyNames
# enum is exactly these six values.
platform_key() {
	case "$1/$2" in
	darwin/arm64) echo darwin-aarch64 ;;
	darwin/amd64) echo darwin-x86_64 ;;
	linux/arm64) echo linux-aarch64 ;;
	linux/amd64) echo linux-x86_64 ;;
	windows/arm64) echo windows-aarch64 ;;
	windows/amd64) echo windows-x86_64 ;;
	*) return 1 ;;
	esac
}

key=$(platform_key "$goos" "$goarch") ||
	die "unsupported platform $goos/$goarch (need one of: darwin|linux|windows x arm64|amd64)"

case "$goos" in
windows) binname="yaah.exe" ;;
*) binname="yaah" ;;
esac

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

# The binary must be executable inside the archive, and named exactly as the
# manifest's cmd expects.
install -m 0755 "$binary" "$staging/$binname"

if [ "$goos" = "windows" ]; then
	archive="$outdir/yaah-${version}-${key}.zip"
	command -v zip >/dev/null || die "zip is required for windows archives"
	# -X drops extra file attributes so the archive is reproducible modulo
	# the binary itself.
	(cd "$staging" && zip -q -X "$archive" "$binname")
else
	archive="$outdir/yaah-${version}-${key}.tar.gz"
	# -C into the staging dir keeps the archive flat: yaah at the root.
	tar -czf "$archive" -C "$staging" "$binname"
fi

[ -f "$archive" ] || die "archive was not created: $archive"

# Guard the flat layout rather than trusting tar/zip flags.
if [ "$goos" = "windows" ]; then
	command -v unzip >/dev/null || die "unzip is required to verify windows archives"
	listing=$(unzip -Z1 "$archive")
else
	listing=$(tar -tzf "$archive")
fi
[ "$listing" = "$binname" ] ||
	die "archive is not flat: expected only $binname, got: $(echo "$listing" | tr '\n' ' ')"

# sha256 is optional in the schema, but if the manifest carries it the value
# must be 64 lowercase hex and is VERIFIED — a mismatch deletes the cached
# archive and hard-fails the run.
sum=$(sha256sum "$archive" | awk '{print $1}')
case "$sum" in
*[!0-9a-f]*) die "sha256 is not lowercase hex: $sum" ;;
esac
[ ${#sum} -eq 64 ] || die "sha256 is not 64 chars: $sum"

printf '%s  %s\n' "$sum" "$(basename "$archive")" >>"$outdir/sha256sums.txt"

echo "$archive"
