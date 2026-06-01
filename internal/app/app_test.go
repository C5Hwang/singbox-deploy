package app

import "testing"

func TestMetadata(t *testing.T) {
	info := Metadata()
	if info.Name != "singbox-deploy" {
		t.Fatalf("Name = %q", info.Name)
	}
	if info.ConfigRoot != "/etc/singbox-deploy" {
		t.Fatalf("ConfigRoot = %q", info.ConfigRoot)
	}
	if info.BinaryPath != "/usr/bin/singbox-deploy" {
		t.Fatalf("BinaryPath = %q", info.BinaryPath)
	}
}
