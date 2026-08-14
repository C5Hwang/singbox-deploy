package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
)

// The links handed over at the end of setup are the ones the operator will
// import, so they name the host the subscription is actually served under —
// whether they come from the seeded salt or from a registry of groups a
// reinstall kept.
func TestInstalledSummaryReportsSubscriptionUnderTheMonitorDomain(t *testing.T) {
	cfg := deploy.Config{
		Domain:                "vpn.example.com",
		MonitorDomain:         "monitor.example.com",
		SubscribePort:         2096,
		MonitorPublicPort:     2097,
		MonitorPort:           19090,
		Salt:                  "testsalt",
		DeployMonitor:         true,
		DeployMonitorFrontend: true,
	}
	for _, tc := range []struct {
		name   string
		groups []subgroups.Group
	}{
		{name: "seeded from the salt"},
		{name: "kept by a reinstall", groups: []subgroups.Group{
			{ID: "g1", Alias: "Live", Salt: "livesalt", Members: []string{subgroups.HubMemberID}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinInstalledRegistry(t, tc.groups)
			rows := renderSummary(installedSubscriptionRows(cfg))
			if !strings.Contains(rows, "https://monitor.example.com:2096/s/") {
				t.Fatalf("summary should publish the subscription under the monitor domain:\n%s", rows)
			}
			if strings.Contains(rows, "https://vpn.example.com:2096/s/") {
				t.Fatalf("summary should not publish the subscription under the site domain:\n%s", rows)
			}
		})
	}
}

// Without a monitor there is no second name, so the links stay where they have
// always been.
func TestInstalledSummaryKeepsSubscriptionOnTheInstallDomainWithoutMonitor(t *testing.T) {
	pinInstalledRegistry(t, nil)
	cfg := deploy.Config{
		Domain:        "vpn.example.com",
		MonitorDomain: "monitor.example.com",
		SubscribePort: 2096,
		Salt:          "testsalt",
	}
	rows := renderSummary(installedSubscriptionRows(cfg))
	if !strings.Contains(rows, "https://vpn.example.com:2096/s/") {
		t.Fatalf("summary should publish the subscription under the install domain:\n%s", rows)
	}
	if strings.Contains(rows, "monitor.example.com") {
		t.Fatalf("a monitor that is not deployed publishes nothing:\n%s", rows)
	}
}

// pinInstalledRegistry points the finished-install summary at a temporary root
// holding exactly the given groups, so the rows it renders do not depend on
// whatever is installed on the machine running the test.
func pinInstalledRegistry(t *testing.T, groups []subgroups.Group) {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	if len(groups) > 0 {
		if err := subgroups.Save(layout, groups); err != nil {
			t.Fatalf("save groups: %v", err)
		}
	}
	old := installedLayout
	t.Cleanup(func() { installedLayout = old })
	installedLayout = func() paths.Layout { return layout }
}

// The status panel reads both names back from state and has to reach the same
// answer the installer printed.
func TestLoadStatusReportsSubscriptionUnderTheMonitorDomain(t *testing.T) {
	cases := []struct {
		name          string
		monitor       string
		monitorDomain string
		wantHost      string
	}{
		{name: "a monitor of its own lends its name", monitor: "yes", monitorDomain: "monitor.example.com", wantHost: "monitor.example.com"},
		{name: "an idn is reported as it is served", monitor: "yes", monitorDomain: "监控.example.com", wantHost: "xn--izun04b.example.com"},
		{name: "a monitor left on the install domain changes nothing", monitor: "yes", monitorDomain: "", wantHost: "vpn.example.com"},
		{name: "no monitor keeps the install domain", monitor: "no", monitorDomain: "monitor.example.com", wantHost: "vpn.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			writeStatusState(t, layout.StateDir, "domain", "vpn.example.com")
			writeStatusState(t, layout.StateDir, "public_ip", "203.0.113.10")
			writeStatusState(t, layout.StateDir, "monitor_domain", tc.monitorDomain)
			writeStatusState(t, layout.StateDir, "subscribe_port", "2096")
			writeStatusState(t, layout.StateDir, "monitor", tc.monitor)

			oldLayout, oldOutput := defaultStatusLayout, statusCommandOutput
			oldGroups, oldNodes := loadStatusGroups, loadStatusNodes
			t.Cleanup(func() {
				defaultStatusLayout, statusCommandOutput = oldLayout, oldOutput
				loadStatusGroups, loadStatusNodes = oldGroups, oldNodes
			})
			defaultStatusLayout = func() paths.Layout { return layout }
			statusCommandOutput = func(name string, args ...string) (string, error) {
				return "", fmt.Errorf("unexpected command: %s %v", name, args)
			}
			loadStatusGroups = func(paths.Layout) ([]subgroups.Group, error) {
				return []subgroups.Group{{ID: "g1", Alias: "Live", Salt: "livesalt", Members: []string{subgroups.HubMemberID}}}, nil
			}
			loadStatusNodes = func(paths.Layout) ([]nodes.Node, error) { return nil, nil }

			status := loadStatus()
			if len(status.Groups) != 1 {
				t.Fatalf("groups = %#v", status.Groups)
			}
			want := "https://" + tc.wantHost + ":2096/s/"
			for _, url := range []string{
				status.Groups[0].Subscription,
				status.Groups[0].ClashMetaSub,
				status.Groups[0].SingBoxSub,
				status.Groups[0].SurgeSub,
			} {
				if !strings.HasPrefix(url, want) {
					t.Fatalf("subscription URL = %q, want the %q prefix", url, want)
				}
			}
		})
	}
}
