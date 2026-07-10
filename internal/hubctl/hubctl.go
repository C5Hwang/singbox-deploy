// Package hubctl is the hub's control plane for spoke nodes. It ties together
// the overlay (wgnet), the node registry (nodes), certificate issuance
// (certmgr), SSH bootstrap (bootstrap), the embedded agent (agentbin), and the
// agent API (nodeapi) into the add/remove/reconfigure/cert-push operations the
// TUI drives.
package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/assets/agentbin"
	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

// Controller performs hub-side node operations.
type Controller struct {
	Layout paths.Layout
	Runner system.Runner // hub command runner (wg, systemctl, firewall)
	// ExpectedVersion is the hub build version. When set, authenticated health
	// checks automatically push the matching embedded agent if a spoke reports
	// a different version.
	ExpectedVersion string

	// WGConfDir overrides the WireGuard config directory (defaults to
	// wgnet.DefaultConfDir); tests point it at a temp directory.
	WGConfDir string

	// Injectable seams for testing; nil falls back to real implementations.
	Bootstrapper *bootstrap.Bootstrapper
	NewClient    func(node nodes.Node) *nodeapi.Client
	CertManager  *certmgr.Manager
	AgentBinary  func(arch string) ([]byte, error)
	// CheckOverlaySubnet rejects conflicts with existing host routes before
	// overlay identity/config state is written. Tests may inject a deterministic
	// checker; production inspects /proc/net/route.
	CheckOverlaySubnet func(subnet string) error
}

func (c *Controller) defaults() {
	if c.Layout.Root == "" {
		c.Layout = paths.DefaultLayout()
	}
	if c.Runner == nil {
		c.Runner = system.NewExecRunner(nil)
	}
	if c.Bootstrapper == nil {
		c.Bootstrapper = &bootstrap.Bootstrapper{}
	}
	if c.NewClient == nil {
		c.NewClient = func(node nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: "http://" + node.AgentAddr(), Token: node.Token}
		}
	}
	if c.CertManager == nil {
		c.CertManager = &certmgr.Manager{Layout: c.Layout}
	}
	if c.AgentBinary == nil {
		c.AgentBinary = agentbin.Binary
	}
	if c.CheckOverlaySubnet == nil {
		c.CheckOverlaySubnet = func(subnet string) error {
			return wgnet.CheckSubnetRouteConflict(subnet, wgnet.InterfaceName)
		}
	}
}

func (c *Controller) wgManager() wgnet.Manager {
	return wgnet.Manager{Runner: c.Runner, ConfDir: c.WGConfDir}
}

// EnsureOverlay brings up (or refreshes) the hub's WireGuard interface with the
// current peer set. endpointHost is the public host/IP spokes dial. It installs
// the WireGuard tools, writes the hub config, enables the interface, and opens
// the overlay UDP port in the firewall.
func (c *Controller) EnsureOverlay(endpointHost string) (nodes.HubIdentity, error) {
	c.defaults()
	subnet := wgnet.DefaultSubnet
	if existing, ok, err := nodes.LoadHubIdentity(c.Layout); err != nil {
		return nodes.HubIdentity{}, err
	} else if ok && existing.Subnet != "" {
		subnet = existing.Subnet
	}
	if err := c.CheckOverlaySubnet(subnet); err != nil {
		return nodes.HubIdentity{}, fmt.Errorf("cannot initialize hub overlay: %w", err)
	}
	identity, err := nodes.EnsureHubIdentity(c.Layout, endpointHost)
	if err != nil {
		return nodes.HubIdentity{}, err
	}
	host, err := system.DetectHost()
	if err != nil {
		return nodes.HubIdentity{}, err
	}
	for _, cmd := range wgnet.InstallCommands(host.OS) {
		if err := c.Runner.Run(cmd); err != nil {
			return nodes.HubIdentity{}, fmt.Errorf("install wireguard-tools: %w", err)
		}
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nodes.HubIdentity{}, err
	}
	if err := c.writeHubConfig(identity, list); err != nil {
		return nodes.HubIdentity{}, err
	}
	mgr := c.wgManager()
	if err := mgr.EnableStart(wgnet.InterfaceName); err != nil {
		return nodes.HubIdentity{}, fmt.Errorf("bring up overlay: %w", err)
	}
	if host.Firewall != system.FirewallNone {
		port := system.Port{Number: identity.ListenPort, Proto: "udp"}
		if err := runCommands(c.Runner, system.FirewallCommands(host.Firewall, []system.Port{port})); err != nil {
			return nodes.HubIdentity{}, fmt.Errorf("open overlay port: %w", err)
		}
	}
	return identity, nil
}

// writeHubConfig renders and writes the hub wg-quick config for the given peers.
func (c *Controller) writeHubConfig(identity nodes.HubIdentity, list []nodes.Node) error {
	hubAddr, err := wgnet.WithPrefix(wgnet.HubAddress, identity.Subnet)
	if err != nil {
		return err
	}
	peers := make([]wgnet.Peer, 0, len(list))
	for _, n := range list {
		if n.WGPublicKey == "" || n.WGIP == "" {
			continue
		}
		allowed, err := allowedIPForHost(n.WGIP)
		if err != nil {
			return err
		}
		peers = append(peers, wgnet.Peer{PublicKey: n.WGPublicKey, AllowedIP: allowed})
	}
	conf := wgnet.RenderHubConfig(wgnet.HubConfig{
		PrivateKey: identity.PrivateKey,
		Address:    hubAddr,
		ListenPort: identity.ListenPort,
		Peers:      peers,
	})
	return c.wgManager().WriteConfig(wgnet.InterfaceName, conf)
}

// AddNodeParams describes a spoke to provision.
type AddNodeParams struct {
	Node bootstrap.Target // SSH connection details
	// The registry entry to create; overlay key material, IP, token, and arch
	// are filled in by AddNode. Alias, Domain, protocol/monitor params are taken
	// as provided.
	Registry nodes.Node
}

// AddNode provisions a new spoke: allocate overlay identity, bootstrap the agent
// over SSH, join it to the overlay, issue and push its certificate, then run the
// install. Progress is streamed to log. The certificate step requires a DNS
// credential covering the node's domain to already exist.
func (c *Controller) AddNode(ctx context.Context, params AddNodeParams, log io.Writer) (result nodes.Node, retErr error) {
	c.defaults()
	if !nodes.HubInstalled(c.Layout) {
		return nodes.Node{}, fmt.Errorf("install the hub before adding spoke nodes")
	}
	identity, ok, err := nodes.LoadHubIdentity(c.Layout)
	if err != nil {
		return nodes.Node{}, err
	}
	if !ok {
		return nodes.Node{}, fmt.Errorf("hub overlay is not initialized")
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nodes.Node{}, err
	}

	node := params.Registry
	node.SSHHost = params.Node.Host
	node.SSHPort = params.Node.Port
	node.SSHUser = params.Node.User
	node.IncludeInSubscription = true
	if node.AgentPort <= 0 {
		node.AgentPort = nodes.DefaultAgentPort
	}
	if node.ID == "" {
		node.ID, err = nodes.GenerateID()
		if err != nil {
			return nodes.Node{}, err
		}
	}
	if err := nodes.ValidateNew(list, node); err != nil {
		return nodes.Node{}, err
	}

	// Allocate the overlay address. The spoke generates its WireGuard private
	// key locally during provisioning; the hub never receives or persists it.
	ip, err := wgnet.AllocateSpokeIP(identity.Subnet, nodes.UsedIPs(list))
	if err != nil {
		return nodes.Node{}, err
	}
	node.WGIP = ip
	node.WGPublicKey = ""
	token, err := nodeapi.GenerateToken()
	if err != nil {
		return nodes.Node{}, err
	}
	node.Token = token

	fmt.Fprintf(log, "detecting architecture of %s...\n", node.SSHHost)
	arch, err := c.Bootstrapper.DetectArch(ctx, params.Node)
	if err != nil {
		return nodes.Node{}, err
	}
	node.Arch = arch
	agentBin, err := c.AgentBinary(arch)
	if err != nil {
		return nodes.Node{}, err
	}

	plan, err := c.buildPlan(identity, node, agentBin)
	if err != nil {
		return nodes.Node{}, err
	}
	fmt.Fprintf(log, "bootstrapping agent on %s...\n", node.SSHHost)
	provisioned, err := c.Bootstrapper.Provision(ctx, params.Node, plan)
	if err != nil {
		return nodes.Node{}, err
	}
	node.WGPublicKey = provisioned.WGPublicKey
	node.AgentVersion = provisioned.AgentVersion

	// Persist the node and join it to the overlay before talking to the agent.
	// Every subsequent step is transactional with respect to the hub registry,
	// live peer set, and durable WireGuard config. (The one-time remote bootstrap
	// is intentionally left reachable for a later retry over SSH.)
	transactionStarted := false
	peerTouched := false
	installAttempted := false
	committed := false
	defer func() {
		if retErr == nil || !transactionStarted || committed {
			return
		}
		if rollbackErr := c.rollbackAdd(params.Node, identity, node, peerTouched, installAttempted, log); rollbackErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("roll back failed node registration: %w", rollbackErr))
		}
	}()
	transactionStarted = true
	if err := nodes.Add(c.Layout, node); err != nil {
		return nodes.Node{}, err
	}
	peerTouched = true
	if err := c.joinOverlay(identity, node); err != nil {
		return nodes.Node{}, err
	}

	fmt.Fprintf(log, "waiting for agent to come online over the overlay...\n")
	healthyNode, err := c.waitHealthy(ctx, node, log)
	if err != nil {
		return nodes.Node{}, err
	}
	node = healthyNode

	installAttempted = true
	if err := c.installNode(ctx, node, log, false); err != nil {
		return nodes.Node{}, err
	}

	if err := nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
		current.Installed = true
		current.PendingCertificate = false
		node = *current
		return nil
	}); err != nil {
		return nodes.Node{}, err
	}
	committed = true
	// Fold the new spoke's nodes into the hub's published subscription.
	if err := c.RefreshSubscriptions(ctx); err != nil {
		fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
	}
	fmt.Fprintf(log, "node %s is online\n", node.EffectiveAlias())
	return node, nil
}

// installNode issues the node's certificate and pushes a full install.
func (c *Controller) installNode(ctx context.Context, node nodes.Node, log io.Writer, configOnly bool) error {
	fmt.Fprintf(log, "ensuring certificate for %s...\n", node.Domain)
	if _, issued, err := c.CertManager.EnsureIssued(ctx, node.Domain, "", certmgr.DefaultRenewBefore); err != nil {
		return fmt.Errorf("ensure certificate: %w", err)
	} else if issued {
		fmt.Fprintf(log, "issued a fresh DNS-01 certificate for %s\n", node.Domain)
	}
	req, err := c.buildInstallRequest(node)
	if err != nil {
		return err
	}
	req.ConfigOnly = configOnly
	fmt.Fprintf(log, "installing sing-box on %s...\n", node.EffectiveAlias())
	return c.NewClient(node).Install(ctx, req, log)
}

// Reconfigure pushes an updated install to an already-registered node (e.g. the
// operator edited its subscription or monitor settings from the hub), then
// refreshes the hub's combined subscription to reflect the change.
func (c *Controller) Reconfigure(ctx context.Context, node nodes.Node, log io.Writer) error {
	c.defaults()
	checked, err := c.CheckHealth(ctx, node, log)
	if err != nil {
		return err
	}
	node.AgentVersion = checked.AgentVersion
	node.LastSeen = checked.LastSeen
	if err := c.installNode(ctx, node, log, true); err != nil {
		return err
	}
	if err := nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
		current.Installed = true
		current.AgentVersion = checked.AgentVersion
		current.LastSeen = checked.LastSeen
		current.PendingCertificate = false
		return nil
	}); err != nil {
		return err
	}
	if err := c.RefreshSubscriptions(ctx); err != nil {
		fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
	}
	return nil
}

// PushCert ships the node's current certificate pair to its agent, used after
// the hub renews it.
func (c *Controller) PushCert(ctx context.Context, node nodes.Node, log io.Writer) error {
	c.defaults()
	if err := c.pushCert(ctx, node, log); err != nil {
		return err
	}
	return nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
		current.PendingCertificate = false
		return nil
	})
}

func (c *Controller) pushCert(ctx context.Context, node nodes.Node, log io.Writer) error {
	certPEM, keyPEM, err := c.readCertPair(node.Domain)
	if err != nil {
		return err
	}
	return c.NewClient(node).ApplyCert(ctx, nodeapi.CertRequest{
		Domain:         node.Domain,
		CertificatePEM: string(certPEM),
		PrivateKeyPEM:  string(keyPEM),
	}, log)
}

// RemoveNode first requires the spoke to acknowledge its runtime/agent teardown,
// then removes it from the overlay and registry. A communication failure keeps
// the registry and peer intact so the operator can retry without losing the
// only authenticated recovery path.
func (c *Controller) RemoveNode(ctx context.Context, node nodes.Node, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "uninstalling %s...\n", node.EffectiveAlias())
	if err := c.NewClient(node).Uninstall(ctx, nodeapi.UninstallRequest{}, log); err != nil {
		return fmt.Errorf("spoke did not acknowledge uninstall; registry retained: %w", err)
	}
	return c.detachNode(ctx, node, log)
}

// ForceDetachNode removes an unreachable spoke from the Hub registry and
// WireGuard overlay without contacting its agent. It is intentionally separate
// from RemoveNode because the remote runtime may keep running and must be
// cleaned manually if that server becomes reachable again.
func (c *Controller) ForceDetachNode(ctx context.Context, node nodes.Node, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "warning: force-detaching %s without remote uninstall acknowledgement\n", node.EffectiveAlias())
	return c.detachNode(ctx, node, log)
}

func (c *Controller) detachNode(ctx context.Context, node nodes.Node, log io.Writer) error {
	if node.WGPublicKey != "" {
		if err := c.wgManager().RemovePeer(wgnet.InterfaceName, node.WGPublicKey); err != nil {
			fmt.Fprintf(log, "warning: remove overlay peer failed: %v\n", err)
		}
	}
	identifier := node.ID
	if identifier == "" {
		identifier = node.WGIP
	}
	if err := nodes.Remove(c.Layout, identifier); err != nil {
		return err
	}
	// Rewrite the hub config so the removed peer does not return on reboot.
	if identity, ok, err := nodes.LoadHubIdentity(c.Layout); err == nil && ok {
		list, _ := nodes.Load(c.Layout)
		if err := c.writeHubConfig(identity, list); err != nil {
			fmt.Fprintf(log, "warning: rewrite overlay config failed: %v\n", err)
		}
	}
	// Drop the removed spoke's nodes from the hub's published subscription.
	if err := c.RefreshSubscriptions(ctx); err != nil {
		fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
	}
	return nil
}

// joinOverlay adds the node as a live peer and persists the updated hub config.
func (c *Controller) joinOverlay(identity nodes.HubIdentity, node nodes.Node) error {
	allowed, err := allowedIPForHost(node.WGIP)
	if err != nil {
		return err
	}
	mgr := c.wgManager()
	if err := mgr.SetPeer(wgnet.InterfaceName, node.WGPublicKey, allowed); err != nil {
		return fmt.Errorf("add overlay peer: %w", err)
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	return c.writeHubConfig(identity, list)
}

func (c *Controller) rollbackAdd(target bootstrap.Target, identity nodes.HubIdentity, node nodes.Node, peerTouched, installAttempted bool, log io.Writer) error {
	var errs []error
	// If the full install started, remove any partially-created sing-box/Nginx
	// runtime while the authenticated overlay path is still present.
	if installAttempted {
		overlayCleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := c.NewClient(node).Uninstall(overlayCleanupCtx, nodeapi.UninstallRequest{KeepOverlay: true}, log); err != nil {
			errs = append(errs, fmt.Errorf("remove partial spoke runtime over WireGuard: %w", err))
		}
		cancel()
	}
	if peerTouched && node.WGPublicKey != "" {
		if err := c.wgManager().RemovePeer(wgnet.InterfaceName, node.WGPublicKey); err != nil {
			errs = append(errs, fmt.Errorf("remove live peer: %w", err))
		}
	}
	if err := nodes.Remove(c.Layout, node.ID); err != nil {
		errs = append(errs, fmt.Errorf("remove incomplete registry entry: %w", err))
	}
	// Render from the current registry after removing only this transaction's
	// node. This preserves any unrelated node committed concurrently.
	current, loadErr := nodes.Load(c.Layout)
	if loadErr != nil {
		errs = append(errs, fmt.Errorf("load registry for overlay rollback: %w", loadErr))
	} else if err := c.writeHubConfig(identity, current); err != nil {
		errs = append(errs, fmt.Errorf("restore overlay config: %w", err))
	}
	// Provision itself succeeded before the hub transaction began. Remove its
	// agent, token, and WireGuard material over the still-authorized initial SSH
	// channel so the failed add does not leave an orphaned daemon behind.
	sshCleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := c.Bootstrapper.Cleanup(sshCleanupCtx, target, wgnet.InterfaceName); err != nil {
		errs = append(errs, fmt.Errorf("clean remote bootstrap over SSH: %w", err))
	}
	cancel()
	if len(errs) == 0 {
		fmt.Fprintf(log, "rolled back incomplete registration for %s\n", node.EffectiveAlias())
	}
	return errors.Join(errs...)
}

func (c *Controller) waitHealthy(ctx context.Context, node nodes.Node, log io.Writer) (nodes.Node, error) {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for attempt := 0; ; attempt++ {
		updated, err := c.CheckHealth(ctx, node, log)
		if err == nil {
			return updated, nil
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return nodes.Node{}, fmt.Errorf("agent did not become reachable over the overlay: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return nodes.Node{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// CheckHealth records authenticated liveness, enforces the hub's expected
// agent version, and retries any certificate delivery left pending by renewal.
// The returned node contains the freshly observed status fields.
func (c *Controller) CheckHealth(ctx context.Context, node nodes.Node, log io.Writer) (nodes.Node, error) {
	c.defaults()
	client := c.NewClient(node)
	health, err := client.Health(ctx)
	if err != nil {
		return node, err
	}
	if !health.OK {
		return node, fmt.Errorf("agent %s reported unhealthy", node.EffectiveAlias())
	}
	if err := c.persistAgentHealth(&node, health.Version); err != nil {
		return node, fmt.Errorf("persist agent health: %w", err)
	}

	expected := strings.TrimSpace(c.ExpectedVersion)
	if expected != "" && health.Version != expected {
		if node.Arch == "" {
			return node, fmt.Errorf("cannot upgrade agent %s: node architecture is unknown", node.EffectiveAlias())
		}
		binary, err := c.AgentBinary(node.Arch)
		if err != nil {
			return node, fmt.Errorf("load %s agent upgrade: %w", node.Arch, err)
		}
		fmt.Fprintf(log, "agent %s reports %s; upgrading to hub version %s...\n", node.EffectiveAlias(), health.Version, expected)
		if err := client.Upgrade(ctx, nodeapi.NewUpgradeRequest(expected, binary), log); err != nil {
			return node, fmt.Errorf("upgrade agent %s: %w", node.EffectiveAlias(), err)
		}
		deadline := time.Now().Add(60 * time.Second)
		var lastErr error
		for {
			select {
			case <-ctx.Done():
				return node, ctx.Err()
			case <-time.After(time.Second):
			}
			health, err = client.Health(ctx)
			if err == nil && health.OK && health.Version == expected {
				if err := c.persistAgentHealth(&node, health.Version); err != nil {
					return node, fmt.Errorf("persist upgraded agent health: %w", err)
				}
				break
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("agent still reports version %q", health.Version)
			}
			if !time.Now().Before(deadline) {
				return node, fmt.Errorf("agent %s did not return on version %s: %w", node.EffectiveAlias(), expected, lastErr)
			}
		}
	}

	if node.PendingCertificate {
		fmt.Fprintf(log, "retrying pending certificate delivery to %s...\n", node.EffectiveAlias())
		if err := c.pushCert(ctx, node, log); err != nil {
			return node, fmt.Errorf("retry pending certificate: %w", err)
		}
		if err := nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
			current.PendingCertificate = false
			node = *current
			return nil
		}); err != nil {
			return node, fmt.Errorf("clear pending certificate state: %w", err)
		}
	}
	return node, nil
}

// persistAgentHealth patches only hub-observed status fields into the current
// registry value. It intentionally reloads the node under the registry lock so
// a concurrent TUI edit cannot be overwritten by a stale health-check copy.
func (c *Controller) persistAgentHealth(node *nodes.Node, version string) error {
	seen := time.Now().UTC()
	return nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
		current.AgentVersion = version
		current.LastSeen = seen
		*node = *current
		return nil
	})
}

func (c *Controller) buildPlan(identity nodes.HubIdentity, node nodes.Node, agentBin []byte) (bootstrap.Plan, error) {
	spokeAddr, err := wgnet.WithPrefix(node.WGIP, identity.Subnet)
	if err != nil {
		return bootstrap.Plan{}, err
	}
	expectedVersion := strings.TrimSpace(c.ExpectedVersion)
	if expectedVersion == "" {
		return bootstrap.Plan{}, fmt.Errorf("hub build version is required to verify the spoke agent")
	}
	unit, err := templatefs.Render("service/singbox-deploy-agent.service.tmpl", map[string]any{
		"Interface": wgnet.InterfaceName,
		"AgentBin":  bootstrap.AgentBinaryPath,
	})
	if err != nil {
		return bootstrap.Plan{}, err
	}
	return bootstrap.Plan{
		AgentBinary:  agentBin,
		AgentVersion: expectedVersion,
		WGAddress:    spokeAddr,
		HubPublicKey: identity.PublicKey,
		HubEndpoint:  identity.Endpoint(),
		Subnet:       identity.Subnet,
		AgentUnit:    unit,
		Token:        node.Token,
		ListenIP:     node.WGIP,
		AgentPort:    node.AgentPort,
		Interface:    wgnet.InterfaceName,
	}, nil
}

func (c *Controller) buildInstallRequest(node nodes.Node) (nodeapi.InstallRequest, error) {
	certPEM, keyPEM, err := c.readCertPair(node.Domain)
	if err != nil {
		return nodeapi.InstallRequest{}, err
	}
	return nodeapi.InstallRequest{
		Domain:               node.Domain,
		DisplayName:          node.EffectiveAlias(),
		RealityServerName:    node.RealityServerName,
		RealityHandshakePort: node.RealityHandshakePort,
		EnabledProtocols:     node.EnabledProtocols,
		Ports: nodeapi.PortSet{
			RealityVision: node.RealityVisionPort,
			RealityGRPC:   node.RealityGRPCPort,
			Hysteria2:     node.Hysteria2Port,
			TUIC:          node.TUICPort,
			AnyTLS:        node.AnyTLSPort,
		},
		Monitor:                node.Monitor,
		MonitorAlias:           node.MonitorAlias,
		MonitorInterface:       node.MonitorInterface,
		MonitorPort:            node.MonitorPort,
		MonitorIntervalSeconds: node.MonitorIntervalSeconds,
		TrafficInLimitBytes:    node.TrafficInLimitBytes,
		TrafficOutLimitBytes:   node.TrafficOutLimitBytes,
		TrafficTotalLimitBytes: node.TrafficTotalLimitBytes,
		ResetDay:               node.ResetDay,
		ResetHour:              node.ResetHour,
		CertificatePEM:         string(certPEM),
		PrivateKeyPEM:          string(keyPEM),
	}, nil
}

func (c *Controller) readCertPair(domain string) (certPEM, keyPEM []byte, err error) {
	certPath, keyPath := certmgr.CertPaths(c.Layout, domain)
	certPEM, err = os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read certificate for %s: %w", domain, err)
	}
	keyPEM, err = os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read key for %s: %w", domain, err)
	}
	if _, err := certmgr.ValidateCertificatePair(certPEM, keyPEM, domain, time.Now()); err != nil {
		return nil, nil, fmt.Errorf("validate certificate for %s before delivery: %w", domain, err)
	}
	return certPEM, keyPEM, nil
}

func allowedIPForHost(ip string) (string, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", fmt.Errorf("empty overlay address")
	}
	return ip + "/32", nil
}

func runCommands(runner system.Runner, cmds []system.Command) error {
	for _, cmd := range cmds {
		if err := runner.Run(cmd); err != nil {
			return err
		}
	}
	return nil
}
