package hubctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// ipBanAppliedFile records, per node, a fingerprint of the rule set that node
// last accepted. Reconciliation compares against it so only nodes whose rules
// actually moved are contacted, and a node that was unreachable is retried.
const ipBanAppliedFile = "ip_ban_applied.json"

// IPBanConfigFor renders the complete rule set nodeID has to enforce.
//
// A ban follows the node a client exits through. nodeID drops its own banned
// addresses on its own protocol ports, and, for every landing node it relays,
// drops that landing node's banned addresses on the relay ports fronting it —
// the landing node only sees the relay's address, so the relay is the last
// place the client's own address can be matched. nodeID's own bans do not
// reach those relay ports: a client banned from the relay alone can still
// reach the landing nodes it fronts.
func IPBanConfigFor(nodeID string, bans []ipban.Ban, links []relaylinks.Link, endpoints []RelayEndpoint) ipban.Config {
	var cfg ipban.Config
	if self, ok := RelayEndpointByID(endpoints, nodeID); ok {
		if addresses := ipban.AddressesFor(bans, self.ID); len(addresses) > 0 {
			rule := ipban.Rule{Label: "own protocols", Addresses: addresses}
			for _, served := range self.Protocols {
				network, known := relaylinks.NetworkFor(served.Protocol)
				if !known || served.Port <= 0 {
					continue
				}
				rule = withPort(rule, network, served.Port)
			}
			if len(rule.TCP)+len(rule.UDP) > 0 {
				cfg.Rules = append(cfg.Rules, rule)
			}
		}
	}
	for _, link := range relaylinks.ServedBy(links, nodeID) {
		addresses := ipban.AddressesFor(bans, link.LandingID)
		if len(addresses) == 0 {
			continue
		}
		label := link.LandingID
		if landing, ok := RelayEndpointByID(endpoints, link.LandingID); ok && landing.Name != "" {
			label = landing.Name
		}
		rule := ipban.Rule{Label: "relay to " + label, Addresses: addresses}
		for _, forward := range link.Forwards {
			rule = withPort(rule, forward.Network, forward.RelayPort)
		}
		if len(rule.TCP)+len(rule.UDP) > 0 {
			cfg.Rules = append(cfg.Rules, rule)
		}
	}
	return cfg
}

func withPort(rule ipban.Rule, network string, port int) ipban.Rule {
	switch network {
	case "tcp":
		rule.TCP = append(rule.TCP, port)
	case "udp":
		rule.UDP = append(rule.UDP, port)
	}
	return rule
}

// ApplyIPBan installs nodeID's complete rule set: locally on the hub, over the
// authenticated overlay on a spoke. An empty configuration lifts every ban.
func (c *Controller) ApplyIPBan(ctx context.Context, nodeID string, cfg ipban.Config, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	if strings.EqualFold(strings.TrimSpace(nodeID), relaylinks.HubNodeID) {
		return c.NewIPBanApplier().Apply(ctx, cfg)
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	for _, n := range list {
		if !strings.EqualFold(n.ID, nodeID) {
			continue
		}
		if !n.Installed {
			return fmt.Errorf("node %s is not installed", n.EffectiveAlias())
		}
		checked, err := c.ProbeHealth(ctx, n)
		if err != nil {
			return fmt.Errorf("check agent %s before applying its IP bans: %w", n.EffectiveAlias(), err)
		}
		return c.NewClient(checked).ApplyIPBan(ctx, ipBanRequest(cfg), log)
	}
	return fmt.Errorf("node %s not found", nodeID)
}

// ReconcileIPBans converges every node on the rule set the ban registry and
// the relay topology currently describe. Only nodes whose rules differ from
// what they last accepted are contacted, so it is cheap to call after any
// fleet change and on the monitor's refresh timer, which is what follows a
// protocol port or relay port that moved underneath a ban.
//
// A node that cannot be reached is reported and retried on the next call; the
// others are still applied.
func (c *Controller) ReconcileIPBans(ctx context.Context, log io.Writer) error {
	return c.reconcileIPBans(ctx, log, false)
}

// ForceReconcileIPBans pushes every node's rule set regardless of what it is
// recorded to have accepted, for an operator who wants to be sure.
func (c *Controller) ForceReconcileIPBans(ctx context.Context, log io.Writer) error {
	return c.reconcileIPBans(ctx, log, true)
}

func (c *Controller) reconcileIPBans(ctx context.Context, log io.Writer, force bool) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	unlock, err := c.lockIPBanApplied()
	if err != nil {
		return err
	}
	defer unlock()

	bans, err := ipban.LoadBans(c.Layout)
	if err != nil {
		return fmt.Errorf("load IP bans: %w", err)
	}
	links, err := relaylinks.Load(c.Layout)
	if err != nil {
		return fmt.Errorf("load relay links: %w", err)
	}
	endpoints, err := c.RelayEndpoints()
	if err != nil {
		return err
	}
	applied, err := c.readIPBanApplied()
	if err != nil {
		return err
	}
	// RelayEndpoints answers empty when the hub's own install state did not
	// load. That is not evidence the fleet emptied, so nothing is withdrawn.
	if len(endpoints) == 0 {
		return nil
	}

	var errs []error
	present := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		present[endpoint.ID] = struct{}{}
		cfg := IPBanConfigFor(endpoint.ID, bans, links, endpoints)
		fingerprint := ipBanFingerprint(cfg)
		previous, known := applied[endpoint.ID]
		if endpoint.ID == relaylinks.HubNodeID {
			// The hub's own rule set is on this disk, so it is compared
			// directly rather than trusted to the record: an uninstall that
			// cleared it leaves the record behind.
			local, loadErr := ipban.Load(c.Layout)
			if loadErr != nil {
				local = ipban.Config{}
			}
			previous = ipBanFingerprint(local)
			known = previous != ""
		}
		if !force && previous == fingerprint {
			continue
		}
		// A node that never accepted a rule set and is owed none is left
		// alone, so an agent that predates bans is never contacted for them.
		if !known && fingerprint == "" {
			continue
		}
		if !endpoint.Installed {
			continue
		}
		name := endpoint.Name
		if name == "" {
			name = endpoint.ID
		}
		if cfg.Empty() {
			fmt.Fprintf(log, "lifting IP bans on %s\n", name)
		} else {
			fmt.Fprintf(log, "applying IP bans on %s\n", name)
		}
		if err := c.ApplyIPBan(ctx, endpoint.ID, cfg, log); err != nil {
			errs = append(errs, fmt.Errorf("apply IP bans on %s: %w", name, ipBanAgentHint(err)))
			continue
		}
		if fingerprint == "" {
			delete(applied, endpoint.ID)
		} else {
			applied[endpoint.ID] = fingerprint
		}
	}
	// A node that left the fleet was uninstalled on the way out, which lifted
	// its rules; there is nothing left to compare against.
	for id := range applied {
		if _, ok := present[id]; !ok {
			delete(applied, id)
		}
	}
	if err := c.writeIPBanApplied(applied); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ipBanAgentHint turns the "not supported" answer of an agent that predates
// bans into an actionable message.
func ipBanAgentHint(err error) error {
	if err != nil && strings.Contains(err.Error(), "not supported by this agent") {
		return fmt.Errorf("%w (upgrade the spoke's agent with Self-update first)", err)
	}
	return err
}

// ipBanFingerprint is "" for a rule set that drops nothing, so a node that has
// nothing to enforce and one never told about bans read the same.
func ipBanFingerprint(cfg ipban.Config) string {
	if cfg.Empty() {
		return ""
	}
	normalized := ipban.Config{Rules: make([]ipban.Rule, 0, len(cfg.Rules))}
	for _, rule := range cfg.Rules {
		r := ipban.Rule{
			Label:     rule.Label,
			Addresses: append([]string(nil), rule.Addresses...),
			TCP:       append([]int(nil), rule.TCP...),
			UDP:       append([]int(nil), rule.UDP...),
		}
		sort.Strings(r.Addresses)
		sort.Ints(r.TCP)
		sort.Ints(r.UDP)
		normalized.Rules = append(normalized.Rules, r)
	}
	raw, _ := json.Marshal(normalized)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *Controller) ipBanAppliedPath() string {
	return filepath.Join(c.Layout.StateDir, ipBanAppliedFile)
}

func (c *Controller) readIPBanApplied() (map[string]string, error) {
	applied := map[string]string{}
	raw, err := os.ReadFile(c.ipBanAppliedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return applied, nil
		}
		return nil, fmt.Errorf("read applied IP ban record: %w", err)
	}
	if err := json.Unmarshal(raw, &applied); err != nil {
		// A corrupt record only costs a full re-push.
		return map[string]string{}, nil
	}
	return applied, nil
}

func (c *Controller) writeIPBanApplied(applied map[string]string) error {
	path := c.ipBanAppliedPath()
	if len(applied) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear applied IP ban record: %w", err)
		}
		return nil
	}
	raw, err := json.MarshalIndent(applied, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory for the applied IP ban record: %w", err)
	}
	return state.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}

// lockIPBanApplied serializes reconciliation between the TUI and the monitor
// service, which both run it and would otherwise race on the applied record.
func (c *Controller) lockIPBanApplied() (func(), error) {
	if err := os.MkdirAll(c.Layout.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory for the IP ban lock: %w", err)
	}
	f, err := os.OpenFile(c.ipBanAppliedPath()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open IP ban lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock IP ban record: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func ipBanRequest(cfg ipban.Config) nodeapi.IPBanRequest {
	req := nodeapi.IPBanRequest{Rules: make([]nodeapi.IPBanRule, 0, len(cfg.Rules))}
	for _, rule := range cfg.Rules {
		req.Rules = append(req.Rules, nodeapi.IPBanRule{
			Label:     rule.Label,
			Addresses: append([]string(nil), rule.Addresses...),
			TCP:       append([]int(nil), rule.TCP...),
			UDP:       append([]int(nil), rule.UDP...),
		})
	}
	return req
}
