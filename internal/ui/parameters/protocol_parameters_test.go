package parameters

import "testing"

func TestRealitySNIDefaultIsSharedByInstallAndEdit(t *testing.T) {
	if got := RealitySNIField().Def; got != DefaultRealityServerName {
		t.Fatalf("install default = %q, want %q", got, DefaultRealityServerName)
	}
	if got := RealitySNIEditField("").Def; got != DefaultRealityServerName {
		t.Fatalf("edit default = %q, want %q", got, DefaultRealityServerName)
	}
	if got := RealitySNIEditField("www.example.com").Def; got != "www.example.com" {
		t.Fatalf("edit current value = %q, want www.example.com", got)
	}
}
