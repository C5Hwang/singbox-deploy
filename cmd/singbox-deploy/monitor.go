package main

import (
	"context"
	"flag"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// secondsToDuration converts a seconds count to a time.Duration.
func secondsToDuration(sec int) time.Duration { return time.Duration(sec) * time.Second }

// runMonitor dispatches the "monitor serve" subcommand that runs the long-lived
// traffic monitor HTTP/API service.
func runMonitor(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return flag.ErrHelp
	}
	layout := paths.DefaultLayout()

	fs := flag.NewFlagSet("monitor serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:19090", "listen address")
	iface := fs.String("interface", "", "monitored network interface (default: auto-detect)")
	dbPath := fs.String("db", layout.TrafficDB, "traffic database path")
	limit := fs.Uint64("limit-bytes", 0, "monthly traffic limit in bytes (0 = unlimited)")
	resetDay := fs.Int("reset-day", 1, "monthly reset day-of-month")
	intervalSec := fs.Int("interval-seconds", 300, "sampling interval in seconds")
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

	cfg := monitor.Config{
		Listen:           *listen,
		Interface:        selectedIface,
		SamplingInterval: secondsToDuration(*intervalSec),
		LimitBytes:       *limit,
		ResetDay:         *resetDay,
	}
	m := monitor.New(store, cfg, systemdSingBox{})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return m.Run(ctx)
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
