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

// ForwardRoute is one flow the host forwards on to another machine, described
// by the address and port it was rewritten to.
type ForwardRoute struct {
	Proto   string
	Address string
	Port    int
}

// FirewallForwardCommands returns the commands that permit (or withdraw)
// forwarded flows. A packet the kernel forwards is never delivered locally, so
// opening an inbound port does nothing for it: ufw and firewalld both filter
// forwarded traffic in a separate chain that defaults to deny.
//
// ufw has a first-class notion of this in `ufw route`. firewalld does not
// expose one for traffic leaving the host, so a direct rule is used; it is the
// narrowest thing firewalld offers that survives a reload.
func FirewallForwardCommands(f Firewall, routes []ForwardRoute, remove bool) []Command {
	cmds := make([]Command, 0, len(routes)+1)
	for _, r := range routes {
		if r.Port <= 0 || r.Address == "" || (r.Proto != "tcp" && r.Proto != "udp") {
			continue
		}
		port := strconv.Itoa(r.Port)
		switch f {
		case FirewallUFW:
			args := []string{"route", "allow", "proto", r.Proto, "from", "any", "to", r.Address, "port", port}
			if remove {
				args = append([]string{"route", "delete"}, args[1:]...)
			}
			cmds = append(cmds, Command{Name: "ufw", Args: args})
		case FirewallFirewalld:
			action := "--add-rule"
			if remove {
				action = "--remove-rule"
			}
			cmds = append(cmds, Command{Name: "firewall-cmd", Args: []string{
				"--permanent", "--direct", action, "ipv4", "filter", "FORWARD", "0",
				"-p", r.Proto, "-d", r.Address, "--dport", port, "-j", "ACCEPT",
			}})
		}
	}
	if f == FirewallFirewalld && len(cmds) > 0 {
		cmds = append(cmds, Command{Name: "firewall-cmd", Args: []string{"--reload"}})
	}
	return cmds
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
