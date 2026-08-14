package ui

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

var toolVersion = "dev"

// SetVersion records the build-time version string for display in the UI.
func SetVersion(v string) { toolVersion = v }

const statusPublicIPLookupTimeout = 2 * time.Second

var (
	defaultStatusLayout = paths.DefaultLayout
	detectStatusHost    = system.DetectHost
	statusNow           = time.Now
	loadStatusGroups    = subgroups.Load
	loadStatusNodes     = nodes.Load
	resolveStatusIPs    = func(ctx context.Context, domain string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", domain)
	}
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
	// An install made before the monitor got its own name served it under the
	// install domain, so that is what a missing key means. Both names are read
	// back in the form Nginx serves them under, so the URL reported here is one
	// that actually selects the monitor's server block.
	monitorDomain := deploy.ServerName(readStatusState(store, "monitor_domain"))
	if monitorDomain == "" {
		monitorDomain = deploy.ServerName(domain)
	}
	monitorEnabled := readMonitorState(store) != "no"
	monitorState := "disabled"
	if monitorEnabled {
		monitorState = serviceState(system.MonitorService)
	}
	// The monitor's own certificate is reported only when it is a second pair;
	// sharing the install domain — however either was spelled — means the
	// Certificate row already covers it.
	monitorCertState := ""
	if monitorEnabled && monitorDomain != deploy.ServerName(domain) {
		monitorCertState = certificateState(layout, monitorDomain)
	}
	// The subscription is published under the monitor's name whenever there is a
	// monitor, matching deploy.Config.SubscriptionHost; without one it stays on
	// the install domain.
	subscriptionDomain := deploy.ServerName(domain)
	if monitorEnabled {
		subscriptionDomain = monitorDomain
	}

	singBoxVer := singBoxVersion(layout.SingBoxBin)
	singBoxState := singBoxServiceState(singBoxVer, store, layout, monitorEnabled)

	return Status{
		ToolVersion:      toolVersion,
		Domain:           domain,
		PublicIP:         loadStatusPublicIP(store, domain),
		OSArch:           osArchStatus(),
		SingBoxVer:       singBoxVer,
		SingBoxState:     singBoxState,
		NginxState:       serviceState("nginx.service"),
		MonitorState:     monitorState,
		CertState:        certificateState(layout, domain),
		MonitorCertState: monitorCertState,
		Protocols:        protocolStrings(protocolsFromValue(readStatusState(store, "enabled_protocols"))),
		MonitorUI:        monitorUIStatus(monitorDomain, monitorPublicPort, monitorEnabled),
		MonitorToken:     monitorTokenStatus(store, monitorEnabled),
		TrafficQuota:     trafficQuotaStatus(store),
		Groups: subscriptionGroupStatuses(layout, subscriptionDomain, subscribePort,
			readStatusState(store, "display_name")),
	}
}

// subscriptionGroupStatuses renders one status entry per published
// subscription group. Groups own every subscription URL the hub serves, so an
// installation with no group yet reports none rather than falling back to a
// salt that nothing publishes.
func subscriptionGroupStatuses(layout paths.Layout, domain, port, displayName string) []SubscriptionGroupStatus {
	groups, err := loadStatusGroups(layout)
	if err != nil || len(groups) == 0 {
		return nil
	}
	list, err := loadStatusNodes(layout)
	if err != nil {
		list = nil
	}
	out := make([]SubscriptionGroupStatus, 0, len(groups))
	for _, g := range groups {
		token := deploy.SubscriptionToken(g.Salt)
		members := groupMemberNames(g, displayName, list)
		status := SubscriptionGroupStatus{
			Alias:       g.EffectiveAlias(),
			Salt:        g.Salt,
			Members:     strings.Join(members, ", "),
			MemberCount: len(members),
			Published:   groupPublishes(g, list),
		}
		// A group with no nodes left is not served at all, so it reports no URLs
		// rather than four that answer 404.
		if status.Published {
			status.Subscription = subscriptionStatus(domain, port, token, "default")
			status.ClashMetaSub = subscriptionStatus(domain, port, token, "clashMetaProfiles")
			status.SingBoxSub = subscriptionStatus(domain, port, token, "singboxProfiles")
			status.SurgeSub = subscriptionStatus(domain, port, token, "surgeProfiles")
		}
		out = append(out, status)
	}
	return out
}

// loadStatusPublicIP is normally a state-only read. New installations persist
// the address captured by domain validation. For an older installation, resolve
// its already-configured domain once with a short deadline and cache the result;
// this avoids putting the status page on the much slower external-IP probe path.
func loadStatusPublicIP(store state.Store, domain string) string {
	if publicIP := readStatusState(store, "public_ip"); publicIP != "" {
		return publicIP
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if literal := net.ParseIP(strings.Trim(domain, "[]")); isPublicStatusIP(literal) {
		publicIP := literal.String()
		_ = store.WriteString("public_ip", publicIP+"\n", 0o600)
		return publicIP
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusPublicIPLookupTimeout)
	defer cancel()
	ips, err := resolveStatusIPs(ctx, domain)
	if err != nil {
		return ""
	}
	ip := preferredPublicStatusIP(ips)
	if ip == nil {
		return ""
	}
	publicIP := ip.String()
	// Status display must remain useful even on a read-only or damaged state
	// directory, so a cache write failure does not discard the resolved value.
	_ = store.WriteString("public_ip", publicIP+"\n", 0o600)
	return publicIP
}

func preferredPublicStatusIP(ips []net.IP) net.IP {
	var ipv6 net.IP
	for _, ip := range ips {
		if !isPublicStatusIP(ip) {
			continue
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4
		}
		if ipv6 == nil {
			ipv6 = ip
		}
	}
	return ipv6
}

func isPublicStatusIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func readStatusState(store state.Store, name string) string {
	value, err := store.ReadString(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// readMonitorState reads the monitor toggle, falling back to the legacy
// traffic_monitor key written by older installs.
func readMonitorState(store state.Store) string {
	value := readStatusState(store, "monitor")
	if value == "" {
		value = readStatusState(store, "traffic_monitor")
	}
	return value
}

// readResetSchedule reads the quota reset day/hour, clamping the day to the
// supported 1..28 window.
func readResetSchedule(store state.Store) (day, hour int) {
	day, _ = strconv.Atoi(readStatusState(store, "reset_day"))
	hour, _ = strconv.Atoi(readStatusState(store, "reset_hour"))
	if day < 1 || day > 28 {
		day = deploy.DefaultResetDay
	}
	return day, hour
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
	resetDay, resetHour := readResetSchedule(store)
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

// monitorTokenStatus returns the token the dashboard asks for. It is printed
// in full because there is nowhere else to read it back from; installs made
// before the token existed have none and report so.
func monitorTokenStatus(store state.Store, enabled bool) string {
	if !enabled {
		return ""
	}
	return readStatusState(store, monitor.AccessTokenFile)
}

func trafficQuotaStatus(store state.Store) string {
	if readMonitorState(store) == "no" {
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
	resetDay, resetHour := readResetSchedule(store)
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
