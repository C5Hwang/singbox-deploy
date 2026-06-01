// Package credentials generates the single user's secrets: UUIDs, passwords,
// Reality key pairs, and Reality short IDs. All randomness comes from
// crypto/rand.
package credentials

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// UUID returns a random RFC 4122 version-4 UUID string.
func UUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Password returns a 24-character URL-safe random password (~144 bits).
func Password() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// RealityKeyPair holds a base64url-encoded X25519 key pair for Reality.
type RealityKeyPair struct {
	PrivateKey string
	PublicKey  string
}

// RealityKeypair generates an X25519 key pair encoded as raw-url base64, the
// form sing-box/Reality expects.
func RealityKeypair() (RealityKeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return RealityKeyPair{}, err
	}
	return RealityKeyPair{
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		PublicKey:  base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
	}, nil
}

// ShortID returns a random 8-byte Reality short ID as 16 hex characters.
func ShortID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Salt returns a random salt for subscription token derivation.
func Salt() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
