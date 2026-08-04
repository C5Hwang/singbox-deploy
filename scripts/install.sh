#!/usr/bin/env bash
set -euo pipefail

# Bootstrap installer for singbox-deploy. Detects OS/arch, downloads the
# matching binary from GitHub Releases, and installs it to
# /usr/bin/singbox-deploy. Release artifacts replace DEFAULT_RELEASE with
# their own tag; the source-tree copy defaults to the latest release.
# SINGBOX_DEPLOY_VERSION can explicitly select another stable tag.

REPO="C5Hwang/singbox-deploy"
BIN="singbox-deploy"
INSTALL_PATH="/usr/bin/${BIN}"
DEFAULT_RELEASE="latest"

release="${DEFAULT_RELEASE}"
if [ "${SINGBOX_DEPLOY_VERSION+x}" = "x" ]; then
  release="${SINGBOX_DEPLOY_VERSION}"
  if [[ ! "${release}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "Invalid SINGBOX_DEPLOY_VERSION: ${release} (expected vMAJOR.MINOR.PATCH)" >&2
    exit 1
  fi
elif [ "${release}" != "latest" ] &&
  [[ ! "${release}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "Invalid packaged release: ${release}" >&2
  exit 1
fi

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
if [ "${release}" = "latest" ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${release}"
fi
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

echo "Downloading ${asset} from ${release} ..."
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
if ! candidate_version="$("${tmp}" --version)"; then
  echo "Downloaded ${asset} failed its version check" >&2
  exit 1
fi
if [[ ! "${candidate_version}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "Downloaded ${asset} reports invalid version: ${candidate_version}" >&2
  exit 1
fi
if [ "${release}" != "latest" ] && [ "${candidate_version}" != "${release}" ]; then
  echo "Downloaded ${asset} reports ${candidate_version}, expected ${release}" >&2
  exit 1
fi
install -m 0755 "${tmp}" "${INSTALL_PATH}"
echo "Installed ${BIN} to ${INSTALL_PATH}"
echo "Run: sudo ${BIN}"
