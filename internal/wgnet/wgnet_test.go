package wgnet

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestGenerateKeyPairRoundTrips(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if !ValidKey(kp.PrivateKey) || !ValidKey(kp.PublicKey) {
		t.Fatalf("generated keys are not valid: %+v", kp)
	}
	raw, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
	if err != nil || len(raw) != 32 {
		t.Fatalf("private key not 32 bytes: %v len=%d", err, len(raw))
	}
	// Clamping must be applied.
	if raw[0]&7 != 0 || raw[31]&128 != 0 || raw[31]&64 == 0 {
		t.Fatalf("private key not clamped: %v", raw)
	}
	derived, err := PublicKeyFromPrivate(kp.PrivateKey)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate: %v", err)
	}
	if derived != kp.PublicKey {
		t.Fatalf("derived public %q != generated %q", derived, kp.PublicKey)
	}
}

func TestGenerateKeyPairUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		kp, err := GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair: %v", err)
		}
		if seen[kp.PrivateKey] {
			t.Fatalf("duplicate key generated")
		}
		seen[kp.PrivateKey] = true
	}
}

func TestAllocateSpokeIP(t *testing.T) {
	tests := []struct {
		name string
		used []string
		want string
	}{
		{"first spoke skips hub", nil, "10.90.0.2"},
		{"skips used", []string{"10.90.0.2", "10.90.0.3"}, "10.90.0.4"},
		{"fills gap", []string{"10.90.0.2", "10.90.0.4"}, "10.90.0.3"},
		{"ignores hub in used", []string{"10.90.0.1"}, "10.90.0.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AllocateSpokeIP(DefaultSubnet, tt.used)
			if err != nil {
				t.Fatalf("AllocateSpokeIP: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestAllocateSpokeIPExhausted(t *testing.T) {
	// A /30 has hosts .1 and .2 only (.0 network, .3 broadcast). .1 is the hub,
	// so exactly one spoke fits.
	first, err := AllocateSpokeIP("10.0.0.0/30", nil)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if first != "10.0.0.2" {
		t.Fatalf("got %q want 10.0.0.2", first)
	}
	if _, err := AllocateSpokeIP("10.0.0.0/30", []string{first}); err == nil {
		t.Fatalf("expected exhaustion error")
	}
}

func TestWithPrefix(t *testing.T) {
	got, err := WithPrefix("10.90.0.2", DefaultSubnet)
	if err != nil {
		t.Fatalf("WithPrefix: %v", err)
	}
	if got != "10.90.0.2/24" {
		t.Fatalf("got %q want 10.90.0.2/24", got)
	}
}

func TestRenderHubConfigSortsPeers(t *testing.T) {
	cfg := HubConfig{
		PrivateKey: "PRIV",
		Address:    "10.90.0.1/24",
		ListenPort: 51820,
		Peers: []Peer{
			{PublicKey: "KEYB", AllowedIP: "10.90.0.3/32"},
			{PublicKey: "KEYA", AllowedIP: "10.90.0.2/32"},
		},
	}
	out := RenderHubConfig(cfg)
	if !strings.Contains(out, "ListenPort = 51820") {
		t.Fatalf("missing listen port:\n%s", out)
	}
	if idx2, idx3 := strings.Index(out, "10.90.0.2/32"), strings.Index(out, "10.90.0.3/32"); idx2 > idx3 {
		t.Fatalf("peers not sorted by allowed ip:\n%s", out)
	}
	if strings.Count(out, "[Peer]") != 2 {
		t.Fatalf("expected 2 peers:\n%s", out)
	}
}

func TestRenderSpokeConfig(t *testing.T) {
	out := RenderSpokeConfig(SpokeConfig{
		PrivateKey:   "SPRIV",
		Address:      "10.90.0.2/24",
		HubPublicKey: "HUBPUB",
		HubEndpoint:  "hub.example.com:51820",
		Subnet:       DefaultSubnet,
	})
	for _, want := range []string{
		"PrivateKey = SPRIV",
		"PublicKey = HUBPUB",
		"Endpoint = hub.example.com:51820",
		"AllowedIPs = 10.90.0.0/24",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("spoke config missing %q:\n%s", want, out)
		}
	}
}

// recordingRunner captures commands for assertions.
type recordingRunner struct{ cmds []system.Command }

func (r *recordingRunner) Run(c system.Command) error {
	r.cmds = append(r.cmds, c)
	return nil
}

func TestManagerWriteConfigPerms(t *testing.T) {
	dir := t.TempDir()
	m := Manager{Runner: &recordingRunner{}, ConfDir: dir}
	if err := m.WriteConfig(InterfaceName, "hello"); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	path := filepath.Join(dir, InterfaceName+".conf")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v want 0600", info.Mode().Perm())
	}
}

func TestManagerPeerCommands(t *testing.T) {
	rr := &recordingRunner{}
	m := Manager{Runner: rr}
	if err := m.SetPeer(InterfaceName, "PUB", "10.90.0.2/32"); err != nil {
		t.Fatalf("SetPeer: %v", err)
	}
	if err := m.RemovePeer(InterfaceName, "PUB"); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if got := rr.cmds[0].String(); got != "wg set sbwg0 peer PUB allowed-ips 10.90.0.2/32" {
		t.Fatalf("set peer cmd = %q", got)
	}
	if got := rr.cmds[1].String(); got != "wg set sbwg0 peer PUB remove" {
		t.Fatalf("remove peer cmd = %q", got)
	}
}

func TestInstallCommands(t *testing.T) {
	apt := InstallCommands(system.OSRelease{PackageManager: "apt"})
	if len(apt) == 0 || !strings.Contains(apt[len(apt)-1].String(), "wireguard-tools") {
		t.Fatalf("apt install missing wireguard-tools: %+v", apt)
	}
	dnf := InstallCommands(system.OSRelease{PackageManager: "dnf"})
	if len(dnf) != 1 || !strings.Contains(dnf[0].String(), "wireguard-tools") {
		t.Fatalf("dnf install missing wireguard-tools: %+v", dnf)
	}
	if InstallCommands(system.OSRelease{PackageManager: "apk"}) != nil {
		t.Fatalf("unsupported package manager should return nil")
	}
}
