package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c system.Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

type fakeIssuer struct{}

func (fakeIssuer) Issue(_ context.Context, _ acme.Request) (acme.Certificate, error) {
	return acme.Certificate{CertificatePEM: []byte("CERTPEM"), PrivateKeyPEM: []byte("KEYPEM")}, nil
}

// writeFakeArchive writes a tar.gz containing a sing-box binary to dest.
func writeFakeArchive(dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := "#!fake-sing-box"
	hdr := &tar.Header{Name: "sing-box-1.12.0-linux-amd64/sing-box", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func testConfig(t *testing.T) Config {
	t.Helper()
	creds, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	return Config{
		Domain:               "example.com",
		Email:                "admin@example.com",
		Challenge:            acme.ChallengeHTTP01,
		Ports:                config.Ports{RealityVision: 443, RealityGRPC: 8443, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		DisplayName:          "US-vps1",
		Salt:                 "testsalt",
		RealityServerName:    "www.microsoft.com",
		RealityHandshakePort: 443,
		SubscribePort:        2096,
		MonitorPort:          19090,
		TrafficLimitBytes:    100 << 30,
		ResetDay:             1,
		MonitorInterface:     "eth0",
		OS:                   system.OSRelease{Family: system.FamilyDebian, PackageManager: "apt"},
		Firewall:             system.FirewallUFW,
		Creds:                creds,
	}
}

func TestOrchestratorRunsFullFlow(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	runner := &recordingRunner{}
	var events []Event

	o := &Orchestrator{
		Runner:        runner,
		Layout:        layout,
		ACME:          acme.NewManager(fakeIssuer{}),
		LatestSingBox: func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:      func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		Progress:      func(e Event) { events = append(events, e) },
		GOOS:          "linux",
		GOARCH:        "amd64",
		DeployBin:     "/usr/bin/singbox-deploy",
		SystemdDir:    filepath.Join(root, "systemd"),
		NginxConfPath: filepath.Join(root, "nginx", "singbox-deploy.conf"),
	}

	if err := o.Run(context.Background(), testConfig(t)); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Every step emitted a final ok.
	okCount := 0
	for _, e := range events {
		if e.Status == "ok" {
			okCount++
		}
		if e.Status == "fail" {
			t.Fatalf("step %q failed: %v", e.Label, e.Err)
		}
	}
	if okCount != 11 {
		t.Fatalf("expected 11 ok steps, got %d", okCount)
	}

	// Key commands were issued.
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"apt-get update",
		"systemctl enable --now sing-box.service",
		"check -c " + layout.ConfigJSON,
		"nginx -t",
		"systemctl enable --now singbox-deploy-monitor.service",
		"ufw allow 9443/udp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}

	// config.json is valid and protocol-complete.
	cfgBytes, err := os.ReadFile(layout.ConfigJSON)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var decoded struct {
		Inbounds []struct {
			Type string `json:"type"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(cfgBytes, &decoded); err != nil {
		t.Fatalf("config not valid json: %v", err)
	}
	if len(decoded.Inbounds) != 5 {
		t.Fatalf("expected 5 inbounds, got %d", len(decoded.Inbounds))
	}

	// Subscription default file exists and decodes to protocol links.
	token := subscription.TokenFromSalt("testsalt")
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "default", token))
	if err != nil {
		t.Fatalf("read default subscription: %v", err)
	}
	decodedLinks, err := subscription.DecodeBase64(string(body))
	if err != nil {
		t.Fatalf("default not base64: %v", err)
	}
	for _, scheme := range []string{"vless://", "hysteria2://", "tuic://", "anytls://"} {
		if !strings.Contains(decodedLinks, scheme) {
			t.Fatalf("default subscription missing %s:\n%s", scheme, decodedLinks)
		}
	}

	// The assembled sing-box client profile is valid JSON.
	profile, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "sing-box", token))
	if err != nil {
		t.Fatalf("read sing-box profile: %v", err)
	}
	var anyJSON any
	if err := json.Unmarshal(profile, &anyJSON); err != nil {
		t.Fatalf("sing-box profile not valid json: %v\n%s", err, profile)
	}

	// Units, nginx config, sing-box binary, and account state were written.
	mustExist(t, filepath.Join(o.SystemdDir, "sing-box.service"))
	mustExist(t, filepath.Join(o.SystemdDir, "singbox-deploy-monitor.service"))
	mustExist(t, o.NginxConfPath)
	mustExist(t, layout.SingBoxBin)
	mustExist(t, filepath.Join(layout.StateDir, "domain"))
	mustExist(t, filepath.Join(layout.WebRoot, "index.html"))
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func TestOrchestratorStopsOnStepFailure(t *testing.T) {
	root := t.TempDir()
	o := &Orchestrator{
		Runner:        &failingRunner{},
		Layout:        paths.LayoutForRoot(root),
		ACME:          acme.NewManager(fakeIssuer{}),
		LatestSingBox: func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:      func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		SystemdDir:    filepath.Join(root, "systemd"),
		NginxConfPath: filepath.Join(root, "nginx.conf"),
	}
	if err := o.Run(context.Background(), testConfig(t)); err == nil {
		t.Fatalf("expected failure when runner errors")
	}
}

type failingRunner struct{}

func (failingRunner) Run(system.Command) error { return errBoom }

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
