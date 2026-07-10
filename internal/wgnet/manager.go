package wgnet

import (
	"fmt"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// DefaultConfDir is where wg-quick expects interface configs.
const DefaultConfDir = "/etc/wireguard"

// Manager drives WireGuard interface lifecycle and live peer changes through a
// system.Runner. Config files are written directly to disk (they hold private
// keys, so 0600); systemd/wg commands go through the runner.
type Manager struct {
	Runner  system.Runner
	ConfDir string // defaults to DefaultConfDir
}

func (m Manager) confDir() string {
	if m.ConfDir == "" {
		return DefaultConfDir
	}
	return m.ConfDir
}

// ConfigPath returns the wg-quick config path for the interface.
func (m Manager) ConfigPath(iface string) string {
	return filepath.Join(m.confDir(), iface+".conf")
}

// WriteConfig writes the interface config atomically with 0600 permissions.
func (m Manager) WriteConfig(iface, contents string) error {
	return state.WriteFileAtomic(m.ConfigPath(iface), []byte(contents), 0o600)
}

// EnableStart enables and starts wg-quick@<iface> so the tunnel survives reboot.
func (m Manager) EnableStart(iface string) error {
	return m.Runner.Run(system.Command{Name: "systemctl", Args: []string{"enable", "--now", m.unit(iface)}})
}

// Restart restarts wg-quick@<iface>, tearing the interface down and back up. On
// the hub this briefly drops every spoke, so prefer SyncConf for peer edits.
func (m Manager) Restart(iface string) error {
	return m.Runner.Run(system.Systemctl("restart", m.unit(iface)))
}

// DisableStop stops and disables wg-quick@<iface>.
func (m Manager) DisableStop(iface string) error {
	return m.Runner.Run(system.Command{Name: "systemctl", Args: []string{"disable", "--now", m.unit(iface)}})
}

// SyncConf reloads the on-disk config into the running interface without a
// teardown, so existing peers keep their connections. Uses `wg syncconf` fed by
// `wg-quick strip`; process substitution needs a shell, matching how the
// installer already runs repo-setup scripts via bash -c.
func (m Manager) SyncConf(iface string) error {
	script := fmt.Sprintf("wg syncconf %[1]s <(wg-quick strip %[1]s)", iface)
	return m.Runner.Run(system.Command{Name: "bash", Args: []string{"-c", script}})
}

// SetPeer adds or updates a peer on the running interface (live, no teardown).
// allowedIP should be the peer's /32 overlay address.
func (m Manager) SetPeer(iface, publicKey, allowedIP string) error {
	return m.Runner.Run(system.Command{Name: "wg", Args: []string{
		"set", iface, "peer", publicKey, "allowed-ips", allowedIP,
	}})
}

// RemovePeer removes a peer from the running interface (live, no teardown).
func (m Manager) RemovePeer(iface, publicKey string) error {
	return m.Runner.Run(system.Command{Name: "wg", Args: []string{
		"set", iface, "peer", publicKey, "remove",
	}})
}

func (m Manager) unit(iface string) string {
	return "wg-quick@" + iface + ".service"
}

// InstallCommands returns the package-manager commands to install the WireGuard
// userspace tools (wg, wg-quick). The kernel module ships in-tree on modern
// kernels, so only the tools need installing.
func InstallCommands(osr system.OSRelease) []system.Command {
	switch osr.PackageManager {
	case "apt":
		return []system.Command{
			{Name: "apt-get", Args: []string{"update"}, Env: aptEnv},
			{Name: "apt-get", Args: aptInstallArgs("wireguard-tools"), Env: aptEnv},
		}
	case "dnf", "yum":
		return []system.Command{
			{Name: osr.PackageManager, Args: []string{"-y", "install", "wireguard-tools"}},
		}
	default:
		return nil
	}
}

var aptEnv = []string{
	"DEBIAN_FRONTEND=noninteractive",
	"APT_LISTCHANGES_FRONTEND=none",
	"NEEDRESTART_MODE=a",
}

func aptInstallArgs(pkg string) []string {
	return []string{
		"-y",
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
		"install", pkg,
	}
}
