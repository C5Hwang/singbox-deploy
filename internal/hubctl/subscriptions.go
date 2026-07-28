package hubctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// spokeSubscriptionCacheDir holds the last successfully fetched subscription
// bodies per spoke, keyed by stable node ID.
const spokeSubscriptionCacheDir = "spoke_subscriptions"

// subscriptionFormats is the fetch/cache order of the four published formats.
var subscriptionFormats = []string{
	nodeapi.FormatDefault,
	nodeapi.FormatClashMeta,
	nodeapi.FormatSingBoxProfiles,
	nodeapi.FormatSurge,
}

// RefreshSubscriptions regenerates the hub's combined subscription outputs from
// its own nodes plus every installed spoke's, fetched over the WireGuard
// overlay (never the public internet). It is called after a spoke is added,
// removed, or reconfigured, and after the hub itself is (re)installed.
//
// A spoke that cannot be reached contributes the bodies cached from its last
// successful fetch, so a transient outage never silently drops that spoke's
// nodes from every subscribed client. Only a spoke that has never been fetched
// is omitted. Cached bodies are re-labeled with the node's current alias, and
// the cache is dropped as soon as the node leaves the registry or is excluded
// from aggregation. The aggregated per-spoke error is returned after the
// combined output is written, so callers can surface it as a warning.
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
	aggregated := make(map[string]struct{}, len(list))
	labels := newAliasLabeler(localCfg.DisplayName)
	for _, n := range list {
		if !n.Installed || !n.IncludeInSubscription {
			continue
		}
		aggregated[n.ID] = struct{}{}
		src, fetchErr := c.fetchNodeSubscription(ctx, n)
		if fetchErr != nil {
			cached, ok := c.cachedNodeSubscription(n)
			if !ok {
				errs = append(errs, fmt.Errorf("%w (no cached subscription to fall back on)", fetchErr))
				continue
			}
			errs = append(errs, fmt.Errorf("%w (reused the last subscription cached for %s)", fetchErr, n.EffectiveAlias()))
			src = cached
		} else if err := c.cacheNodeSubscription(n, src); err != nil {
			errs = append(errs, err)
		}
		if distinct := labels.distinct(src.Alias); distinct != src.Alias {
			errs = append(errs, fmt.Errorf("alias %q is already in use; publishing %s nodes as %q instead",
				src.Alias, n.EffectiveAlias(), distinct))
			src.Alias = distinct
		}
		sources = append(sources, src)
	}
	pos := deploy.LoadLocalSubscriptionPosition(c.Layout)
	if err := deploy.WriteSubscriptionsWithSources(c.Layout, localCfg, sources, pos); err != nil {
		return err
	}
	if err := c.pruneSubscriptionCache(aggregated); err != nil {
		errs = append(errs, err)
	}
	return joinErrors(errs)
}

// fetchNodeSubscription confirms the agent is reachable, then pulls its four
// subscription formats over the overlay and wraps them as a SubscriptionSource
// labeled with the node alias.
func (c *Controller) fetchNodeSubscription(ctx context.Context, n nodes.Node) (deploy.SubscriptionSource, error) {
	checked, err := c.ProbeHealth(ctx, n)
	if err != nil {
		return deploy.SubscriptionSource{}, fmt.Errorf("check agent %s before subscription fetch: %w", n.EffectiveAlias(), err)
	}
	n = checked
	client := c.NewClient(n)
	bodies := map[string][]byte{}
	for _, format := range subscriptionFormats {
		body, err := client.Subscription(ctx, format)
		if err != nil {
			return deploy.SubscriptionSource{}, fmt.Errorf("fetch %s subscription from %s: %w", format, n.EffectiveAlias(), err)
		}
		bodies[format] = body
	}
	return subscriptionSource(n, bodies), nil
}

// aliasLabeler keeps every aggregated source label distinct. Node names in all
// four output formats are derived from the source alias, so a duplicate emits
// duplicate Clash proxy names and duplicate sing-box outbound tags, which
// clients reject outright. The registry already refuses duplicate spoke
// aliases; this is the last line of defence, covering the hub's own display
// name and registries written before that rule existed.
type aliasLabeler struct{ taken map[string]struct{} }

func newAliasLabeler(reserved ...string) *aliasLabeler {
	l := &aliasLabeler{taken: make(map[string]struct{})}
	for _, alias := range reserved {
		if key := aliasLabelKey(alias); key != "" {
			l.taken[key] = struct{}{}
		}
	}
	return l
}

// distinct claims alias, returning a numbered variant when it is already taken.
func (l *aliasLabeler) distinct(alias string) string {
	key := aliasLabelKey(alias)
	if key == "" {
		key = aliasLabelKey("node")
		alias = "node"
	}
	if _, clash := l.taken[key]; !clash {
		l.taken[key] = struct{}{}
		return alias
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", alias, n)
		if key := aliasLabelKey(candidate); !l.claimed(key) {
			l.taken[key] = struct{}{}
			return candidate
		}
	}
}

func (l *aliasLabeler) claimed(key string) bool {
	_, ok := l.taken[key]
	return ok
}

func aliasLabelKey(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

func subscriptionSource(n nodes.Node, bodies map[string][]byte) deploy.SubscriptionSource {
	return deploy.SubscriptionSource{
		Alias:       n.EffectiveSubscriptionAlias(),
		DefaultBody: bodies[nodeapi.FormatDefault],
		ClashBody:   bodies[nodeapi.FormatClashMeta],
		SingBoxBody: bodies[nodeapi.FormatSingBoxProfiles],
		SurgeBody:   bodies[nodeapi.FormatSurge],
	}
}

// cacheNodeSubscription records the bodies just fetched from a spoke. They
// carry that node's credentials, so the cache is root-only.
func (c *Controller) cacheNodeSubscription(n nodes.Node, src deploy.SubscriptionSource) error {
	dir, ok := c.subscriptionCachePath(n.ID)
	if !ok {
		return fmt.Errorf("cache subscription for %s: node ID %q is not a valid cache key", n.EffectiveAlias(), n.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache subscription for %s: %w", n.EffectiveAlias(), err)
	}
	bodies := map[string][]byte{
		nodeapi.FormatDefault:         src.DefaultBody,
		nodeapi.FormatClashMeta:       src.ClashBody,
		nodeapi.FormatSingBoxProfiles: src.SingBoxBody,
		nodeapi.FormatSurge:           src.SurgeBody,
	}
	for _, format := range subscriptionFormats {
		if err := state.WriteFileAtomic(filepath.Join(dir, format), bodies[format], 0o600); err != nil {
			return fmt.Errorf("cache %s subscription for %s: %w", format, n.EffectiveAlias(), err)
		}
	}
	return nil
}

// cachedNodeSubscription returns the last successfully fetched bodies for a
// spoke, relabeled with its current subscription alias. A cache missing any format is
// rejected: a partial source would publish an inconsistent set of formats.
func (c *Controller) cachedNodeSubscription(n nodes.Node) (deploy.SubscriptionSource, bool) {
	dir, ok := c.subscriptionCachePath(n.ID)
	if !ok {
		return deploy.SubscriptionSource{}, false
	}
	bodies := make(map[string][]byte, len(subscriptionFormats))
	for _, format := range subscriptionFormats {
		body, err := os.ReadFile(filepath.Join(dir, format))
		if err != nil {
			return deploy.SubscriptionSource{}, false
		}
		bodies[format] = body
	}
	return subscriptionSource(n, bodies), true
}

// pruneSubscriptionCache drops cached bodies for nodes that no longer take part
// in aggregation, so a removed or excluded spoke cannot reappear later.
func (c *Controller) pruneSubscriptionCache(aggregated map[string]struct{}) error {
	root := filepath.Join(c.Layout.StateDir, spokeSubscriptionCacheDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect cached spoke subscriptions: %w", err)
	}
	for _, entry := range entries {
		if _, keep := aggregated[entry.Name()]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("drop cached subscription %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// subscriptionCachePath maps a stable node ID to its cache directory. Registry
// IDs are lowercase hex, and anything else is refused rather than turned into a
// path component.
func (c *Controller) subscriptionCachePath(id string) (string, bool) {
	if id == "" || len(id) > 64 {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		switch ch := id[i]; {
		case ch >= '0' && ch <= '9', ch >= 'a' && ch <= 'f':
		default:
			return "", false
		}
	}
	return filepath.Join(c.Layout.StateDir, spokeSubscriptionCacheDir, id), true
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
