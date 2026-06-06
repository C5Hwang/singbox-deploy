// Package paths defines the on-disk filesystem layout used by singbox-deploy.
package paths

// Layout enumerates every well-known path the program reads from or writes to.
// Storing them in one struct keeps the layout reviewable and lets tests
// substitute a temporary root.
type Layout struct {
	Root         string
	StateDir     string
	SingBoxBin   string
	FragmentsDir string
	ConfigJSON   string
	SubscribeDir string
	WebRoot      string
	MonitorDB    string
	TLSDir       string
}

// DefaultLayout returns the production layout rooted at /etc/singbox-deploy.
func DefaultLayout() Layout {
	return LayoutForRoot("/etc/singbox-deploy")
}

// LayoutForRoot derives a layout from an arbitrary root directory. Tests use
// this with a temporary directory; production uses DefaultLayout.
func LayoutForRoot(root string) Layout {
	return Layout{
		Root:         root,
		StateDir:     root + "/state",
		SingBoxBin:   root + "/sing-box/sing-box",
		FragmentsDir: root + "/sing-box/conf/fragments",
		ConfigJSON:   root + "/sing-box/conf/config.json",
		SubscribeDir: root + "/subscribe",
		WebRoot:      root + "/www",
		MonitorDB:    root + "/monitor/monitor.db",
		TLSDir:       root + "/tls",
	}
}
