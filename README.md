# singbox-deploy

`singbox-deploy` is a Go tool for deploying and managing
[sing-box](https://github.com/SagerNet/sing-box) on Linux.

On a single server it installs sing-box, configures Nginx, obtains Let's Encrypt
certificates, and generates subscription files. Across multiple servers it runs
as a hub: additional servers join as spokes over a private WireGuard overlay and
are managed from the hub's terminal UI.

## Core Features

- Hub-and-spoke topology. The TUI runs on the hub, which handles adding,
  configuring, monitoring, and removing spokes.
- Spokes are bootstrapped once over SSH; control traffic then moves to the
  WireGuard overlay.
- The hub issues and renews Let's Encrypt certificates over ACME DNS-01 and
  distributes them to the spokes.
- Subscriptions aggregate every node in the fleet, served in share-link,
  Clash Meta, sing-box, and Surge formats.
- Resource monitoring through a web dashboard, with per-node quotas.
- Nginx serves a masquerade site, chosen from bundled
  [HTML5 UP](https://html5up.net) templates.

## Supported Protocols

- [VLESS Reality](https://github.com/XTLS/REALITY)
- [Hysteria2](https://v2.hysteria.network)
- [TUIC](https://github.com/tuic-protocol/tuic)
- [AnyTLS](https://github.com/anytls/anytls-go)

## Install

```bash
curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh | sudo bash
```

To install a specific release, set `SINGBOX_DEPLOY_VERSION` to its tag:

```bash
curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh | \
  sudo env SINGBOX_DEPLOY_VERSION=v0.3.0 bash
```

Each installer is pinned to the release it ships with; setting
`SINGBOX_DEPLOY_VERSION` to any `vMAJOR.MINOR.PATCH` tag overrides that. The tag
must be v0.2.3 or later, as earlier releases ship no `SHA256SUMS`.

The installer detects your platform and downloads the corresponding release
binary to `/usr/bin/singbox-deploy`. Then run `sudo singbox-deploy` to start
the interactive setup.

## Build From Source

The monitor UI is embedded into the Go binary via `go:embed`, so it must be
built before compiling the Go binary.

The hub binary also embeds the spoke **agent** binaries for both architectures
(`assets/agentbin`), so the agents must be cross-compiled into the embed
directory before the hub is built.

### Requirements

- Go 1.25 or newer.
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
