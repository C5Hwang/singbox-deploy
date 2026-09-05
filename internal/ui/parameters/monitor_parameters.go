package parameters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
)

// Labels and notes shared by every form that collects the same monitor
// parameter, so the same input always reads the same way whether it is being
// set during setup, edited on the hub, or edited on a spoke.
const (
	LabelMonitorEnabled   = "Enable monitor"
	LabelMonitorWebUI     = "Enable monitor web UI"
	LabelMonitorAlias     = "Monitor alias"
	LabelMonitorToken     = "Monitor access token"
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
	LabelPackageIn        = "Inbound traffic package"
	LabelPackageOut       = "Outbound traffic package"
	LabelPackageTotal     = "Total traffic package"

	// NoteDNSZone states the one precondition every certificate-bearing
	// domain shares, so setup, spoke creation, and the monitor domain all word
	// it the same way.
	NoteDNSZone = "Needs a DNS zone covering this name in Certificate management."

	// NoteMonitorEnabled* say what the monitor is for before asking whether to
	// have one, so the answer does not depend on already knowing.
	NoteMonitorEnabledInstall = noteMonitorPurpose + "\nChoose no to skip it."
	NoteMonitorEnabledEdit    = noteMonitorPurpose + "\nChoose no to stop it."
	noteMonitorPurpose        = "Records traffic, resource use and latency per server."

	NoteMonitorWebUI = "The dashboard you open in a browser.\n" +
		"Choose no to serve only its data API."
	NoteMonitorAlias      = "The name this server appears under on the dashboard."
	NoteSpokeMonitorAlias = "The name this node appears under on the dashboard.\n" +
		"Blank reuses the node name."

	// MonitorTokenNone is the word that clears the token. Blank already means
	// "keep the default", so turning the gate off needs a word of its own; the
	// minimum token length keeps it from colliding with a real token.
	MonitorTokenNone = "none"

	noteMonitorTokenShared = "The password for opening the dashboard.\n" +
		"At least " + minMonitorTokenLengthText + " characters, no spaces."
	NoteMonitorTokenInstall = noteMonitorTokenShared + "\nBlank generates one."
	NoteMonitorTokenEdit    = noteMonitorTokenShared +
		"\nBlank keeps the current one; enter " + MonitorTokenNone + " to remove it."
	NoteMonitorDomain    = "The address you open the dashboard at, and the one subscription links use.\n" + NoteDNSZone
	NoteMonitorPublic    = "The HTTPS port the dashboard is served on."
	NoteMonitorPort      = "Internal only. Change it if another program uses this port."
	NoteMonitorInterface = "The network card whose traffic is counted.\n" +
		"auto picks the default one."
	NoteMonitorInterval = "How often usage is measured.\n" +
		"Smaller means finer charts and more stored data."
	NoteResetDay  = "The day of the month the traffic count starts over."
	NoteResetHour = "The hour on that day, in GMT."
)

// The quota consequence is stated once, on the first limit, rather than
// repeated on all three.
var (
	NoteTrafficIn = TrafficSizeNote("How much this server may download per cycle.\n" +
		"0 means no limit. Going over any limit stops the proxy.")
	NoteTrafficOut   = TrafficSizeNote("How much this server may upload per cycle.\n0 means no limit.")
	NoteTrafficTotal = TrafficSizeNote("Download and upload together.\n0 means no limit.")

	// A package is explained once too: what it is on the first field, and
	// that it is temporary, which is the whole point of it.
	NotePackageIn = TrafficSizeNote("Extra download allowance granted for this cycle, on top of the limit.\n" +
		"It lapses at the next reset. Only a limited direction can take one.")
	NotePackageOut   = TrafficSizeNote("Extra upload allowance granted for this cycle.")
	NotePackageTotal = TrafficSizeNote("Extra download-and-upload allowance granted for this cycle.")

	NotePackageGrantIn = TrafficSizeNote("Extra download allowance to add for this cycle, on top of the limit.\n" +
		"0 adds nothing. It lapses at the next reset. Only a limited direction can take one.")
	NotePackageGrantOut   = TrafficSizeNote("Extra upload allowance to add for this cycle.\n0 adds nothing.")
	NotePackageGrantTotal = TrafficSizeNote("Extra download-and-upload allowance to add for this cycle.\n0 adds nothing.")
)

// Keys of the fields that carry a traffic package, so the screens that read
// them and the validation that guards them name them once.
const (
	KeyPackageIn    = "package_in_traffic"
	KeyPackageOut   = "package_out_traffic"
	KeyPackageTotal = "package_total_traffic"

	KeyPackageGrantIn    = "package_in_grant"
	KeyPackageGrantOut   = "package_out_grant"
	KeyPackageGrantTotal = "package_total_grant"
)

// installDomainDefault prefills a setup field with the install domain already
// entered in the same form.
func installDomainDefault(vals map[string]string) string {
	return strings.TrimSpace(vals["domain"])
}

func MonitorInstallFields(monitorDisabled func(map[string]string) bool) []Field {
	return []Field{
		{Key: "monitor", Label: LabelMonitorEnabled, Def: "yes", Options: []string{"yes", "no"}, Note: NoteMonitorEnabledInstall},
		{Key: "monitor_frontend", Label: LabelMonitorWebUI, Def: "yes", Options: []string{"yes", "no"}, Note: NoteMonitorWebUI, Skip: monitorDisabled},
		{Key: "monitor_alias", Label: LabelMonitorAlias, Def: deploy.DefaultMonitorAlias, Note: NoteMonitorAlias, Skip: monitorDisabled},
		{Key: "monitor_token", Label: LabelMonitorToken, Note: NoteMonitorTokenInstall, Secret: true, Skip: monitorDisabled},
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
		{Key: "monitor", Label: LabelMonitorEnabled, Def: YesNoString(cfg.DeployMonitor), Options: []string{"yes", "no"}, Note: NoteMonitorEnabledEdit},
		{Key: "monitor_frontend", Label: LabelMonitorWebUI, Def: YesNoString(cfg.DeployMonitorFrontend), Options: []string{"yes", "no"}, Note: NoteMonitorWebUI, Skip: monitorDisabled},
		{Key: "monitor_alias", Label: LabelMonitorAlias, Def: StringDefault(cfg.MonitorAlias, deploy.DefaultMonitorAlias), Note: NoteMonitorAlias, Skip: monitorDisabled},
		{Key: "monitor_token", Label: LabelMonitorToken, Def: cfg.MonitorToken, Note: NoteMonitorTokenEdit, Secret: true, Skip: monitorDisabled},
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

// MonitorUsageFields is the form that rewrites one node's current cycle: what
// it has used, and what it has been granted on top of its limits. The package
// sits in the same form because it is a figure of the same cycle, corrected
// the same way.
func MonitorUsageFields(totals monitor.TrafficTotals, pkg monitor.TrafficPackage) []Field {
	return []Field{
		{Key: "current_in_traffic", Label: "Current inbound used", Def: FormatTrafficSizeInput(totals.InBytes), Note: TrafficSizeNote("Download already counted this cycle.")},
		{Key: "current_out_traffic", Label: "Current outbound used", Def: FormatTrafficSizeInput(totals.OutBytes), Note: TrafficSizeNote("Upload already counted this cycle.")},
		{Key: KeyPackageIn, Label: LabelPackageIn, Def: FormatTrafficSizeInput(pkg.InBytes), Note: NotePackageIn},
		{Key: KeyPackageOut, Label: LabelPackageOut, Def: FormatTrafficSizeInput(pkg.OutBytes), Note: NotePackageOut},
		{Key: KeyPackageTotal, Label: LabelPackageTotal, Def: FormatTrafficSizeInput(pkg.TotalBytes), Note: NotePackageTotal},
	}
}

// MonitorPackageGrantFields is the form that adds a package to the current
// cycle. Every field starts at 0, so a direction left alone gets nothing.
func MonitorPackageGrantFields() []Field {
	return []Field{
		{Key: KeyPackageGrantIn, Label: LabelPackageIn, Def: "0", Note: NotePackageGrantIn},
		{Key: KeyPackageGrantOut, Label: LabelPackageOut, Def: "0", Note: NotePackageGrantOut},
		{Key: KeyPackageGrantTotal, Label: LabelPackageTotal, Def: "0", Note: NotePackageGrantTotal},
	}
}

// PackageFromValues reads the three package fields written under the given
// keys back into a package. A value that fails to parse reads as 0; the form
// validated it before it was stored.
func PackageFromValues(vals map[string]string, inKey, outKey, totalKey string) monitor.TrafficPackage {
	in, _ := ParseTrafficSize(vals[inKey])
	out, _ := ParseTrafficSize(vals[outKey])
	total, _ := ParseTrafficSize(vals[totalKey])
	return monitor.TrafficPackage{InBytes: in, OutBytes: out, TotalBytes: total}
}

func ValidateMonitorParameterValue(key, val string) error {
	switch {
	case key == "monitor_alias":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("monitor alias is required")
		}
	case key == "monitor_token":
		return ValidateMonitorToken(val)
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
	case key == "traffic_in_limit" || key == "traffic_out_limit" || key == "traffic_total_limit" ||
		key == "current_in_traffic" || key == "current_out_traffic" ||
		key == KeyPackageIn || key == KeyPackageOut || key == KeyPackageTotal ||
		key == KeyPackageGrantIn || key == KeyPackageGrantOut || key == KeyPackageGrantTotal:
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

const (
	minMonitorTokenLength     = 8
	minMonitorTokenLengthText = "8"
	maxMonitorTokenLength     = 128
)

// ValidateMonitorToken accepts a blank answer (the field default applies), the
// clearing sentinel, or a token of printable non-space ASCII. The token travels
// in an HTTP header, so anything outside that range cannot be sent reliably.
func ValidateMonitorToken(val string) error {
	token := strings.TrimSpace(val)
	if token == "" || token == MonitorTokenNone {
		return nil
	}
	if len(token) < minMonitorTokenLength || len(token) > maxMonitorTokenLength {
		return fmt.Errorf("monitor access token must be %d-%d characters", minMonitorTokenLength, maxMonitorTokenLength)
	}
	for _, r := range token {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("monitor access token must use printable ASCII without spaces")
		}
	}
	return nil
}

// MonitorTokenValue resolves a submitted token to the value to store. The
// clearing sentinel becomes an empty token, which publishes the dashboard
// without a gate.
func MonitorTokenValue(val string) string {
	token := strings.TrimSpace(val)
	if token == MonitorTokenNone {
		return ""
	}
	return token
}

// MonitorTokenSummary renders a token for a summary screen without reprinting
// the secret. Status is the one screen that shows the token itself.
func MonitorTokenSummary(val string) string {
	if MonitorTokenValue(val) == "" {
		return "none"
	}
	return "set"
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

// TrafficSizeNote states what a size means first and how to type it second, so
// the reason for the field is not buried behind its input format.
func TrafficSizeNote(meaning string) string {
	return meaning + "\nAccepts bytes or a size like 500MB or 1.5GB."
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
