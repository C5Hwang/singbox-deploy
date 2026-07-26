package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
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

func TestSynchronizedBufferConcurrentWrites(t *testing.T) {
	const (
		writers = 64
		writes  = 100
	)
	var (
		buf synchronizedBuffer
		wg  sync.WaitGroup
	)
	wg.Add(writers)
	for writer := 0; writer < writers; writer++ {
		go func(writer int) {
			defer wg.Done()
			chunk := fmt.Sprintf("writer-%02d\n", writer)
			for range writes {
				if _, err := buf.Write([]byte(chunk)); err != nil {
					t.Errorf("Write() error = %v", err)
					return
				}
			}
		}(writer)
	}
	wg.Wait()

	output := buf.String()
	for writer := 0; writer < writers; writer++ {
		chunk := fmt.Sprintf("writer-%02d\n", writer)
		if got := strings.Count(output, chunk); got != writes {
			t.Errorf("chunk %q count = %d, want %d", chunk, got, writes)
		}
	}
}
