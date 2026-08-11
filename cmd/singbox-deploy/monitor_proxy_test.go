package main

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
)

// The hub does not proxy URLs to a spoke, it proxies an enum: monitorEndpointForPath
// turns the path its own dashboard asked for back into one of the endpoints the
// agent serves, and refuses anything else. That refusal is the right default,
// but it means a monitor route added on both ends and left out of the enum
// reaches a 502 only on spokes, and only for whoever opens that one view —
// which is exactly how /api/ping-series shipped broken.
//
// So the two directions have to stay in step, in both directions.
func TestEveryProxiedMonitorPathResolvesToItsEndpoint(t *testing.T) {
	// The summary is the one endpoint the hub fetches for itself rather than on
	// behalf of a dashboard request — hubctl calls it directly to refresh what it
	// knows about each spoke, so no dashboard path ever has to resolve to it.
	const hubFetchesItself = "/api/summary"

	paths := nodeapi.MonitorHandlerPaths()
	if len(paths) < 2 {
		t.Fatalf("proxied monitor paths = %v, want the full set", paths)
	}
	for _, path := range paths {
		if path == hubFetchesItself {
			continue
		}
		endpoint, err := monitorEndpointForPath(path)
		if err != nil {
			t.Fatalf("monitorEndpointForPath(%q): %v", path, err)
		}
		if endpoint == "" {
			t.Fatalf("monitorEndpointForPath(%q) resolved to the empty endpoint", path)
		}
	}
}

func TestUnknownMonitorPathIsRefused(t *testing.T) {
	for _, path := range []string{"/api/nope", "/api/../etc/passwd", "http://evil.invalid/", ""} {
		if _, err := monitorEndpointForPath(path); err == nil {
			t.Fatalf("monitorEndpointForPath(%q) was accepted", path)
		}
	}
}
