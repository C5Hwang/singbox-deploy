package wireguard

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyPair is a WireGuard Curve25519 key pair, base64-encoded as wg-quick
// expects them.
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair returns a fresh WireGuard key pair. The private key is
// generated from the system CSPRNG and clamped per Curve25519 rules; the
// public key is derived via X25519 scalar multiplication of the base point.
func GenerateKeyPair() (KeyPair, error) {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(priv); err != nil {
		return KeyPair{}, fmt.Errorf("read random: %w", err)
	}
	// Curve25519 clamp.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("derive public key: %w", err)
	}
	return KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// ValidKey reports whether s decodes to a 32-byte raw key, the size accepted
// by wg-quick for both private and public keys.
func ValidKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	return len(b) == curve25519.ScalarSize
}
