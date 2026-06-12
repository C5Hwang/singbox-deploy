package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

const (
	remoteSubscriptionsDir = "remotes"
	monitorSourcesDir      = "monitor_sources"
)

// RemoteSubscription is one same-version remote server aggregated into local
// subscription outputs. Remote node names are preserved unchanged.
type RemoteSubscription struct {
	Domain string
	Port   int
	Alias  string
	Salt   string
}

// MonitorSource is a remote server whose /monitor/api/summary is aggregated
// into the local monitor page. Independent of subscription configuration.
type MonitorSource struct {
	Domain            string
	Alias             string
	MonitorPublicPort int
}

// SubscriptionFetcher fetches remote subscription or monitor JSON endpoints.
type SubscriptionFetcher func(context.Context, string) ([]byte, error)

// DefaultSubscriptionFetch is the default HTTP fetcher for remote subscription endpoints.
func DefaultSubscriptionFetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// ValidateRemoteSubscriptions checks that all remote entries are well-formed.
func ValidateRemoteSubscriptions(remotes []RemoteSubscription) error {
	seen := map[string]bool{}
	for _, r := range remotes {
		domain := strings.TrimSpace(r.Domain)
		if domain == "" {
			return fmt.Errorf("remote domain is required")
		}
		if r.Port <= 0 || r.Port > 65535 {
			return fmt.Errorf("remote %s subscription port must be between 1 and 65535", domain)
		}
		if strings.TrimSpace(r.effectiveAlias()) == "" {
			return fmt.Errorf("remote %s alias is required", domain)
		}
		if strings.TrimSpace(r.Salt) == "" {
			return fmt.Errorf("remote %s salt is required", domain)
		}
		key := strings.ToLower(domain) + ":" + strconv.Itoa(r.Port)
		if seen[key] {
			return fmt.Errorf("duplicate remote subscription %s", key)
		}
		seen[key] = true
	}
	return nil
}

// LoadRemoteSubscriptions reads configured remote subscription entries.
func LoadRemoteSubscriptions(layout paths.Layout) ([]RemoteSubscription, error) {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	dir := filepath.Join(layout.StateDir, remoteSubscriptionsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var remotes []RemoteSubscription
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		remote := RemoteSubscription{
			Domain: readRemoteStateDefault(root, "domain", ""),
			Port:   readRemoteStateIntDefault(root, "subscribe_port", 0),
			Alias:  readRemoteStateDefault(root, "alias", ""),
			Salt:   readRemoteStateDefault(root, "salt", ""),
		}
		if strings.TrimSpace(remote.Alias) == "" {
			remote.Alias = remote.Domain
		}
		remotes = append(remotes, remote)
	}
	return remotes, ValidateRemoteSubscriptions(remotes)
}

// SaveRemoteSubscriptions persists remote subscription entries as small state
// files, one directory per remote.
func SaveRemoteSubscriptions(layout paths.Layout, remotes []RemoteSubscription) error {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	dir := filepath.Join(layout.StateDir, remoteSubscriptionsDir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for i, remote := range remotes {
		entryDir := filepath.Join(dir, fmt.Sprintf("%03d", i+1))
		values := map[string]string{
			"domain":         strings.TrimSpace(remote.Domain),
			"subscribe_port": itoa(remote.Port),
			"alias":          strings.TrimSpace(remote.effectiveAlias()),
			"salt":           strings.TrimSpace(remote.Salt),
		}
		for name, value := range values {
			if err := writePrivateStateFile(entryDir, name, value+"\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func readRemoteStateDefault(root, name, fallback string) string {
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return fallback
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return fallback
	}
	return value
}

func readRemoteStateIntDefault(root, name string, fallback int) int {
	value := readRemoteStateDefault(root, name, "")
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func writePrivateStateFile(dir, name, value string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filepath.Clean(name)), []byte(value), 0o600)
}

// WriteSubscriptionsWithRemotes generates subscription outputs aggregating local and remote nodes.
func WriteSubscriptionsWithRemotes(ctx context.Context, layout paths.Layout, cfg Config, remotes []RemoteSubscription, fetch SubscriptionFetcher) error {
	out, err := cfg.buildSubscriptionsWithRemotes(ctx, remotes, fetch)
	if err != nil {
		return err
	}
	return writeSubscriptionOutputs(layout, cfg, out)
}

func (c Config) buildSubscriptionsWithRemotes(ctx context.Context, remotes []RemoteSubscription, fetch SubscriptionFetcher) (subscriptionOutputs, error) {
	out, err := c.buildSubscriptions()
	if err != nil {
		return subscriptionOutputs{}, err
	}
	if len(remotes) == 0 {
		return out, nil
	}
	if fetch == nil {
		fetch = DefaultSubscriptionFetch
	}

	defaultBody, err := subscription.DecodeBase64(out.DefaultBase64)
	if err != nil {
		return subscriptionOutputs{}, err
	}
	defaultParts := splitNonEmptyLines(defaultBody)
	clashParts := []string{stripClashHeader(out.ClashFragment)}
	surgeParts := []string{strings.TrimRight(out.SurgeFragment, "\n")}
	outbounds, err := decodeSubscriptionOutbounds([]byte(out.SingBoxOutbounds))
	if err != nil {
		return subscriptionOutputs{}, err
	}

	for _, remote := range remotes {
		entry := remote.entry()

		remoteDefault, err := fetch(ctx, entry.DefaultURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote default %s: %w", remote.Domain, err)
		}
		decodedDefault, err := subscription.DecodeBase64(string(remoteDefault))
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("decode remote default %s: %w", remote.Domain, err)
		}
		alias := remote.effectiveAlias()
		defaultParts = append(defaultParts, splitNonEmptyLines(subscription.RenameDefaultLinks(decodedDefault, alias))...)

		remoteClash, err := fetch(ctx, entry.ClashURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote clash %s: %w", remote.Domain, err)
		}
		clashParts = append(clashParts, stripClashHeader(subscription.RenameClashFragment(string(remoteClash), alias)))

		remoteSingBox, err := fetch(ctx, entry.SingBoxProfilesURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote sing-box %s: %w", remote.Domain, err)
		}
		nodeOutbounds, err := subscription.ExtractSingBoxNodeOutbounds(remoteSingBox)
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("extract remote sing-box %s: %w", remote.Domain, err)
		}
		renamedOutbounds, err := subscription.RenameSingBoxOutbounds(nodeOutbounds, alias)
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("rename remote sing-box %s: %w", remote.Domain, err)
		}
		remoteOutbounds, err := decodeSubscriptionOutbounds(renamedOutbounds)
		if err != nil {
			return subscriptionOutputs{}, err
		}
		outbounds = append(outbounds, remoteOutbounds...)

		remoteSurge, err := fetch(ctx, entry.SurgeURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote surge %s: %w", remote.Domain, err)
		}
		surgeParts = append(surgeParts, subscription.RenameSurgeFragment(string(remoteSurge), alias))
	}

	out.DefaultBase64 = subscription.EncodeBase64(strings.Join(defaultParts, "\n"))
	out.ClashFragment = "proxies:\n" + strings.Join(nonEmptyStrings(clashParts), "\n") + "\n"
	out.SurgeFragment = strings.Join(nonEmptyStrings(surgeParts), "\n") + "\n"
	clashProviderURL := fmt.Sprintf("https://%s:%d/s/clashMeta/%s", c.Domain, c.SubscribePort, subscriptionToken(c.Salt))
	surgeProviderURL := fmt.Sprintf("https://%s:%d/s/surge/%s", c.Domain, c.SubscribePort, subscriptionToken(c.Salt))
	if err := fillProfiles(&out, outbounds, clashProviderURL, surgeProviderURL); err != nil {
		return subscriptionOutputs{}, err
	}
	return out, nil
}

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

func removeStaleSubscriptionFiles(subscribeDir, token string) error {
	// "sing-box" and "sing-boxProfiles" are legacy directories from older versions.
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "sing-box", "sing-boxProfiles", "singboxProfiles", "surge", "surgeProfiles"} {
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
	return nil
}

func decodeSubscriptionOutbounds(b []byte) ([]map[string]any, error) {
	var outbounds []map[string]any
	if err := json.Unmarshal(b, &outbounds); err != nil {
		return nil, err
	}
	return outbounds, nil
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func stripClashHeader(fragment string) string {
	fragment = strings.Trim(fragment, "\r\n")
	if strings.HasPrefix(fragment, "proxies:") {
		fragment = strings.Trim(strings.TrimPrefix(fragment, "proxies:"), "\r\n")
	}
	return fragment
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

func (r RemoteSubscription) entry() subscription.RemoteEntry {
	return subscription.RemoteEntry{Domain: strings.TrimSpace(r.Domain), Port: r.Port, Alias: r.effectiveAlias(), Salt: strings.TrimSpace(r.Salt)}
}

func (r RemoteSubscription) effectiveAlias() string {
	alias := strings.TrimSpace(r.Alias)
	if alias == "" {
		alias = strings.TrimSpace(r.Domain)
	}
	return alias
}

func (s MonitorSource) effectiveAlias() string {
	alias := strings.TrimSpace(s.Alias)
	if alias == "" {
		alias = strings.TrimSpace(s.Domain)
	}
	return alias
}

func (s MonitorSource) monitorURL() string {
	return fmt.Sprintf("https://%s:%d/monitor/api/summary", strings.TrimSpace(s.Domain), s.MonitorPublicPort)
}

func (s MonitorSource) monitorBaseURL() string {
	return fmt.Sprintf("https://%s:%d/monitor", strings.TrimSpace(s.Domain), s.MonitorPublicPort)
}

// RemoteMonitorPath returns the path to the remote monitor snapshot JSON.
func RemoteMonitorPath(layout paths.Layout) string {
	return filepath.Join(layout.StateDir, "remote_monitor.json")
}

// FetchRemoteMonitorSources fetches monitor snapshots from all monitor sources.
func FetchRemoteMonitorSources(ctx context.Context, sources []MonitorSource, fetch SubscriptionFetcher) ([]monitor.SourceSummary, error) {
	if fetch == nil {
		fetch = DefaultSubscriptionFetch
	}
	var out []monitor.SourceSummary
	for _, src := range sources {
		body, err := fetch(ctx, src.monitorURL())
		if err != nil {
			return nil, fmt.Errorf("fetch remote monitor %s: %w", src.Domain, err)
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
			return nil, fmt.Errorf("decode remote monitor %s: %w", src.Domain, err)
		}
		var remoteSampledAt string
		if len(payload.Sources) > 0 {
			remoteSampledAt = payload.Sources[0].SampledAt
		}
		out = append(out, monitor.SourceSummary{
			Name:                subscription.AddNodePrefixFlag(src.effectiveAlias()),
			FetchedAt:           time.Now().UTC().Format(time.RFC3339),
			SampledAt:           remoteSampledAt,
			MonitorURL:          src.monitorBaseURL(),
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
		})
	}
	return out, nil
}

// RefreshRemoteMonitor fetches and persists monitor snapshots from all monitor sources.
func RefreshRemoteMonitor(ctx context.Context, layout paths.Layout, sources []MonitorSource, fetch SubscriptionFetcher) error {
	fetched, err := FetchRemoteMonitorSources(ctx, sources, fetch)
	if err != nil {
		return err
	}
	return monitor.WriteRemoteSources(RemoteMonitorPath(layout), fetched)
}

// ValidateMonitorSources checks that all monitor source entries are well-formed.
func ValidateMonitorSources(sources []MonitorSource) error {
	seen := map[string]bool{}
	for _, s := range sources {
		domain := strings.TrimSpace(s.Domain)
		if domain == "" {
			return fmt.Errorf("monitor source domain is required")
		}
		if strings.TrimSpace(s.effectiveAlias()) == "" {
			return fmt.Errorf("monitor source %s alias is required", domain)
		}
		if s.MonitorPublicPort <= 0 || s.MonitorPublicPort > 65535 {
			return fmt.Errorf("monitor source %s port must be between 1 and 65535", domain)
		}
		key := strings.ToLower(domain) + ":" + strconv.Itoa(s.MonitorPublicPort)
		if seen[key] {
			return fmt.Errorf("duplicate monitor source %s", key)
		}
		seen[key] = true
	}
	return nil
}

// LoadMonitorSources reads configured monitor source entries.
func LoadMonitorSources(layout paths.Layout) ([]MonitorSource, error) {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	dir := filepath.Join(layout.StateDir, monitorSourcesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var sources []MonitorSource
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		src := MonitorSource{
			Domain:            readRemoteStateDefault(root, "domain", ""),
			Alias:             readRemoteStateDefault(root, "alias", ""),
			MonitorPublicPort: readRemoteStateIntDefault(root, "monitor_public_port", 0),
		}
		if strings.TrimSpace(src.Alias) == "" {
			src.Alias = src.Domain
		}
		sources = append(sources, src)
	}
	return sources, ValidateMonitorSources(sources)
}

// SaveMonitorSources persists monitor source entries as small state files.
func SaveMonitorSources(layout paths.Layout, sources []MonitorSource) error {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	dir := filepath.Join(layout.StateDir, monitorSourcesDir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for i, src := range sources {
		entryDir := filepath.Join(dir, fmt.Sprintf("%03d", i+1))
		values := map[string]string{
			"domain":              strings.TrimSpace(src.Domain),
			"alias":               strings.TrimSpace(src.effectiveAlias()),
			"monitor_public_port": itoa(src.MonitorPublicPort),
		}
		for name, value := range values {
			if err := writePrivateStateFile(entryDir, name, value+"\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

// MigrateMonitorSources copies monitor-enabled remote subscriptions into the
// independent monitor_sources storage on first load. If the monitor_sources
// directory already exists the migration is a no-op.
func MigrateMonitorSources(layout paths.Layout) error {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	dir := filepath.Join(layout.StateDir, monitorSourcesDir)
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	remotesDir := filepath.Join(layout.StateDir, remoteSubscriptionsDir)
	entries, err := os.ReadDir(remotesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var sources []MonitorSource
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(remotesDir, entry.Name())
		monitorValue := readRemoteStateDefault(root, "monitor", "")
		if monitorValue == "" {
			monitorValue = readRemoteStateDefault(root, "traffic", "no")
		}
		if monitorValue != "yes" {
			continue
		}
		port := readRemoteStateIntDefault(root, "monitor_public_port", 0)
		if port == 0 {
			port = readRemoteStateIntDefault(root, "traffic_port", 0)
		}
		alias := readRemoteStateDefault(root, "alias", "")
		if alias == "" {
			alias = readRemoteStateDefault(root, "domain", "")
		}
		sources = append(sources, MonitorSource{
			Domain:            readRemoteStateDefault(root, "domain", ""),
			Alias:             alias,
			MonitorPublicPort: port,
		})
	}
	if len(sources) == 0 {
		return os.MkdirAll(dir, 0o700)
	}
	return SaveMonitorSources(layout, sources)
}
