package ui

// Step is one labeled stage of the install flow, shown with a step counter and
// a short summary of the action it performs.
type Step struct {
	Label   string
	Summary string
}

// InstallSteps returns the ordered install-flow steps. The slice length is the
// total used for the "N/M" progress counter.
func InstallSteps() []Step {
	return []Step{
		{Label: "System check", Summary: "root, OS, arch, package/service/firewall managers"},
		{Label: "Conflict check", Summary: "detect existing sing-box service or binary"},
		{Label: "Domain & DNS", Summary: "validate domain resolves to this host"},
		{Label: "Port check", Summary: "ensure required ports are free"},
		{Label: "Dependencies", Summary: "install base packages"},
		{Label: "Nginx", Summary: "install nginx.org mainline and managed config"},
		{Label: "Firewall", Summary: "open required TCP/UDP ports"},
		{Label: "Certificates", Summary: "obtain Let's Encrypt certificate via ACME"},
		{Label: "sing-box core", Summary: "download and install latest stable release"},
		{Label: "Config", Summary: "generate fragments and final config.json"},
		{Label: "Services", Summary: "install and start sing-box.service"},
		{Label: "Subscriptions", Summary: "generate subscription files"},
		{Label: "Monitor", Summary: "install and start traffic monitor service"},
		{Label: "Reload Nginx", Summary: "validate and reload nginx"},
		{Label: "Summary", Summary: "show account, subscriptions, traffic URL"},
	}
}
