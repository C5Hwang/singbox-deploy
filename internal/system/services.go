package system

// Managed systemd unit names.
const (
	SingBoxService   = "sing-box.service"
	MonitorService   = "singbox-deploy-monitor.service"
	CertRenewService = "singbox-deploy-cert-renew.service"
	CertRenewTimer   = "singbox-deploy-cert-renew.timer"
	// RelayService reinstalls the relay forwarding ruleset after a reboot.
	// nftables rules do not survive one, and the node it fronts keeps
	// publishing this relay's address in the meantime.
	RelayService = "singbox-deploy-relay.service"
)

// Systemctl returns a systemctl command for the given action and unit, e.g.
// Systemctl("restart", SingBoxService).
func Systemctl(action, service string) Command {
	return Command{Name: "systemctl", Args: []string{action, service}}
}
