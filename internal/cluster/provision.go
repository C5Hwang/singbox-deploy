package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/release"
)

// downloadSingBoxCore downloads the sing-box core tarball, extracts the
// binary, and installs it under /etc/singbox-deploy/sing-box/. Used during
// initial node provisioning so the agent has a working core to start when the
// master pushes config.
func downloadSingBoxCore(ctx context.Context, client sshClient, coreVersion string) error {
	arch, err := detectArch(ctx, client)
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(strings.TrimSpace(coreVersion), "v")
	archive := release.SingBoxArchiveName("v"+version, "linux", arch)
	url := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/%s", version, archive)
	extracted := fmt.Sprintf("sing-box-%s-linux-%s", version, arch)
	script := strings.Join([]string{
		"set -e",
		"mkdir -p /etc/singbox-deploy/sing-box/conf/fragments",
		"tmp=$(mktemp -d)",
		fmt.Sprintf("curl -fsSL %s -o ${tmp}/sing-box.tar.gz", shellQuote(url)),
		"tar -xzf ${tmp}/sing-box.tar.gz -C ${tmp}",
		fmt.Sprintf("install -m 0755 ${tmp}/%s/sing-box /etc/singbox-deploy/sing-box/sing-box", extracted),
		"rm -rf ${tmp}",
	}, " && ")
	if _, err := client.MustRun(ctx, script); err != nil {
		return fmt.Errorf("install sing-box core: %w", err)
	}
	return nil
}

// installSingBoxUnit writes the sing-box.service systemd unit and registers
// (but does not start) the service. The unit is started by the agent later
// after the initial config push.
func installSingBoxUnit(ctx context.Context, client sshClient) error {
	unit := `[Unit]
Description=Sing-Box Service
After=network.target nss-lookup.target

[Service]
User=root
ExecStart=/etc/singbox-deploy/sing-box/sing-box run -c /etc/singbox-deploy/sing-box/conf/config.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=10
LimitNPROC=infinity
LimitNOFILE=infinity

[Install]
WantedBy=multi-user.target
`
	if err := client.WriteFile(ctx, "/etc/systemd/system/sing-box.service", []byte(unit), 0o644); err != nil {
		return err
	}
	_, err := client.MustRun(ctx, "systemctl daemon-reload")
	return err
}

// openNodeFirewall opens the protocol listen ports on the node using whichever
// firewall front-end (ufw / firewalld) is installed. If no firewall is
// detected, the node ports are assumed to be already accessible.
func openNodeFirewall(ctx context.Context, client sshClient, ports config.Ports, protocols []config.Protocol) error {
	specs := protocolFirewallSpecs(ports, protocols)
	// 443/tcp is always opened for the masquerade site; every node serves it
	// regardless of which sing-box protocols are enabled.
	specs = append(specs, firewallSpec{port: 443, proto: "tcp"})
	if len(specs) == 0 {
		return nil
	}
	// Detect firewall.
	if res, _ := client.Run(ctx, "command -v ufw"); res.ExitCode == 0 {
		for _, s := range specs {
			if _, err := client.MustRun(ctx, fmt.Sprintf("ufw allow %d/%s", s.port, s.proto)); err != nil {
				return err
			}
		}
		return nil
	}
	if res, _ := client.Run(ctx, "command -v firewall-cmd"); res.ExitCode == 0 {
		for _, s := range specs {
			if _, err := client.MustRun(ctx, fmt.Sprintf("firewall-cmd --add-port=%d/%s --permanent", s.port, s.proto)); err != nil {
				return err
			}
		}
		_, err := client.MustRun(ctx, "firewall-cmd --reload")
		return err
	}
	// No managed firewall — assume the host's network already allows inbound.
	return nil
}

type firewallSpec struct {
	port  int
	proto string
}

func protocolFirewallSpecs(ports config.Ports, protocols []config.Protocol) []firewallSpec {
	want := map[config.Protocol]firewallSpec{
		config.ProtocolRealityVision: {port: ports.RealityVision, proto: "tcp"},
		config.ProtocolRealityGRPC:   {port: ports.RealityGRPC, proto: "tcp"},
		config.ProtocolHysteria2:     {port: ports.Hysteria2, proto: "udp"},
		config.ProtocolTUIC:          {port: ports.TUIC, proto: "udp"},
		config.ProtocolAnyTLS:        {port: ports.AnyTLS, proto: "tcp"},
	}
	var specs []firewallSpec
	for _, p := range protocols {
		if spec, ok := want[p]; ok && spec.port > 0 {
			specs = append(specs, spec)
		}
	}
	return specs
}

// issueAndDeployNodeCert issues a TLS certificate for the node's domain via
// DNS-01 ACME and pushes the renewed material to the node via the agent API.
// Every node serves the masquerade site on 443, so the certificate is
// required regardless of which protocol set the node runs.
func (o *Orchestrator) issueAndDeployNodeCert(ctx context.Context, node Node) error {
	if o.ACME == nil {
		return fmt.Errorf("ACME manager is not configured")
	}
	creds, err := o.Registry.DNS().FindForDomain(node.Domain)
	if err != nil {
		return fmt.Errorf("dns credentials for %s: %w", node.Domain, err)
	}
	cert, err := o.ACME.Obtain(ctx, acme.Request{
		Domain:      node.Domain,
		DNSProvider: creds.Provider,
		Credentials: creds.EnvMap(),
	})
	if err != nil {
		return fmt.Errorf("issue cert: %w", err)
	}
	agent := NewAgentClient(node)
	if err := agent.DeployCert(ctx, CertDeploy{
		Cert: string(cert.CertificatePEM),
		Key:  string(cert.PrivateKeyPEM),
	}); err != nil {
		return fmt.Errorf("push cert to node: %w", err)
	}
	return nil
}

// deployNodeSite asks the node to install Nginx and the masquerade site.
func (o *Orchestrator) deployNodeSite(ctx context.Context, node Node, siteTemplate string) error {
	agent := NewAgentClient(node)
	return agent.DeploySite(ctx, SiteDeploy{
		Domain:       node.Domain,
		SiteTemplate: siteTemplate,
	})
}

// pushInitialConfig sends the node's first sing-box config update so the
// node renders config.json and starts sing-box.service.
func (o *Orchestrator) pushInitialConfig(ctx context.Context, node Node) error {
	agent := NewAgentClient(node)
	return agent.UpdateConfig(ctx, ConfigUpdate{
		EnabledProtocols:     protocolStrings(node.EnabledProtocols),
		ProtocolPorts:        protocolPortsMap(node.Ports, node.EnabledProtocols),
		Domain:               node.Domain,
		Credentials:          credentialsMap(node.Creds),
		RealityServerName:    node.RealityServerName,
		RealityHandshakePort: node.RealityHandshakePort,
	})
}

func protocolStrings(protocols []config.Protocol) []string {
	out := make([]string, len(protocols))
	for i, p := range protocols {
		out[i] = string(p)
	}
	return out
}

func protocolPortsMap(ports config.Ports, protocols []config.Protocol) map[string]int {
	out := map[string]int{}
	mapping := map[config.Protocol]int{
		config.ProtocolRealityVision: ports.RealityVision,
		config.ProtocolRealityGRPC:   ports.RealityGRPC,
		config.ProtocolHysteria2:     ports.Hysteria2,
		config.ProtocolTUIC:          ports.TUIC,
		config.ProtocolAnyTLS:        ports.AnyTLS,
	}
	for _, p := range protocols {
		if port, ok := mapping[p]; ok && port > 0 {
			out[string(p)] = port
		}
	}
	return out
}

// credentialsMap exposes the per-protocol credential map for ConfigUpdate.
// Only non-empty fields are included so the agent doesn't blank existing
// values when receiving a partial update.
func credentialsMap(creds deploy.Credentials) map[string]string {
	out := map[string]string{}
	pairs := []struct{ key, value string }{
		{"reality_vision_uuid", creds.RealityVisionUUID},
		{"reality_grpc_uuid", creds.RealityGRPCUUID},
		{"hysteria2_password", creds.HysteriaPassword},
		{"tuic_uuid", creds.TUICUUID},
		{"tuic_password", creds.TUICPassword},
		{"anytls_password", creds.AnyTLSPassword},
		{"reality_private_key", creds.RealityPrivateKey},
		{"reality_public_key", creds.RealityPublicKey},
		{"reality_short_id", creds.RealityShortID},
	}
	for _, p := range pairs {
		if p.value != "" {
			out[p.key] = p.value
		}
	}
	return out
}
