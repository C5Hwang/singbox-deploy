package hubctl

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestCertificateConsumersKeepStableIDSeparateFromDisplayLabel(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const domain = "uk.example.com"
	if err := nodes.Add(layout, nodes.Node{
		Alias:     "UK UI",
		SSHHost:   "192.0.2.10",
		Domain:    domain,
		WGIP:      "10.90.0.2",
		Installed: true,
	}); err != nil {
		t.Fatalf("add spoke: %v", err)
	}
	list, err := nodes.Load(layout)
	if err != nil {
		t.Fatalf("load spoke: %v", err)
	}
	if len(list) != 1 || list[0].ID == "" {
		t.Fatalf("unexpected spoke registry: %+v", list)
	}
	stableID := list[0].ID

	if err := nodes.Mutate(layout, stableID, func(node *nodes.Node) error {
		node.Alias = "London Edge"
		return nil
	}); err != nil {
		t.Fatalf("rename spoke: %v", err)
	}

	consumers, err := (&Controller{Layout: layout}).CertificateConsumers(domain)
	if err != nil {
		t.Fatalf("CertificateConsumers: %v", err)
	}
	if len(consumers) != 1 {
		t.Fatalf("consumer count = %d, want 1: %+v", len(consumers), consumers)
	}
	if consumers[0].ID != stableID {
		t.Errorf("consumer stable ID = %q, want %q", consumers[0].ID, stableID)
	}
	if consumers[0].Label != "London Edge (uk.example.com)" {
		t.Errorf("consumer label = %q", consumers[0].Label)
	}
	if consumers[0].Label == consumers[0].ID {
		t.Errorf("raw stable ID leaked into operator label: %+v", consumers[0])
	}
}
