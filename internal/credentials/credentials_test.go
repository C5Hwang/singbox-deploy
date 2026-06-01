package credentials

import (
	"encoding/base64"
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDFormatAndUniqueness(t *testing.T) {
	a, err := UUID()
	if err != nil {
		t.Fatalf("UUID error: %v", err)
	}
	if !uuidRE.MatchString(a) {
		t.Fatalf("UUID %q is not a valid v4", a)
	}
	b, _ := UUID()
	if a == b {
		t.Fatalf("UUIDs should be unique")
	}
}

func TestPasswordLengthAndUniqueness(t *testing.T) {
	p, err := Password()
	if err != nil {
		t.Fatalf("Password error: %v", err)
	}
	if len(p) < 16 {
		t.Fatalf("password too short: %q", p)
	}
	q, _ := Password()
	if p == q {
		t.Fatalf("passwords should be unique")
	}
}

func TestRealityKeypair(t *testing.T) {
	kp, err := RealityKeypair()
	if err != nil {
		t.Fatalf("RealityKeypair error: %v", err)
	}
	priv, err := base64.RawURLEncoding.DecodeString(kp.PrivateKey)
	if err != nil {
		t.Fatalf("private key not base64url: %v", err)
	}
	pub, err := base64.RawURLEncoding.DecodeString(kp.PublicKey)
	if err != nil {
		t.Fatalf("public key not base64url: %v", err)
	}
	if len(priv) != 32 || len(pub) != 32 {
		t.Fatalf("key lengths = %d/%d, want 32/32", len(priv), len(pub))
	}
	if kp.PrivateKey == kp.PublicKey {
		t.Fatalf("private and public keys must differ")
	}
}

func TestShortID(t *testing.T) {
	id, err := ShortID()
	if err != nil {
		t.Fatalf("ShortID error: %v", err)
	}
	if len(id) != 16 {
		t.Fatalf("short id len = %d, want 16 hex chars", len(id))
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{16}$`, id); !matched {
		t.Fatalf("short id not hex: %q", id)
	}
}
