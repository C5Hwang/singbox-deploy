// Package wgnet manages the WireGuard control-plane network that links the hub
// node to its spokes. Key generation is pure Go (no external wg binary needed),
// while interface lifecycle and live peer changes go through a system.Runner so
// the flow is exercisable with a recording runner.
//
// The network is a hub-and-spoke overlay: the hub owns 10.90.0.1 and every
// spoke gets a /32 inside the same /24. All control traffic — subscription
// aggregation, monitor metrics, cert delivery, agent RPC — rides this overlay
// instead of the public internet.
package wgnet

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyPair is a WireGuard key pair encoded in the standard base64 form the wg
// tools use (44 characters, "=" padded).
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair produces a fresh Curve25519 key pair in WireGuard's on-disk
// format. The private scalar is clamped exactly as wg genkey does so the
// derived public key matches what `wg pubkey` would compute.
func GenerateKeyPair() (KeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("read random key: %w", err)
	}
	clampPrivateKey(&priv)
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("derive public key: %w", err)
	}
	return KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// PublicKeyFromPrivate derives the base64 public key for a base64 private key.
func PublicKeyFromPrivate(privateKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(raw))
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// ValidKey reports whether s decodes to a 32-byte WireGuard key.
func ValidKey(s string) bool {
	raw, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(raw) == 32
}

func clampPrivateKey(k *[32]byte) {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}
