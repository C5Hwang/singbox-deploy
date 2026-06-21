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

// SyncPeers writes a fresh master config and applies the diff to a running
// interface via "wg syncconf". The interface is reloaded in-place without
// dropping existing peer sessions.
func SyncPeers(runner system.Runner, body string) error {
	if err := WriteConfig(body); err != nil {
		return err
	}
	// wg syncconf accepts only the [Interface]/[Peer] form, not wg-quick's
	// post-up scripts. Strip the AllowedIPs and Address sections nothing —
	// our config has only Interface/Peer entries, which wg accepts as-is.
	stripped, err := stripPath()
	if err != nil {
		return err
	}
	for _, cmd := range []system.Command{
		{Name: "wg", Args: []string{"syncconf", InterfaceName, stripped}},
	} {
		if err := runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd.String(), err)
		}
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

// stripPath writes the current config to a temp file that "wg syncconf" can
// consume. wg syncconf rejects the Address= line that wg-quick injects, so we
// emit only the Interface and Peer sections we generate ourselves (they
// already exclude wg-quick-specific keys).
func stripPath() (string, error) {
	body, err := os.ReadFile(ConfigPath)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "wg-sync-*.conf")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(body); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}
