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

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type statusLevel int

const (
	statusLevelUnknown statusLevel = iota
	statusLevelRunning
	statusLevelStopped
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
		subscribePort = "2096"
	}

	return Status{
		Domain:       domain,
		PublicIP:     readStatusState(store, "public_ip"),
		OSArch:       osArchStatus(),
		SingBoxVer:   singBoxVersion(layout.SingBoxBin),
		SingBoxState: serviceState(system.SingBoxService),
		NginxState:   serviceState("nginx.service"),
		MonitorState: serviceState(system.MonitorService),
		CertState:    certificateState(layout, domain),
		Protocols:    readStatusState(store, "enabled_protocols"),
		Subscription: subscriptionStatus(domain, subscribePort, readStatusState(store, "subscribe_token")),
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

func subscriptionStatus(domain, port, token string) string {
	if domain == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("https://%s:%s/s/default/%s", domain, port, token)
}

func trafficQuotaStatus(store state.Store) string {
	raw := readStatusState(store, "traffic_limit_bytes")
	if raw == "" {
		return ""
	}
	limit, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return "unknown"
	}
	if limit == 0 {
		return "unlimited"
	}
	resetDay := readStatusState(store, "reset_day")
	if resetDay == "" {
		return "limit " + byteSize(limit)
	}
	return fmt.Sprintf("limit %s, reset day %s", byteSize(limit), resetDay)
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

func runningStatusLevel(value string) statusLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running", "active", "ok", "healthy":
		return statusLevelRunning
	case "not running", "inactive", "failed", "stopped", "dead":
		return statusLevelStopped
	default:
		return statusLevelUnknown
	}
}
