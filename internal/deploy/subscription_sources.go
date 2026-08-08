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
	return c.buildSubscriptionGroup(SubscriptionGroupSpec{
		Salt:          c.Salt,
		Sources:       sources,
		IncludeLocal:  true,
		LocalPosition: localPosition,
	})
}

// SubscriptionGroupSpec is one subscription group ready to render: the salt
// that derives its URL token, the already-fetched spoke sources it aggregates,
// and whether the hub's own nodes take part.
type SubscriptionGroupSpec struct {
	Salt          string
	Sources       []SubscriptionSource
	IncludeLocal  bool
	LocalPosition int
}

// WriteSubscriptionGroups publishes one set of subscription files per group and
// then deletes every file whose token no longer belongs to a group. Groups are
// written before the sweep, so a token shared by two specs (rejected by the
// registry, but possible in a hand-edited state tree) keeps the last write
// rather than leaving a hole.
func WriteSubscriptionGroups(layout paths.Layout, cfg Config, specs []SubscriptionGroupSpec) error {
	if err := ensurePublicLayoutRoot(layout); err != nil {
		return err
	}
	tokens := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		out, err := cfg.buildSubscriptionGroup(spec)
		if err != nil {
			return err
		}
		token := SubscriptionToken(spec.Salt)
		if err := writeSubscriptionFiles(layout, token, out); err != nil {
			return err
		}
		tokens[token] = struct{}{}
	}
	return removeStaleSubscriptionFiles(layout.SubscribeDir, tokens)
}

// buildSubscriptionGroup renders every output format for one group. The group
// salt replaces the config salt so the Clash and Surge profiles point their
// provider URLs at this group's own token rather than another group's.
func (c Config) buildSubscriptionGroup(spec SubscriptionGroupSpec) (subscriptionOutputs, error) {
	if salt := strings.TrimSpace(spec.Salt); salt != "" {
		c.Salt = salt
	}
	out, err := c.buildSubscriptions()
	if err != nil {
		return subscriptionOutputs{}, err
	}
	if spec.IncludeLocal && len(spec.Sources) == 0 {
		return out, nil
	}
	remoteParts := make([]subscriptionSourceParts, 0, len(spec.Sources))
	for _, s := range spec.Sources {
		label := strings.TrimSpace(s.Alias)
		parts, err := remoteSourcePartsFromBodies(label, s.Alias, s.DefaultBody, s.ClashBody, s.SingBoxBody, s.SurgeBody)
		if err != nil {
			return subscriptionOutputs{}, err
		}
		remoteParts = append(remoteParts, parts)
	}
	return c.assembleSourceSubscriptions(out, remoteParts, spec.LocalPosition, spec.IncludeLocal)
}
