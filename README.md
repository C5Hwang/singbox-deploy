# singbox-deploy

`singbox-deploy` is an **unofficial** [sing-box](https://github.com/SagerNet/sing-box)-focused
deployment and management tool written in Go, not affiliated with SagerNet or sing-box.
It automates the setup of sing-box, Nginx, Let's Encrypt certificates, and subscription
files on Linux servers, and includes a built-in resource monitor.

## Core Features

- Interactive terminal UI for deployment and management.
- Automated deployment of sing-box, Nginx, certificates, and subscriptions.
- Let's Encrypt certificate issuance and renewal via HTTP-01 and DNS-01 challenges.
- Subscription output in share-link, Clash Meta, and sing-box formats.
- Selectable HTML5 UP masquerade site templates served by Nginx.
- Resource monitor with a web dashboard and quota enforcement.

## Supported Protocols

- [VLESS Reality](https://github.com/XTLS/REALITY)
- [Hysteria2](https://v2.hysteria.network)
- [TUIC](https://github.com/tuic-protocol/tuic)
- [AnyTLS](https://github.com/anytls/anytls-go)

## Install

### Install from the latest release

```bash
curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh | sudo bash
```

The installer detects your platform and downloads the corresponding release
binary to `/usr/bin/singbox-deploy`. Then run `sudo singbox-deploy` to start
the interactive setup.

## Build From Source

The monitor UI is embedded into the Go binary via `go:embed`, so it must be
built before compiling the Go binary.

### Requirements

- Go 1.25 or newer.
- Node.js 22 or newer.
- pnpm 9 or newer.

### 1. Build the monitor UI

```bash
pnpm --dir web/monitor install --frozen-lockfile
pnpm --dir web/monitor build
```

This writes the production build output to `assets/monitor-ui`, which the Go
build embeds automatically.

### 2. Build the binary

```bash
# Build for the current Linux architecture.
mkdir -p dist
for bin in singbox-deploy singbox-monitor singbox-node; do
  CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o dist/${bin} ./cmd/${bin}
done

# Cross-build for Linux amd64 / arm64.
for arch in amd64 arm64; do
  for bin in singbox-deploy singbox-monitor singbox-node; do
    CGO_ENABLED=0 GOOS=linux GOARCH=${arch} go build -trimpath -ldflags="-s -w" -o dist/${bin}-linux-${arch} ./cmd/${bin}
  done
done
```

The three binaries serve distinct roles:
- `singbox-deploy` — interactive TUI on the master, manages cluster membership.
- `singbox-monitor` — long-lived monitor service on both master and nodes.
- `singbox-node` — persistent agent on each node, listens on the WireGuard interface.

## Acknowledgments

Inspired by [`mack-a/v2ray-agent`](https://github.com/mack-a/v2ray-agent).

## License

Licensed under the GNU Affero General Public License v3.0 (AGPL-3.0-only).
See [`LICENSE`](LICENSE) for details.
