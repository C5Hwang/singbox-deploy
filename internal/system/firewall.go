package system

import "strconv"

// Firewall identifies the host firewall front-end.
type Firewall string

const (
	FirewallUFW       Firewall = "ufw"
	FirewallFirewalld Firewall = "firewalld"
	FirewallNone      Firewall = ""
)

// Port is a numbered port with its transport protocol ("tcp" or "udp").
type Port struct {
	Number int
	Proto  string
}

// FirewallCommands returns the commands to open the given ports for the
// detected firewall. An empty/None firewall returns no commands.
func FirewallCommands(f Firewall, ports []Port) []Command {
	cmds := make([]Command, 0, len(ports))
	for _, p := range ports {
		spec := strconv.Itoa(p.Number) + "/" + p.Proto
		switch f {
		case FirewallUFW:
			cmds = append(cmds, Command{Name: "ufw", Args: []string{"allow", spec}})
		case FirewallFirewalld:
			cmds = append(cmds, Command{Name: "firewall-cmd", Args: []string{"--add-port=" + spec, "--permanent"}})
		}
	}
	return cmds
}
