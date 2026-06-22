package wireguard

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// WriteConfig writes the rendered wg-quick config body to the canonical path
// with 0600 permissions, creating the parent directory if needed.
func WriteConfig(body string) error {
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ConfigPath, []byte(body), 0o600)
}

// EnableAndStart enables and starts the wg-quick service for the cluster
// interface. Used during initial provisioning on both master and nodes.
func EnableAndStart(runner system.Runner) error {
	unit := "wg-quick@" + InterfaceName + ".service"
	for _, cmd := range []system.Command{
		{Name: "systemctl", Args: []string{"daemon-reload"}},
		{Name: "systemctl", Args: []string{"enable", "--now", unit}},
	} {
		if err := runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd.String(), err)
		}
	}
	return nil
}

// SyncPeers persists fullBody as the canonical wg-quick config and reloads
// the running interface with syncBody via "wg syncconf". fullBody is the
// complete wg-quick(8) config (with Address=) so a service restart picks up
// the latest peer set; syncBody must be the protocol-only form rendered with
// forSync=true, because wg(8) syncconf rejects wg-quick-specific keys. The
// interface is reloaded in-place without dropping existing peer sessions.
func SyncPeers(runner system.Runner, fullBody, syncBody string) error {
	if err := WriteConfig(fullBody); err != nil {
		return err
	}
	tmp, err := writeSyncTempFile(syncBody)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	cmd := system.Command{Name: "wg", Args: []string{"syncconf", InterfaceName, tmp}}
	if err := runner.Run(cmd); err != nil {
		return fmt.Errorf("%s: %w", cmd.String(), err)
	}
	return nil
}

// Down stops and disables the wg-quick service for the cluster interface.
func Down(runner system.Runner) error {
	unit := "wg-quick@" + InterfaceName + ".service"
	for _, cmd := range []system.Command{
		{Name: "systemctl", Args: []string{"disable", "--now", unit}},
	} {
		// Best-effort: ignore errors from a unit that does not exist.
		_ = runner.Run(cmd)
	}
	return nil
}

// RemoveConfig deletes the wg-quick config file. Used during uninstall.
func RemoveConfig() error {
	err := os.Remove(ConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writeSyncTempFile drops syncBody to a temp file that "wg syncconf" can
// consume and returns the path. The caller is responsible for removing it.
func writeSyncTempFile(syncBody string) (string, error) {
	tmp, err := os.CreateTemp("", "wg-sync-*.conf")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write([]byte(syncBody)); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
