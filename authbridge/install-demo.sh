#!/bin/sh
# install-demo.sh — one-line installer + launcher for the Cortex local demo.
#
#   curl -fsSL https://raw.githubusercontent.com/rossoctl/cortex/main/authbridge/install-demo.sh | sh
#
# Detects your OS/arch, downloads the prebuilt `abctl` and `authbridge-proxy`
# binaries for the newest release, verifies their SHA-256 checksums, installs
# them to ~/.local/bin, and starts `authbridge-proxy --demo` (Ctrl-C to stop).
# macOS + Linux, amd64 + arm64. No cluster, Keycloak, or SPIRE needed.
#
# Environment:
#   AUTHBRIDGE_VERSION=vX.Y.Z   install a specific release tag (default: newest)
#   AUTHBRIDGE_INSTALL_ONLY=1   install the binaries but do not start the demo
set -eu

REPO="rossoctl/cortex"
BIN_DIR="${HOME}/.local/bin"

info() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

# Verify the checklist file passed as $1 (run from the directory holding the
# files). shasum is preferred: it's always present on macOS and its -c reads the
# GNU-style checksums.txt reliably, whereas some non-GNU sha256sum builds reject
# -c. Linux without shasum falls back to sha256sum (GNU coreutils).
sha_check() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$1"
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c "$1"
	else
		die "need shasum or sha256sum to verify downloads"
	fi
}

# --- detect platform ---
os=$(uname -s)
case "$os" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported OS: $os (the demo installer supports macOS and Linux)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture: $arch (supported: amd64, arm64)" ;;
esac

# --- resolve the release tag ---
# `releases/latest` excludes prereleases, and the project ships prereleases, so
# list releases (newest first) and take the first tag_name instead.
version="${AUTHBRIDGE_VERSION:-}"
if [ -z "$version" ]; then
	info "Resolving newest release..."
	version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases?per_page=1" \
		| grep -m1 '"tag_name"' | sed -e 's/.*"tag_name": *"//' -e 's/".*//')
	[ -n "$version" ] || die "could not resolve the newest release (set AUTHBRIDGE_VERSION=vX.Y.Z)"
fi
info "Release: $version"

# --- download + verify ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

base="https://github.com/${REPO}/releases/download/${version}"
abctl_tgz="abctl_${version}_${os}_${arch}.tar.gz"
proxy_tgz="authbridge-proxy_${version}_${os}_${arch}.tar.gz"

info "Downloading binaries for ${os}/${arch}..."
curl -fsSL "${base}/${abctl_tgz}" -o "${tmp}/${abctl_tgz}" || die "download failed: ${abctl_tgz}"
curl -fsSL "${base}/${proxy_tgz}" -o "${tmp}/${proxy_tgz}" || die "download failed: ${proxy_tgz}"
curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt" || die "download failed: checksums.txt"

info "Verifying checksums..."
# Match exactly the two archives we downloaded (anchored to the end of the line),
# not every entry for this platform — so an unrelated future artifact in
# checksums.txt can't make verification fail on a file we never fetched.
grep -E "(${abctl_tgz}|${proxy_tgz})\$" "${tmp}/checksums.txt" > "${tmp}/checksums.filtered" \
	|| die "no checksum entries for ${abctl_tgz} / ${proxy_tgz} in checksums.txt"
( cd "$tmp" && sha_check checksums.filtered ) || die "checksum verification failed"

# --- extract + install ---
info "Installing to ${BIN_DIR}..."
mkdir -p "$BIN_DIR"
tar -xzf "${tmp}/${abctl_tgz}" -C "$tmp"
tar -xzf "${tmp}/${proxy_tgz}" -C "$tmp"
for b in abctl authbridge-proxy; do
	[ -f "${tmp}/${b}" ] || die "archive did not contain expected binary: ${b}"
	chmod +x "${tmp}/${b}"
	mv -f "${tmp}/${b}" "${BIN_DIR}/${b}"
done

# macOS: clear the quarantine flag so Gatekeeper doesn't block the unsigned binaries.
if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
	xattr -dr com.apple.quarantine "${BIN_DIR}/abctl" "${BIN_DIR}/authbridge-proxy" 2>/dev/null || true
fi

rm -rf "$tmp"
trap - EXIT

# --- report + next steps ---
proxy="${BIN_DIR}/authbridge-proxy"
case ":${PATH}:" in
	*":${BIN_DIR}:"*) abctl_cmd="abctl" proxy_cmd="authbridge-proxy" ;;
	*) abctl_cmd="${BIN_DIR}/abctl" proxy_cmd="$proxy" ;;
esac

info ""
info "Installed abctl and authbridge-proxy (${version}) to ${BIN_DIR}"
case ":${PATH}:" in
	*":${BIN_DIR}:"*) ;;
	*)
		warn "${BIN_DIR} is not on your PATH; the commands below use full paths."
		warn "Add it for future sessions:  export PATH=\"${BIN_DIR}:\$PATH\""
		;;
esac
info ""
info "In two more terminals (from this directory, so ./cortex-ca resolves):"
info "  View the live session:  ${abctl_cmd} --endpoint http://localhost:9094"
info '  Run your agent, e.g. Claude Code:'
# $PWD is intentionally literal here — printed for the user's shell to expand
# when they paste the command, not expanded by this script.
# shellcheck disable=SC2016
info '    HTTPS_PROXY=http://localhost:8081 NODE_EXTRA_CA_CERTS="$PWD/cortex-ca/ca.crt" CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 claude'
info ""

if [ "${AUTHBRIDGE_INSTALL_ONLY:-}" = "1" ]; then
	info "Install-only mode. Start the demo with:  ${proxy_cmd} --demo"
	exit 0
fi

info "Starting the demo proxy (Ctrl-C to stop)..."
info ""
exec "$proxy" --demo
