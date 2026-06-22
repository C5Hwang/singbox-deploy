package cluster

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestEnsureNodeProtocolPortsAllocatesForNewlyEnabled(t *testing.T) {
	registry := NewRegistry(paths.LayoutForRoot(t.TempDir()))
	node := Node{ID: "001", Alias: "Tokyo", Ports: config.Ports{RealityVision: 27443}}
	if err := registry.Save(node); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	target := []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2}
	updated, err := EnsureNodeProtocolPorts(registry, node, target, nil)
	if err != nil {
		t.Fatalf("EnsureNodeProtocolPorts: %v", err)
	}
	if updated.Ports.RealityVision != 27443 {
		t.Errorf("existing port should be preserved, got %d", updated.Ports.RealityVision)
	}
	if updated.Ports.Hysteria2 == 0 {
		t.Errorf("newly enabled protocol got no allocated port")
	}
	if updated.Ports.Hysteria2 == 443 {
		t.Errorf("newly allocated port was 443, must skip the masquerade port")
	}
	if updated.Ports.Hysteria2 == updated.Ports.RealityVision {
		t.Errorf("new port collides with existing: both %d", updated.Ports.Hysteria2)
	}

	reloaded, err := registry.Load("001")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Ports.Hysteria2 != updated.Ports.Hysteria2 {
		t.Errorf("registry was not updated: stored=%d returned=%d", reloaded.Ports.Hysteria2, updated.Ports.Hysteria2)
	}
}

func TestEnsureNodeProtocolPortsHonoursOverrides(t *testing.T) {
	registry := NewRegistry(paths.LayoutForRoot(t.TempDir()))
	node := Node{ID: "001", Ports: config.Ports{Hysteria2: 28000}}
	if err := registry.Save(node); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	updated, err := EnsureNodeProtocolPorts(registry, node,
		[]config.Protocol{config.ProtocolHysteria2, config.ProtocolTUIC},
		map[config.Protocol]int{config.ProtocolTUIC: 28100})
	if err != nil {
		t.Fatalf("EnsureNodeProtocolPorts: %v", err)
	}
	if updated.Ports.Hysteria2 != 28000 {
		t.Errorf("preserved port changed unexpectedly: %d", updated.Ports.Hysteria2)
	}
	if updated.Ports.TUIC != 28100 {
		t.Errorf("override not applied: got %d, want 28100", updated.Ports.TUIC)
	}
}

func TestEnsureNodeProtocolPortsRejectsOverride443(t *testing.T) {
	registry := NewRegistry(paths.LayoutForRoot(t.TempDir()))
	node := Node{ID: "001"}
	if err := registry.Save(node); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	_, err := EnsureNodeProtocolPorts(registry, node,
		[]config.Protocol{config.ProtocolHysteria2},
		map[config.Protocol]int{config.ProtocolHysteria2: 443})
	if err == nil {
		t.Fatalf("expected error for 443 override")
	}
}

func TestEnsureNodeProtocolPortsRejectsOverrideCollision(t *testing.T) {
	registry := NewRegistry(paths.LayoutForRoot(t.TempDir()))
	node := Node{ID: "001", Ports: config.Ports{RealityVision: 27443}}
	if err := registry.Save(node); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	_, err := EnsureNodeProtocolPorts(registry, node,
		[]config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
		map[config.Protocol]int{config.ProtocolHysteria2: 27443})
	if err == nil {
		t.Fatalf("expected collision error for override matching another protocol's port")
	}
}
