package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// AsDeployConfig converts a Node into the subset of deploy.Config needed to
// render subscription entries for that node. Fields not relevant to
// subscription generation (cert challenge, monitor knobs, firewall flavour)
// are left at their zero values.
func (n Node) AsDeployConfig() deploy.Config {
	return deploy.Config{
		Domain:               n.Domain,
		DisplayName:          n.SubscriptionDisplayName(),
		Enabled:              n.EnabledProtocols,
		Ports:                n.Ports,
		Creds:                n.Creds,
		RealityServerName:    n.RealityServerName,
		RealityHandshakePort: n.RealityHandshakePort,
	}
}

// WriteFleetSubscriptions regenerates the master's subscription files using
// the local install config plus every node in the registry. Each node
// contributes one set of per-protocol entries; the local install always
// appears first.
func (r Registry) WriteFleetSubscriptions(layout paths.Layout, local deploy.Config) error {
	nodes, err := r.List()
	if err != nil {
		return err
	}
	nodeCfgs := make([]deploy.Config, 0, len(nodes))
	for _, n := range nodes {
		nodeCfgs = append(nodeCfgs, n.AsDeployConfig())
	}
	return deploy.WriteSubscriptionsForFleet(layout, local, nodeCfgs)
}

// RefreshMonitorSnapshot pulls /api/summary from each node's monitor over the
// WireGuard interface and writes the aggregated result to the master's
// remote_monitor.json snapshot file. The monitor server reads that file when
// assembling its own /api/summary, so the master UI shows fleet-wide totals.
func (r Registry) RefreshMonitorSnapshot(ctx context.Context, layout paths.Layout) error {
	nodes, err := r.List()
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		// Write an empty snapshot so the monitor server gets a fresh file
		// instead of stale data.
		return monitor.WriteRemoteSources(deploy.RemoteMonitorPath(layout), nil)
	}
	summaries := make([]monitor.SourceSummary, 0, len(nodes))
	for _, node := range nodes {
		summary, err := fetchNodeSummary(ctx, node)
		if err != nil {
			// Skip unreachable nodes so one bad node doesn't blank the
			// whole snapshot. Logging is the caller's responsibility.
			continue
		}
		summaries = append(summaries, summary)
	}
	return monitor.WriteRemoteSources(deploy.RemoteMonitorPath(layout), summaries)
}

func fetchNodeSummary(ctx context.Context, node Node) (monitor.SourceSummary, error) {
	client := NewAgentClient(node)
	body, err := client.MonitorSummary(ctx)
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
		Trend               []monitor.HourlyPoint     `json:"trend"`
		Resources           *monitor.ResourceSnapshot `json:"resources,omitempty"`
		Sources             []struct {
			SampledAt string `json:"sampledAt"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return monitor.SourceSummary{}, fmt.Errorf("decode monitor summary %s: %w", node.WGIP, err)
	}
	var sampledAt string
	if len(payload.Sources) > 0 {
		sampledAt = payload.Sources[0].SampledAt
	}
	return monitor.SourceSummary{
		Name:                subscription.AddNodePrefixFlag(node.SubscriptionDisplayName()),
		FetchedAt:           time.Now().UTC().Format(time.RFC3339),
		SampledAt:           sampledAt,
		MonitorURL:          node.MonitorAPIURL(),
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
		Trend:               payload.Trend,
		Resources:           payload.Resources,
	}, nil
}

// MonitorRefreshPath returns the path used by the monitor service to read the
// fleet snapshot. Mirrors deploy.RemoteMonitorPath for callers that already
// imported cluster.
func MonitorRefreshPath(layout paths.Layout) string {
	return deploy.RemoteMonitorPath(layout)
}
