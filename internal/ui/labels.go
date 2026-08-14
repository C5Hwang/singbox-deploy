package ui

import (
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// noteDNSZone is defined next to the monitor domain field that also
// states it, so the precondition is worded once for every domain the hub
// issues a certificate for.
const noteDNSZone = uiparams.NoteDNSZone

// withCoveredZones names the zones certificate management can issue under today
// on every field that states the DNS-01 precondition. A domain is typed here but
// authorized there, so without this the operator only learns which names are
// acceptable by having one rejected. The list is resolved once per form rather
// than per keystroke, which is also when it is newest.
func withCoveredZones(layout paths.Layout, fields []field) []field {
	note := coveredZonesNote(layout)
	for i := range fields {
		if strings.Contains(fields[i].note, noteDNSZone) {
			fields[i].note += "\n" + note
		}
	}
	return fields
}

func coveredZonesNote(layout paths.Layout) string {
	zones, err := certmgr.LoadCredentials(layout)
	if err != nil || len(zones) == 0 {
		return "No DNS zones yet, so any name sends you there first."
	}
	names := make([]string, 0, len(zones))
	for _, zone := range zones {
		names = append(names, zone.Domain)
	}
	return "Covered: " + strings.Join(names, ", ") + " (and anything under them)."
}

// Labels and notes shared by more than one screen. Anything collected or
// reported in two places lives here so the same input always reads the same
// way — during setup, when editing the hub, and when editing a spoke.
//
// Monitor, subscription, and protocol parameters are defined alongside their
// fields in internal/ui/parameters instead.
const (
	labelSpokeSubscriptionAlias = "Spoke subscription alias"
	labelSpokeMonitorEnabled    = "Enable monitor on spoke"
	labelSpokeMonitorAlias      = "Spoke monitor alias"

	// noteSpokeTransport replaces the four differently-worded explanations the
	// spoke pickers used to carry.
	noteSpokeTransport         = "The other servers this hub manages, reached over WireGuard."
	noteSpokeSubscriptionAlias = "The name this node shows under in client apps.\n" +
		"Blank reuses the node name."

	// Screen titles, matched to the menu item that opens each screen.
	titleSetup         = "Setup"
	titleProtocols     = "Protocol settings"
	titleSubscriptions = "Subscription settings"
	titleMonitoring    = "Monitoring"
	titleCore          = "sing-box core"
)
