package system

// Managed systemd unit names.
const (
	SingBoxService = "sing-box.service"
	MonitorService = "singbox-deploy-monitor.service"
)

// Systemctl returns a systemctl command for the given action and unit, e.g.
// Systemctl("restart", SingBoxService).
func Systemctl(action, service string) Command {
	return Command{Name: "systemctl", Args: []string{action, service}}
}
