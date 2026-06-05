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

	"github.com/C5Hwang/singbox-deploy/internal/install"
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
		subscribePort = strconv.Itoa(install.DefaultSubscribePort)
	}
	trafficPort := readStatusState(store, "traffic_port")
	if trafficPort == "" {
		trafficPort = subscribePort
	}
	monitorEnabled := readStatusState(store, "traffic_monitor") != "no"
	monitorState := "disabled"
	if monitorEnabled {
		monitorState = serviceState(system.MonitorService)
	}

	return Status{
		Domain:       domain,
		PublicIP:     readStatusState(store, "public_ip"),
		OSArch:       osArchStatus(),
		SingBoxVer:   singBoxVersion(layout.SingBoxBin),
		SingBoxState: serviceState(system.SingBoxService),
		NginxState:   serviceState("nginx.service"),
		MonitorState: monitorState,
		CertState:    certificateState(layout, domain),
		Protocols:    readStatusState(store, "enabled_protocols"),
		Subscription: subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "default"),
		ClashMetaSub: subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "clashMetaProfiles"),
		SingBoxSub:   subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token"), "sing-box"),
		TrafficUI:    trafficUIStatus(domain, trafficPort, monitorEnabled),
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

func trafficUIStatus(domain, port string, enabled bool) string {
	if !enabled || domain == "" || port == "" {
		return ""
	}
	return fmt.Sprintf("https://%s:%s/traffic/", domain, port)
}

func trafficQuotaStatus(store state.Store) string {
	if readStatusState(store, "traffic_monitor") == "no" {
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
	resetDay := readStatusState(store, "reset_day")
	parts := []string{
		"in " + statusLimitLabel(inLimit),
		"out " + statusLimitLabel(outLimit),
		"total " + statusLimitLabel(totalLimit),
	}
	if resetDay != "" {
		parts = append(parts, "reset day "+resetDay)
	}
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
