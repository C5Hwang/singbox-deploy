#!/usr/bin/env bash
set -euo pipefail

# Bootstrap installer for singbox-deploy (master side). Downloads both
# singbox-deploy (TUI) and singbox-monitor (monitor service) from the latest
# GitHub Release and installs them to /usr/bin/. singbox-node is fetched onto
# each remote node automatically when it is added via Node Management.

REPO="C5Hwang/singbox-deploy"
INSTALL_DIR="/usr/bin"
BINARIES=(singbox-deploy singbox-monitor)

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

if command -v curl >/dev/null 2>&1; then
  fetcher=("curl" "-fsSL" "-o")
elif command -v wget >/dev/null 2>&1; then
  fetcher=("wget" "-qO")
else
  echo "curl or wget is required" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

for bin in "${BINARIES[@]}"; do
  asset="${bin}-${os}-${arch}"
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
  out="${tmpdir}/${bin}"
  echo "Downloading ${asset} ..."
  "${fetcher[@]}" "${out}" "${url}"
  chmod +x "${out}"
done

for bin in "${BINARIES[@]}"; do
  install -m 0755 "${tmpdir}/${bin}" "${INSTALL_DIR}/${bin}"
  echo "Installed ${bin} to ${INSTALL_DIR}/${bin}"
done

echo "Run: sudo singbox-deploy"
