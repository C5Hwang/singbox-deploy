package hubctl

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// writeHubState seeds the install-state keys that decide which certificates the
// hub's own Nginx holds open.
func writeHubState(t *testing.T, layout paths.Layout, values map[string]string) {
	t.Helper()
	store := state.NewStore(layout.StateDir)
	for name, value := range values {
		if err := store.WriteString(name, value+"\n", 0o600); err != nil {
			t.Fatalf("write state %s: %v", name, err)
		}
	}
}

// A renewed monitor certificate only reaches clients once Nginx is restarted:
// without the reload the new pair sits on disk while the old one stays open,
// and the monitor serves an expired certificate from the day it lapses.
func TestDistributeCertificateReloadsForTheMonitorDomain(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	writeHubState(t, layout, map[string]string{
		"domain": "vpn.example.com", "monitor": "yes", "monitor_domain": "monitor.example.com",
	})
	runner := &hubCommandRunner{}
	ctrl := &Controller{Layout: layout, Runner: runner}

	var events []deploy.Event
	if err := ctrl.DistributeCertificate(context.Background(), "monitor.example.com", io.Discard, func(e deploy.Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("DistributeCertificate: %v", err)
	}
	restarted := false
	for _, cmd := range runner.commands {
		if cmd.Name == "systemctl" && strings.Join(cmd.Args, " ") == "restart nginx" {
			restarted = true
		}
	}
	if !restarted {
		t.Fatalf("a renewed monitor certificate never reached Nginx: %+v", runner.commands)
	}
	if len(events) == 0 {
		t.Fatalf("the hub reload reported no progress")
	}
	if events[0].Label != "Reload hub services" || events[0].Total != 1 {
		t.Fatalf("distribution progress = %+v", events[0])
	}
}

// The monitor's own name is a second certificate the hub's Nginx holds open, so
// deleting it must be refused exactly as deleting the install domain's is.
func TestCertificateConsumersReportsTheMonitorDomain(t *testing.T) {
	cases := []struct {
		name         string
		state        map[string]string
		domain       string
		wantConsumed bool
	}{
		{
			name:         "monitor domain is in use",
			state:        map[string]string{"domain": "vpn.example.com", "monitor": "yes", "monitor_domain": "monitor.example.com"},
			domain:       "monitor.example.com",
			wantConsumed: true,
		},
		{
			name:         "install domain is still in use",
			state:        map[string]string{"domain": "vpn.example.com", "monitor": "yes", "monitor_domain": "monitor.example.com"},
			domain:       "vpn.example.com",
			wantConsumed: true,
		},
		{
			name:         "a disabled monitor holds nothing open",
			state:        map[string]string{"domain": "vpn.example.com", "monitor": "no", "monitor_domain": "monitor.example.com"},
			domain:       "monitor.example.com",
			wantConsumed: false,
		},
		{
			name:         "an unrelated name is not in use",
			state:        map[string]string{"domain": "vpn.example.com", "monitor": "yes", "monitor_domain": "monitor.example.com"},
			domain:       "other.example.com",
			wantConsumed: false,
		},
		{
			name:         "an install predating the monitor domain still protects its own",
			state:        map[string]string{"domain": "vpn.example.com", "monitor": "yes"},
			domain:       "vpn.example.com",
			wantConsumed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			writeHubState(t, layout, tc.state)
			consumers, err := (&Controller{Layout: layout}).CertificateConsumers(tc.domain)
			if err != nil {
				t.Fatalf("CertificateConsumers: %v", err)
			}
			if got := len(consumers) > 0; got != tc.wantConsumed {
				t.Fatalf("consumed = %v, want %v (%+v)", got, tc.wantConsumed, consumers)
			}
			if tc.wantConsumed && consumers[0].ID != "hub" {
				t.Fatalf("consumer = %+v, want the hub", consumers[0])
			}
		})
	}
}

func TestCertificateConsumersKeepStableIDSeparateFromDisplayLabel(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const domain = "uk.example.com"
	if err := nodes.Add(layout, nodes.Node{
		Alias:     "UK UI",
		SSHHost:   "192.0.2.10",
		Domain:    domain,
		WGIP:      "10.90.0.2",
		Installed: true,
	}); err != nil {
		t.Fatalf("add spoke: %v", err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatalf("load spoke: %v", err)
	}
	if len(list) != 1 || list[0].ID == "" {
		t.Fatalf("unexpected spoke registry: %+v", list)
	}
	stableID := list[0].ID

	if err := nodes.Mutate(layout, stableID, func(node *nodes.Node) error {
		node.Alias = "London Edge"
		return nil
	}); err != nil {
		t.Fatalf("rename spoke: %v", err)
	}

	consumers, err := (&Controller{Layout: layout}).CertificateConsumers(domain)
	if err != nil {
		t.Fatalf("CertificateConsumers: %v", err)
	}
	if len(consumers) != 1 {
		t.Fatalf("consumer count = %d, want 1: %+v", len(consumers), consumers)
	}
	if consumers[0].ID != stableID {
		t.Errorf("consumer stable ID = %q, want %q", consumers[0].ID, stableID)
	}
	if consumers[0].Label != "London Edge (uk.example.com)" {
		t.Errorf("consumer label = %q", consumers[0].Label)
	}
	if consumers[0].Label == consumers[0].ID {
		t.Errorf("raw stable ID leaked into operator label: %+v", consumers[0])
	}
}
