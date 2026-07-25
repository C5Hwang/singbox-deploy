package agentfirewall

import (
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestScopedFirewallCommands(t *testing.T) {
	base := Rule{
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}

	ufw := base
	ufw.Backend = system.FirewallUFW
	open, err := ufw.OpenCommands()
	if err != nil {
		t.Fatal(err)
	}
	remove, err := ufw.RemoveCommands()
	if err != nil {
		t.Fatal(err)
	}
	if got := open[0].String(); got != "ufw allow in on sbwg0 from 10.90.0.1 to 10.90.0.2 port 19091 proto tcp comment singbox-deploy-agent" {
		t.Fatalf("unexpected UFW open command: %s", got)
	}
	if got := remove[0].String(); got != "ufw --force delete allow in on sbwg0 from 10.90.0.1 to 10.90.0.2 port 19091 proto tcp" {
		t.Fatalf("unexpected UFW remove command: %s", got)
	}

	firewalld := base
	firewalld.Backend = system.FirewallFirewalld
	firewalld.Zone = "public"
	open, err = firewalld.OpenCommands()
	if err != nil {
		t.Fatal(err)
	}
	remove, err = firewalld.RemoveCommands()
	if err != nil {
		t.Fatal(err)
	}
	for _, commands := range [][]system.Command{open, remove} {
		if len(commands) != 2 || commands[1].String() != "firewall-cmd --reload" {
			t.Fatalf("unexpected firewalld command sequence: %+v", commands)
		}
		first := commands[0].String()
		for _, want := range []string{"--zone=public", `source address="10.90.0.1/32"`, `destination address="10.90.0.2/32"`, `port="19091"`} {
			if !strings.Contains(first, want) {
				t.Fatalf("firewalld command missing %q: %s", want, first)
			}
		}
	}
}

func TestRuleStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Rule{
		Backend:   system.FirewallFirewalld,
		Zone:      "work",
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok || got != want {
		t.Fatalf("loaded rule = %+v, %v; want %+v, true", got, ok, want)
	}
}

func TestRuleRejectsUnscopedOrMalformedValues(t *testing.T) {
	for _, rule := range []Rule{
		{Backend: system.FirewallUFW, Interface: "", HubIP: "10.90.0.1", ListenIP: "10.90.0.2", Port: 19091},
		{Backend: system.FirewallUFW, Interface: "sbwg0", HubIP: "not-an-ip", ListenIP: "10.90.0.2", Port: 19091},
		{Backend: system.FirewallUFW, Interface: "sbwg0", HubIP: "10.90.0.1", ListenIP: "10.90.0.2", Port: 0},
		{Backend: system.FirewallFirewalld, Interface: "sbwg0", HubIP: "10.90.0.1", ListenIP: "10.90.0.2", Port: 19091},
	} {
		if err := rule.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", rule)
		}
	}
}
