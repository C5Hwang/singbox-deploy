#!/usr/bin/env bash
#
# Builds the singbox-deploy hub binaries for linux/amd64 and linux/arm64.
#
# The hub embeds the spoke agent binaries (assets/agentbin), so the agents for
# BOTH architectures must be cross-compiled into the embed directory before the
# hub is compiled.
#
# Usage: VERSION=v1.2.3 scripts/build.sh   (VERSION defaults to "dev")
#        SKIP_MONITOR_UI=1 ...             (reuse an already-built UI)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-dev}"
AGENT_EMBED_DIR="assets/agentbin"
ARCHES=(amd64 arm64)
LDFLAGS="-s -w -X main.version=${VERSION}"

if [[ "${SKIP_MONITOR_UI:-0}" != "1" ]]; then
  echo "==> Building monitor UI"
  pnpm --dir web/monitor install --frozen-lockfile
  pnpm --dir web/monitor build
else
  echo "==> Reusing the existing embedded monitor UI"
fi

echo "==> Cross-compiling spoke agents into ${AGENT_EMBED_DIR}"
rm -f "${AGENT_EMBED_DIR}"/singbox-deploy-agent-linux-*
for arch in "${ARCHES[@]}"; do
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath \
    -ldflags="${LDFLAGS}" \
    -o "${AGENT_EMBED_DIR}/singbox-deploy-agent-linux-${arch}" \
    ./cmd/singbox-deploy-agent
done

echo "==> Building hub binaries"
mkdir -p dist
for arch in "${ARCHES[@]}"; do
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath \
    -ldflags="${LDFLAGS}" \
    -o "dist/singbox-deploy-linux-${arch}" \
    ./cmd/singbox-deploy
done

echo "==> Done. Hub binaries in dist/:"
ls -la dist/singbox-deploy-linux-*
