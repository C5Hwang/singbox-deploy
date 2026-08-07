package ui

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

	noteDNSCredential = "Needs a matching DNS credential in Certificate management."
	// noteSpokeTransport replaces the four differently-worded explanations the
	// spoke pickers used to carry.
	noteSpokeTransport         = "The hub reaches every spoke over WireGuard."
	noteSpokeSubscriptionAlias = "Names this spoke's nodes in client apps. Blank uses the node alias."

	// Screen titles, matched to the menu item that opens each screen.
	titleSetup         = "Setup"
	titleProtocols     = "Protocol settings"
	titleSubscriptions = "Subscription settings"
	titleMonitoring    = "Monitoring"
	titleCore          = "sing-box core"
)
