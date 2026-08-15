package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// relayPublicationFile records the relay topology the published subscriptions
// currently describe. Republishing rewrites every group's files, so the marker
// is what keeps a periodic reconcile from doing that on every tick.
const relayPublicationFile = "relay_publication"

// RelayAvailable reports whether a relay is currently carrying traffic. A relay
// that has used up its quota is not: its monitor stops it forwarding, so a
// landing node published under its address would be unreachable until the
// cycle resets. Anything the hub has no usage figures for counts as available,
// because the alternative is withdrawing a relay that is working fine.
func (c *Controller) RelayAvailable() (func(relayID string) bool, error) {
	c.defaults()
	exhausted := make(map[string]bool, 8)
	sources, err := monitor.ReadRemoteSources(deploy.RemoteMonitorPath(c.Layout))
	if err != nil {
		return nil, fmt.Errorf("read spoke monitor snapshot: %w", err)
	}
	for _, source := range sources {
		if source.ID != "" {
			exhausted[strings.ToLower(source.ID)] = sourceQuotaExhausted(source)
		}
	}
	if localCfg, err := deploy.LoadProtocolConfig(c.Layout); err == nil && localCfg.DeployMonitor {
		exhausted[relaylinks.HubNodeID] = c.hubQuotaExhausted(localCfg)
	}
	return func(relayID string) bool {
		return !exhausted[strings.ToLower(strings.TrimSpace(relayID))]
	}, nil
}

// sourceQuotaExhausted reads one spoke's summary. Each direction is checked
// separately because a node can exhaust its inbound allowance while its total
// still has room.
func sourceQuotaExhausted(source monitor.SourceSummary) bool {
	return (source.InLimitBytes > 0 && source.InRemainingBytes == 0) ||
		(source.OutLimitBytes > 0 && source.OutRemainingBytes == 0) ||
		(source.TotalLimitBytes > 0 && source.TotalRemainingBytes == 0)
}

// hubQuotaExhausted answers the same question for the hub, whose usage is in
// its own monitor database rather than in the spoke snapshot. A database that
// cannot be read reports "not exhausted": an unreadable counter is not evidence
// that the allowance is gone.
func (c *Controller) hubQuotaExhausted(cfg deploy.Config) bool {
	limits := monitor.TrafficLimits{
		InBytes:    cfg.TrafficInLimitBytes,
		OutBytes:   cfg.TrafficOutLimitBytes,
		TotalBytes: cfg.TrafficTotalLimitBytes,
	}
	if limits == (monitor.TrafficLimits{}) {
		return false
	}
	used, err := monitor.CurrentTrafficTotals(c.Layout, cfg.ResetDay, cfg.ResetHour, time.Now().UTC())
	if err != nil {
		return false
	}
	return limits.Exceeded(used)
}

// ReconcileRelayPublication converges the fleet on the relay topology the
// registries currently describe. It republishes the subscriptions and reinstalls
// the affected rulesets whenever that topology has moved — a relay crossed its
// quota or got its allowance back at a cycle reset, or a landing node's ports or
// address were edited out from under the relay fronting it. A client recovers by
// refetching alone, because only the address inside each node changes.
//
// It is called on the hub monitor's refresh timer, so the comparison against the
// last converged state is what keeps it from rewriting every group's files and
// re-pushing every ruleset on every tick.
func (c *Controller) ReconcileRelayPublication(ctx context.Context) error {
	c.defaults()
	desired, relays, err := c.relayPublicationState()
	if err != nil {
		return err
	}
	previous, err := c.readRelayPublication()
	if err != nil {
		return err
	}
	if desired == previous {
		return nil
	}
	var errs []error
	// The rulesets are installed before the addresses are published, so a
	// client is never handed a relay that is not forwarding yet.
	for _, relayID := range relays {
		if err := c.ApplyRelayFor(ctx, relayID, io.Discard); err != nil {
			errs = append(errs, fmt.Errorf("reinstall relay forwarding on %s: %w", relayID, err))
		}
	}
	if err := c.RefreshSubscriptions(ctx); err != nil {
		errs = append(errs, fmt.Errorf("republish subscriptions after a relay change: %w", err))
	}
	if len(errs) > 0 {
		// The marker is left untouched so the next tick retries rather than
		// treating a half-converged fleet as the state clients are served.
		return errors.Join(errs...)
	}
	return c.writeRelayPublication(desired)
}

// relayPublicationState renders the topology the next convergence would produce
// — one line per fronted node naming the relay actually carrying it and the
// exact ports it forwards, or "direct" when no relay is — together with every
// relay that would have to be reinstalled to get there.
//
// The ports are part of the line because they are what drifts silently: editing
// a landing node's protocols moves the port its relay has to send to, and
// nothing else about the topology changes to say so.
func (c *Controller) relayPublicationState() (string, []string, error) {
	links, err := relaylinks.Load(c.Layout)
	if err != nil {
		return "", nil, fmt.Errorf("load relay links: %w", err)
	}
	if len(links) == 0 {
		return "", nil, nil
	}
	endpoints, err := c.RelayEndpoints()
	if err != nil {
		return "", nil, err
	}
	available, err := c.RelayAvailable()
	if err != nil {
		return "", nil, err
	}
	rewrites := RelayRewrites(links, endpoints, available)

	lines := make([]string, 0, len(links))
	relays := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		if _, duplicate := seen[link.RelayID]; !duplicate {
			seen[link.RelayID] = struct{}{}
			relays = append(relays, link.RelayID)
		}
		rewrite, fronted := rewrites[link.LandingID]
		if !fronted {
			lines = append(lines, link.LandingID+"=direct")
			continue
		}
		mappings := make([]string, 0, len(rewrite.Ports))
		for target, relayPort := range rewrite.Ports {
			mappings = append(mappings, fmt.Sprintf("%d>%d", relayPort, target))
		}
		sort.Strings(mappings)
		lines = append(lines, link.LandingID+"="+link.RelayID+"|"+rewrite.To+"|"+strings.Join(mappings, ","))
	}
	sort.Strings(lines)
	sort.Strings(relays)
	return strings.Join(lines, "\n"), relays, nil
}

func (c *Controller) relayPublicationPath() string {
	return filepath.Join(c.Layout.StateDir, relayPublicationFile)
}

func (c *Controller) readRelayPublication() (string, error) {
	raw, err := os.ReadFile(c.relayPublicationPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read relay publication marker: %w", err)
	}
	return strings.TrimRight(string(raw), "\n"), nil
}

func (c *Controller) writeRelayPublication(value string) error {
	path := c.relayPublicationPath()
	if value == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear relay publication marker: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory for the relay publication marker: %w", err)
	}
	return state.WriteFileAtomic(path, []byte(value+"\n"), 0o600)
}
