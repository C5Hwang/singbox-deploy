package wireguard

import (
	"strings"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if !ValidKey(pair.PrivateKey) {
		t.Errorf("private key %q is not a valid 32-byte base64 key", pair.PrivateKey)
	}
	if !ValidKey(pair.PublicKey) {
		t.Errorf("public key %q is not a valid 32-byte base64 key", pair.PublicKey)
	}
	if pair.PrivateKey == pair.PublicKey {
		t.Errorf("private and public key must differ")
	}
	// Distinct calls return distinct material.
	second, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair second: %v", err)
	}
	if second.PrivateKey == pair.PrivateKey {
		t.Errorf("two key generations should not collide")
	}
}

func TestAllocateIP(t *testing.T) {
	tests := []struct {
		name     string
		assigned []string
		want     string
		wantErr  bool
	}{
		{name: "empty cluster picks first node IP", assigned: nil, want: "10.10.0.2"},
		{name: "consecutive allocations", assigned: []string{"10.10.0.2"}, want: "10.10.0.3"},
		{name: "recycle gap", assigned: []string{"10.10.0.2", "10.10.0.4"}, want: "10.10.0.3"},
		{name: "ignore master IP", assigned: []string{"10.10.0.1"}, want: "10.10.0.2"},
		{name: "ignore foreign subnet", assigned: []string{"192.168.1.5"}, want: "10.10.0.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AllocateIP(tt.assigned)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AllocateIP: err = %v wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("AllocateIP = %q want %q", got, tt.want)
			}
		})
	}
}

func TestAllocateIPExhaustion(t *testing.T) {
	var taken []string
	for i := 2; i <= 254; i++ {
		taken = append(taken, "10.10.0."+itoa(i))
	}
	if _, err := AllocateIP(taken); err == nil {
		t.Fatalf("AllocateIP should fail when subnet is exhausted")
	}
}

func TestValidNodeIP(t *testing.T) {
	tests := map[string]bool{
		"10.10.0.2":   true,
		"10.10.0.254": true,
		"10.10.0.1":   false, // master, not a node
		"10.10.0.255": false, // broadcast
		"10.10.0.0":   false, // network
		"10.10.1.2":   false, // wrong /24
		"":            false,
		"not.an.ip":   false,
	}
	for ip, want := range tests {
		if got := ValidNodeIP(ip); got != want {
			t.Errorf("ValidNodeIP(%q) = %v want %v", ip, got, want)
		}
	}
}

func TestRenderMaster(t *testing.T) {
	cfg := MasterConfig{
		PrivateKey: "MASTERPRIVKEY==",
		ListenPort: 51820,
		Peers: []Peer{
			{Alias: "Tokyo", PublicKey: "TOKYOPUBKEY==", IP: "10.10.0.2"},
			{Alias: "Singapore", PublicKey: "SGPPUBKEY==", IP: "10.10.0.3"},
		},
	}
	got, err := RenderMaster(cfg)
	if err != nil {
		t.Fatalf("RenderMaster: %v", err)
	}
	for _, must := range []string{
		"[Interface]",
		"PrivateKey = MASTERPRIVKEY==",
		"Address = 10.10.0.1/24",
		"ListenPort = 51820",
		"# Tokyo",
		"PublicKey = TOKYOPUBKEY==",
		"AllowedIPs = 10.10.0.2/32",
		"# Singapore",
		"AllowedIPs = 10.10.0.3/32",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("RenderMaster missing %q\n%s", must, got)
		}
	}
}

func TestRenderMasterDefaultsPort(t *testing.T) {
	got, err := RenderMaster(MasterConfig{PrivateKey: "PRIV"})
	if err != nil {
		t.Fatalf("RenderMaster: %v", err)
	}
	if !strings.Contains(got, "ListenPort = 51820") {
		t.Errorf("RenderMaster did not default ListenPort: %s", got)
	}
}

func TestRenderMasterPeerOrder(t *testing.T) {
	// Peers must be emitted in IP order regardless of input order.
	cfg := MasterConfig{
		PrivateKey: "PRIV",
		Peers: []Peer{
			{PublicKey: "B", IP: "10.10.0.5"},
			{PublicKey: "A", IP: "10.10.0.2"},
			{PublicKey: "C", IP: "10.10.0.10"},
		},
	}
	got, err := RenderMaster(cfg)
	if err != nil {
		t.Fatalf("RenderMaster: %v", err)
	}
	posA := strings.Index(got, "10.10.0.2/32")
	posB := strings.Index(got, "10.10.0.5/32")
	posC := strings.Index(got, "10.10.0.10/32")
	if !(posA < posB && posB < posC) {
		t.Errorf("peers not sorted by IP: A=%d B=%d C=%d\n%s", posA, posB, posC, got)
	}
}

func TestRenderNode(t *testing.T) {
	cfg := NodeConfig{
		PrivateKey:      "NODEPRIV==",
		IP:              "10.10.0.7",
		MasterPublicKey: "MASTERPUB==",
		MasterEndpoint:  "203.0.113.10:51820",
	}
	got, err := RenderNode(cfg)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}
	for _, must := range []string{
		"PrivateKey = NODEPRIV==",
		"Address = 10.10.0.7/24",
		"PublicKey = MASTERPUB==",
		"Endpoint = 203.0.113.10:51820",
		"AllowedIPs = 10.10.0.1/32",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("RenderNode missing %q\n%s", must, got)
		}
	}
}

func TestRenderNodeRejectsBadIP(t *testing.T) {
	_, err := RenderNode(NodeConfig{
		PrivateKey: "X", IP: "10.10.0.1", MasterPublicKey: "Y", MasterEndpoint: "z:51820",
	})
	if err == nil {
		t.Fatalf("RenderNode should reject master IP as node IP")
	}
}

func TestPeerValidate(t *testing.T) {
	good := Peer{PublicKey: "K", IP: "10.10.0.2"}
	if err := good.Validate(); err != nil {
		t.Errorf("good peer rejected: %v", err)
	}
	bad := []Peer{
		{IP: "10.10.0.2"},                   // missing key
		{PublicKey: "K"},                    // missing ip
		{PublicKey: "K", IP: "192.168.1.1"}, // wrong subnet
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("bad peer %+v should fail validation", p)
		}
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
