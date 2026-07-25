// Package agentfirewall owns the host-firewall rule that admits a Hub to a
// spoke Agent API over the managed WireGuard interface.
package agentfirewall

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const (
	BackendFile   = "firewall_backend"
	ZoneFile      = "firewall_zone"
	HubIPFile     = "hub_ip"
	ListenIPFile  = "listen_ip"
	AgentPortFile = "agent_port"
	InterfaceFile = "interface"
)

// Rule is the exact, source- and interface-scoped Agent API firewall rule.
type Rule struct {
	Backend   system.Firewall
	Zone      string
	Interface string
	HubIP     string
	ListenIP  string
	Port      int
}

// Validate rejects malformed state before it reaches a privileged command.
func (r Rule) Validate() error {
	if r.Backend != system.FirewallNone && r.Backend != system.FirewallUFW && r.Backend != system.FirewallFirewalld {
		return fmt.Errorf("unsupported agent firewall backend %q", r.Backend)
	}
	if r.Backend == system.FirewallNone {
		return nil
	}
	hub, err := netip.ParseAddr(strings.TrimSpace(r.HubIP))
	if err != nil {
		return fmt.Errorf("invalid Hub overlay IP %q: %w", r.HubIP, err)
	}
	listen, err := netip.ParseAddr(strings.TrimSpace(r.ListenIP))
	if err != nil {
		return fmt.Errorf("invalid Agent listen IP %q: %w", r.ListenIP, err)
	}
	if hub.BitLen() != listen.BitLen() {
		return fmt.Errorf("Hub and Agent firewall addresses use different families")
	}
	if r.Port <= 0 || r.Port > 65535 {
		return fmt.Errorf("agent firewall port must be between 1 and 65535")
	}
	if !validInterface(r.Interface) {
		return fmt.Errorf("invalid WireGuard interface %q", r.Interface)
	}
	if r.Backend == system.FirewallFirewalld && strings.TrimSpace(r.Zone) == "" {
		return fmt.Errorf("firewalld zone is required")
	}
	return nil
}

// RichRule returns the firewalld rich-rule expression for r.
func (r Rule) RichRule() string {
	family, prefix := "ipv4", 32
	if ip, err := netip.ParseAddr(r.HubIP); err == nil && ip.Is6() {
		family, prefix = "ipv6", 128
	}
	return fmt.Sprintf(`rule family="%s" source address="%s/%d" destination address="%s/%d" port port="%d" protocol="tcp" accept`,
		family, r.HubIP, prefix, r.ListenIP, prefix, r.Port)
}

// OpenCommands returns idempotent commands that install r.
func (r Rule) OpenCommands() ([]system.Command, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	switch r.Backend {
	case system.FirewallNone:
		return nil, nil
	case system.FirewallUFW:
		return []system.Command{{Name: "ufw", Args: []string{
			"allow", "in", "on", r.Interface, "from", r.HubIP, "to", r.ListenIP,
			"port", strconv.Itoa(r.Port), "proto", "tcp", "comment", "singbox-deploy-agent",
		}}}, nil
	case system.FirewallFirewalld:
		return []system.Command{
			{Name: "firewall-cmd", Args: []string{"--permanent", "--zone=" + r.Zone, "--add-rich-rule=" + r.RichRule()}},
			{Name: "firewall-cmd", Args: []string{"--reload"}},
		}, nil
	default:
		panic("validated firewall backend is not handled")
	}
}

// RemoveCommands returns commands that remove r.
func (r Rule) RemoveCommands() ([]system.Command, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	switch r.Backend {
	case system.FirewallNone:
		return nil, nil
	case system.FirewallUFW:
		return []system.Command{{Name: "ufw", Args: []string{
			"--force", "delete", "allow", "in", "on", r.Interface, "from", r.HubIP,
			"to", r.ListenIP, "port", strconv.Itoa(r.Port), "proto", "tcp",
		}}}, nil
	case system.FirewallFirewalld:
		return []system.Command{
			{Name: "firewall-cmd", Args: []string{"--permanent", "--zone=" + r.Zone, "--remove-rich-rule=" + r.RichRule()}},
			{Name: "firewall-cmd", Args: []string{"--reload"}},
		}, nil
	default:
		panic("validated firewall backend is not handled")
	}
}

// Load reads a rule saved under the Agent's private state directory. A missing
// backend file means the node predates managed Agent firewall rules.
func Load(agentStateDir string) (Rule, bool, error) {
	store := state.NewStore(agentStateDir)
	backend, err := store.ReadValue(BackendFile, false)
	if err != nil || strings.TrimSpace(backend) == "" {
		return Rule{}, false, nil
	}
	if strings.TrimSpace(backend) == "none" {
		return Rule{Backend: system.FirewallNone}, true, nil
	}
	portText, err := store.ReadValue(AgentPortFile, true)
	if err != nil {
		return Rule{}, false, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Rule{}, false, fmt.Errorf("parse saved Agent firewall port: %w", err)
	}
	rule := Rule{
		Backend:   system.Firewall(strings.TrimSpace(backend)),
		Zone:      readOptional(store, ZoneFile),
		Interface: readOptional(store, InterfaceFile),
		HubIP:     readOptional(store, HubIPFile),
		ListenIP:  readOptional(store, ListenIPFile),
		Port:      port,
	}
	if err := rule.Validate(); err != nil {
		return Rule{}, false, err
	}
	return rule, true, nil
}

// Save restores enough private state to retry firewall cleanup after another
// uninstall step has already removed the runtime state directory.
func Save(agentStateDir string, r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	store := state.NewStore(agentStateDir)
	values := map[string]string{
		BackendFile:   string(r.Backend),
		ZoneFile:      r.Zone,
		HubIPFile:     r.HubIP,
		ListenIPFile:  r.ListenIP,
		AgentPortFile: strconv.Itoa(r.Port),
		InterfaceFile: r.Interface,
	}
	for name, value := range values {
		if err := store.WriteString(name, value+"\n", 0o600); err != nil {
			return err
		}
	}
	return nil
}

func readOptional(store state.Store, name string) string {
	value, _ := store.ReadValue(name, false)
	return strings.TrimSpace(value)
}

func validInterface(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 15 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}
