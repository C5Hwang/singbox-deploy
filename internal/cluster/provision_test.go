package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
)

// TestOpenNodeFirewallAlwaysAllows443 checks that every protocol layout —
// including a Reality-only node that has no public TLS protocol — still ends
// up opening 443/tcp for the masquerade site nginx listener.
func TestOpenNodeFirewallAlwaysAllows443(t *testing.T) {
	cases := []struct {
		name      string
		protocols []config.Protocol
		ports     config.Ports
	}{
		{"reality-only", []config.Protocol{config.ProtocolRealityVision}, config.Ports{RealityVision: 27443}},
		{"hysteria2-only", []config.Protocol{config.ProtocolHysteria2}, config.Ports{Hysteria2: 28443}},
		{"reality+anytls", []config.Protocol{config.ProtocolRealityVision, config.ProtocolAnyTLS}, config.Ports{RealityVision: 27443, AnyTLS: 29443}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeSSHClient()
			if err := openNodeFirewall(context.Background(), client, tc.ports, tc.protocols); err != nil {
				t.Fatalf("openNodeFirewall: %v", err)
			}
			if !runsContain(client.runs, "ufw allow 443/tcp") {
				t.Errorf("expected ufw allow 443/tcp; ran: %v", client.runs)
			}
		})
	}
}

func runsContain(cmds []string, want string) bool {
	for _, c := range cmds {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}
