package parameters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// Labels and notes shared by every form that collects the same monitor
// parameter, so the same input always reads the same way whether it is being
// set during setup, edited on the hub, or edited on a spoke.
const (
	LabelMonitorEnabled   = "Enable monitor"
	LabelMonitorWebUI     = "Enable monitor web UI"
	LabelMonitorAlias     = "Monitor alias"
	LabelMonitorDomain    = "Monitor domain"
	LabelMonitorPublic    = "Monitor public HTTPS port"
	LabelMonitorPort      = "Monitor local port"
	LabelMonitorInterface = "Monitored network interface"
	LabelMonitorInterval  = "Sampling interval (seconds)"
	LabelTrafficIn        = "Inbound traffic limit"
	LabelTrafficOut       = "Outbound traffic limit"
	LabelTrafficTotal     = "Total traffic limit"
	LabelResetDay         = "Monthly reset day (1-28)"
	LabelResetHour        = "Monthly reset hour GMT (0-23)"

	// NoteDNSCredential states the one precondition every certificate-bearing
	// domain shares, so setup, spoke creation, and the monitor domain all word
	// it the same way.
	NoteDNSCredential = "Needs a matching DNS credential in Certificate management."

	NoteMonitorWebUI      = "Choose no to serve the API only."
	NoteMonitorAlias      = "Names the hub on the monitor dashboard."
	NoteSpokeMonitorAlias = "Blank uses the node alias."
	NoteMonitorDomain     = "Serves the monitor under its own name, so it is not reachable through the masquerade site's domain. " +
		NoteDNSCredential + " It is not required to resolve to this server."
	NoteMonitorPublic    = "Nginx listens on this public HTTPS port for /monitor."
	NoteMonitorPort      = "The monitor listens on 127.0.0.1 and Nginx proxies /monitor to this port."
	NoteMonitorInterface = "Use auto to detect the default egress interface."
	NoteMonitorInterval  = "Lower values write more samples."
	NoteResetDay         = "Day of month when the traffic quota cycle resets."
	NoteResetHour        = "Hour of day in GMT when the traffic quota cycle resets."
)

// The quota consequence is stated once, on the first limit, rather than
// repeated on all three.
var (
	NoteTrafficIn    = TrafficSizeNote("0 means unlimited. Exceeding any limit stops sing-box.")
	NoteTrafficOut   = TrafficSizeNote("0 means unlimited.")
	NoteTrafficTotal = TrafficSizeNote("0 means unlimited.")
)

// installDomainDefault prefills a setup field with the install domain already
// entered in the same form.
func installDomainDefault(vals map[string]string) string {
	return strings.TrimSpace(vals["domain"])
}

func MonitorInstallFields(monitorDisabled func(map[string]string) bool) []Field {
	return []Field{
		{Key: "monitor", Label: LabelMonitorEnabled, Def: "yes", Options: []string{"yes", "no"}, Note: "Choose no to skip the monitor service."},
		{Key: "monitor_frontend", Label: LabelMonitorWebUI, Def: "yes", Options: []string{"yes", "no"}, Note: NoteMonitorWebUI, Skip: monitorDisabled},
		{Key: "monitor_alias", Label: LabelMonitorAlias, Def: deploy.DefaultMonitorAlias, Note: NoteMonitorAlias, Skip: monitorDisabled},
		{Key: "monitor_domain", Label: LabelMonitorDomain, DefFunc: installDomainDefault, Note: NoteMonitorDomain, Skip: monitorDisabled},
		{Key: "monitor_public_port", Label: LabelMonitorPublic, Def: strconv.Itoa(deploy.DefaultMonitorPublicPort), Note: NoteMonitorPublic, Skip: monitorDisabled},
		{Key: "monitor_port", Label: LabelMonitorPort, Def: strconv.Itoa(deploy.DefaultMonitorPort), Note: NoteMonitorPort, Skip: monitorDisabled},
		{Key: "monitor_interval_seconds", Label: LabelMonitorInterval, Def: strconv.Itoa(deploy.DefaultMonitorIntervalSeconds), Note: NoteMonitorInterval, Skip: monitorDisabled},
		{Key: "traffic_in_limit", Label: LabelTrafficIn, Def: "0", Note: NoteTrafficIn, Skip: monitorDisabled},
		{Key: "traffic_out_limit", Label: LabelTrafficOut, Def: "0", Note: NoteTrafficOut, Skip: monitorDisabled},
		{Key: "traffic_total_limit", Label: LabelTrafficTotal, Def: "0", Note: NoteTrafficTotal, Skip: monitorDisabled},
		{Key: "reset_day", Label: LabelResetDay, Def: strconv.Itoa(deploy.DefaultResetDay), Note: NoteResetDay, Skip: monitorDisabled},
		{Key: "reset_hour", Label: LabelResetHour, Def: strconv.Itoa(deploy.DefaultResetHour), Note: NoteResetHour, Skip: monitorDisabled},
	}
}

func MonitorLocalFields(cfg deploy.Config, monitorDisabled func(map[string]string) bool) []Field {
	return []Field{
		{Key: "monitor", Label: LabelMonitorEnabled, Def: YesNoString(cfg.DeployMonitor), Options: []string{"yes", "no"}, Note: "Choose no to stop the monitor service."},
		{Key: "monitor_frontend", Label: LabelMonitorWebUI, Def: YesNoString(cfg.DeployMonitorFrontend), Options: []string{"yes", "no"}, Note: NoteMonitorWebUI, Skip: monitorDisabled},
		{Key: "monitor_alias", Label: LabelMonitorAlias, Def: StringDefault(cfg.MonitorAlias, deploy.DefaultMonitorAlias), Note: NoteMonitorAlias, Skip: monitorDisabled},
		{Key: "monitor_domain", Label: LabelMonitorDomain, Def: cfg.MonitorHost(), Note: NoteMonitorDomain, Skip: monitorDisabled},
		{Key: "monitor_public_port", Label: LabelMonitorPublic, Def: strconv.Itoa(cfg.MonitorPublicPort), Note: NoteMonitorPublic, Skip: monitorDisabled},
		{Key: "monitor_port", Label: LabelMonitorPort, Def: strconv.Itoa(cfg.MonitorPort), Note: NoteMonitorPort, Skip: monitorDisabled},
		{Key: "monitor_interface", Label: LabelMonitorInterface, Def: cfg.MonitorInterface, Note: NoteMonitorInterface, Skip: monitorDisabled},
		{Key: "monitor_interval_seconds", Label: LabelMonitorInterval, Def: strconv.Itoa(DefaultMonitorInterval(cfg)), Note: NoteMonitorInterval, Skip: monitorDisabled},
		{Key: "traffic_in_limit", Label: LabelTrafficIn, Def: FormatTrafficSizeInput(cfg.TrafficInLimitBytes), Note: NoteTrafficIn, Skip: monitorDisabled},
		{Key: "traffic_out_limit", Label: LabelTrafficOut, Def: FormatTrafficSizeInput(cfg.TrafficOutLimitBytes), Note: NoteTrafficOut, Skip: monitorDisabled},
		{Key: "traffic_total_limit", Label: LabelTrafficTotal, Def: FormatTrafficSizeInput(cfg.TrafficTotalLimitBytes), Note: NoteTrafficTotal, Skip: monitorDisabled},
		{Key: "reset_day", Label: LabelResetDay, Def: strconv.Itoa(DefaultResetDay(cfg)), Note: NoteResetDay, Skip: monitorDisabled},
		{Key: "reset_hour", Label: LabelResetHour, Def: strconv.Itoa(DefaultResetHour(cfg)), Note: NoteResetHour, Skip: monitorDisabled},
	}
}

func MonitorUsageFields(inBytes, outBytes uint64) []Field {
	return []Field{
		{Key: "current_in_traffic", Label: "Current inbound used", Def: FormatTrafficSizeInput(inBytes), Note: TrafficSizeNote("Sets the current quota-cycle inbound total.")},
		{Key: "current_out_traffic", Label: "Current outbound used", Def: FormatTrafficSizeInput(outBytes), Note: TrafficSizeNote("Sets the current quota-cycle outbound total.")},
	}
}

func ValidateMonitorParameterValue(key, val string) error {
	switch {
	case key == "monitor_alias":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("monitor alias is required")
		}
	case key == "monitor_domain":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("monitor domain is required")
		}
	case key == "monitor_public_port" || key == "monitor_port" || strings.HasPrefix(key, "remote_monitor_public_port_"):
		return ValidateRequiredPort(val)
	case key == "monitor_interface":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("network interface is required")
		}
	case key == "monitor_interval_seconds":
		seconds, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || seconds < 10 {
			return fmt.Errorf("sampling interval must be at least 10 seconds")
		}
	case key == "traffic_in_limit" || key == "traffic_out_limit" || key == "traffic_total_limit" || key == "current_in_traffic" || key == "current_out_traffic":
		_, err := ParseTrafficSize(val)
		return err
	case key == "reset_day":
		day, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || day < 1 || day > 28 {
			return fmt.Errorf("reset day must be between 1 and 28")
		}
	case key == "reset_hour":
		hour, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || hour < 0 || hour > 23 {
			return fmt.Errorf("reset hour must be between 0 and 23")
		}
	}
	return nil
}

func ValidateRequiredPort(val string) error {
	port, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func ParseTrafficSize(value string) (uint64, error) {
	raw := strings.TrimSpace(strings.ToUpper(value))
	if raw == "" {
		return 0, nil
	}
	multiplier := float64(1)
	for _, unit := range []struct {
		suffix string
		mul    float64
	}{
		{"TB", 1 << 40}, {"T", 1 << 40},
		{"GB", 1 << 30}, {"G", 1 << 30},
		{"MB", 1 << 20}, {"M", 1 << 20},
		{"KB", 1 << 10}, {"K", 1 << 10},
		{"B", 1},
	} {
		if strings.HasSuffix(raw, unit.suffix) {
			multiplier = unit.mul
			raw = strings.TrimSpace(strings.TrimSuffix(raw, unit.suffix))
			break
		}
	}
	valueFloat, err := strconv.ParseFloat(raw, 64)
	if err != nil || valueFloat < 0 {
		return 0, fmt.Errorf("traffic size must be a non-negative number")
	}
	bytes := valueFloat * multiplier
	if bytes > float64(^uint64(0)) {
		return 0, fmt.Errorf("traffic size is too large")
	}
	return uint64(bytes), nil
}

func FormatTrafficSizeInput(value uint64) string {
	if value == 0 {
		return "0"
	}
	const (
		gib = uint64(1 << 30)
		mib = uint64(1 << 20)
		kib = uint64(1 << 10)
	)
	switch {
	case value%gib == 0:
		return fmt.Sprintf("%dGB", value/gib)
	case value%mib == 0:
		return fmt.Sprintf("%dMB", value/mib)
	case value%kib == 0:
		return fmt.Sprintf("%dKB", value/kib)
	default:
		return strconv.FormatUint(value, 10)
	}
}

func TrafficSizeNote(suffix string) string {
	return "Accepts bytes or B/KB/MB/GB/TB suffixes, for example 500MB or 1.5GB. " + suffix
}

func DefaultMonitorInterval(cfg deploy.Config) int {
	if cfg.MonitorIntervalSeconds > 0 {
		return cfg.MonitorIntervalSeconds
	}
	return deploy.DefaultMonitorIntervalSeconds
}

func DefaultResetDay(cfg deploy.Config) int {
	if cfg.ResetDay > 0 {
		return cfg.ResetDay
	}
	return deploy.DefaultResetDay
}

func DefaultResetHour(cfg deploy.Config) int {
	if cfg.ResetHour >= 0 && cfg.ResetHour <= 23 {
		return cfg.ResetHour
	}
	return deploy.DefaultResetHour
}

func StringDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func YesNoString(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
