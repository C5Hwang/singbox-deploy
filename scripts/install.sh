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
base="https://github.com/${REPO}/releases/latest/download"
tmp="$(mktemp)"
sums="$(mktemp)"
trap 'rm -f "${tmp}" "${sums}"' EXIT

fetch() {
  # fetch <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

echo "Downloading ${asset} ..."
fetch "${base}/${asset}" "${tmp}"

echo "Verifying checksum ..."
fetch "${base}/SHA256SUMS" "${sums}"
expected="$(awk -v f="${asset}" '$2 == f {print $1}' "${sums}")"
if [ -z "${expected}" ]; then
  echo "No checksum found for ${asset} in SHA256SUMS" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${tmp}" | awk '{print $1}')"
else
  echo "sha256sum or shasum is required to verify the download" >&2
  exit 1
fi
if [ "${expected}" != "${actual}" ]; then
  echo "Checksum mismatch for ${asset}: expected ${expected}, got ${actual}" >&2
  exit 1
fi

chmod +x "${tmp}"
install -m 0755 "${tmp}" "${INSTALL_PATH}"
echo "Installed ${BIN} to ${INSTALL_PATH}"
echo "Run: sudo ${BIN}"
