package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPinnedHostKeyCallback(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	if err := pinnedHostKeyCallback(fingerprint)("host", nil, signer.PublicKey()); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}
	if err := pinnedHostKeyCallback("SHA256:not-the-key")("host", nil, signer.PublicKey()); err == nil || !strings.Contains(err.Error(), fingerprint) {
		t.Fatalf("mismatched fingerprint was not clearly rejected: %v", err)
	}
}

func TestSSHTargetAddressIPv6(t *testing.T) {
	for _, host := range []string{"2001:db8::1", "[2001:db8::1]"} {
		if got := sshTargetAddress(Target{Host: host, Port: 2222}); got != "[2001:db8::1]:2222" {
			t.Fatalf("sshTargetAddress(%q) = %q", host, got)
		}
	}
}
