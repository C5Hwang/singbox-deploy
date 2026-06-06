# singbox-deploy

`singbox-deploy` is an **unofficial deployment tool** for sing-box. It is not
affiliated with SagerNet or sing-box.

`singbox-deploy` is a [sing-box](https://github.com/SagerNet/sing-box)-only deployment and management tool written in Go,
which is able to deploy sing-box, Nginx, Let's Encrypt certificates, subscription files,
and a built-in monitor on Linux servers.

The project is inspired by [`mack-a/v2ray-agent`](https://github.com/mack-a/v2ray-agent).

## Core Features

- Interactive terminal UI for deployment and management.
- Automated sing-box, Nginx, certificate, subscription, and service deployment.
- Let's Encrypt certificate issuance with HTTP-01 and DNS-01 support.
- Subscription output for default links, Clash Meta, and sing-box clients.
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

The installer downloads the matching release binary and installs it to
`/usr/bin/singbox-deploy`. Then run `sudo singbox-deploy` to deploy and manage
the server.

## Build From Source

The release binary uses Go `embed`, so the monitor UI must be built before the Go
binary when UI assets change.

### Requirements

- Go 1.25 or newer.
- Node.js 22 or newer.
- pnpm 9 or newer.

### 1. Build embedded monitor UI assets

```bash
pnpm --dir web/monitor install --frozen-lockfile
pnpm --dir web/monitor build
```

This writes the Vue build output to `template/monitor-ui`, where it is embedded
by the Go build.

### 2. Build static binaries

```bash
# Build for the current Linux architecture.
mkdir -p dist
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o dist/singbox-deploy ./cmd/singbox-deploy

# Build for Linux amd64.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/singbox-deploy-linux-amd64 ./cmd/singbox-deploy

# Build for Linux arm64.
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/singbox-deploy-linux-arm64 ./cmd/singbox-deploy
```

## License

Licensed under the GNU Affero General Public License v3.0 (AGPL-3.0-only).
See [`LICENSE`](LICENSE) for details.
