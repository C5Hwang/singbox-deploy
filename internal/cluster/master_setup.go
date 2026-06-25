package cluster

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/wireguard"
)

// EnsureMasterWireGuard makes sure the master is fully set up to serve as the
// hub of its WireGuard subnet: wireguard-tools installed, the master config
// rendered with the current peer set, wg-quick@wg-sdeploy enabled, and the
// WireGuard UDP listen port allowed through the local firewall. Safe to call
// repeatedly — every step is idempotent.
func (o *Orchestrator) EnsureMasterWireGuard(_ context.Context) error {
	if err := o.installMasterWireGuardPackage(); err != nil {
		return err
	}
	if err := o.openMasterWireGuardPort(); err != nil {
		return err
	}
	keys, err := o.Registry.EnsureMasterKeys()
	if err != nil {
		return err
	}
	existing, err := o.Registry.List()
	if err != nil {
		return err
	}
	peers := make([]wireguard.Peer, 0, len(existing))
	for _, n := range existing {
		peers = append(peers, wireguard.Peer{Alias: n.Alias, PublicKey: n.WGPublicKey, IP: n.WGIP})
	}
	body, err := wireguard.RenderMaster(wireguard.MasterConfig{
		PrivateKey: keys.PrivateKey,
		ListenPort: wireguard.DefaultListenPort,
		Peers:      peers,
	}, false)
	if err != nil {
		return err
	}
	if err := wireguard.WriteConfig(body); err != nil {
		return err
	}
	return wireguard.EnableAndStart(o.Runner)
}

// openMasterWireGuardPort opens UDP DefaultListenPort on whichever managed
// firewall the master uses (ufw / firewalld). Without this, nodes' WireGuard
// handshake packets to the master are silently dropped on hosts where the
// installer's initial `Firewall` step left default-deny inbound in place.
// Both ufw and firewalld treat re-adding an existing rule as a no-op so this
// is safe to run on every AddNode.
func (o *Orchestrator) openMasterWireGuardPort() error {
	fw := system.DetectFirewall()
	if fw == system.FirewallNone {
		return nil
	}
	cmds := system.FirewallCommands(fw, []system.Port{{Number: wireguard.DefaultListenPort, Proto: "udp"}})
	if fw == system.FirewallFirewalld {
		cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
	}
	for _, c := range cmds {
		if err := o.Runner.Run(c); err != nil {
			return fmt.Errorf("open WireGuard listen port: %w", err)
		}
	}
	return nil
}

// installMasterWireGuardPackage installs wireguard-tools on the master using
// whichever package manager is available. Skips installation when `wg` is
// already on the PATH.
func (o *Orchestrator) installMasterWireGuardPackage() error {
	if _, err := exec.LookPath("wg"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return o.Runner.Run(system.Command{Name: "bash", Args: []string{"-c", "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard"}})
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return o.Runner.Run(system.Command{Name: "dnf", Args: []string{"install", "-y", "wireguard-tools"}})
	}
	if _, err := exec.LookPath("yum"); err == nil {
		return o.Runner.Run(system.Command{Name: "yum", Args: []string{"install", "-y", "wireguard-tools"}})
	}
	return fmt.Errorf("could not find a supported package manager (apt-get/dnf/yum) on the master")
}
