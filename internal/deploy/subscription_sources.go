package deploy

import (
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// SubscriptionSource is one remote node's already-fetched subscription bodies,
// one per output format. The hub fetches these from each spoke's agent over the
// WireGuard overlay (see hubctl.RefreshSubscriptions) and hands them here so the
// same merge machinery that serves the public-URL path can combine them.
type SubscriptionSource struct {
	Alias       string
	DefaultBody []byte // base64 share-link list
	ClashBody   []byte // Clash Meta proxies fragment
	SingBoxBody []byte // sing-box client profile (nodes extracted from it)
	SurgeBody   []byte // Surge proxies fragment
}

// WriteSubscriptionsWithSources generates subscription outputs aggregating the
// local node with pre-fetched remote sources. localPosition controls where the
// local nodes appear in the combined output (0 = first, len(sources) = last).
func WriteSubscriptionsWithSources(layout paths.Layout, cfg Config, sources []SubscriptionSource, localPosition int) error {
	out, err := cfg.buildSubscriptionsWithSources(sources, localPosition)
	if err != nil {
		return err
	}
	return writeSubscriptionOutputs(layout, cfg, out)
}

func (c Config) buildSubscriptionsWithSources(sources []SubscriptionSource, localPosition int) (subscriptionOutputs, error) {
	out, err := c.buildSubscriptions()
	if err != nil {
		return subscriptionOutputs{}, err
	}
	if len(sources) == 0 {
		return out, nil
	}
	remoteParts := make([]subscriptionSourceParts, 0, len(sources))
	for _, s := range sources {
		label := strings.TrimSpace(s.Alias)
		parts, err := remoteSourcePartsFromBodies(label, s.Alias, s.DefaultBody, s.ClashBody, s.SingBoxBody, s.SurgeBody)
		if err != nil {
			return subscriptionOutputs{}, err
		}
		remoteParts = append(remoteParts, parts)
	}
	return c.assembleCombinedSubscriptions(out, remoteParts, localPosition)
}
