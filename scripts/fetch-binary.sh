#!/usr/bin/env bash
# fetch-binary.sh — download the prebuilt noti binary from GitHub Releases into
# the plugin data dir. Falls back to building from source if Go is available.
#
#   bash scripts/fetch-binary.sh                       # latest release -> $CLAUDE_PLUGIN_DATA/bin/noti
#   OUT=/path/to/noti   bash scripts/fetch-binary.sh   # custom target
#   NOTI_VERSION=v2.0.0 bash scripts/fetch-binary.sh   # pin a version (default: latest)
set -uo pipefail

REPO="AnkushinDaniil/noti"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DATA_DIR="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}"
OUT="${OUT:-${DATA_DIR}/bin/noti}"
VERSION="${NOTI_VERSION:-latest}"

mkdir -p "$(dirname "${OUT}")"

build_from_source() {
  if command -v go >/dev/null 2>&1 && [ -f "${REPO_ROOT}/go.mod" ]; then
    echo "fetch-binary: building from source via Go…" >&2
    OUT="${OUT}" bash "${REPO_ROOT}/scripts/build.sh"
    return $?
  fi
  return 1
}

# --- detect os/arch (goreleaser GOOS/GOARCH tokens) ---
os="$(uname -s)"; arch="$(uname -m)"
case "$os" in Darwin) os=darwin ;; Linux) os=linux ;; *) os="" ;; esac
case "$arch" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) arch="" ;; esac

if [ -z "$os" ] || [ -z "$arch" ]; then
  echo "fetch-binary: unsupported platform ($(uname -s)/$(uname -m)); trying source build…" >&2
  build_from_source && exit 0
  echo "fetch-binary: cannot fetch and Go is unavailable to build." >&2
  exit 1
fi

asset="noti_${os}_${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "fetch-binary: downloading ${asset} (${VERSION})…" >&2
if ! curl -fsSL -o "${tmp}/${asset}" "${base}/${asset}"; then
  echo "fetch-binary: download failed; trying source build…" >&2
  build_from_source && exit 0
  echo "fetch-binary: could not download or build the binary." >&2
  exit 1
fi

# --- checksum verify (skipped only if checksums.txt is unavailable) ---
if curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then sumcmd=(sha256sum); else sumcmd=(shasum -a 256); fi
  got="$(cd "$tmp" && "${sumcmd[@]}" "${asset}" | awk '{print $1}')"
  want="$(awk -v a="${asset}" '$2==a {print $1}' "${tmp}/checksums.txt" | head -1)"
  if [ -n "$want" ] && [ "$got" != "$want" ]; then
    echo "fetch-binary: checksum mismatch for ${asset} (got ${got}, want ${want})" >&2
    exit 1
  fi
fi

tar -xzf "${tmp}/${asset}" -C "$tmp" noti
mkdir -p "$(dirname "${OUT}")"
cp "${tmp}/noti" "${OUT}"
chmod +x "${OUT}"
echo "fetch-binary: installed -> ${OUT}" >&2
"${OUT}" version >/dev/null 2>&1 && echo "fetch-binary: ok ($("${OUT}" version))" >&2 || true
