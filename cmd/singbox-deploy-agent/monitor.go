package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// monitorSupervisor runs the in-process monitor sampler and makes its handler
// available to the authenticated agent API. Its own listener is loopback-only.
// It reads settings from install state and can restart after a reconfigure. A
// spoke does not aggregate remote sources, so no remote refresh is wired.
type monitorSupervisor struct {
	layout paths.Layout
	// parent is the agent process lifetime. Reloads must never inherit an HTTP
	// request context: net/http cancels that context as soon as the install or
	// reconfigure handler returns, which would immediately stop sampling.
	parent context.Context
	// now is injectable so lifecycle tests do not need a network-time lookup.
	// Production leaves it nil and uses the network-backed clock below.
	now func() time.Time
	// newNetworkClock is a test seam for network-time failure. Production uses
	// monitor.NewNetworkClock and falls back to the host's UTC clock if every
	// trusted HTTP Date source is temporarily unavailable.
	newNetworkClock func(context.Context) (*monitor.NetworkClock, error)
	// newMonitor is a lifecycle seam for tests that cannot bind sockets. It
	// returns the mounted handler and the blocking sampler/server function.
	newMonitor func(*monitor.Store, monitor.Config) (http.Handler, func(context.Context) error)
	// startSingBox is used only when disabling a monitor that previously
	// stopped sing-box for quota enforcement.
	startSingBox func() error

	// lifecycle serializes reload/stop so two callers cannot interleave one
	// caller's teardown with another's startup. It is deliberately separate from
	// mu, which only guards the fields below and is also taken by ServeHTTP.
	lifecycle sync.Mutex

	mu      sync.RWMutex
	cancel  context.CancelFunc
	done    chan struct{}
	handler http.Handler
	active  *monitor.Monitor
	store   *monitor.Store
	cfg     monitor.Config

	// retryTimer and stopped are guarded by lifecycle. retryDelay is injectable
	// so tests can prove recovery without waiting for the production backoff.
	retryTimer *time.Timer
	retryDelay time.Duration
	stopped    bool
}

const defaultMonitorRetryDelay = 10 * time.Second

func newMonitorSupervisor(parent context.Context, layout paths.Layout) *monitorSupervisor {
	if parent == nil {
		parent = context.Background()
	}
	return &monitorSupervisor{layout: layout, parent: parent}
}

// reload (re)starts the monitor from current install state. It is a no-op when
// monitoring is disabled or the install is incomplete.
func (s *monitorSupervisor) reload() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.stopped {
		return
	}
	s.cancelRetryLocked()
	s.stopRunning()

	store := state.NewStore(s.layout.StateDir)
	if v, _ := store.ReadValue("monitor", false); v == "no" {
		start := s.startSingBox
		if start == nil {
			start = systemdSingBox{}.Start
		}
		if err := monitor.ReleaseQuotaStop(s.layout.MonitorDB, start); err != nil {
			log.Printf("agent monitor: release quota stop: %v", err)
			s.scheduleRetryLocked()
		}
		return
	}
	if _, err := store.ReadValue("domain", true); err != nil {
		return // not installed yet
	}

	cfg, err := s.buildConfig(store)
	if err != nil {
		log.Printf("agent monitor: %v", err)
		s.scheduleRetryLocked()
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.layout.MonitorDB), 0o755); err != nil {
		log.Printf("agent monitor: create store directory: %v", err)
		s.scheduleRetryLocked()
		return
	}
	dbStore, err := monitor.OpenStore(s.layout.MonitorDB)
	if err != nil {
		log.Printf("agent monitor: open store: %v", err)
		s.scheduleRetryLocked()
		return
	}
	parent := s.parent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	newMonitor := s.newMonitor
	var active *monitor.Monitor
	if newMonitor == nil {
		newMonitor = func(store *monitor.Store, cfg monitor.Config) (http.Handler, func(context.Context) error) {
			m := monitor.New(store, cfg, relay.NewQuotaController(systemdSingBox{}, s.layout, agentBinaryPath))
			active = m
			return m.Handler(), m.Run
		}
	}
	handler, run := newMonitor(dbStore, cfg)
	done := make(chan struct{})
	// Publish before starting the sampler so a monitor that exits immediately
	// still recognizes itself as the current one and retires cleanly.
	s.mu.Lock()
	s.cancel = cancel
	s.done = done
	s.handler = handler
	s.active = active
	s.store = dbStore
	s.cfg = cfg
	s.mu.Unlock()

	go func() {
		defer close(done)
		defer dbStore.Close()
		err := run(ctx)
		retry := false
		if ctx.Err() == nil {
			if err != nil {
				log.Printf("agent monitor exited: %v", err)
			}
			// Unexpected exit: wait for any in-flight API read, then make the
			// closed monitor unavailable. stopRunning never holds mu while it
			// waits on done, so taking the write lock here cannot deadlock an
			// intentional stop that raced past the cancellation check above.
			s.mu.Lock()
			if s.done == done {
				s.handler = nil
				s.cancel = nil
				s.done = nil
				s.active = nil
				s.store = nil
				s.cfg = monitor.Config{}
				retry = true
			}
			s.mu.Unlock()
		}
		if retry {
			// Wait until the deferred database close and done close have both
			// completed before a retry can open the same SQLite store.
			go s.retryAfter(done)
		}
	}()
}

func (s *monitorSupervisor) buildConfig(store state.Store) (monitor.Config, error) {
	iface := readString(store, "monitor_interface", "")
	if iface == "" {
		detected, err := monitor.DefaultInterface()
		if err != nil {
			return monitor.Config{}, err
		}
		iface = detected
	}
	interval := readInt(store, "monitor_interval_seconds", deploy.DefaultMonitorIntervalSeconds)
	now := s.now
	if now == nil {
		newClock := s.newNetworkClock
		if newClock == nil {
			newClock = monitor.NewNetworkClock
		}
		clock, err := newClock(context.Background())
		if err != nil {
			log.Printf("agent monitor: network GMT unavailable; using host UTC clock: %v", err)
			now = func() time.Time { return time.Now().UTC() }
		} else {
			now = clock.Now
		}
	}
	return monitor.Config{
		// Monitor reads are mounted behind the bearer-authenticated agent API.
		// Monitor.Run still owns its sampling lifecycle and HTTP server, so bind
		// that internal listener to an ephemeral loopback port only. AccessToken
		// stays empty for the same reason: the agent API token already guards
		// these reads, and a second token here would only lock out the hub.
		Listen:            net.JoinHostPort("127.0.0.1", "0"),
		Interface:         iface,
		SamplingInterval:  time.Duration(interval) * time.Second,
		InLimitBytes:      readUint(store, "traffic_in_limit_bytes", 0),
		OutLimitBytes:     readUint(store, "traffic_out_limit_bytes", 0),
		TotalLimitBytes:   readUint(store, "traffic_total_limit_bytes", 0),
		ResetDay:          readInt(store, "reset_day", deploy.DefaultResetDay),
		ResetHour:         readInt(store, "reset_hour", deploy.DefaultResetHour),
		Alias:             readString(store, "monitor_alias", deploy.DefaultMonitorAlias),
		LocalPositionPath: s.layout.StateDir + "/local_monitor_position",
		// A spoke that relays for other nodes measures the route to each of
		// them, on the same schedule as the carrier probes.
		ExtraPingTargets: relay.PingTargets(s.layout),
		// It also meters the flows it forwards, per client address, which the
		// input/output counters alone would never see.
		RelayForwardPorts: relay.ForwardListenPorts(s.layout),
		Now:               now,
	}, nil
}

func (s *monitorSupervisor) stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.stopped = true
	s.cancelRetryLocked()
	s.stopRunning()
}

func (s *monitorSupervisor) retryAfter(done <-chan struct{}) {
	<-done
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.scheduleRetryLocked()
}

func (s *monitorSupervisor) scheduleRetryLocked() {
	if s.stopped || s.retryTimer != nil {
		return
	}
	if s.parent != nil && s.parent.Err() != nil {
		return
	}
	s.mu.RLock()
	active := s.done != nil
	s.mu.RUnlock()
	if active {
		return
	}
	delay := s.retryDelay
	if delay <= 0 {
		delay = defaultMonitorRetryDelay
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.lifecycle.Lock()
		if s.retryTimer != timer {
			s.lifecycle.Unlock()
			return
		}
		s.retryTimer = nil
		stopped := s.stopped
		s.lifecycle.Unlock()
		if !stopped {
			s.reload()
		}
	})
	s.retryTimer = timer
}

func (s *monitorSupervisor) cancelRetryLocked() {
	if s.retryTimer == nil {
		return
	}
	s.retryTimer.Stop()
	s.retryTimer = nil
}

// stopRunning retires the active monitor and waits for it to release its
// database. The registry fields are cleared under mu, but the wait itself
// happens with mu released: a monitor that exits on its own also needs the
// write lock to retire itself, so waiting while holding it would deadlock
// whenever an intentional stop raced a spontaneous exit. Callers hold
// lifecycle, which is what actually serializes teardown against startup.
func (s *monitorSupervisor) stopRunning() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.done = nil
	s.handler = nil
	s.active = nil
	s.store = nil
	s.cfg = monitor.Config{}
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// ServeHTTP dispatches to the currently active in-process monitor. Holding a
// read lock for the request prevents reload from closing its database while a
// monitor response is being produced.
func (s *monitorSupervisor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.handler == nil {
		http.Error(w, "agent monitor is not running", http.StatusServiceUnavailable)
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *monitorSupervisor) trafficUsage() (monitor.TrafficUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil {
		return monitor.TrafficUsage{}, fmt.Errorf("agent monitor is not running")
	}
	if s.active != nil {
		return s.active.CurrentTrafficUsage()
	}
	now := time.Now().UTC()
	if s.cfg.Now != nil {
		now = s.cfg.Now().UTC()
	}
	cycleStart := monitor.CycleStart(now, s.cfg.ResetDay, s.cfg.ResetHour)
	totals, err := s.store.TotalsSince(cycleStart.Unix())
	if err != nil {
		return monitor.TrafficUsage{}, err
	}
	return monitor.TrafficUsage{Totals: totals, CycleStart: cycleStart}, nil
}

func (s *monitorSupervisor) setTrafficUsage(expectedCycleStart int64, target monitor.TrafficTotals) (monitor.TrafficUsageUpdate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil {
		return monitor.TrafficUsageUpdate{}, fmt.Errorf("agent monitor is not running")
	}
	if s.active != nil {
		return s.active.SetCurrentTrafficUsage(expectedCycleStart, target)
	}
	now := time.Now().UTC()
	if s.cfg.Now != nil {
		now = s.cfg.Now().UTC()
	}
	cycleStart := monitor.CycleStart(now, s.cfg.ResetDay, s.cfg.ResetHour)
	if cycleStart.Unix() != expectedCycleStart {
		return monitor.TrafficUsageUpdate{}, monitor.ErrTrafficCycleChanged
	}
	previous, err := s.store.ReplaceTotalsSince(cycleStart.Unix(), now.Unix(), target)
	if err != nil {
		return monitor.TrafficUsageUpdate{}, err
	}
	return monitor.TrafficUsageUpdate{
		Previous: monitor.TrafficUsage{Totals: previous, CycleStart: cycleStart},
		Applied:  monitor.TrafficUsage{Totals: target, CycleStart: cycleStart},
	}, nil
}

func readString(store state.Store, name, fallback string) string {
	v, err := store.ReadValue(name, false)
	if err != nil || v == "" {
		return fallback
	}
	return v
}

func readInt(store state.Store, name string, fallback int) int {
	v := readString(store, name, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func readUint(store state.Store, name string, fallback uint64) uint64 {
	v := readString(store, name, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// systemdSingBox controls sing-box.service for quota enforcement.
type systemdSingBox struct {
	// systemctlOutput is a test seam for the read-only status query. Start and
	// Stop intentionally keep using exec.Command directly.
	systemctlOutput func(...string) ([]byte, error)
}

func (systemdSingBox) Start() error {
	return exec.Command("systemctl", "start", "sing-box.service").Run()
}

func (systemdSingBox) Stop() error {
	return exec.Command("systemctl", "stop", "sing-box.service").Run()
}

func (s systemdSingBox) IsActive() (bool, error) {
	run := s.systemctlOutput
	if run == nil {
		run = func(args ...string) ([]byte, error) {
			return exec.Command("systemctl", args...).Output()
		}
	}
	out, err := run(
		"show",
		"--property=LoadState",
		"--property=ActiveState",
		"sing-box.service",
	)
	if err != nil {
		return false, fmt.Errorf("query sing-box systemd state: %w", err)
	}
	properties := make(map[string]string, 2)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			properties[key] = strings.TrimSpace(value)
		}
	}
	if loadState := properties["LoadState"]; loadState != "loaded" {
		if loadState == "" {
			loadState = "missing"
		}
		return false, fmt.Errorf("sing-box systemd LoadState is %q", loadState)
	}
	switch activeState := properties["ActiveState"]; activeState {
	case "active", "activating", "reloading", "deactivating", "maintenance", "refreshing":
		// Transitional states may still be serving traffic. Treat them as
		// active so an exceeded quota always issues an idempotent stop.
		return true, nil
	case "inactive", "failed":
		return false, nil
	case "":
		return false, fmt.Errorf("sing-box systemd ActiveState is missing")
	default:
		return false, fmt.Errorf("sing-box systemd ActiveState is unknown: %q", activeState)
	}
}
