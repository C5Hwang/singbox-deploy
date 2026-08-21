package hubctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// RefreshMonitor pulls each installed spoke's monitor summary over the overlay
// and writes the combined snapshot the hub's monitor service reads. Every fetch
// goes to the node's overlay address, so monitor data never leaves the
// encrypted tunnel. A node that fails
// to answer keeps its previous snapshot entry (a stale FetchedAt makes that
// visible) rather than vanishing from the dashboard.
//
// The hub's monitor service calls this on a short timer, so it deliberately
// uses the read-only ProbeHealth: agent version reconciliation and certificate
// delivery belong to operator-driven operations, not to data collection.
func (c *Controller) RefreshMonitor(ctx context.Context) error {
	c.defaults()
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	path := deploy.RemoteMonitorPath(c.Layout)
	previous := map[string]monitor.SourceSummary{}
	if prev, err := monitor.ReadRemoteSources(path); err == nil {
		for _, s := range prev {
			previous[monitorSourceKey(s)] = s
		}
	}
	out := make([]monitor.SourceSummary, 0, len(list))
	var refreshErrs []error
	for _, n := range list {
		if !n.Installed || !n.Monitor {
			continue
		}
		monitorAlias := strings.TrimSpace(n.MonitorAlias)
		if monitorAlias == "" {
			monitorAlias = n.EffectiveAlias()
		}
		name := subscription.AddNodePrefixFlag(monitorAlias)
		checked, healthErr := c.ProbeHealth(ctx, n)
		if healthErr != nil {
			if prev, ok := previous[n.ID]; ok {
				out = append(out, prev)
				refreshErrs = append(refreshErrs, fmt.Errorf("refresh monitor for %s: %w (kept previous snapshot)", n.EffectiveAlias(), healthErr))
			} else {
				refreshErrs = append(refreshErrs, fmt.Errorf("refresh monitor for %s: %w (no previous snapshot)", n.EffectiveAlias(), healthErr))
			}
			continue
		}
		n = checked
		summary, err := fetchNodeSummary(ctx, c.NewClient(n), n, name)
		if err != nil {
			if prev, ok := previous[n.ID]; ok {
				out = append(out, prev)
				refreshErrs = append(refreshErrs, fmt.Errorf("refresh monitor for %s: %w (kept previous snapshot)", n.EffectiveAlias(), err))
			} else {
				refreshErrs = append(refreshErrs, fmt.Errorf("refresh monitor for %s: %w (no previous snapshot)", n.EffectiveAlias(), err))
			}
			continue
		}
		out = append(out, summary)
	}
	if err := monitor.WriteRemoteSources(path, out); err != nil {
		return err
	}
	return errors.Join(refreshErrs...)
}

// fetchNodeSummary retrieves a spoke summary through the authenticated agent
// API over WireGuard and maps the totals into one dashboard source. Drill-down
// requests use Controller.MonitorData, so no direct monitor URL is persisted or
// exposed to the browser.
func fetchNodeSummary(ctx context.Context, client *nodeapi.Client, n nodes.Node, name string) (monitor.SourceSummary, error) {
	body, err := client.Monitor(ctx, nodeapi.MonitorSummary, "")
	if err != nil {
		return monitor.SourceSummary{}, err
	}
	var payload struct {
		InUsedBytes         uint64                    `json:"inUsedBytes"`
		OutUsedBytes        uint64                    `json:"outUsedBytes"`
		TotalUsedBytes      uint64                    `json:"totalUsedBytes"`
		InRemainingBytes    uint64                    `json:"inRemainingBytes"`
		OutRemainingBytes   uint64                    `json:"outRemainingBytes"`
		TotalRemainingBytes uint64                    `json:"totalRemainingBytes"`
		InLimitBytes        uint64                    `json:"inLimitBytes"`
		OutLimitBytes       uint64                    `json:"outLimitBytes"`
		TotalLimitBytes     uint64                    `json:"totalLimitBytes"`
		ResetTime           string                    `json:"resetTime"`
		Resources           *monitor.ResourceSnapshot `json:"resources,omitempty"`
		Sources             []struct {
			SampledAt string `json:"sampledAt"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return monitor.SourceSummary{}, err
	}
	var sampledAt string
	if len(payload.Sources) > 0 {
		sampledAt = payload.Sources[0].SampledAt
	}
	return monitor.SourceSummary{
		ID:                  n.ID,
		Name:                name,
		FetchedAt:           time.Now().UTC().Format(time.RFC3339),
		SampledAt:           sampledAt,
		InUsedBytes:         payload.InUsedBytes,
		OutUsedBytes:        payload.OutUsedBytes,
		TotalUsedBytes:      payload.TotalUsedBytes,
		InRemainingBytes:    payload.InRemainingBytes,
		OutRemainingBytes:   payload.OutRemainingBytes,
		TotalRemainingBytes: payload.TotalRemainingBytes,
		InLimitBytes:        payload.InLimitBytes,
		OutLimitBytes:       payload.OutLimitBytes,
		TotalLimitBytes:     payload.TotalLimitBytes,
		ResetTime:           payload.ResetTime,
		Resources:           payload.Resources,
	}, nil
}

// MonitorData reads one fixed monitor resource for a registered spoke through
// its bearer-authenticated agent API. nodeID is a stable registry ID; aliases
// are intentionally not accepted because they are mutable and may duplicate.
// address, when the endpoint drills into one, is the only parameter the
// protocol carries; it is parsed on both sides so no caller-typed text reaches
// the spoke's monitor.
func (c *Controller) MonitorData(ctx context.Context, nodeID string, endpoint nodeapi.MonitorEndpoint, address string) ([]byte, error) {
	c.defaults()
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nil, err
	}
	for _, node := range list {
		if node.ID != nodeID {
			continue
		}
		if !node.Installed || !node.Monitor {
			return nil, fmt.Errorf("monitor is not enabled for node %s", node.EffectiveAlias())
		}
		// A node that did not answer its last liveness probe is not dialed again
		// on a dashboard's behalf. Dialing one costs the full connect timeout —
		// a powered-off spoke drops packets inside the tunnel rather than
		// refusing them — and the page is asking about every node at once, so
		// one that is down would otherwise set the pace for all of them. The
		// refresh timer keeps probing, so the answer resumes on its own.
		if unreachable, ok := c.reach().lastFailure(node.ID); ok {
			return nil, fmt.Errorf("node %s did not answer its last liveness probe: %w", node.EffectiveAlias(), unreachable)
		}
		return c.NewClient(node).Monitor(ctx, endpoint, address)
	}
	return nil, fmt.Errorf("monitor node %s not found", nodeID)
}

// TrafficUsage reads one installed Spoke's authoritative current-cycle totals
// through its authenticated WireGuard Agent endpoint.
func (c *Controller) TrafficUsage(ctx context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
	c.defaults()
	if !node.Installed {
		return nodeapi.TrafficUsage{}, fmt.Errorf("node %s is not installed", node.EffectiveAlias())
	}
	checked, err := c.CheckHealth(ctx, node, io.Discard)
	if err != nil {
		return nodeapi.TrafficUsage{}, fmt.Errorf("reconcile Agent before reading traffic usage: %w", err)
	}
	return c.NewClient(checked).TrafficUsage(ctx)
}

// SetTrafficUsage replaces one installed Spoke's absolute current-cycle totals.
// Dynamic usage is Agent-owned and is deliberately never persisted in the Hub
// node registry.
func (c *Controller) SetTrafficUsage(
	ctx context.Context,
	node nodes.Node,
	req nodeapi.TrafficUsageRequest,
) (nodeapi.TrafficUsageUpdate, error) {
	c.defaults()
	if !node.Installed {
		return nodeapi.TrafficUsageUpdate{}, fmt.Errorf("node %s is not installed", node.EffectiveAlias())
	}
	checked, err := c.CheckHealth(ctx, node, io.Discard)
	if err != nil {
		return nodeapi.TrafficUsageUpdate{}, fmt.Errorf("reconcile Agent before setting traffic usage: %w", err)
	}
	return c.NewClient(checked).SetTrafficUsage(ctx, req)
}

// ResetMonitorHistory clears one scope of an installed Spoke's recorded monitor
// history through its authenticated WireGuard Agent endpoint.
func (c *Controller) ResetMonitorHistory(ctx context.Context, node nodes.Node, req nodeapi.MonitorResetRequest) error {
	c.defaults()
	if !node.Installed {
		return fmt.Errorf("node %s is not installed", node.EffectiveAlias())
	}
	checked, err := c.CheckHealth(ctx, node, io.Discard)
	if err != nil {
		return fmt.Errorf("reconcile Agent before clearing monitor history: %w", err)
	}
	return c.NewClient(checked).ResetMonitorHistory(ctx, req)
}

func monitorSourceKey(source monitor.SourceSummary) string {
	if source.ID != "" {
		return source.ID
	}
	return source.Name
}
