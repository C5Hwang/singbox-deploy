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
	"github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
	"golang.org/x/mod/semver"
)

// Controller performs hub-side node operations.
type Controller struct {
	Layout paths.Layout
	Runner system.Runner // hub command runner (wg, systemctl, firewall)
	// ExpectedVersion is the hub build version. When set, authenticated health
	// checks automatically advance older agents to the matching embedded agent.
	// Newer agents are left untouched so a stale hub process cannot downgrade
	// them after a coordinated self-update.
	ExpectedVersion string
	// AllowAgentDowngrade permits exact version reconciliation even when the
	// expected version is older or cannot be ordered. It is reserved for an
	// explicit recovery path after a failed coordinated self-update.
	AllowAgentDowngrade bool
	// RequireExactAgentVersion turns a newer or unordered Agent version into an
	// error instead of leaving it untouched. Coordinated self-update enables
	// this so the Hub binary is not committed unless every installed spoke is
	// already running the exact candidate Agent version.
	RequireExactAgentVersion bool
	// ExpectedCoreVersion pins a full spoke install to the exact sing-box
	// release already running on the Hub. It is normally detected from the Hub
	// binary; tests and recovery tooling may supply it explicitly.
	ExpectedCoreVersion string
	// Progress receives the high-level phases of operator-driven spoke
	// operations. Agent-side deployment logs remain on the supplied log writer.
	Progress func(deploy.Event)

	// WGConfDir overrides the WireGuard config directory (defaults to
	// wgnet.DefaultConfDir); tests point it at a temp directory.
	WGConfDir string

	// Injectable seams for testing; nil falls back to real implementations.
	Bootstrapper *bootstrap.Bootstrapper
	NewClient    func(node nodes.Node) *nodeapi.Client
	CertManager  *certmgr.Manager
	AgentBinary  func(arch string) ([]byte, error)
	// BeforeAgentUpgrade is called immediately before an Agent upgrade request
	// is sent. Coordinated self-update uses it to record the node as possibly
	// changed before the request, because the response can be lost after the
	// remote Agent has already committed its replacement.
	BeforeAgentUpgrade func(node nodes.Node)
	// Core seams keep fleet convergence and rollback testable without replacing
	// the test process' binaries or contacting GitHub.
	CurrentCoreVersion func(ctx context.Context) (string, error)
	ChangeLocalCore    func(ctx context.Context, tag string, log io.Writer) error
	LocalCoreActive    func() error
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
	if c.CurrentCoreVersion == nil {
		c.CurrentCoreVersion = func(ctx context.Context) (string, error) {
			return core.InstalledVersion(ctx, c.Layout.SingBoxBin)
		}
	}
	if c.ChangeLocalCore == nil {
		c.ChangeLocalCore = func(ctx context.Context, tag string, _ io.Writer) error {
			host, err := system.DetectHost()
			if err != nil {
				return fmt.Errorf("detect Hub architecture: %w", err)
			}
			manager := &core.Manager{
				Runner: c.Runner,
				Layout: c.Layout,
				GOOS:   "linux",
				GOARCH: host.Arch,
			}
			_, err = manager.Run(ctx, core.ActionChangeStable, tag)
			return err
		}
	}
	if c.LocalCoreActive == nil {
		c.LocalCoreActive = func() error {
			return c.Runner.Run(system.Command{
				Name: "systemctl",
				Args: []string{"is-active", "--quiet", system.SingBoxService},
			})
		}
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
	const progressTotal = 7
	var activeProgress *deploy.Event
	beginProgress := func(index int, label, detail string) {
		event := deploy.Event{Index: index, Total: progressTotal, Label: label, Detail: detail, Status: "running"}
		activeProgress = &event
		deploy.EmitProgress(c.Progress, event)
	}
	completeProgress := func() {
		if activeProgress == nil {
			return
		}
		event := *activeProgress
		event.Status = "ok"
		deploy.EmitProgress(c.Progress, event)
		activeProgress = nil
	}
	defer func() {
		if retErr == nil || activeProgress == nil {
			return
		}
		event := *activeProgress
		event.Status = "fail"
		event.Err = retErr
		deploy.EmitProgress(c.Progress, event)
	}()

	beginProgress(1, "Hub validation", "validate the registry and allocate an overlay address")
	c.defaults()
	if !nodes.HubInstalled(c.Layout) {
		return nodes.Node{}, fmt.Errorf("install the hub before adding spoke nodes")
	}
	coreVersion, err := c.expectedCoreVersion(ctx)
	if err != nil {
		return nodes.Node{}, fmt.Errorf("pin spoke sing-box core to the Hub: %w", err)
	}
	c.ExpectedCoreVersion = coreVersion
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
	completeProgress()

	beginProgress(2, "Spoke architecture", "detect the target and select its embedded agent")
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
	completeProgress()

	beginProgress(3, "SSH bootstrap", "install the agent and WireGuard configuration")
	fmt.Fprintf(log, "bootstrapping agent on %s...\n", node.SSHHost)
	provisioned, err := c.Bootstrapper.Provision(ctx, params.Node, plan)
	if err != nil {
		return nodes.Node{}, err
	}
	node.WGPublicKey = provisioned.WGPublicKey
	node.AgentVersion = provisioned.AgentVersion
	completeProgress()

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
	beginProgress(4, "WireGuard overlay", "register the spoke and activate its encrypted peer")
	if err := nodes.Add(c.Layout, node); err != nil {
		return nodes.Node{}, err
	}
	peerTouched = true
	if err := c.joinOverlay(identity, node); err != nil {
		return nodes.Node{}, err
	}
	completeProgress()

	beginProgress(5, "Agent health", "wait for the authenticated agent over WireGuard")
	fmt.Fprintf(log, "waiting for agent to come online over the overlay...\n")
	healthyNode, err := c.waitHealthy(ctx, node, log)
	if err != nil {
		return nodes.Node{}, err
	}
	node = healthyNode

	// A successful bootstrap can expose an agent on a server that already has a
	// standalone singbox-deploy runtime. Do not start a destructive conversion:
	// the current install flow has no complete snapshot of packages, firewall,
	// service, certificate, and runtime state to restore if migration fails.
	// Keeping installAttempted false is intentional—the deferred rollback then
	// removes only this transaction's agent/overlay bootstrap and never asks the
	// agent to uninstall the pre-existing runtime.
	health, err := c.NewClient(node).Health(ctx)
	if err != nil {
		return nodes.Node{}, fmt.Errorf("confirm spoke has no existing deployment: %w", err)
	}
	if !health.OK {
		return nodes.Node{}, fmt.Errorf("agent %s reported unhealthy before install%s", node.EffectiveAlias(), healthErrorSuffix(health))
	}
	if health.Installed {
		existing := strings.TrimSpace(health.Domain)
		if existing == "" {
			existing = "unknown domain"
		}
		return nodes.Node{}, fmt.Errorf(
			"server already has a singbox-deploy installation for %s; automatic standalone-to-spoke migration is disabled to preserve the existing deployment",
			existing,
		)
	}
	completeProgress()

	beginProgress(6, "Spoke installation", "issue the certificate and install the proxy runtime")
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
	completeProgress()

	// Fold the new spoke's nodes into the hub's published subscription.
	beginProgress(7, "Subscriptions", "publish the new spoke in the aggregated outputs")
	if err := c.RefreshSubscriptions(ctx); err != nil {
		fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
	}
	completeProgress()
	fmt.Fprintf(log, "node %s is online\n", node.EffectiveAlias())
	return node, nil
}

// installNode pushes a full install or a certificate-independent settings
// reconfiguration. Only the full-install path issues and distributes a pair.
func (c *Controller) installNode(ctx context.Context, node nodes.Node, log io.Writer, configOnly bool) error {
	var (
		req nodeapi.InstallRequest
		err error
	)
	if configOnly {
		// Certificate renewal and distribution have their own transactional
		// path. Keeping certificates out of settings reconfiguration prevents
		// a request prepared before a renewal from writing the old pair back
		// after the renewal has already reached the Agent.
		req = c.buildNodeRequest(node)
	} else {
		fmt.Fprintf(log, "ensuring certificate for %s...\n", node.Domain)
		if _, issued, issueErr := c.CertManager.EnsureIssued(ctx, node.Domain, "", certmgr.DefaultRenewBefore); issueErr != nil {
			return fmt.Errorf("ensure certificate: %w", issueErr)
		} else if issued {
			fmt.Fprintf(log, "issued a fresh DNS-01 certificate for %s\n", node.Domain)
		}
		req, err = c.buildInstallRequest(node)
		if err != nil {
			return err
		}
	}
	req.ConfigOnly = configOnly
	fmt.Fprintf(log, "installing sing-box on %s...\n", node.EffectiveAlias())
	if err := c.NewClient(node).Install(ctx, req, log); err != nil {
		return err
	}
	if configOnly {
		return nil
	}
	if _, err := c.verifySpokeCore(ctx, node, req.SingBoxVersion); err != nil {
		return fmt.Errorf("verify pinned spoke core after install: %w", err)
	}
	return nil
}

// Reconfigure pushes an updated install to an already-registered node (e.g. the
// operator edited its subscription or monitor settings from the hub), then
// refreshes the hub's combined subscription to reflect the change.
func (c *Controller) Reconfigure(ctx context.Context, node nodes.Node, log io.Writer) error {
	return c.reconfigure(ctx, node, log, nil, false, "")
}

// ProtocolState reads the Agent's current editable protocol state over the
// authenticated WireGuard control path.
func (c *Controller) ProtocolState(ctx context.Context, node nodes.Node) (nodeapi.ProtocolStateResponse, error) {
	c.defaults()
	return c.NewClient(node).ProtocolState(ctx)
}

// PatchProtocolRevision applies one protocol's credentials and listen port
// without shipping a complete spoke configuration or certificate. The Agent
// compares expectedRevision while holding its mutation gate, then merges the
// patch into its current local configuration so concurrent monitor, display,
// certificate, and unrelated protocol changes cannot be overwritten.
func (c *Controller) PatchProtocolRevision(
	ctx context.Context,
	node nodes.Node,
	patch nodeapi.ProtocolPatch,
	expectedRevision string,
	log io.Writer,
) error {
	if err := nodeapi.ValidateProtocolPatch(patch); err != nil {
		return err
	}
	if err := nodeapi.ValidateProtocolRevision(expectedRevision); err != nil {
		return err
	}
	return c.reconfigure(ctx, node, log, &patch, false, expectedRevision)
}

// ReplaceProtocolStateRevision applies the complete non-secret protocol
// selection, ports, and shared Reality settings stored in node. Credentials
// remain Agent-owned. The revision precondition prevents an older full
// protocol form from overwriting a newer single-protocol patch.
func (c *Controller) ReplaceProtocolStateRevision(
	ctx context.Context,
	node nodes.Node,
	expectedRevision string,
	log io.Writer,
) error {
	if err := nodeapi.ValidateProtocolRevision(expectedRevision); err != nil {
		return err
	}
	return c.reconfigure(ctx, node, log, nil, true, expectedRevision)
}

func (c *Controller) reconfigure(
	ctx context.Context,
	node nodes.Node,
	log io.Writer,
	protocolPatch *nodeapi.ProtocolPatch,
	replaceProtocolState bool,
	expectedProtocolRevision string,
) error {
	c.defaults()
	// A complete protocol replacement must apply exactly the registry snapshot
	// owned by its calling transaction. Connection and status fields are
	// reloaded below, but a later transaction's not-yet-applied protocol intent
	// must not be absorbed into this request.
	desiredProtocolNode := node
	var checked nodes.Node
	return deploy.RunSteps(ctx, c.Progress, []deploy.Step{
		{
			Label:  "Agent health",
			Detail: "authenticate the spoke and reconcile its agent version",
			Run: func(ctx context.Context) error {
				var err error
				checked, err = c.CheckHealth(ctx, node, log)
				return err
			},
		},
		{
			Label:  "Spoke configuration",
			Detail: "apply the requested settings over WireGuard",
			Run: func(ctx context.Context) error {
				// Reload after health/version reconciliation. Settings
				// transactions persist their owned registry fields before
				// reaching the Agent, so carrying the caller's older snapshot
				// here could undo a transaction that committed while health
				// checking was in progress.
				registered, err := nodes.Load(c.Layout)
				if err != nil {
					return fmt.Errorf("reload spoke registry before apply: %w", err)
				}
				found := false
				for _, current := range registered {
					if current.ID == node.ID {
						node = current
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("node %s was removed before reconfigure", node.ID)
				}
				node.AgentVersion = checked.AgentVersion
				node.LastSeen = checked.LastSeen
				if protocolPatch != nil {
					req := nodeapi.InstallRequest{
						ConfigOnly:               true,
						ProtocolPatch:            protocolPatch,
						ExpectedProtocolRevision: expectedProtocolRevision,
					}
					fmt.Fprintf(log, "patching %s protocol settings on %s...\n", protocolPatch.Protocol, node.EffectiveAlias())
					return c.NewClient(node).Install(ctx, req, log)
				}
				if replaceProtocolState {
					desired := node
					copyNodeProtocolState(&desired, desiredProtocolNode)
					req := c.buildNodeRequest(desired)
					req.ConfigOnly = true
					req.ReplaceProtocolState = true
					req.ExpectedProtocolRevision = expectedProtocolRevision
					fmt.Fprintf(log, "replacing protocol settings on %s...\n", node.EffectiveAlias())
					return c.NewClient(node).Install(ctx, req, log)
				}
				return c.installNode(ctx, node, log, true)
			},
		},
		{
			Label:  "Registry status",
			Detail: "record the applied spoke state",
			Run: func(context.Context) error {
				return nodes.Mutate(c.Layout, node.ID, func(current *nodes.Node) error {
					current.Installed = true
					current.AgentVersion = checked.AgentVersion
					current.LastSeen = checked.LastSeen
					return nil
				})
			},
		},
		{
			Label:  "Subscriptions",
			Detail: "republish the aggregated subscription",
			Run: func(ctx context.Context) error {
				if err := c.RefreshSubscriptions(ctx); err != nil {
					if protocolPatch != nil || replaceProtocolState {
						return fmt.Errorf("refresh subscriptions after protocol change: %w", err)
					}
					fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
				}
				return nil
			},
		},
	})
}

func copyNodeProtocolState(dst *nodes.Node, src nodes.Node) {
	dst.RealityServerName = src.RealityServerName
	dst.RealityHandshakePort = src.RealityHandshakePort
	dst.EnabledProtocols = append([]string(nil), src.EnabledProtocols...)
	dst.RealityVisionPort = src.RealityVisionPort
	dst.RealityGRPCPort = src.RealityGRPCPort
	dst.Hysteria2Port = src.Hysteria2Port
	dst.TUICPort = src.TUICPort
	dst.AnyTLSPort = src.AnyTLSPort
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
	return deploy.RunSteps(ctx, c.Progress, []deploy.Step{
		{
			Label:  "Remote uninstall",
			Detail: "remove the managed runtime and agent from the spoke",
			Run: func(ctx context.Context) error {
				fmt.Fprintf(log, "uninstalling %s...\n", node.EffectiveAlias())
				if err := c.NewClient(node).Uninstall(ctx, nodeapi.UninstallRequest{}, log); err != nil {
					return fmt.Errorf("spoke did not acknowledge uninstall; registry retained: %w", err)
				}
				return nil
			},
		},
		{
			Label:  "Hub detach",
			Detail: "revoke the WireGuard peer and remove the registry entry",
			Run: func(ctx context.Context) error {
				if err := c.detachNode(ctx, node, log); err != nil {
					return fmt.Errorf("spoke teardown was acknowledged, but local detach did not complete; registry retained for force-detach retry: %w", err)
				}
				return nil
			},
		},
	})
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
	return deploy.RunSteps(ctx, c.Progress, []deploy.Step{{
		Label:  "Hub detach",
		Detail: "revoke the unreachable spoke without remote acknowledgement",
		Run: func(ctx context.Context) error {
			fmt.Fprintf(log, "warning: force-detaching %s without remote uninstall acknowledgement\n", node.EffectiveAlias())
			return c.detachNode(ctx, node, log)
		},
	}})
}

func (c *Controller) detachNode(ctx context.Context, node nodes.Node, log io.Writer) error {
	identity, ok, err := nodes.LoadHubIdentity(c.Layout)
	if err != nil {
		return fmt.Errorf("load hub overlay identity; registry retained: %w", err)
	}
	if !ok {
		return fmt.Errorf("hub overlay identity is missing; registry retained")
	}

	list, err := nodes.Load(c.Layout)
	if err != nil {
		return fmt.Errorf("load node registry for detach: %w", err)
	}
	remaining := make([]nodes.Node, 0, len(list))
	found := false
	for _, current := range list {
		matches := node.ID != "" && strings.EqualFold(current.ID, node.ID)
		if node.ID == "" {
			matches = node.WGIP != "" && current.WGIP == node.WGIP
		}
		if matches {
			found = true
			continue
		}
		remaining = append(remaining, current)
	}
	if !found {
		return fmt.Errorf("node %s is not present in the registry; no overlay state changed", node.EffectiveAlias())
	}

	// Revoke the peer from the durable configuration first. If this write
	// fails, neither the live interface nor the registry has changed. If a
	// later step fails, the durable config remains fail-closed while the
	// registry retains the key/token metadata required for an explicit retry.
	if err := c.writeHubConfig(identity, remaining); err != nil {
		return fmt.Errorf("persist overlay config without %s; registry retained: %w", node.EffectiveAlias(), err)
	}
	if node.WGPublicKey != "" {
		if err := c.wgManager().RemovePeer(wgnet.InterfaceName, node.WGPublicKey); err != nil {
			return fmt.Errorf("remove live overlay peer for %s; registry retained: %w", node.EffectiveAlias(), err)
		}
	}
	identifier := node.ID
	if identifier == "" {
		identifier = node.WGIP
	}
	if err := nodes.Remove(c.Layout, identifier); err != nil {
		return fmt.Errorf("remove %s from node registry after overlay revocation: %w", node.EffectiveAlias(), err)
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
		if err := c.NewClient(node).Uninstall(overlayCleanupCtx, nodeapi.UninstallRequest{
			KeepOverlay:           true,
			RollbackTransactionID: node.ID,
		}, log); err != nil {
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

// ProbeHealth records authenticated liveness and nothing else. It is the read
// path for timer-driven aggregation (subscriptions, monitor): collecting data
// must never replace a remote binary or restart remote services as a side
// effect, because a persistently failing reconciliation would otherwise be
// retried on every refresh tick forever. The returned node carries the freshly
// observed status fields.
func (c *Controller) ProbeHealth(ctx context.Context, node nodes.Node) (nodes.Node, error) {
	c.defaults()
	_, err := c.probeAgent(ctx, &node)
	return node, err
}

// CheckHealth probes the agent, advances an older agent to the hub's expected
// version, and retries any certificate delivery left pending by renewal. It
// mutates the spoke, so it is reserved for operator-driven operations: adding a
// node, reconfiguring one, and coordinated self-update. The returned node
// contains the freshly observed status fields.
func (c *Controller) CheckHealth(ctx context.Context, node nodes.Node, log io.Writer) (nodes.Node, error) {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	health, err := c.probeAgent(ctx, &node)
	if err != nil {
		return node, err
	}
	if err := c.reconcileAgentVersion(ctx, &node, health, log); err != nil {
		return node, err
	}
	return c.deliverPendingCertificate(ctx, node, log)
}

// syncCertificate probes the agent and completes a certificate delivery left
// pending by an earlier failure. Certificate work is driven by the renewal
// timer rather than the operator, so it deliberately skips agent version
// reconciliation.
func (c *Controller) syncCertificate(ctx context.Context, node nodes.Node, log io.Writer) (nodes.Node, error) {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	if _, err := c.probeAgent(ctx, &node); err != nil {
		return node, err
	}
	return c.deliverPendingCertificate(ctx, node, log)
}

// probeAgent performs the authenticated liveness check shared by every health
// path and folds the observed status back into both the registry and node.
func (c *Controller) probeAgent(ctx context.Context, node *nodes.Node) (nodeapi.HealthResponse, error) {
	health, err := c.NewClient(*node).Health(ctx)
	if err != nil {
		return nodeapi.HealthResponse{}, err
	}
	if !health.OK {
		return health, fmt.Errorf("agent %s reported unhealthy%s", node.EffectiveAlias(), healthErrorSuffix(health))
	}
	if err := c.persistAgentHealth(node, health); err != nil {
		return health, fmt.Errorf("persist agent health: %w", err)
	}
	return health, nil
}

// reconcileAgentVersion advances an agent that is older than the hub to the
// embedded binary and waits for it to come back on the expected version.
func (c *Controller) reconcileAgentVersion(ctx context.Context, node *nodes.Node, health nodeapi.HealthResponse, log io.Writer) error {
	expected := strings.TrimSpace(c.ExpectedVersion)
	if expected == "" || health.Version == expected {
		return nil
	}
	if !shouldReplaceAgentVersion(health.Version, expected, c.AllowAgentDowngrade) {
		if c.RequireExactAgentVersion {
			return fmt.Errorf("agent %s reports version %q; coordinated update requires exact version %q",
				node.EffectiveAlias(), health.Version, expected)
		}
		fmt.Fprintf(log, "agent %s reports %s, newer or unordered relative to hub version %s; leaving it unchanged\n",
			node.EffectiveAlias(), health.Version, expected)
		return nil
	}
	if node.Arch == "" {
		return fmt.Errorf("cannot reconcile agent %s: node architecture is unknown", node.EffectiveAlias())
	}
	binary, err := c.AgentBinary(node.Arch)
	if err != nil {
		return fmt.Errorf("load %s agent binary: %w", node.Arch, err)
	}
	client := c.NewClient(*node)
	fmt.Fprintf(log, "agent %s reports %s; reconciling to hub version %s...\n", node.EffectiveAlias(), health.Version, expected)
	if c.BeforeAgentUpgrade != nil {
		c.BeforeAgentUpgrade(*node)
	}
	if err := client.Upgrade(ctx, nodeapi.NewUpgradeRequest(expected, binary), log); err != nil {
		return fmt.Errorf("reconcile agent %s: %w", node.EffectiveAlias(), err)
	}
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		health, err = client.Health(ctx)
		if err == nil && health.OK && health.Version == expected {
			if err := c.persistAgentHealth(node, health); err != nil {
				return fmt.Errorf("persist reconciled agent health: %w", err)
			}
			return nil
		}
		switch {
		case err != nil:
			lastErr = err
		case !health.OK:
			lastErr = fmt.Errorf("agent reported unhealthy%s", healthErrorSuffix(health))
		default:
			lastErr = fmt.Errorf("agent still reports version %q", health.Version)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("agent %s did not return on version %s: %w", node.EffectiveAlias(), expected, lastErr)
		}
	}
}

// deliverPendingCertificate retries a certificate push the hub could not
// complete earlier and clears the pending marker once the spoke accepts it.
func (c *Controller) deliverPendingCertificate(ctx context.Context, node nodes.Node, log io.Writer) (nodes.Node, error) {
	if !node.PendingCertificate {
		return node, nil
	}
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
	return node, nil
}

// shouldReplaceAgentVersion implements the automatic upgrade-only policy.
// Release versions use semantic ordering; a release hub may also repair an
// unversioned/legacy agent. Unknown expected versions never overwrite a
// different agent unless an explicit recovery operation opts into downgrade.
func shouldReplaceAgentVersion(reported, expected string, allowDowngrade bool) bool {
	reported = strings.TrimSpace(reported)
	expected = strings.TrimSpace(expected)
	if expected == "" || reported == expected {
		return false
	}
	if allowDowngrade {
		return true
	}
	expectedSemver := canonicalAgentSemver(expected)
	if expectedSemver == "" {
		return false
	}
	reportedSemver := canonicalAgentSemver(reported)
	if reportedSemver == "" {
		return true
	}
	return semver.Compare(expectedSemver, reportedSemver) > 0
}

func canonicalAgentSemver(version string) string {
	version = strings.TrimSpace(version)
	if semver.IsValid(version) {
		return version
	}
	if !strings.HasPrefix(version, "v") {
		withPrefix := "v" + version
		if semver.IsValid(withPrefix) {
			return withPrefix
		}
	}
	return ""
}

// persistAgentHealth patches only hub-observed status fields into the current
// registry value. It intentionally reloads the node under the registry lock so
// a concurrent TUI edit cannot be overwritten by a stale health-check copy, and
// uses the status-only writer so a fleet-wide probe does not restage the whole
// registry once per node.
func (c *Controller) persistAgentHealth(node *nodes.Node, health nodeapi.HealthResponse) error {
	seen := time.Now().UTC()
	updated, err := nodes.MutateStatus(c.Layout, node.ID, func(current *nodes.Node) error {
		current.AgentVersion = health.Version
		current.SingBoxVersion = health.SingBoxVersion
		current.LastSeen = seen
		return nil
	})
	if err != nil {
		return err
	}
	*node = updated
	return nil
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
		HubIP:        wgnet.HubAddress,
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
	req := c.buildNodeRequest(node)
	req.CertificatePEM = string(certPEM)
	req.PrivateKeyPEM = string(keyPEM)
	return req, nil
}

func (c *Controller) buildNodeRequest(node nodes.Node) nodeapi.InstallRequest {
	return nodeapi.InstallRequest{
		InstallTransactionID: node.ID,
		SingBoxVersion:       strings.TrimSpace(c.ExpectedCoreVersion),
		Domain:               node.Domain,
		DisplayName:          node.EffectiveSubscriptionAlias(),
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
	}
}

func (c *Controller) expectedCoreVersion(ctx context.Context) (string, error) {
	tag := strings.TrimSpace(c.ExpectedCoreVersion)
	if tag == "" {
		var err error
		tag, err = c.CurrentCoreVersion(ctx)
		if err != nil {
			return "", err
		}
	}
	if err := nodeapi.ValidateStableSingBoxTag(tag); err != nil {
		return "", fmt.Errorf("Hub reports %q: %w", tag, err)
	}
	return tag, nil
}

func healthErrorSuffix(health nodeapi.HealthResponse) string {
	detail := strings.TrimSpace(health.Error)
	if detail == "" {
		return ""
	}
	return ": " + detail
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
