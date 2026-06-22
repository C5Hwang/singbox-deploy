package acme

import (
	"context"
	"testing"
)

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
	valid := Request{Domain: "example.com", DNSProvider: "cloudflare"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	noProvider := Request{Domain: "example.com"}
	if err := noProvider.Validate(); err == nil {
		t.Fatalf("request without dns provider should be invalid")
	}
	badProvider := Request{Domain: "example.com", DNSProvider: "route53"}
	if err := badProvider.Validate(); err == nil {
		t.Fatalf("unsupported dns provider should be invalid")
	}
	missingDomain := Request{DNSProvider: "cloudflare"}
	if err := missingDomain.Validate(); err == nil {
		t.Fatalf("missing domain should be invalid")
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
	cert, err := m.Obtain(context.Background(), Request{Domain: "example.com", DNSProvider: "cloudflare"})
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
	if _, err := m.Obtain(context.Background(), Request{}); err == nil {
		t.Fatalf("expected validation error")
	}
}
