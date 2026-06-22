package deploy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// fakeDNSLookup always returns Cloudflare credentials; used so stepCertificates
// can issue without requiring a real DNSStore.
func fakeDNSLookup(_ string) (string, map[string]string, error) {
	return "cloudflare", map[string]string{"CF_API_TOKEN": "test"}, nil
}

type countingIssuer struct {
	calls int
	got   acme.Request
	cert  acme.Certificate
	err   error
}

func (i *countingIssuer) Issue(_ context.Context, r acme.Request) (acme.Certificate, error) {
	i.calls++
	i.got = r
	return i.cert, i.err
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
		Domain:                 "example.com",
		Ports:                  config.Ports{RealityVision: 443, RealityGRPC: 8443, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		DisplayName:            "US-vps1",
		Salt:                   "testsalt",
		SiteTemplate:           "massively",
		RealityServerName:      "www.microsoft.com",
		RealityHandshakePort:   config.DefaultRealityHandshakePort,
		SubscribePort:          DefaultSubscribePort,
		MonitorPublicPort:      DefaultMonitorPublicPort,
		MonitorPort:            DefaultMonitorPort,
		DeployMonitor:          true,
		MonitorAlias:           "US-local",
		TrafficInLimitBytes:    40 << 30,
		TrafficOutLimitBytes:   50 << 30,
		TrafficTotalLimitBytes: 100 << 30,
		ResetDay:               DefaultResetDay,
		ResetHour:              DefaultResetHour,
		MonitorInterface:       "eth0",
		MonitorIntervalSeconds: DefaultMonitorIntervalSeconds,
		OS:                     system.OSRelease{Family: system.FamilyDebian, PackageManager: "apt"},
		Firewall:               system.FirewallUFW,
		Creds:                  creds,
	}
}

func TestOrchestratorRunsFullFlow(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	runner := &recordingRunner{}
	var events []Event

	o := &Orchestrator{
		Runner:         runner,
		Layout:         layout,
		ACME:           acme.NewManager(fakeIssuer{}),
		LatestSingBox:  func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:       func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		CheckConflicts: func(context.Context, Config) error { return nil },
		CheckPorts:     func(context.Context, Config) error { return nil },
		DNSLookup:      fakeDNSLookup,
		Progress:       func(e Event) { events = append(events, e) },
		GOOS:           "linux",
		GOARCH:         "amd64",
		DeployBin:      "/usr/bin/singbox-deploy",
		SystemdDir:     filepath.Join(root, "systemd"),
		NginxConfPath:  filepath.Join(root, "nginx", "singbox-deploy.conf"),
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
	if okCount != 13 {
		t.Fatalf("expected 13 ok steps, got %d", okCount)
	}

	// Key commands were issued.
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"apt-get update",
		"systemctl enable --now sing-box.service",
		"systemctl enable --now singbox-deploy-cert-renew.timer",
		"check -c " + layout.ConfigJSON,
		"nginx -t",
		"systemctl enable --now singbox-deploy-monitor.service",
		"ufw allow 2097/tcp",
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
	assertNewDNSServerFormat(t, "config.json", cfgBytes)
	assertDefaultDomainResolver(t, "config.json", cfgBytes, "google")
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
	profile, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "singboxProfiles", token))
	if err != nil {
		t.Fatalf("read sing-box profile: %v", err)
	}
	var anyJSON any
	if err := json.Unmarshal(profile, &anyJSON); err != nil {
		t.Fatalf("sing-box profile not valid json: %v\n%s", err, profile)
	}
	assertNewDNSServerFormat(t, "sing-box profile", profile)
	assertDefaultDomainResolver(t, "sing-box profile", profile, "local")
	profileText := string(profile)
	for _, want := range []string{
		`"tag": "全球代理"`,
		`"tag": "CNCIDR"`,
		`"final": "漏网之鱼"`,
		`https://fastly.jsdelivr.net/gh/QuixoticHeart/rule-set@ruleset/singbox/version2/ai.srs`,
		`https://fastly.jsdelivr.net/gh/QuixoticHeart/rule-set@ruleset/singbox/version2/cncidr.srs`,
	} {
		if !strings.Contains(profileText, want) {
			t.Fatalf("sing-box profile missing %q:\n%s", want, profileText)
		}
	}

	clashFragment, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "clashMeta", token))
	if err != nil {
		t.Fatalf("read clash fragment: %v", err)
	}
	clashText := string(clashFragment)
	if !strings.Contains(clashText, "  - name: \"🇺🇸 US-vps1-VLESS-Reality-Vision\"\n    type: vless") {
		t.Fatalf("clash fragment should use block-style proxies with UTF-8 names:\n%s", clashText)
	}
	if strings.Contains(clashText, "  - {") || strings.Contains(clashText, "reality-opts: {") {
		t.Fatalf("clash fragment should not use inline mappings:\n%s", clashText)
	}
	clashProfile, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "clashMetaProfiles", token))
	if err != nil {
		t.Fatalf("read clash profile: %v", err)
	}
	clashProfileText := string(clashProfile)
	for _, want := range []string{
		"proxy-providers:\n  provider:\n",
		"    url: \"https://example.com:2096/s/clashMeta/" + token + "\"\n",
		"    use:\n      - provider\n    proxies: null\n",
		"rule-providers:\n",
		"  AI:\n",
		"  CN:\n",
		"  - RULE-SET,AI,AI服务\n",
		"  - RULE-SET,CNCIDR,本地直连,no-resolve\n",
		"  - MATCH,漏网之鱼\n",
	} {
		if !strings.Contains(clashProfileText, want) {
			t.Fatalf("clash profile missing %q:\n%s", want, clashProfileText)
		}
	}
	if strings.Contains(clashProfileText, "proxies:\n  - name:") {
		t.Fatalf("clash profile should load proxies from provider, not inline proxies:\n%s", clashProfileText)
	}

	surgeFragment, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "surge", token))
	if err != nil {
		t.Fatalf("read surge fragment: %v", err)
	}
	surgeText := string(surgeFragment)
	if !strings.Contains(surgeText, "US-vps1-Hysteria2 = hysteria2") {
		t.Fatalf("surge fragment missing hysteria2 proxy:\n%s", surgeText)
	}
	if !strings.Contains(surgeText, "US-vps1-TUIC = tuic-v5") {
		t.Fatalf("surge fragment missing tuic proxy:\n%s", surgeText)
	}
	if !strings.Contains(surgeText, "US-vps1-AnyTLS = anytls") {
		t.Fatalf("surge fragment missing anytls proxy:\n%s", surgeText)
	}
	if strings.Contains(surgeText, "vless") {
		t.Fatalf("surge fragment should not contain vless (unsupported by Surge):\n%s", surgeText)
	}

	surgeProfile, err := os.ReadFile(filepath.Join(layout.SubscribeDir, "surgeProfiles", token))
	if err != nil {
		t.Fatalf("read surge profile: %v", err)
	}
	surgeProfileText := string(surgeProfile)
	for _, want := range []string{
		"[General]",
		"[Proxy Group]",
		"[Rule]",
		"policy-path=https://example.com:2096/s/surge/" + token,
		"FINAL,漏网之鱼",
	} {
		if !strings.Contains(surgeProfileText, want) {
			t.Fatalf("surge profile missing %q:\n%s", want, surgeProfileText)
		}
	}

	// Units, nginx config, sing-box binary, and account state were written.
	mustExist(t, filepath.Join(o.SystemdDir, "sing-box.service"))
	mustExist(t, filepath.Join(o.SystemdDir, "singbox-deploy-cert-renew.service"))
	mustExist(t, filepath.Join(o.SystemdDir, "singbox-deploy-cert-renew.timer"))
	mustExist(t, filepath.Join(o.SystemdDir, "singbox-deploy-monitor.service"))
	monitorUnit, err := os.ReadFile(filepath.Join(o.SystemdDir, "singbox-deploy-monitor.service"))
	if err != nil {
		t.Fatalf("read monitor unit: %v", err)
	}
	for _, want := range []string{"--in-limit-bytes 42949672960", "--out-limit-bytes 53687091200", "--total-limit-bytes 107374182400"} {
		if !strings.Contains(string(monitorUnit), want) {
			t.Fatalf("monitor unit missing %q:\n%s", want, monitorUnit)
		}
	}
	mustExist(t, o.NginxConfPath)
	nginxConf, err := os.ReadFile(o.NginxConfPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	for _, want := range []string{"listen 2096 ssl;", "listen 2097 ssl;", "http2 on;", "proxy_pass http://127.0.0.1:19090/"} {
		if !strings.Contains(string(nginxConf), want) {
			t.Fatalf("nginx config missing %q:\n%s", want, nginxConf)
		}
	}
	mustExist(t, layout.SingBoxBin)
	mustExist(t, filepath.Join(layout.StateDir, "domain"))
	mustNotExist(t, filepath.Join(layout.StateDir, "acme_challenge"))
	mustExist(t, filepath.Join(layout.StateDir, "traffic_in_limit_bytes"))
	mustExist(t, filepath.Join(layout.StateDir, "traffic_out_limit_bytes"))
	mustExist(t, filepath.Join(layout.StateDir, "traffic_total_limit_bytes"))
	mustExist(t, filepath.Join(layout.StateDir, "monitor_public_port"))
	mustExist(t, filepath.Join(layout.StateDir, "site_template"))
	mustExist(t, filepath.Join(layout.WebRoot, "index.html"))
	mustExist(t, filepath.Join(layout.WebRoot, "LICENSE.txt"))
	mustExist(t, filepath.Join(layout.WebRoot, "assets", "css", "main.css"))
}

func TestOrchestratorSkipsMonitorWhenDisabled(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	runner := &recordingRunner{}
	cfg := testConfig(t)
	cfg.DeployMonitor = false
	cfg.TrafficInLimitBytes = 0
	cfg.TrafficOutLimitBytes = 0
	cfg.TrafficTotalLimitBytes = 0
	cfg.MonitorInterface = ""

	o := &Orchestrator{
		Runner:         runner,
		Layout:         layout,
		ACME:           acme.NewManager(fakeIssuer{}),
		LatestSingBox:  func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:       func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		CheckConflicts: func(context.Context, Config) error { return nil },
		CheckPorts:     func(context.Context, Config) error { return nil },
		DNSLookup:      fakeDNSLookup,
		GOOS:           "linux",
		GOARCH:         "amd64",
		DeployBin:      "/usr/bin/singbox-deploy",
		SystemdDir:     filepath.Join(root, "systemd"),
		NginxConfPath:  filepath.Join(root, "nginx", "singbox-deploy.conf"),
	}

	if err := o.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	joined := strings.Join(runner.commands, "\n")
	if strings.Contains(joined, system.MonitorService) {
		t.Fatalf("monitor service should not be enabled when disabled:\n%s", joined)
	}
	mustNotExist(t, filepath.Join(o.SystemdDir, system.MonitorService))
	nginxConf, err := os.ReadFile(o.NginxConfPath)
	if err != nil {
		t.Fatalf("read nginx config: %v", err)
	}
	if strings.Contains(string(nginxConf), "/monitor/") {
		t.Fatalf("nginx config should not include traffic locations when monitor is disabled:\n%s", nginxConf)
	}
	state, err := os.ReadFile(filepath.Join(layout.StateDir, "monitor"))
	if err != nil {
		t.Fatalf("read monitor state: %v", err)
	}
	if strings.TrimSpace(string(state)) != "no" {
		t.Fatalf("monitor state = %q, want no", state)
	}
	mustNotExist(t, filepath.Join(layout.StateDir, "traffic_in_limit_bytes"))
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected file %s not to exist", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func TestStepCertificatesReusesExistingManagedCertificate(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	runner := &recordingRunner{}
	issuer := &countingIssuer{}
	o := &Orchestrator{Runner: runner, Layout: layout, ACME: acme.NewManager(issuer)}
	certPath, keyPath := o.certPaths(cfg)
	writeTestCertificatePair(t, certPath, keyPath, cfg.Domain)

	if err := o.stepCertificates(context.Background(), cfg); err != nil {
		t.Fatalf("stepCertificates error: %v", err)
	}
	if issuer.calls != 0 {
		t.Fatalf("expected existing certificate to skip ACME, got %d calls", issuer.calls)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("expected no commands when reusing certificate, got %#v", runner.commands)
	}
}

func TestStepCertificatesImportsLetsEncryptCertificate(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	oldLiveDir := letsEncryptLiveDir
	letsEncryptLiveDir = filepath.Join(root, "letsencrypt", "live")
	t.Cleanup(func() { letsEncryptLiveDir = oldLiveDir })
	srcCert := filepath.Join(letsEncryptLiveDir, cfg.Domain, "fullchain.pem")
	srcKey := filepath.Join(letsEncryptLiveDir, cfg.Domain, "privkey.pem")
	certPEM, keyPEM := writeTestCertificatePair(t, srcCert, srcKey, cfg.Domain)
	issuer := &countingIssuer{}
	o := &Orchestrator{Runner: &recordingRunner{}, Layout: layout, ACME: acme.NewManager(issuer)}

	if err := o.stepCertificates(context.Background(), cfg); err != nil {
		t.Fatalf("stepCertificates error: %v", err)
	}
	if issuer.calls != 0 {
		t.Fatalf("expected imported certificate to skip ACME, got %d calls", issuer.calls)
	}
	dstCert, dstKey := o.certPaths(cfg)
	gotCert, err := os.ReadFile(dstCert)
	if err != nil {
		t.Fatalf("read imported cert: %v", err)
	}
	gotKey, err := os.ReadFile(dstKey)
	if err != nil {
		t.Fatalf("read imported key: %v", err)
	}
	if string(gotCert) != string(certPEM) || string(gotKey) != string(keyPEM) {
		t.Fatalf("imported certificate pair mismatch")
	}
}

func TestStepCertificatesObtainsWhenExistingCertificateInvalid(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	runner := &recordingRunner{}
	issuer := &countingIssuer{cert: acme.Certificate{CertificatePEM: []byte("NEWCERT"), PrivateKeyPEM: []byte("NEWKEY")}}
	o := &Orchestrator{Runner: runner, Layout: layout, ACME: acme.NewManager(issuer), DNSLookup: fakeDNSLookup}
	certPath, keyPath := o.certPaths(cfg)
	if err := WriteFile(certPath, []byte("invalid cert"), 0o644); err != nil {
		t.Fatalf("write invalid cert: %v", err)
	}
	if err := WriteFile(keyPath, []byte("invalid key"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}

	if err := o.stepCertificates(context.Background(), cfg); err != nil {
		t.Fatalf("stepCertificates error: %v", err)
	}
	if issuer.calls != 1 {
		t.Fatalf("expected ACME call, got %d", issuer.calls)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("dns-01 issuance must not run shell commands, got %#v", runner.commands)
	}
	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if string(gotCert) != "NEWCERT" || string(gotKey) != "NEWKEY" {
		t.Fatalf("new certificate pair not written")
	}
}

func TestStepCertificatesRejectsMissingDNSCredentials(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	issuer := &countingIssuer{}
	missLookup := func(_ string) (string, map[string]string, error) {
		return "", nil, os.ErrNotExist
	}
	o := &Orchestrator{Runner: &recordingRunner{}, Layout: layout, ACME: acme.NewManager(issuer), DNSLookup: missLookup}
	err := o.stepCertificates(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error when DNS credentials are missing")
	}
	if !strings.Contains(err.Error(), "no DNS credentials configured for example.com") {
		t.Fatalf("error should mention missing DNS credentials, got %v", err)
	}
	if issuer.calls != 0 {
		t.Fatalf("ACME must not be called when lookup misses, got %d calls", issuer.calls)
	}
}

func TestStepCertificatesPassesLookupCredentialsToACME(t *testing.T) {
	root := t.TempDir()
	layout := paths.LayoutForRoot(root)
	cfg := testConfig(t)
	issuer := &countingIssuer{cert: acme.Certificate{CertificatePEM: []byte("PEM"), PrivateKeyPEM: []byte("KEY")}}
	wantCreds := map[string]string{"CF_API_TOKEN": "tok"}
	lookup := func(host string) (string, map[string]string, error) {
		if host != cfg.Domain {
			t.Fatalf("lookup got host %q, want %q", host, cfg.Domain)
		}
		return "cloudflare", wantCreds, nil
	}
	o := &Orchestrator{Runner: &recordingRunner{}, Layout: layout, ACME: acme.NewManager(issuer), DNSLookup: lookup}
	if err := o.stepCertificates(context.Background(), cfg); err != nil {
		t.Fatalf("stepCertificates: %v", err)
	}
	if issuer.calls != 1 {
		t.Fatalf("expected 1 ACME call, got %d", issuer.calls)
	}
	if issuer.got.DNSProvider != "cloudflare" || issuer.got.Credentials["CF_API_TOKEN"] != "tok" {
		t.Fatalf("ACME request did not carry lookup credentials: %#v", issuer.got)
	}
}

func writeTestCertificatePair(t *testing.T, certPath, keyPath, domain string) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domain},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPEM, keyPEM
}

func assertNewDNSServerFormat(t *testing.T, name string, b []byte) {
	t.Helper()
	var decoded struct {
		DNS struct {
			Servers []map[string]any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("%s not valid json: %v", name, err)
	}
	if len(decoded.DNS.Servers) == 0 {
		t.Fatalf("%s missing dns.servers", name)
	}
	for i, server := range decoded.DNS.Servers {
		if _, ok := server["address"]; ok {
			t.Fatalf("%s dns.servers[%d] uses legacy address field", name, i)
		}
		if typ, ok := server["type"].(string); !ok || typ == "" {
			t.Fatalf("%s dns.servers[%d] missing type", name, i)
		}
	}
}

func assertDefaultDomainResolver(t *testing.T, name string, b []byte, wantServer string) {
	t.Helper()
	var decoded struct {
		Route struct {
			DefaultDomainResolver struct {
				Server string `json:"server"`
			} `json:"default_domain_resolver"`
		} `json:"route"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("%s not valid json: %v", name, err)
	}
	if decoded.Route.DefaultDomainResolver.Server != wantServer {
		t.Fatalf("%s route.default_domain_resolver server = %q, want %q", name, decoded.Route.DefaultDomainResolver.Server, wantServer)
	}
}

func TestOrchestratorStopsOnStepFailure(t *testing.T) {
	root := t.TempDir()
	o := &Orchestrator{
		Runner:         &failingRunner{},
		Layout:         paths.LayoutForRoot(root),
		ACME:           acme.NewManager(fakeIssuer{}),
		LatestSingBox:  func(context.Context) (string, error) { return "v1.12.0", nil },
		Download:       func(_ context.Context, _, dest string) error { return writeFakeArchive(dest) },
		CheckConflicts: func(context.Context, Config) error { return nil },
		CheckPorts:     func(context.Context, Config) error { return nil },
		DNSLookup:      fakeDNSLookup,
		SystemdDir:     filepath.Join(root, "systemd"),
		NginxConfPath:  filepath.Join(root, "nginx.conf"),
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
