package cluster

import (
	"reflect"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func tempRegistry(t *testing.T) Registry {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(paths.LayoutForRoot(dir))
}

func TestRegistryAllocateNextID(t *testing.T) {
	r := tempRegistry(t)

	id, err := r.AllocateNextID()
	if err != nil {
		t.Fatalf("AllocateNextID: %v", err)
	}
	if id != "001" {
		t.Errorf("first allocation = %q want 001", id)
	}

	must(t, r.Save(Node{ID: "001", Alias: "Tokyo", Domain: "jp.example.com", WGIP: "10.10.0.2"}))
	id, err = r.AllocateNextID()
	if err != nil {
		t.Fatalf("AllocateNextID after 001: %v", err)
	}
	if id != "002" {
		t.Errorf("second allocation = %q want 002", id)
	}

	must(t, r.Save(Node{ID: "002", Alias: "Singapore", Domain: "sg.example.com", WGIP: "10.10.0.3"}))
	must(t, r.Delete("001"))
	id, err = r.AllocateNextID()
	if err != nil {
		t.Fatalf("AllocateNextID after delete: %v", err)
	}
	if id != "001" {
		t.Errorf("recycled allocation = %q want 001", id)
	}
}

func TestRegistrySaveAndLoad(t *testing.T) {
	r := tempRegistry(t)
	want := Node{
		ID:                     "007",
		Alias:                  "Hong Kong",
		PublicIP:               "203.0.113.7",
		Domain:                 "hk.example.com",
		WGIP:                   "10.10.0.7",
		WGPublicKey:            "WGPUB==",
		APIToken:               "supersecrettoken",
		EnabledProtocols:       []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		Ports:                  config.Ports{RealityVision: 443, Hysteria2: 34567},
		RealityServerName:      "www.microsoft.com",
		RealityHandshakePort:   443,
		MonitorInterface:       "eth0",
		MonitorIntervalSeconds: 60,
		TrafficInLimitBytes:    100 << 30,
		TrafficTotalLimitBytes: 200 << 30,
		ResetDay:               1,
		ResetHour:              0,
		Creds: deploy.Credentials{
			RealityVisionUUID: "uuid-1",
			HysteriaPassword:  "h2-password",
			RealityPrivateKey: "rpriv",
			RealityPublicKey:  "rpub",
			RealityShortID:    "abcd",
		},
	}
	if err := r.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Load("007")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch\nwant %+v\ngot  %+v", want, got)
	}
}

func TestRegistryListEmpty(t *testing.T) {
	r := tempRegistry(t)
	got, err := r.List()
	if err != nil {
		t.Fatalf("List on empty registry: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list got %d", len(got))
	}
}

func TestRegistryListSorted(t *testing.T) {
	r := tempRegistry(t)
	must(t, r.Save(Node{ID: "003", Alias: "C", WGIP: "10.10.0.4"}))
	must(t, r.Save(Node{ID: "001", Alias: "A", WGIP: "10.10.0.2"}))
	must(t, r.Save(Node{ID: "002", Alias: "B", WGIP: "10.10.0.3"}))
	nodes, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes got %d", len(nodes))
	}
	if nodes[0].ID != "001" || nodes[1].ID != "002" || nodes[2].ID != "003" {
		t.Errorf("not sorted: %s %s %s", nodes[0].ID, nodes[1].ID, nodes[2].ID)
	}
}

func TestRegistryAssignedWGIPs(t *testing.T) {
	r := tempRegistry(t)
	must(t, r.Save(Node{ID: "001", WGIP: "10.10.0.2"}))
	must(t, r.Save(Node{ID: "003", WGIP: "10.10.0.4"}))
	ips, err := r.AssignedWGIPs()
	if err != nil {
		t.Fatalf("AssignedWGIPs: %v", err)
	}
	if len(ips) != 2 || ips[0] != "10.10.0.2" || ips[1] != "10.10.0.4" {
		t.Errorf("unexpected ips: %v", ips)
	}
}

func TestRegistryMasterKeys(t *testing.T) {
	r := tempRegistry(t)
	if _, err := r.MasterKeys(); err != ErrNoMasterKeys {
		t.Errorf("MasterKeys on empty registry = %v want ErrNoMasterKeys", err)
	}
	first, err := r.EnsureMasterKeys()
	if err != nil {
		t.Fatalf("EnsureMasterKeys: %v", err)
	}
	if first.PrivateKey == "" || first.PublicKey == "" {
		t.Fatalf("EnsureMasterKeys returned empty pair")
	}
	second, err := r.EnsureMasterKeys()
	if err != nil {
		t.Fatalf("EnsureMasterKeys second call: %v", err)
	}
	if second.PrivateKey != first.PrivateKey || second.PublicKey != first.PublicKey {
		t.Errorf("EnsureMasterKeys should be idempotent: %+v vs %+v", first, second)
	}
	loaded, err := r.MasterKeys()
	if err != nil {
		t.Fatalf("MasterKeys after Ensure: %v", err)
	}
	if loaded != first {
		t.Errorf("MasterKeys did not round-trip: %+v vs %+v", first, loaded)
	}
}

func TestParseProtocols(t *testing.T) {
	got := parseProtocols("hysteria2,vless-reality-vision,unknown,tuic,hysteria2")
	want := []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2, config.ProtocolTUIC}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseProtocols = %v want %v", got, want)
	}
}

func TestNodeHasTLSProtocol(t *testing.T) {
	tests := []struct {
		name string
		ps   []config.Protocol
		want bool
	}{
		{"reality only", []config.Protocol{config.ProtocolRealityVision}, false},
		{"hysteria2", []config.Protocol{config.ProtocolHysteria2}, true},
		{"mixed", []config.Protocol{config.ProtocolRealityVision, config.ProtocolAnyTLS}, true},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := Node{EnabledProtocols: tt.ps}
			if got := n.HasTLSProtocol(); got != tt.want {
				t.Errorf("HasTLSProtocol = %v want %v", got, tt.want)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
