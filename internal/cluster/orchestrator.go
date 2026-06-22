package cluster

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/sshexec"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/wireguard"
)

// osFileMode is a tiny shim so the orchestrator can keep uint32 in its
// internal interface while sshexec accepts os.FileMode.
func osFileMode(m uint32) os.FileMode { return os.FileMode(m) }

// AddNodeRequest carries everything the user provided in the TUI before the
// orchestrator can SSH into the new node.
type AddNodeRequest struct {
	Alias             string
	PublicIP          string
	SSHTarget         sshexec.Target
	SSHAuth           sshexec.Auth
	Domain            string
	EnabledProtocols  []config.Protocol
	Ports             config.Ports
	RealityServerName string

	// Version is the release tag the node should download to match the master.
	Version string

	// CoreVersion is the sing-box core version (without leading "v") the node
	// downloads from upstream. Required so node and master run matching cores.
	CoreVersion string

	// SiteTemplate names the masquerade site template the node serves when
	// TLS protocols are enabled. Defaults to deploy.DefaultSiteTemplate.
	SiteTemplate string

	// MasterPublicEndpoint is the master's externally-reachable host:port the
	// node uses as its WireGuard Endpoint.
	MasterPublicEndpoint string

	// CredentialOverrides lets the caller supply explicit UUID/password values
	// for individual protocols; non-empty fields replace the auto-generated
	// secrets, blank fields keep the random ones. Reality key material is
	// always derived from the freshly generated keypair.
	CredentialOverrides deploy.Credentials
}

// Event reports the progress of one orchestration step to the caller (TUI).
type Event struct {
	Index  int
	Total  int
	Label  string
	Detail string
	Status string // "running", "ok", "fail"
	Err    error
}

// Orchestrator coordinates node lifecycle operations on top of the Registry.
// SSH and WireGuard reload commands are pluggable so the flow is testable.
type Orchestrator struct {
	Registry Registry
	Runner   system.Runner // executes WireGuard reload on the master

	// ACME issues node certificates via DNS-01 using the registry's DNS
	// credential store. Required when adding nodes that use TLS protocols.
	ACME *acme.Manager

	// SSHDial opens an SSH connection to a node. Defaults to sshexec.Dial.
	SSHDial func(ctx context.Context, target sshexec.Target, auth sshexec.Auth) (sshClient, error)

	// VerifyConnectivity tests that a newly added node is reachable on its
	// WireGuard IP. Defaults to a TCP dial against the agent port with a
	// 15s deadline. Tests can substitute a faster check.
	VerifyConnectivity func(ctx context.Context, node Node) error

	// PostRegister runs after the node is saved to the registry. Default does
	// nothing; the AddNode flow appends the cert / site / initial-config
	// steps automatically. Callers (mostly tests) can use this hook to inject
	// extra behaviour or stub out the live agent calls entirely.
	PostRegister func(ctx context.Context, node Node) error

	// Progress receives a step Event for every orchestration phase.
	Progress func(Event)
}

// sshClient is the interface the orchestrator needs from an SSH connection.
// Both sshexec.Client and test fakes implement it.
type sshClient interface {
	Run(ctx context.Context, cmd string) (sshexec.RunResult, error)
	MustRun(ctx context.Context, cmd string) (sshexec.RunResult, error)
	WriteFile(ctx context.Context, path string, content []byte, mode uint32) error
	Close() error
}

// addStep is one labeled orchestration action.
type addStep struct {
	label  string
	detail string
	run    func(ctx context.Context) error
}

// AddNode runs the full add-node flow. On success, the new node is registered
// and the master's WireGuard config has been reloaded with the new peer. The
// master's own WireGuard setup is ensured (installing wireguard-tools,
// rendering wg-sdeploy.conf, enabling wg-quick) before any remote action.
func (o *Orchestrator) AddNode(ctx context.Context, req AddNodeRequest) (Node, error) {
	if err := req.Validate(); err != nil {
		return Node{}, err
	}
	if err := o.EnsureMasterWireGuard(ctx); err != nil {
		return Node{}, fmt.Errorf("ensure master WireGuard: %w", err)
	}

	// Allocate identity and credentials before any remote action — failure
	// here costs nothing because nothing has changed on the node yet.
	id, err := o.Registry.AllocateNextID()
	if err != nil {
		return Node{}, err
	}
	assigned, err := o.Registry.AssignedWGIPs()
	if err != nil {
		return Node{}, err
	}
	wgIP, err := wireguard.AllocateIP(assigned)
	if err != nil {
		return Node{}, err
	}
	masterKeys, err := o.Registry.EnsureMasterKeys()
	if err != nil {
		return Node{}, err
	}
	nodeKeys, err := wireguard.GenerateKeyPair()
	if err != nil {
		return Node{}, err
	}
	apiToken, err := credentials.Password()
	if err != nil {
		return Node{}, err
	}
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		return Node{}, err
	}
	creds.ApplyOverrides(req.CredentialOverrides)

	node := Node{
		ID:                     id,
		Alias:                  strings.TrimSpace(req.Alias),
		PublicIP:               strings.TrimSpace(req.PublicIP),
		Domain:                 strings.TrimSpace(req.Domain),
		WGIP:                   wgIP,
		WGPublicKey:            nodeKeys.PublicKey,
		APIToken:               apiToken,
		EnabledProtocols:       canonicalProtocols(req.EnabledProtocols),
		Ports:                  req.Ports,
		RealityServerName:      req.RealityServerName,
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		MonitorInterface:       "",
		MonitorIntervalSeconds: deploy.DefaultMonitorIntervalSeconds,
		ResetDay:               deploy.DefaultResetDay,
		ResetHour:              deploy.DefaultResetHour,
		Creds:                  creds,
	}

	dial := o.SSHDial
	if dial == nil {
		dial = func(ctx context.Context, t sshexec.Target, a sshexec.Auth) (sshClient, error) {
			c, err := sshexec.Dial(ctx, t, a)
			if err != nil {
				return nil, err
			}
			return realClient{c}, nil
		}
	}

	client, err := dial(ctx, req.SSHTarget, req.SSHAuth)
	if err != nil {
		return Node{}, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	wgConfig, err := wireguard.RenderNode(wireguard.NodeConfig{
		PrivateKey:      nodeKeys.PrivateKey,
		IP:              wgIP,
		MasterPublicKey: masterKeys.PublicKey,
		MasterEndpoint:  req.MasterPublicEndpoint,
	})
	if err != nil {
		return Node{}, fmt.Errorf("render node wg config: %w", err)
	}

	siteTemplate := strings.TrimSpace(req.SiteTemplate)
	if siteTemplate == "" {
		siteTemplate = deploy.DefaultSiteTemplate
	}

	steps := []addStep{
		{"Preflight", "install base packages (curl, ca-certificates, tar, WireGuard) on the node", func(ctx context.Context) error {
			return installNodeBasePackages(ctx, client)
		}},
		{"Binaries", "download singbox-node and singbox-monitor", func(ctx context.Context) error {
			return downloadAgentBinaries(ctx, client, req.Version)
		}},
		{"sing-box core", "download sing-box core on the node", func(ctx context.Context) error {
			return downloadSingBoxCore(ctx, client, req.CoreVersion)
		}},
		{"sing-box unit", "install sing-box systemd unit (started later)", func(ctx context.Context) error {
			return installSingBoxUnit(ctx, client)
		}},
		{"Firewall", "open protocol ports on the node", func(ctx context.Context) error {
			return openNodeFirewall(ctx, client, node.Ports, node.EnabledProtocols)
		}},
		{"WireGuard", "write node config and start tunnel", func(ctx context.Context) error {
			if err := client.WriteFile(ctx, wireguard.ConfigPath, []byte(wgConfig), 0o600); err != nil {
				return err
			}
			_, err := client.MustRun(ctx, "systemctl enable --now wg-quick@"+wireguard.InterfaceName+".service")
			return err
		}},
		{"Agent state", "write API token and start node agent", func(ctx context.Context) error {
			return setupAgentState(ctx, client, node, masterKeys.PublicKey, req.MasterPublicEndpoint)
		}},
		{"Register peer", "add node to master WireGuard peers", func(ctx context.Context) error {
			return o.applyMasterPeers(node)
		}},
		{"Connectivity", "verify WireGuard handshake", func(ctx context.Context) error {
			verify := o.VerifyConnectivity
			if verify == nil {
				verify = o.verifyConnectivity
			}
			return verify(ctx, node)
		}},
		{"Register node", "save node to state/nodes/", func(ctx context.Context) error {
			return o.Registry.Save(node)
		}},
	}
	if o.PostRegister != nil {
		steps = append(steps, addStep{"Post-register", "run caller-provided post-registration hook", func(ctx context.Context) error {
			return o.PostRegister(ctx, node)
		}})
	} else {
		steps = append(steps,
			addStep{"Issue cert", "issue TLS certificate via DNS-01", func(ctx context.Context) error {
				return o.issueAndDeployNodeCert(ctx, node)
			}},
			addStep{"Site", "deploy masquerade site and start Nginx on the node", func(ctx context.Context) error {
				return o.deployNodeSite(ctx, node, siteTemplate)
			}},
			addStep{"Initial config", "push initial sing-box config and start the service", func(ctx context.Context) error {
				return o.pushInitialConfig(ctx, node)
			}},
		)
	}

	for i, s := range steps {
		o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx); err != nil {
			o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Node{}, fmt.Errorf("%s: %w", s.label, err)
		}
		o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return node, nil
}

// RemoveNode tears down a registered node. If the agent is unreachable and
// force is true, the master-side cleanup proceeds anyway and the operator
// must reclaim the node manually.
func (o *Orchestrator) RemoveNode(ctx context.Context, id string, force bool) error {
	node, err := o.Registry.Load(id)
	if err != nil {
		return fmt.Errorf("load node: %w", err)
	}

	steps := []addStep{
		{"Agent teardown", "request node self-destruct", func(ctx context.Context) error {
			client := NewAgentClient(node)
			err := client.Teardown(ctx)
			if err != nil && !force {
				return err
			}
			return nil
		}},
		{"Unpeer", "remove node from master WireGuard config", func(ctx context.Context) error {
			return o.removeMasterPeer(node)
		}},
		{"Deregister", "delete node from state/nodes/", func(ctx context.Context) error {
			return o.Registry.Delete(id)
		}},
	}
	for i, s := range steps {
		o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx); err != nil {
			o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			if !force {
				return fmt.Errorf("%s: %w", s.label, err)
			}
		} else {
			o.emit(Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
		}
	}
	return nil
}

// applyMasterPeers re-renders the master WireGuard config from the full
// peer set including the new node, then reloads the interface in place.
func (o *Orchestrator) applyMasterPeers(newNode Node) error {
	keys, err := o.Registry.MasterKeys()
	if err != nil {
		return err
	}
	existing, err := o.Registry.List()
	if err != nil {
		return err
	}
	peers := make([]wireguard.Peer, 0, len(existing)+1)
	for _, n := range existing {
		if n.ID == newNode.ID {
			continue
		}
		peers = append(peers, wireguard.Peer{
			Alias: n.Alias, PublicKey: n.WGPublicKey, IP: n.WGIP,
		})
	}
	peers = append(peers, wireguard.Peer{
		Alias: newNode.Alias, PublicKey: newNode.WGPublicKey, IP: newNode.WGIP,
	})
	cfg := wireguard.MasterConfig{
		PrivateKey: keys.PrivateKey,
		ListenPort: wireguard.DefaultListenPort,
		Peers:      peers,
	}
	fullBody, err := wireguard.RenderMaster(cfg, false)
	if err != nil {
		return err
	}
	syncBody, err := wireguard.RenderMaster(cfg, true)
	if err != nil {
		return err
	}
	return wireguard.SyncPeers(o.Runner, fullBody, syncBody)
}

// removeMasterPeer regenerates the master WireGuard config without the named
// node and applies the diff in place.
func (o *Orchestrator) removeMasterPeer(removed Node) error {
	keys, err := o.Registry.MasterKeys()
	if err != nil {
		return err
	}
	existing, err := o.Registry.List()
	if err != nil {
		return err
	}
	peers := make([]wireguard.Peer, 0, len(existing))
	for _, n := range existing {
		if n.ID == removed.ID {
			continue
		}
		peers = append(peers, wireguard.Peer{
			Alias: n.Alias, PublicKey: n.WGPublicKey, IP: n.WGIP,
		})
	}
	cfg := wireguard.MasterConfig{
		PrivateKey: keys.PrivateKey,
		ListenPort: wireguard.DefaultListenPort,
		Peers:      peers,
	}
	fullBody, err := wireguard.RenderMaster(cfg, false)
	if err != nil {
		return err
	}
	syncBody, err := wireguard.RenderMaster(cfg, true)
	if err != nil {
		return err
	}
	return wireguard.SyncPeers(o.Runner, fullBody, syncBody)
}

// verifyConnectivity pings the node's WireGuard IP from the master.
func (o *Orchestrator) verifyConnectivity(ctx context.Context, node Node) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(node.WGIP, strconv.Itoa(19091)), 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("node %s did not become reachable on %s", node.Alias, node.WGIP)
}

func (o *Orchestrator) emit(e Event) {
	if o.Progress != nil {
		o.Progress(e)
	}
}

// Validate checks the request before any remote action.
func (r AddNodeRequest) Validate() error {
	if strings.TrimSpace(r.Alias) == "" {
		return fmt.Errorf("alias is required")
	}
	if strings.TrimSpace(r.SSHTarget.Host) == "" {
		return fmt.Errorf("ssh host is required")
	}
	if strings.TrimSpace(r.Domain) == "" {
		return fmt.Errorf("domain is required")
	}
	if len(r.EnabledProtocols) == 0 {
		return fmt.Errorf("at least one protocol must be enabled")
	}
	if strings.TrimSpace(r.MasterPublicEndpoint) == "" {
		return fmt.Errorf("master public endpoint is required")
	}
	if strings.TrimSpace(r.Version) == "" {
		return fmt.Errorf("version is required so the node downloads matching binaries")
	}
	if strings.TrimSpace(r.CoreVersion) == "" {
		return fmt.Errorf("core version is required so the node downloads a matching sing-box core")
	}
	return nil
}

func canonicalProtocols(in []config.Protocol) []config.Protocol {
	seen := map[config.Protocol]bool{}
	for _, p := range in {
		seen[p] = true
	}
	var out []config.Protocol
	for _, p := range config.AllProtocols {
		if seen[p] {
			out = append(out, p)
		}
	}
	return out
}

// installNodeBasePackages installs the packages every node needs before the
// rest of the add-node flow runs: curl (binary/core downloads), ca-certificates
// (TLS verification for those downloads), tar (sing-box core extraction), and
// the WireGuard userspace tools. Supports Debian-family (apt) and RHEL-family
// (dnf/yum) hosts.
func installNodeBasePackages(ctx context.Context, client sshClient) error {
	// Detect package manager.
	for _, candidate := range []struct{ check, install string }{
		{"command -v apt-get", "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates tar wireguard"},
		{"command -v dnf", "dnf install -y curl ca-certificates tar wireguard-tools"},
		{"command -v yum", "yum install -y curl ca-certificates tar wireguard-tools"},
	} {
		if res, _ := client.Run(ctx, candidate.check); res.ExitCode == 0 {
			_, err := client.MustRun(ctx, candidate.install)
			return err
		}
	}
	return fmt.Errorf("could not detect a supported package manager (apt/dnf/yum)")
}

// downloadAgentBinaries fetches singbox-node and singbox-monitor for the
// node's architecture from the master's matching release tag.
func downloadAgentBinaries(ctx context.Context, client sshClient, version string) error {
	arch, err := detectArch(ctx, client)
	if err != nil {
		return err
	}
	const repo = "C5Hwang/singbox-deploy"
	for _, name := range []string{"singbox-node", "singbox-monitor"} {
		asset := fmt.Sprintf("%s-linux-%s", name, arch)
		url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, version, asset)
		dest := "/usr/bin/" + name
		cmd := fmt.Sprintf("curl -fsSL %s -o %s.new && chmod 0755 %s.new && mv %s.new %s",
			shellQuote(url), shellQuote(dest), shellQuote(dest), shellQuote(dest), shellQuote(dest))
		if _, err := client.MustRun(ctx, cmd); err != nil {
			return fmt.Errorf("download %s: %w", name, err)
		}
	}
	return nil
}

func detectArch(ctx context.Context, client sshClient) (string, error) {
	res, err := client.MustRun(ctx, "uname -m")
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(res.Stdout) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", res.Stdout)
	}
}

// setupAgentState writes the agent's bootstrap state file and enables the
// systemd unit so the agent starts immediately and on every reboot.
func setupAgentState(ctx context.Context, client sshClient, node Node, masterPubKey, masterEndpoint string) error {
	cmd := fmt.Sprintf("/usr/bin/singbox-node setup --api-token %s --wg-ip %s --master-public-key %s --master-endpoint %s",
		shellQuote(node.APIToken), shellQuote(node.WGIP), shellQuote(masterPubKey), shellQuote(masterEndpoint))
	if _, err := client.MustRun(ctx, cmd); err != nil {
		return err
	}
	// Install the systemd unit. We embed it inline rather than depending on
	// templatefs (which would couple the cluster package to template assets).
	unit := fmt.Sprintf(`[Unit]
Description=singbox-deploy node agent
After=network.target wg-quick@%s.service
Requires=wg-quick@%s.service

[Service]
User=root
ExecStart=/usr/bin/singbox-node serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, wireguard.InterfaceName, wireguard.InterfaceName)
	if err := client.WriteFile(ctx, "/etc/systemd/system/singbox-node.service", []byte(unit), 0o644); err != nil {
		return err
	}
	if _, err := client.MustRun(ctx, "systemctl daemon-reload && systemctl enable --now singbox-node.service"); err != nil {
		return err
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// realClient adapts *sshexec.Client to the sshClient interface. The cluster
// package uses uint32 for permissions so test fakes don't need to depend on
// os.FileMode.
type realClient struct{ *sshexec.Client }

func (r realClient) WriteFile(ctx context.Context, path string, content []byte, mode uint32) error {
	return r.Client.WriteFile(ctx, path, content, osFileMode(mode))
}
