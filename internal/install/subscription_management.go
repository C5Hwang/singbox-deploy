package install

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
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const remoteSubscriptionsDir = "remotes"

// RemoteSubscription is one same-version remote server aggregated into local
// subscription outputs. Remote node names are preserved unchanged.
type RemoteSubscription struct {
	Domain      string
	Port        int
	Salt        string
	Traffic     bool
	TrafficPort int
}

// SubscriptionFetcher fetches remote subscription or traffic JSON endpoints.
type SubscriptionFetcher func(context.Context, string) ([]byte, error)

// SubscriptionUpdateOptions describes a subscription settings update.
type SubscriptionUpdateOptions struct {
	Layout paths.Layout
	Runner system.Runner

	Salt          string
	SubscribePort int
	Remotes       []RemoteSubscription
	SetRemotes    bool

	Firewall      system.Firewall
	CheckPorts    func(context.Context, Config, int) error
	Fetch         SubscriptionFetcher
	Progress      func(Event)
	NginxConfPath string
}

// UpdateSubscriptions updates local subscription settings, rewrites generated
// subscription files, persists remote subscription entries, and reloads Nginx
// when the public subscription port changes.
func UpdateSubscriptions(ctx context.Context, opts SubscriptionUpdateOptions) (Config, error) {
	opts = defaultSubscriptionOptions(opts)
	cfg, err := LoadProtocolConfig(opts.Layout)
	if err != nil {
		return Config{}, err
	}
	oldPort := cfg.SubscribePort
	if strings.TrimSpace(opts.Salt) != "" {
		cfg.Salt = strings.TrimSpace(opts.Salt)
	}
	if opts.SubscribePort > 0 {
		cfg.SubscribePort = opts.SubscribePort
	}
	if cfg.SubscribePort <= 0 || cfg.SubscribePort > 65535 {
		return Config{}, fmt.Errorf("subscription port must be between 1 and 65535")
	}

	remotes := opts.Remotes
	if !opts.SetRemotes {
		remotes, err = LoadRemoteSubscriptions(opts.Layout)
		if err != nil {
			return Config{}, err
		}
	}
	if err := validateRemoteSubscriptions(remotes); err != nil {
		return Config{}, err
	}

	steps := subscriptionUpdateSteps(opts, oldPort, cfg.SubscribePort, remotes)
	for i, s := range steps {
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "running"})
		if err := s.run(ctx, cfg); err != nil {
			emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "fail", Err: err})
			return Config{}, fmt.Errorf("%s: %w", s.label, err)
		}
		emitProtocolProgress(opts.Progress, Event{Index: i + 1, Total: len(steps), Label: s.label, Detail: s.detail, Status: "ok"})
	}
	return cfg, nil
}

type subscriptionUpdateStep struct {
	label  string
	detail string
	run    func(context.Context, Config) error
}

func subscriptionUpdateSteps(opts SubscriptionUpdateOptions, oldPort, newPort int, remotes []RemoteSubscription) []subscriptionUpdateStep {
	portChanged := oldPort != newPort
	var steps []subscriptionUpdateStep
	if portChanged {
		steps = append(steps, subscriptionUpdateStep{label: "Port check", detail: "check new subscription HTTPS port", run: func(ctx context.Context, cfg Config) error {
			return opts.CheckPorts(ctx, cfg, newPort)
		}})
		if opts.Firewall != system.FirewallNone {
			steps = append(steps, subscriptionUpdateStep{label: "Firewall", detail: "open new subscription HTTPS port", run: func(_ context.Context, _ Config) error {
				cmds := system.FirewallCommands(opts.Firewall, []system.Port{{Number: newPort, Proto: "tcp", Label: "subscription/Nginx"}})
				if opts.Firewall == system.FirewallFirewalld && len(cmds) > 0 {
					cmds = append(cmds, system.Command{Name: "firewall-cmd", Args: []string{"--reload"}})
				}
				return runProtocolCommands(opts.Runner, cmds...)
			}})
		}
	}
	steps = append(steps,
		subscriptionUpdateStep{label: "Remote traffic", detail: "refresh remote traffic snapshots", run: func(ctx context.Context, _ Config) error {
			return refreshRemoteTraffic(ctx, opts.Layout, remotes, opts.Fetch)
		}},
		subscriptionUpdateStep{label: "Subscriptions", detail: "regenerate local and remote subscription outputs", run: func(ctx context.Context, cfg Config) error {
			return writeSubscriptionsWithRemotes(ctx, opts.Layout, cfg, remotes, opts.Fetch)
		}},
		subscriptionUpdateStep{label: "State", detail: "persist subscription settings", run: func(_ context.Context, cfg Config) error {
			if err := writeInstallState(opts.Layout.StateDir, cfg); err != nil {
				return err
			}
			return SaveRemoteSubscriptions(opts.Layout, remotes)
		}},
	)
	if portChanged {
		steps = append(steps, subscriptionUpdateStep{label: "Nginx", detail: "rewrite managed Nginx config and restart", run: func(_ context.Context, cfg Config) error {
			if err := writeManagedNginxConfig(opts.Layout, cfg, opts.NginxConfPath); err != nil {
				return err
			}
			return runProtocolCommands(opts.Runner,
				system.Command{Name: "nginx", Args: []string{"-t"}},
				system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
			)
		}})
	}
	return steps
}

func defaultSubscriptionOptions(opts SubscriptionUpdateOptions) SubscriptionUpdateOptions {
	if opts.Layout.Root == "" {
		opts.Layout = paths.DefaultLayout()
	}
	if opts.Runner == nil {
		opts.Runner = system.NewExecRunner(nil)
	}
	if opts.Fetch == nil {
		opts.Fetch = defaultSubscriptionFetch
	}
	if opts.CheckPorts == nil {
		opts.CheckPorts = func(ctx context.Context, cfg Config, port int) error {
			return system.CheckPorts(ctx, cfg.Domain, []system.Port{{Number: port, Proto: "tcp", Label: "subscription/Nginx", Public: true}})
		}
	}
	if opts.NginxConfPath == "" {
		opts.NginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"
	}
	return opts
}

func defaultSubscriptionFetch(ctx context.Context, url string) ([]byte, error) {
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

func validateRemoteSubscriptions(remotes []RemoteSubscription) error {
	seen := map[string]bool{}
	for _, r := range remotes {
		domain := strings.TrimSpace(r.Domain)
		if domain == "" {
			return fmt.Errorf("remote domain is required")
		}
		if r.Port <= 0 || r.Port > 65535 {
			return fmt.Errorf("remote %s subscription port must be between 1 and 65535", domain)
		}
		if strings.TrimSpace(r.Salt) == "" {
			return fmt.Errorf("remote %s salt is required", domain)
		}
		if r.Traffic && (r.TrafficPort <= 0 || r.TrafficPort > 65535) {
			return fmt.Errorf("remote %s traffic port must be between 1 and 65535", domain)
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
			Domain:      readRemoteStateDefault(root, "domain", ""),
			Port:        readRemoteStateIntDefault(root, "subscribe_port", 0),
			Salt:        readRemoteStateDefault(root, "salt", ""),
			Traffic:     readRemoteStateDefault(root, "traffic", "no") == "yes",
			TrafficPort: readRemoteStateIntDefault(root, "traffic_port", 0),
		}
		remotes = append(remotes, remote)
	}
	return remotes, validateRemoteSubscriptions(remotes)
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
			"salt":           strings.TrimSpace(remote.Salt),
			"traffic":        yesNoString(remote.Traffic),
			"traffic_port":   itoa(remote.TrafficPort),
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

func writeSubscriptionsWithRemotes(ctx context.Context, layout paths.Layout, cfg Config, remotes []RemoteSubscription, fetch SubscriptionFetcher) error {
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
		fetch = defaultSubscriptionFetch
	}

	defaultBody, err := subscription.DecodeBase64(out.DefaultBase64)
	if err != nil {
		return subscriptionOutputs{}, err
	}
	defaultParts := splitNonEmptyLines(defaultBody)
	clashParts := []string{stripClashHeader(out.ClashFragment)}
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
		defaultParts = append(defaultParts, splitNonEmptyLines(subscription.FilterDefaultLinks(decodedDefault))...)

		remoteClash, err := fetch(ctx, entry.ClashURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote clash %s: %w", remote.Domain, err)
		}
		clashParts = append(clashParts, stripClashHeader(string(remoteClash)))

		remoteSingBox, err := fetch(ctx, entry.SingBoxURL())
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("fetch remote sing-box %s: %w", remote.Domain, err)
		}
		nodeOutbounds, err := subscription.ExtractSingBoxNodeOutbounds(remoteSingBox)
		if err != nil {
			return subscriptionOutputs{}, fmt.Errorf("extract remote sing-box %s: %w", remote.Domain, err)
		}
		remoteOutbounds, err := decodeSubscriptionOutbounds(nodeOutbounds)
		if err != nil {
			return subscriptionOutputs{}, err
		}
		outbounds = append(outbounds, remoteOutbounds...)
	}

	out.DefaultBase64 = subscription.EncodeBase64(strings.Join(defaultParts, "\n"))
	out.ClashFragment = "proxies:\n" + strings.Join(nonEmptyStrings(clashParts), "\n") + "\n"
	if err := fillSingBoxOutputs(&out, outbounds); err != nil {
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
		"sing-boxProfiles":  out.SingBoxOutbounds,
		"sing-box":          out.SingBoxProfile,
	}
	for dir, body := range pathsByDir {
		if err := writeFile(filepath.Join(layout.SubscribeDir, dir, token), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := removeStaleSubscriptionFiles(layout.SubscribeDir, token); err != nil {
		return err
	}
	return writeStateFile(layout.StateDir, "subscribe_salt", cfg.Salt+"\n")
}

func removeStaleSubscriptionFiles(subscribeDir, token string) error {
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "sing-boxProfiles", "sing-box"} {
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
	return subscription.RemoteEntry{Domain: strings.TrimSpace(r.Domain), Port: r.Port, Salt: strings.TrimSpace(r.Salt)}
}

func (r RemoteSubscription) trafficURL() string {
	return fmt.Sprintf("https://%s:%d/traffic/api/summary", strings.TrimSpace(r.Domain), r.TrafficPort)
}

func remoteTrafficPath(layout paths.Layout) string {
	return filepath.Join(layout.StateDir, "remote_traffic.json")
}

func refreshRemoteTraffic(ctx context.Context, layout paths.Layout, remotes []RemoteSubscription, fetch SubscriptionFetcher) error {
	if fetch == nil {
		fetch = defaultSubscriptionFetch
	}
	var sources []monitor.SourceSummary
	for _, remote := range remotes {
		if !remote.Traffic {
			continue
		}
		body, err := fetch(ctx, remote.trafficURL())
		if err != nil {
			return fmt.Errorf("fetch remote traffic %s: %w", remote.Domain, err)
		}
		var payload struct {
			InUsedBytes         uint64                `json:"inUsedBytes"`
			OutUsedBytes        uint64                `json:"outUsedBytes"`
			TotalUsedBytes      uint64                `json:"totalUsedBytes"`
			InRemainingBytes    uint64                `json:"inRemainingBytes"`
			OutRemainingBytes   uint64                `json:"outRemainingBytes"`
			TotalRemainingBytes uint64                `json:"totalRemainingBytes"`
			InLimitBytes        uint64                `json:"inLimitBytes"`
			OutLimitBytes       uint64                `json:"outLimitBytes"`
			TotalLimitBytes     uint64                `json:"totalLimitBytes"`
			ResetTime           string                `json:"resetTime"`
			Trend               []monitor.HourlyPoint `json:"trend"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return fmt.Errorf("decode remote traffic %s: %w", remote.Domain, err)
		}
		sources = append(sources, monitor.SourceSummary{
			Name:                strings.TrimSpace(remote.Domain),
			FetchedAt:           time.Now().Format(time.RFC3339),
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
		})
	}
	return monitor.WriteRemoteSources(remoteTrafficPath(layout), sources)
}
