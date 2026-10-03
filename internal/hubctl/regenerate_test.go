package hubctl

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// failOnceRunner records commands and fails the first one containing failOn.
type failOnceRunner struct {
	failOn   string
	commands []string
	failed   bool
}

func (r *failOnceRunner) Run(cmd system.Command) error {
	line := cmd.Name + " " + strings.Join(cmd.Args, " ")
	r.commands = append(r.commands, line)
	if !r.failed && strings.Contains(line, r.failOn) {
		r.failed = true
		return errors.New("command failed")
	}
	return nil
}

// regenerateTestController keeps the Hub's Nginx config inside the test's
// layout so a run never touches /etc/nginx.
func regenerateTestController(layout paths.Layout, runner system.Runner) *Controller {
	return &Controller{Layout: layout, Runner: runner, NginxConfPath: filepath.Join(layout.Root, "nginx", "singbox-deploy.conf")}
}

func regenerateTestHub(t *testing.T) paths.Layout {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	if err := deploy.WriteInstallState(layout.StateDir, hysteriaConfig(t, "hub.example.com", "HUB", "hub-salt", 9443)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.FragmentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.ConfigJSON, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nginxConf := filepath.Join(layout.Root, "nginx", "singbox-deploy.conf")
	if err := os.MkdirAll(filepath.Dir(nginxConf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nginxConf, []byte("# stale nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return layout
}

func TestRegenerateConfigsRebuildsTheHubLikeASpoke(t *testing.T) {
	layout := regenerateTestHub(t)
	runner := &hubCommandRunner{}
	ctrl := regenerateTestController(layout, runner)

	results, err := ctrl.RegenerateConfigs(context.Background(), true, nil, io.Discard)
	if err != nil {
		t.Fatalf("RegenerateConfigs: %v", err)
	}
	if len(results) != 1 || results[0].Name != "Hub" || results[0].Err != nil {
		t.Fatalf("results = %+v, want one successful Hub entry", results)
	}
	got, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"default_domain_resolver"`) || strings.Contains(string(got), "stale") {
		t.Fatalf("config.json was not re-rendered from the template:\n%s", got)
	}
	nginxConf, err := os.ReadFile(ctrl.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nginxConf), "stale nginx") || !strings.Contains(string(nginxConf), "hub.example.com") {
		t.Fatalf("Nginx config was not re-rendered from the template:\n%s", nginxConf)
	}
	var joined []string
	for _, cmd := range runner.commands {
		joined = append(joined, cmd.Name+" "+strings.Join(cmd.Args, " "))
	}
	order := []string{
		"check -c " + deploy.ProtocolConfigCandidate(layout),
		"systemctl restart " + system.SingBoxService,
		"nginx -t",
		"systemctl restart nginx",
	}
	next := 0
	for _, line := range joined {
		if next < len(order) && strings.HasSuffix(line, order[next]) {
			next++
		}
	}
	if next != len(order) {
		t.Fatalf("commands = %q, want in order %q", joined, order)
	}
}

func TestRegenerateConfigsRestoresTheHubConfigWhenRestartFails(t *testing.T) {
	layout := regenerateTestHub(t)
	runner := &failOnceRunner{failOn: "restart " + system.SingBoxService}
	ctrl := regenerateTestController(layout, runner)

	if _, err := ctrl.RegenerateConfigs(context.Background(), true, nil, io.Discard); err == nil {
		t.Fatal("RegenerateConfigs succeeded although sing-box failed to restart")
	}
	got, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"stale":true}` {
		t.Fatalf("config.json = %s, want the previous config restored", got)
	}
	if n := strings.Count(strings.Join(runner.commands, "\n"), "restart "+system.SingBoxService); n != 2 {
		t.Fatalf("restarts = %d, want the failed one plus one on the restored config", n)
	}
}

func TestRegenerateConfigsKeepsGoingPastAFailedNode(t *testing.T) {
	layout := regenerateTestHub(t)
	var events []deploy.Event
	ctrl := regenerateTestController(layout, &hubCommandRunner{})
	ctrl.Progress = func(e deploy.Event) { events = append(events, e) }

	results, err := ctrl.RegenerateConfigs(context.Background(), true, []string{"gone"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no longer registered") {
		t.Fatalf("err = %v, want the missing spoke reported", err)
	}
	if len(results) != 2 || results[0].Err != nil || results[1].Err == nil {
		t.Fatalf("results = %+v, want the Hub to succeed and the missing spoke to fail", results)
	}
	last := events[len(events)-1]
	if last.Index != 2 || last.Total != 2 || last.Status != "fail" {
		t.Fatalf("last event = %+v, want step 2/2 failed", last)
	}
}

func TestRegenerateConfigsRestoresTheHubNginxConfigWhenNginxRejectsIt(t *testing.T) {
	layout := regenerateTestHub(t)
	runner := &failOnceRunner{failOn: "nginx -t"}
	ctrl := regenerateTestController(layout, runner)

	_, err := ctrl.RegenerateConfigs(context.Background(), true, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "previous Nginx config restored") {
		t.Fatalf("err = %v, want the Nginx failure reported with the old config restored", err)
	}
	got, err := os.ReadFile(ctrl.NginxConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# stale nginx\n" {
		t.Fatalf("Nginx config = %q, want the previous config restored", got)
	}
}
