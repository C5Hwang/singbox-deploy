# singbox-deploy

`singbox-deploy` is a Go tool for deploying and managing
[sing-box](https://github.com/SagerNet/sing-box) on Linux.

It sets up sing-box, Nginx, certificates, and subscriptions on a single server,
and scales to a fleet managed from one hub.

## Core Features

- Hub-and-spoke management: the hub's TUI adds and manages spokes over a
  private WireGuard overlay.
- Centralized Let's Encrypt certificates, issued on the hub and distributed
  to spokes.
- Fleet-wide subscriptions in share-link, Clash Meta, sing-box, and Surge
  formats, with an optional Tailscale mode.
- Node-to-node relaying over lower-latency ingress paths.
- Web dashboard for fleet monitoring and subscription link generation.
- Monthly traffic quotas per node, with temporary top-up packages.
- Nginx masquerade site from bundled [HTML5 UP](https://html5up.net)
  templates.

## Supported Protocols

- [VLESS Reality](https://github.com/XTLS/REALITY)
- [Hysteria2](https://v2.hysteria.network)
- [TUIC](https://github.com/tuic-protocol/tuic)
- [AnyTLS](https://github.com/anytls/anytls-go)

## Install

```bash
curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh | sudo bash
```

This installs the binary to `/usr/bin/singbox-deploy`. Run
`sudo singbox-deploy` to start the interactive setup.

To install a specific release (v0.2.3 or later), set `SINGBOX_DEPLOY_VERSION`:

```bash
curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh | \
  sudo env SINGBOX_DEPLOY_VERSION=v0.3.0 bash
```

## Build From Source

The monitor UI is embedded into the Go binary via `go:embed`, so it must be
built before compiling the Go binary.

The hub binary also embeds the spoke **agent** binaries for both architectures
(`assets/agentbin`), so the agents must be cross-compiled into the embed
directory before the hub is built.

### Requirements

- Go 1.26 or newer.
- Node.js 22 or newer.
- pnpm 9 or newer.

### Build everything with one script

```bash
VERSION=v1.2.3 scripts/build.sh
```

`scripts/build.sh` runs the whole pipeline: it builds the monitor UI,
cross-compiles the `amd64` and `arm64` spoke agents into `assets/agentbin/`,
then builds the hub binaries into `dist/`.

If the monitor UI has already been built, set `SKIP_MONITOR_UI=1` to skip that
step and reuse the existing assets.

### Build from scratch

```bash
# 1. Build the monitor UI.
pnpm --dir web/monitor install --frozen-lockfile
pnpm --dir web/monitor build

# 2. Cross-compile the spoke agents into the embed dir.
rm -f assets/agentbin/singbox-deploy-agent-linux-*
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" \
    -o assets/agentbin/singbox-deploy-agent-linux-$arch ./cmd/singbox-deploy-agent
done

# 3. Build the hub binaries.
mkdir -p dist
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w" \
    -o dist/singbox-deploy-linux-$arch ./cmd/singbox-deploy
done
```

## Acknowledgments

Inspired by [`mack-a/v2ray-agent`](https://github.com/mack-a/v2ray-agent).

## License

Licensed under the MIT License. See [`LICENSE`](LICENSE) for details.
