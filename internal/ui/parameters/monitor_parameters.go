package parameters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

func MonitorInstallFields(monitorDisabled func(map[string]string) bool) []Field {
	return []Field{
		{Key: "monitor", Label: "Deploy monitor", Def: "yes", Options: []string{"yes", "no"}, Note: "Choose no to skip the monitor service."},
		{Key: "monitor_alias", Label: "Monitor alias", Def: deploy.DefaultMonitorAlias, Note: "Shown as the local source name on /monitor.", Skip: monitorDisabled},
		{Key: "monitor_public_port", Label: "Monitor public HTTPS port", Def: strconv.Itoa(deploy.DefaultMonitorPublicPort), Note: "Nginx listens on this public HTTPS port for /monitor.", Skip: monitorDisabled},
		{Key: "monitor_port", Label: "Monitor local port", Def: strconv.Itoa(deploy.DefaultMonitorPort), Note: "The monitor listens on 127.0.0.1 and Nginx proxies /monitor to this port.", Skip: monitorDisabled},
		{Key: "monitor_interval_seconds", Label: "Traffic sampling interval seconds", Def: strconv.Itoa(deploy.DefaultMonitorIntervalSeconds), Note: "Default is 300 seconds. Lower values write more samples.", Skip: monitorDisabled},
		{Key: "traffic_in_limit_gb", Label: "Monthly inbound traffic limit in GB (0 = unlimited)", Def: "0", Note: "Inbound uses the monitored interface RX counter. When any configured limit is exceeded, sing-box.service is stopped automatically.", Skip: monitorDisabled},
		{Key: "traffic_out_limit_gb", Label: "Monthly outbound traffic limit in GB (0 = unlimited)", Def: "0", Note: "Outbound uses the monitored interface TX counter.", Skip: monitorDisabled},
		{Key: "traffic_total_limit_gb", Label: "Monthly total traffic limit in GB (0 = unlimited)", Def: "0", Note: "Total traffic is inbound + outbound.", Skip: monitorDisabled},
		{Key: "reset_day", Label: "Monthly reset day (1-28)", Def: strconv.Itoa(deploy.DefaultResetDay), Note: "Day of month when the traffic quota cycle resets.", Skip: monitorDisabled},
		{Key: "reset_hour", Label: "Monthly reset hour (0-23)", Def: strconv.Itoa(deploy.DefaultResetHour), Note: "Hour of day in GMT when the traffic quota cycle resets.", Skip: monitorDisabled},
	}
}

func MonitorLocalFields(cfg deploy.Config, monitorDisabled func(map[string]string) bool) []Field {
	return []Field{
		{Key: "monitor", Label: "Deploy monitor", Def: YesNoString(cfg.DeployMonitor), Options: []string{"yes", "no"}, Note: "Choose no to stop the monitor service."},
		{Key: "monitor_alias", Label: "Monitor alias", Def: StringDefault(cfg.MonitorAlias, deploy.DefaultMonitorAlias), Note: "Shown as local source name on /monitor.", Skip: monitorDisabled},
		{Key: "monitor_public_port", Label: "Monitor public HTTPS port", Def: strconv.Itoa(cfg.MonitorPublicPort), Skip: monitorDisabled},
		{Key: "monitor_port", Label: "Monitor local port", Def: strconv.Itoa(cfg.MonitorPort), Skip: monitorDisabled},
		{Key: "monitor_interface", Label: "Monitored network interface", Def: cfg.MonitorInterface, Note: "Leave as current/default interface unless you know the VPS egress interface changed.", Skip: monitorDisabled},
		{Key: "monitor_interval_seconds", Label: "Sampling interval seconds", Def: strconv.Itoa(DefaultMonitorInterval(cfg)), Skip: monitorDisabled},
		{Key: "traffic_in_limit", Label: "Inbound traffic limit", Def: FormatTrafficSizeInput(cfg.TrafficInLimitBytes), Note: TrafficSizeNote("0 means unlimited."), Skip: monitorDisabled},
		{Key: "traffic_out_limit", Label: "Outbound traffic limit", Def: FormatTrafficSizeInput(cfg.TrafficOutLimitBytes), Note: TrafficSizeNote("0 means unlimited."), Skip: monitorDisabled},
		{Key: "traffic_total_limit", Label: "Total traffic limit", Def: FormatTrafficSizeInput(cfg.TrafficTotalLimitBytes), Note: TrafficSizeNote("0 means unlimited."), Skip: monitorDisabled},
		{Key: "reset_day", Label: "Monthly reset day (1-28)", Def: strconv.Itoa(DefaultResetDay(cfg)), Note: "Day of month when the traffic quota cycle resets.", Skip: monitorDisabled},
		{Key: "reset_hour", Label: "Monthly reset hour GMT (0-23)", Def: strconv.Itoa(DefaultResetHour(cfg)), Note: "Hour of day in GMT when the traffic quota cycle resets.", Skip: monitorDisabled},
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
	case strings.HasPrefix(key, "traffic_") && strings.HasSuffix(key, "_limit_gb"):
		if _, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64); err != nil {
			return fmt.Errorf("traffic limit must be a non-negative integer")
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
