package ui

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

var (
	defaultStatusLayout = paths.DefaultLayout
	detectStatusHost    = system.DetectHost
	statusNow           = time.Now
	statusCommandOutput = func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		return string(out), err
	}
)

func loadStatus() Status {
	layout := defaultStatusLayout()
	store := state.NewStore(layout.StateDir)
	domain := readStatusState(store, "domain")
	subscribePort := readStatusState(store, "subscribe_port")
	if subscribePort == "" {
		subscribePort = strconv.Itoa(deploy.DefaultSubscribePort)
	}
	monitorPublicPort := readStatusState(store, "monitor_public_port")
	if monitorPublicPort == "" {
		monitorPublicPort = readStatusState(store, "traffic_port")
	}
	if monitorPublicPort == "" {
		monitorPublicPort = subscribePort
	}
	monitorStateValue := readStatusState(store, "monitor")
	if monitorStateValue == "" {
		monitorStateValue = readStatusState(store, "traffic_monitor")
	}
	monitorEnabled := monitorStateValue != "no"
	monitorState := "disabled"
	if monitorEnabled {
		monitorState = serviceState(system.MonitorService)
	}

	singBoxVer := singBoxVersion(layout.SingBoxBin)
	singBoxState := singBoxServiceState(singBoxVer, store, layout, monitorEnabled)

	return Status{
		Domain:       domain,
		PublicIP:     readStatusState(store, "public_ip"),
		OSArch:       osArchStatus(),
		SingBoxVer:   singBoxVer,
		SingBoxState: singBoxState,
		NginxState:   serviceState("nginx.service"),
		MonitorState: monitorState,
		CertState:    certificateState(layout, domain),
		Protocols:    readStatusState(store, "enabled_protocols"),
		Subscription: subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "default"),
		ClashMetaSub: subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "clashMetaProfiles"),
		SingBoxSub:   subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "sing-box"),
		MonitorUI:    monitorUIStatus(domain, monitorPublicPort, monitorEnabled),
		TrafficQuota: trafficQuotaStatus(store),
	}
}

func readStatusState(store state.Store, name string) string {
	value, err := store.ReadString(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func osArchStatus() string {
	host, err := detectStatusHost()
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	osName := strings.TrimSpace(host.OS.ID)
	if osName == "" {
		osName = runtime.GOOS
	}
	if host.OS.VersionID != "" {
		osName += " " + host.OS.VersionID
	}
	arch := host.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	return osName + "/" + arch
}

func singBoxVersion(bin string) string {
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	out, err := statusCommandOutput(bin, "version")
	if err != nil {
		return "installed"
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "installed"
}

func serviceState(unit string) string {
	out, err := statusCommandOutput("systemctl", "is-active", unit)
	raw := strings.ToLower(strings.TrimSpace(out))
	if raw == "" {
		return "unknown"
	}
	switch raw {
	case "active", "reloading":
		return "running"
	case "inactive", "failed", "deactivating":
		return "not running"
	case "unknown":
		return "unknown"
	default:
		if err != nil {
			return "unknown"
		}
		return raw
	}
}

func singBoxServiceState(version string, store state.Store, layout paths.Layout, monitorEnabled bool) string {
	if version == "" {
		return "not installed"
	}
	state := serviceState(system.SingBoxService)
	if state == "not running" && monitorEnabled && isQuotaExceeded(store, layout) {
		return "stopped (quota exceeded)"
	}
	return state
}

func isQuotaExceeded(store state.Store, layout paths.Layout) bool {
	inRaw := readStatusState(store, "traffic_in_limit_bytes")
	outRaw := readStatusState(store, "traffic_out_limit_bytes")
	totalRaw := readStatusState(store, "traffic_total_limit_bytes")
	inLimit, _ := parseStatusLimit(inRaw)
	outLimit, _ := parseStatusLimit(outRaw)
	totalLimit, _ := parseStatusLimit(totalRaw)
	if inLimit == 0 && outLimit == 0 && totalLimit == 0 {
		return false
	}
	resetDay, _ := strconv.Atoi(readStatusState(store, "reset_day"))
	resetHour, _ := strconv.Atoi(readStatusState(store, "reset_hour"))
	if resetDay < 1 || resetDay > 28 {
		resetDay = deploy.DefaultResetDay
	}
	totals, err := monitor.CurrentTrafficTotals(layout, resetDay, resetHour, statusNow().UTC())
	if err != nil {
		return false
	}
	limits := monitor.TrafficLimits{InBytes: inLimit, OutBytes: outLimit, TotalBytes: totalLimit}
	return limits.Exceeded(totals)
}

func certificateState(layout paths.Layout, domain string) string {
	if domain == "" {
		return ""
	}
	certPEM, err := os.ReadFile(filepath.Join(layout.TLSDir, domain+".crt"))
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "invalid"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "invalid"
	}
	now := statusNow()
	switch {
	case now.Before(cert.NotBefore):
		return "not valid yet"
	case now.After(cert.NotAfter):
		return "expired"
	default:
		return "valid until " + cert.NotAfter.Format("2006-01-02")
	}
}

func subscriptionStatus(domain, port, token, kind string) string {
	if domain == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("https://%s:%s/s/%s/%s", domain, port, kind, token)
}

func monitorUIStatus(domain, port string, enabled bool) string {
	if !enabled || domain == "" || port == "" {
		return ""
	}
	return fmt.Sprintf("https://%s:%s/monitor/", domain, port)
}

func trafficQuotaStatus(store state.Store) string {
	monitorStateValue := readStatusState(store, "monitor")
	if monitorStateValue == "" {
		monitorStateValue = readStatusState(store, "traffic_monitor")
	}
	if monitorStateValue == "no" {
		return "disabled"
	}
	inRaw := readStatusState(store, "traffic_in_limit_bytes")
	outRaw := readStatusState(store, "traffic_out_limit_bytes")
	totalRaw := readStatusState(store, "traffic_total_limit_bytes")
	if inRaw == "" && outRaw == "" && totalRaw == "" {
		return ""
	}
	inLimit, err := parseStatusLimit(inRaw)
	if err != nil {
		return "unknown"
	}
	outLimit, err := parseStatusLimit(outRaw)
	if err != nil {
		return "unknown"
	}
	totalLimit, err := parseStatusLimit(totalRaw)
	if err != nil {
		return "unknown"
	}
	resetDay, _ := strconv.Atoi(readStatusState(store, "reset_day"))
	resetHour, _ := strconv.Atoi(readStatusState(store, "reset_hour"))
	if resetDay < 1 || resetDay > 28 {
		resetDay = deploy.DefaultResetDay
	}
	parts := []string{
		"in " + statusLimitLabel(inLimit),
		"out " + statusLimitLabel(outLimit),
		"total " + statusLimitLabel(totalLimit),
	}
	parts = append(parts, "next reset "+nextResetLabel(resetDay, resetHour))
	return strings.Join(parts, ", ")
}

func parseStatusLimit(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}

func statusLimitLabel(limit uint64) string {
	if limit == 0 {
		return "unlimited"
	}
	return "limit " + byteSize(limit)
}

func nextResetLabel(day, hour int) string {
	next := monitor.NextCycleReset(statusNow().UTC(), day, hour)
	return next.Format("2006-01-02 15:04") + " GMT"
}

func byteSize(n uint64) string {
	const (
		gib = 1 << 30
		mib = 1 << 20
	)
	if n%gib == 0 {
		return fmt.Sprintf("%d GB", n/gib)
	}
	if n%mib == 0 {
		return fmt.Sprintf("%d MB", n/mib)
	}
	return fmt.Sprintf("%d bytes", n)
}
