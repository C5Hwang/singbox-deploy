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
	Label  string
	Public bool
}

// FirewallCommands returns the commands to open the given ports for the
// detected firewall, including the firewalld reload needed to activate the
// permanent rules. An empty/None firewall returns no commands.
func FirewallCommands(f Firewall, ports []Port) []Command {
	return firewallPortCommands(f, ports, false)
}

// FirewallRemoveCommands returns the commands to close the given ports for the
// detected firewall, including the firewalld reload. An empty/None firewall
// returns no commands.
func FirewallRemoveCommands(f Firewall, ports []Port) []Command {
	return firewallPortCommands(f, ports, true)
}

func firewallPortCommands(f Firewall, ports []Port, remove bool) []Command {
	cmds := make([]Command, 0, len(ports)+1)
	for _, p := range ports {
		if p.Number <= 0 {
			continue
		}
		spec := strconv.Itoa(p.Number) + "/" + p.Proto
		switch f {
		case FirewallUFW:
			if remove {
				cmds = append(cmds, Command{Name: "ufw", Args: []string{"delete", "allow", spec}})
			} else {
				cmds = append(cmds, Command{Name: "ufw", Args: []string{"allow", spec}})
			}
		case FirewallFirewalld:
			flag := "--add-port=" + spec
			if remove {
				flag = "--remove-port=" + spec
			}
			cmds = append(cmds, Command{Name: "firewall-cmd", Args: []string{flag, "--permanent"}})
		}
	}
	// Permanent firewalld rules only take effect after a reload; folding it in
	// here keeps every caller from re-deriving it (and forgetting it).
	if f == FirewallFirewalld && len(cmds) > 0 {
		cmds = append(cmds, Command{Name: "firewall-cmd", Args: []string{"--reload"}})
	}
	return cmds
}
