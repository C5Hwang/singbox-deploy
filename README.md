# singbox-deploy

`singbox-deploy` is a single-user, sing-box-only deployment and management tool
with an interactive Bubble Tea terminal UI. It installs and manages sing-box,
Nginx, certificates (via a built-in ACME client), subscriptions, and a built-in
traffic monitor on Ubuntu, Debian, and RHEL-like systems.

## Supported protocols

- Reality Vision
- Reality gRPC
- Hysteria2
- TUIC
- AnyTLS

Xray, Trojan, VMess, WS, and Naive are intentionally **not** supported.

## Install

```bash
bash <(curl -fsSL https://github.com/C5Hwang/singbox-deploy/releases/latest/download/install.sh)
sudo singbox-deploy
```

The bootstrap installs `/usr/bin/singbox-deploy` from the latest GitHub Release
asset matching your OS and CPU architecture, then you launch the TUI.

## License

Licensed under the GNU Affero General Public License v3.0 (AGPL-3.0-only).
See [`LICENSE`](LICENSE) and the full text in [`AGPL-3.0.txt`](AGPL-3.0.txt).
