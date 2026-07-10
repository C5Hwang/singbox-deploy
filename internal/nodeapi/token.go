package nodeapi

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateToken returns a 32-byte random bearer token as hex. The hub mints one
// per node during bootstrap and hands it to the agent.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
