package hubctl

import (
	"context"
	"fmt"
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

// ReconcileRelayPublication republishes the subscriptions when the set of
// relays carrying traffic has changed — a relay crossed its quota, or a cycle
// reset gave one its allowance back. A client recovers by refetching alone,
// because only the address inside each node changes.
//
// It is called on the hub monitor's refresh timer, so the comparison against
// the last published topology is what keeps it from rewriting every group's
// files every tick.
func (c *Controller) ReconcileRelayPublication(ctx context.Context) error {
	c.defaults()
	published, err := c.relayPublicationState()
	if err != nil {
		return err
	}
	previous, err := c.readRelayPublication()
	if err != nil {
		return err
	}
	if published == previous {
		return nil
	}
	if err := c.RefreshSubscriptions(ctx); err != nil {
		// The marker is left untouched so the next tick retries rather than
		// treating a failed publish as the state clients are being served.
		return fmt.Errorf("republish subscriptions after a relay availability change: %w", err)
	}
	return c.writeRelayPublication(published)
}

// relayPublicationState renders the topology the next publish would produce:
// one line per fronted node naming the relay actually carrying it, or "direct"
// when none is.
func (c *Controller) relayPublicationState() (string, error) {
	links, err := relaylinks.Load(c.Layout)
	if err != nil {
		return "", fmt.Errorf("load relay links: %w", err)
	}
	if len(links) == 0 {
		return "", nil
	}
	endpoints, err := c.RelayEndpoints()
	if err != nil {
		return "", err
	}
	available, err := c.RelayAvailable()
	if err != nil {
		return "", err
	}
	rewrites := RelayRewrites(links, endpoints, available)
	lines := make([]string, 0, len(links))
	for _, link := range links {
		target := "direct"
		if _, fronted := rewrites[link.LandingID]; fronted {
			target = link.RelayID
		}
		lines = append(lines, link.LandingID+"="+target)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
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
