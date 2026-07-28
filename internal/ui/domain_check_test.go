package ui

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestValidateDomainReturnsMatchedPublicIPs(t *testing.T) {
	oldDomainLookup := lookupDomainIPs
	oldPublicLookup := lookupCurrentPublicIPs
	t.Cleanup(func() {
		lookupDomainIPs = oldDomainLookup
		lookupCurrentPublicIPs = oldPublicLookup
	})

	lookupDomainIPs = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("203.0.113.10"),
			net.ParseIP("2001:db8::10"),
			net.ParseIP("203.0.113.10"),
		}, nil
	}
	lookupCurrentPublicIPs = func(context.Context) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("198.51.100.8"),
			net.ParseIP("203.0.113.10"),
			net.ParseIP("2001:db8::10"),
		}, nil
	}

	got, err := validateDomainResolvesToCurrentIP(context.Background(), "vpn.example.com")
	if err != nil {
		t.Fatalf("validateDomainResolvesToCurrentIP: %v", err)
	}
	if got != "203.0.113.10, 2001:db8::10" {
		t.Fatalf("matched public IPs = %q", got)
	}
}

func TestValidateDomainRejectsNonMatchingPublicIP(t *testing.T) {
	oldDomainLookup := lookupDomainIPs
	oldPublicLookup := lookupCurrentPublicIPs
	t.Cleanup(func() {
		lookupDomainIPs = oldDomainLookup
		lookupCurrentPublicIPs = oldPublicLookup
	})

	lookupDomainIPs = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	}
	lookupCurrentPublicIPs = func(context.Context) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.51.100.8")}, nil
	}

	_, err := validateDomainResolvesToCurrentIP(context.Background(), "vpn.example.com")
	if err == nil || !strings.Contains(err.Error(), "domain resolves to 203.0.113.10") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}
