package system

// Managed systemd unit names.
const (
	SingBoxService   = "sing-box.service"
	MonitorService   = "singbox-deploy-monitor.service"
	CertRenewService = "singbox-deploy-cert-renew.service"
	CertRenewTimer   = "singbox-deploy-cert-renew.timer"
)

// Systemctl returns a systemctl command for the given action and unit, e.g.
// Systemctl("restart", SingBoxService).
func Systemctl(action, service string) Command {
	return Command{Name: "systemctl", Args: []string{action, service}}
}
