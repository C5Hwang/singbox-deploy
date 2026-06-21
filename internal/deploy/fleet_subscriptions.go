package deploy

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// WriteSubscriptionsForFleet generates aggregated subscription outputs from
// the master's local install plus every cluster node, then writes them under
// layout.SubscribeDir. Unlike the old remote-fetch flow, node entries are
// generated locally from the registry: nothing is fetched over the public
// internet, so the master is the only place that needs to be reachable.
func WriteSubscriptionsForFleet(layout paths.Layout, local Config, nodes []Config) error {
	outputs, err := buildFleetSubscriptions(local, nodes)
	if err != nil {
		return err
	}
	return writeSubscriptionOutputs(layout, local, outputs)
}

// buildFleetSubscriptions merges the master's locally-generated subscription
// entries with one set per cluster node. Each node Config is rendered through
// the same buildNodes path used for the master, so all output formats stay
// consistent regardless of which node a protocol entry belongs to.
func buildFleetSubscriptions(local Config, nodes []Config) (subscriptionOutputs, error) {
	allParts := []subscriptionSourceParts{partsFromConfig(local)}
	for _, nodeCfg := range nodes {
		allParts = append(allParts, partsFromConfig(nodeCfg))
	}

	var defaultParts, clashParts, surgeParts []string
	var outbounds []map[string]any
	for _, sp := range allParts {
		defaultParts = append(defaultParts, sp.defaultLines...)
		clashParts = append(clashParts, sp.clashPart)
		surgeParts = append(surgeParts, sp.surgePart)
		outbounds = append(outbounds, sp.outbounds...)
	}

	out := subscriptionOutputs{
		DefaultBase64: subscription.EncodeBase64(strings.Join(defaultParts, "\n")),
		ClashFragment: "proxies:\n" + strings.Join(nonEmptyStrings(clashParts), "\n") + "\n",
		SurgeFragment: strings.Join(nonEmptyStrings(surgeParts), "\n") + "\n",
	}
	clashProviderURL := "https://" + local.Domain + ":" + itoa(local.SubscribePort) + "/s/clashMeta/" + subscriptionToken(local.Salt)
	surgeProviderURL := "https://" + local.Domain + ":" + itoa(local.SubscribePort) + "/s/surge/" + subscriptionToken(local.Salt)
	if err := fillProfiles(&out, outbounds, clashProviderURL, surgeProviderURL); err != nil {
		return subscriptionOutputs{}, err
	}
	return out, nil
}

// partsFromConfig renders the four subscription fragments (default, clash,
// surge, sing-box outbounds) for one Config. Used for both the master's local
// install and each cluster node.
func partsFromConfig(cfg Config) subscriptionSourceParts {
	parts := subscriptionSourceParts{}
	for _, n := range cfg.buildNodes() {
		parts.defaultLines = append(parts.defaultLines, n.DefaultLink)
		parts.clashPart = appendBlock(parts.clashPart, n.ClashYAML)
		if n.SurgeLine != "" {
			parts.surgePart = appendLine(parts.surgePart, n.SurgeLine)
		}
		parts.outbounds = append(parts.outbounds, n.SingBoxOutbound)
	}
	return parts
}

func appendBlock(existing, next string) string {
	next = strings.Trim(next, "\r\n")
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "\n" + next
}

func appendLine(existing, next string) string {
	next = strings.Trim(next, "\r\n")
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "\n" + next
}

// subscriptionSourceParts holds the four rendered subscription fragments for
// a single Config (master or one node). The fleet renderer concatenates parts
// from every Config to produce the aggregated output set.
type subscriptionSourceParts struct {
	defaultLines []string
	clashPart    string
	surgePart    string
	outbounds    []map[string]any
}

// writeSubscriptionOutputs writes the rendered outputs under the layout's
// subscribe directory and cleans up any stale token-keyed files left behind.
func writeSubscriptionOutputs(layout paths.Layout, cfg Config, out subscriptionOutputs) error {
	token := subscriptionToken(cfg.Salt)
	pathsByDir := map[string]string{
		"default":           out.DefaultBase64,
		"clashMeta":         out.ClashFragment,
		"clashMetaProfiles": out.ClashProfile,
		"singboxProfiles":   out.SingBoxProfile,
		"surge":             out.SurgeFragment,
		"surgeProfiles":     out.SurgeProfile,
	}
	for dir, body := range pathsByDir {
		if err := WriteFile(filepath.Join(layout.SubscribeDir, dir, token), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := removeStaleSubscriptionFiles(layout.SubscribeDir, token); err != nil {
		return err
	}
	return writeStateFile(layout.StateDir, "subscribe_salt", cfg.Salt+"\n")
}

func nonEmptyStrings(values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.Trim(value, "\r\n")
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// removeStaleSubscriptionFiles deletes any token-keyed files in the subscribe
// directory that don't match the current token. Called after every write so
// rotated salts don't leave dead entries hanging around.
func removeStaleSubscriptionFiles(subscribeDir, token string) error {
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "singboxProfiles", "surge", "surgeProfiles"} {
		entries, err := os.ReadDir(filepath.Join(subscribeDir, dir))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == token {
				continue
			}
			if err := os.Remove(filepath.Join(subscribeDir, dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, legacy := range []string{"sing-box", "sing-boxProfiles"} {
		if err := os.RemoveAll(filepath.Join(subscribeDir, legacy)); err != nil {
			return err
		}
	}
	return nil
}
