package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
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
	// newMonitor is a lifecycle seam for tests that cannot bind sockets. It
	// returns the mounted handler and the blocking sampler/server function.
	newMonitor func(*monitor.Store, monitor.Config) (http.Handler, func(context.Context) error)

	// lifecycle serializes reload/stop so two callers cannot interleave one
	// caller's teardown with another's startup. It is deliberately separate from
	// mu, which only guards the fields below and is also taken by ServeHTTP.
	lifecycle sync.Mutex

	mu      sync.RWMutex
	cancel  context.CancelFunc
	done    chan struct{}
	handler http.Handler
}

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
	s.stopRunning()

	store := state.NewStore(s.layout.StateDir)
	if v, _ := store.ReadValue("monitor", false); v == "no" {
		return
	}
	if _, err := store.ReadValue("domain", true); err != nil {
		return // not installed yet
	}

	cfg, err := s.buildConfig(store)
	if err != nil {
		log.Printf("agent monitor: %v", err)
		return
	}
	dbStore, err := monitor.OpenStore(s.layout.MonitorDB)
	if err != nil {
		log.Printf("agent monitor: open store: %v", err)
		return
	}
	parent := s.parent
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	newMonitor := s.newMonitor
	if newMonitor == nil {
		newMonitor = func(store *monitor.Store, cfg monitor.Config) (http.Handler, func(context.Context) error) {
			m := monitor.New(store, cfg, systemdSingBox{})
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
	s.mu.Unlock()

	go func() {
		defer close(done)
		defer dbStore.Close()
		err := run(ctx)
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
			}
			s.mu.Unlock()
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
		clock, err := monitor.NewNetworkClock(context.Background())
		if err != nil {
			return monitor.Config{}, err
		}
		now = clock.Now
	}
	return monitor.Config{
		// Monitor reads are mounted behind the bearer-authenticated agent API.
		// Monitor.Run still owns its sampling lifecycle and HTTP server, so bind
		// that internal listener to an ephemeral loopback port only.
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
		Now:               now,
	}, nil
}

func (s *monitorSupervisor) stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.stopRunning()
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
