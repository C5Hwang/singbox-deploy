package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/relaylinks"
)

// runMonitor dispatches the "monitor serve" subcommand that runs the long-lived
// monitor HTTP/API service.
func runMonitor(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return flag.ErrHelp
	}
	layout := paths.DefaultLayout()

	fs := flag.NewFlagSet("monitor serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(deploy.DefaultMonitorPort), "listen address")
	iface := fs.String("interface", "", "monitored network interface (default: auto-detect)")
	dbPath := fs.String("db", layout.MonitorDB, "monitor database path")
	inLimit := fs.Uint64("in-limit-bytes", 0, "monthly inbound traffic limit in bytes (0 = unlimited)")
	outLimit := fs.Uint64("out-limit-bytes", 0, "monthly outbound traffic limit in bytes (0 = unlimited)")
	totalLimit := fs.Uint64("total-limit-bytes", 0, "monthly total traffic limit in bytes (0 = unlimited)")
	resetDay := fs.Int("reset-day", deploy.DefaultResetDay, "monthly reset day-of-month")
	resetHour := fs.Int("reset-hour", deploy.DefaultResetHour, "monthly reset hour in GMT, 0-23")
	alias := fs.String("alias", deploy.DefaultMonitorAlias, "traffic source alias shown in the UI")
	intervalSec := fs.Int("interval-seconds", deploy.DefaultMonitorIntervalSeconds, "sampling interval in seconds")
	remoteMonitorPath := fs.String("remote-monitor", filepath.Join(layout.StateDir, "remote_monitor.json"), "remote monitor snapshot JSON path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	selectedIface := *iface
	if selectedIface == "" {
		detected, err := monitor.DefaultInterface()
		if err != nil {
			return err
		}
		selectedIface = detected
	}

	store, err := monitor.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	clock, err := monitor.NewNetworkClock(context.Background())
	now := func() time.Time { return time.Now().UTC() }
	if err != nil {
		log.Printf("monitor: network GMT unavailable; using host UTC clock: %v", err)
	} else {
		now = clock.Now
	}

	ctrl := &hubctl.Controller{Layout: layout, ExpectedVersion: version}
	cfg := monitor.Config{
		Listen:            *listen,
		Interface:         selectedIface,
		SamplingInterval:  time.Duration(*intervalSec) * time.Second,
		InLimitBytes:      *inLimit,
		OutLimitBytes:     *outLimit,
		TotalLimitBytes:   *totalLimit,
		ResetDay:          *resetDay,
		ResetHour:         *resetHour,
		Alias:             *alias,
		RemoteMonitorPath: *remoteMonitorPath,
		LocalPositionPath: filepath.Join(layout.StateDir, "local_monitor_position"),
		// Read from state rather than passed as a flag, so the secret never
		// reaches the unit file, the process table, or the journal. An older
		// unit that predates the token therefore still picks one up.
		AccessToken: monitor.ReadAccessToken(layout.StateDir),
		// The hub pulls every spoke's monitor summary over the WireGuard overlay,
		// so monitor data never traverses the public internet.
		RefreshRemoteSources: func(ctx context.Context) error {
			refreshErr := ctrl.RefreshMonitor(ctx)
			// The snapshot that was just refreshed is what says whether each
			// relay still has traffic left, so this is the moment to republish
			// if any of them crossed its limit — or came back after a reset.
			return errors.Join(refreshErr, ctrl.ReconcileRelayPublication(ctx))
		},
		FetchRemoteData: func(ctx context.Context, sourceID, path string, query url.Values) ([]byte, error) {
			endpoint, err := monitorEndpointForPath(path)
			if err != nil {
				return nil, err
			}
			return ctrl.MonitorData(ctx, sourceID, endpoint, query.Get("ip"))
		},
		// A hub that relays for other nodes measures the route to each of
		// them, on the same schedule as the carrier probes.
		ExtraPingTargets: relay.PingTargets(layout),
		// It also meters the flows it forwards, per client address, which the
		// input/output counters alone would never see.
		RelayForwards: relay.MonitorForwards(layout),
		// The registry is the fleet's answer to "is anything relayed", and the
		// dashboard hides its relay page entirely when nothing is. It also names
		// the relays, so the page asks them for their probes instead of asking
		// the whole fleet and discarding most of the answers.
		RelayRegistry: func() ([]string, int) {
			links, err := relaylinks.Load(layout)
			if err != nil {
				log.Printf("monitor: read relay links: %v", err)
				return nil, 0
			}
			relays := make([]string, 0, len(links))
			seen := map[string]bool{}
			for _, link := range links {
				// One relay commonly fronts several landing nodes, and the
				// dashboard wants each relay once.
				id := monitorSourceID(link.RelayID)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				relays = append(relays, id)
			}
			return relays, len(links)
		},
		Now: now,
	}
	m := monitor.New(store, cfg, relay.NewQuotaController(systemdSingBox{}, layout, "/usr/bin/singbox-deploy"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return m.Run(ctx)
}

// monitorSourceID maps a relay registry node ID onto the ID the dashboard knows
// that node by: the hub is its own "local" source there, and a spoke keeps its
// stable registry ID.
func monitorSourceID(nodeID string) string {
	id := strings.TrimSpace(nodeID)
	if id == relaylinks.HubNodeID {
		return "local"
	}
	return id
}

func monitorEndpointForPath(path string) (nodeapi.MonitorEndpoint, error) {
	switch path {
	case "/api/traffic-trend":
		return nodeapi.MonitorTrafficTrend, nil
	case "/api/traffic-recent":
		return nodeapi.MonitorTrafficRecent, nil
	case "/api/resource-trend":
		return nodeapi.MonitorResourceTrend, nil
	case "/api/resource-recent":
		return nodeapi.MonitorResourceRecent, nil
	case "/api/ping-trend":
		return nodeapi.MonitorPingTrend, nil
	case "/api/ping-series":
		return nodeapi.MonitorPingSeries, nil
	case "/api/ip-traffic":
		return nodeapi.MonitorIPTraffic, nil
	case "/api/ip-detail":
		return nodeapi.MonitorIPDetail, nil
	default:
		return "", fmt.Errorf("unsupported monitor resource %q", path)
	}
}

// systemdSingBox controls sing-box.service via systemctl for quota enforcement.
type systemdSingBox struct{}

func (systemdSingBox) Start() error {
	return exec.Command("systemctl", "start", "sing-box.service").Run()
}
func (systemdSingBox) Stop() error {
	return exec.Command("systemctl", "stop", "sing-box.service").Run()
}

func (systemdSingBox) IsActive() (bool, error) {
	err := exec.Command("systemctl", "is-active", "--quiet", "sing-box.service").Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}
