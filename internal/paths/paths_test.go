package paths

import "testing"

func TestDefaultLayout(t *testing.T) {
	l := DefaultLayout()
	checks := map[string]string{
		"root":       l.Root,
		"state":      l.StateDir,
		"singboxBin": l.SingBoxBin,
		"fragments":  l.FragmentsDir,
		"config":     l.ConfigJSON,
		"subscribe":  l.SubscribeDir,
		"www":        l.WebRoot,
		"trafficDB":  l.TrafficDB,
		"tls":        l.TLSDir,
	}
	expected := map[string]string{
		"root":       "/etc/singbox-deploy",
		"state":      "/etc/singbox-deploy/state",
		"singboxBin": "/etc/singbox-deploy/sing-box/sing-box",
		"fragments":  "/etc/singbox-deploy/sing-box/conf/fragments",
		"config":     "/etc/singbox-deploy/sing-box/conf/config.json",
		"subscribe":  "/etc/singbox-deploy/subscribe",
		"www":        "/etc/singbox-deploy/www",
		"trafficDB":  "/etc/singbox-deploy/monitor/traffic.db",
		"tls":        "/etc/singbox-deploy/tls",
	}
	for k, want := range expected {
		if checks[k] != want {
			t.Fatalf("%s = %q, want %q", k, checks[k], want)
		}
	}
}
