package acme

import (
	"context"
	"testing"
)

func TestChallengeSelection(t *testing.T) {
	if ChallengeHTTP01.String() != "http-01" {
		t.Fatalf("bad http challenge")
	}
	if ChallengeDNS01.String() != "dns-01" {
		t.Fatalf("bad dns challenge")
	}
}

func TestSupportedDNSProviders(t *testing.T) {
	if !SupportedDNSProvider("cloudflare") {
		t.Fatalf("cloudflare must be supported")
	}
	if !SupportedDNSProvider("aliyun") {
		t.Fatalf("aliyun must be supported")
	}
	if SupportedDNSProvider("route53") {
		t.Fatalf("route53 must not be supported in MVP")
	}
}

func TestRequestValidate(t *testing.T) {
	valid := Request{Domain: "example.com", Email: "a@b.com", Challenge: ChallengeHTTP01}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	withoutEmail := Request{Domain: "example.com", Challenge: ChallengeHTTP01}
	if err := withoutEmail.Validate(); err != nil {
		t.Fatalf("request without email rejected: %v", err)
	}
	dnsNoProvider := Request{Domain: "example.com", Email: "a@b.com", Challenge: ChallengeDNS01}
	if err := dnsNoProvider.Validate(); err == nil {
		t.Fatalf("dns-01 without provider should be invalid")
	}
	dnsBadProvider := Request{Domain: "example.com", Email: "a@b.com", Challenge: ChallengeDNS01, DNSProvider: "route53"}
	if err := dnsBadProvider.Validate(); err == nil {
		t.Fatalf("unsupported dns provider should be invalid")
	}
}

type fakeIssuer struct {
	got Request
	ret Certificate
}

func (f *fakeIssuer) Issue(_ context.Context, r Request) (Certificate, error) {
	f.got = r
	return f.ret, nil
}

func TestManagerObtainDelegatesToIssuer(t *testing.T) {
	fake := &fakeIssuer{ret: Certificate{CertificatePEM: []byte("CERT"), PrivateKeyPEM: []byte("KEY")}}
	m := NewManager(fake)
	cert, err := m.Obtain(context.Background(), Request{Domain: "example.com", Email: "a@b.com", Challenge: ChallengeHTTP01})
	if err != nil {
		t.Fatalf("Obtain error: %v", err)
	}
	if string(cert.CertificatePEM) != "CERT" {
		t.Fatalf("unexpected cert: %s", cert.CertificatePEM)
	}
	if fake.got.Domain != "example.com" {
		t.Fatalf("issuer did not receive request")
	}
}

func TestManagerObtainRejectsInvalid(t *testing.T) {
	m := NewManager(&fakeIssuer{})
	if _, err := m.Obtain(context.Background(), Request{Challenge: ChallengeDNS01}); err == nil {
		t.Fatalf("expected validation error")
	}
}
