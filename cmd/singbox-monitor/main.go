// singbox-monitor is the standalone monitor service that runs on both the
// master and every node. It samples interface counters, enforces the local
// traffic quota, and serves the monitor HTTP API/UI.
//
// On the master it binds to 127.0.0.1 behind Nginx; on a node it binds to the
// WireGuard IP so the master can pull aggregated samples over the internal
// network.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "singbox-monitor:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: singbox-monitor serve [flags]")
		return flag.ErrHelp
	}
	layout := paths.DefaultLayout()

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(deploy.DefaultMonitorPort), "listen address")
	iface := fs.String("interface", "", "monitored network interface (default: auto-detect)")
	dbPath := fs.String("db", layout.MonitorDB, "monitor database path")
	inLimit := fs.Uint64("in-limit-bytes", 0, "monthly inbound traffic limit in bytes (0 = unlimited)")
	outLimit := fs.Uint64("out-limit-bytes", 0, "monthly outbound traffic limit in bytes (0 = unlimited)")
	totalLimit := fs.Uint64("total-limit-bytes", 0, "monthly total traffic limit in bytes (0 = unlimited)")
	resetDay := fs.Int("reset-day", deploy.DefaultResetDay, "monthly reset day-of-month")
	resetHour := fs.Int("reset-hour", deploy.DefaultResetHour, "monthly reset hour GMT 0-23")
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
	if err != nil {
		return err
	}

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
		Now:               clock.Now,
	}
	m := monitor.New(store, cfg, systemdSingBox{})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return m.Run(ctx)
}

// systemdSingBox controls sing-box.service via systemctl for quota enforcement.
// Same shape as the original implementation that lived in cmd/singbox-deploy.
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
