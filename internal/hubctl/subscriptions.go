package hubctl

import (
	"context"
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
)

// RefreshSubscriptions regenerates the hub's combined subscription outputs from
// its own nodes plus every installed spoke's, fetched over the WireGuard
// overlay (never the public internet). It is called after a spoke is added,
// removed, or reconfigured, and after the hub itself is (re)installed.
//
// Unreachable spokes are skipped so one dead peer cannot block publishing the
// rest; their nodes reappear on the next refresh once reachable. The aggregated
// per-spoke fetch error is returned after the combined output is written, so
// callers can surface it as a warning.
func (c *Controller) RefreshSubscriptions(ctx context.Context) error {
	c.defaults()
	localCfg, err := deploy.LoadProtocolConfig(c.Layout)
	if err != nil {
		// The hub is not installed yet, so there is nothing to publish.
		return nil
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	var sources []deploy.SubscriptionSource
	var errs []error
	for _, n := range list {
		if !n.Installed || !n.IncludeInSubscription {
			continue
		}
		checked, err := c.ProbeHealth(ctx, n)
		if err != nil {
			errs = append(errs, fmt.Errorf("check agent %s before subscription fetch: %w", n.EffectiveAlias(), err))
			continue
		}
		n = checked
		src, err := c.fetchNodeSubscription(ctx, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sources = append(sources, src)
	}
	pos := deploy.LoadLocalSubscriptionPosition(c.Layout)
	if err := deploy.WriteSubscriptionsWithSources(c.Layout, localCfg, sources, pos); err != nil {
		return err
	}
	return joinErrors(errs)
}

// fetchNodeSubscription pulls a spoke's four subscription formats over the
// overlay and wraps them as a SubscriptionSource labeled with the node alias.
func (c *Controller) fetchNodeSubscription(ctx context.Context, n nodes.Node) (deploy.SubscriptionSource, error) {
	client := c.NewClient(n)
	bodies := map[string][]byte{}
	for _, format := range []string{
		nodeapi.FormatDefault,
		nodeapi.FormatClashMeta,
		nodeapi.FormatSingBoxProfiles,
		nodeapi.FormatSurge,
	} {
		body, err := client.Subscription(ctx, format)
		if err != nil {
			return deploy.SubscriptionSource{}, fmt.Errorf("fetch %s subscription from %s: %w", format, n.EffectiveAlias(), err)
		}
		bodies[format] = body
	}
	return deploy.SubscriptionSource{
		Alias:       n.EffectiveAlias(),
		DefaultBody: bodies[nodeapi.FormatDefault],
		ClashBody:   bodies[nodeapi.FormatClashMeta],
		SingBoxBody: bodies[nodeapi.FormatSingBoxProfiles],
		SurgeBody:   bodies[nodeapi.FormatSurge],
	}, nil
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msg := "subscription aggregation had failures:"
	for _, e := range errs {
		msg += "\n  - " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
