#!/usr/bin/env bash
set -euo pipefail

# Bootstrap installer for singbox-deploy. Detects OS/arch, downloads the
# matching binary from the latest GitHub Release, and installs it to
# /usr/bin/singbox-deploy. Interactive use only; no non-interactive mode.

REPO="C5Hwang/singbox-deploy"
BIN="singbox-deploy"
INSTALL_PATH="/usr/bin/${BIN}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "${arch}" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *)
    echo "Unsupported architecture: ${arch}" >&2
    exit 1
    ;;
esac

case "${os}" in
  linux) ;;
  *)
    echo "Unsupported OS: ${os}" >&2
    exit 1
    ;;
esac

if [ "$(id -u)" -ne 0 ]; then
  echo "This installer must run as root (try: sudo bash install.sh)" >&2
  exit 1
fi

asset="${BIN}-${os}-${arch}"
url="https://github.com/${REPO}/releases/latest/download/${asset}"
tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

echo "Downloading ${asset} ..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "${url}" -o "${tmp}"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${tmp}" "${url}"
else
  echo "curl or wget is required" >&2
  exit 1
fi

chmod +x "${tmp}"
install -m 0755 "${tmp}" "${INSTALL_PATH}"
echo "Installed ${BIN} to ${INSTALL_PATH}"
echo "Run: sudo ${BIN}"
