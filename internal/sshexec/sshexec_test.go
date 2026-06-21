package sshexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   string
	}{
		{name: "default port", target: Target{Host: "example.com"}, want: "example.com:22"},
		{name: "explicit port", target: Target{Host: "1.2.3.4", Port: 2222}, want: "1.2.3.4:2222"},
		{name: "ipv6", target: Target{Host: "::1", Port: 22}, want: "[::1]:22"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.target.Endpoint(); got != tt.want {
				t.Errorf("Endpoint = %q want %q", got, tt.want)
			}
		})
	}
}

func TestShellEscape(t *testing.T) {
	tests := map[string]string{
		"plain":             "'plain'",
		"/path/with spaces": "'/path/with spaces'",
		"don't":             `'don'\''t'`,
	}
	for in, want := range tests {
		if got := shellEscape(in); got != want {
			t.Errorf("shellEscape(%q) = %q want %q", in, got, want)
		}
	}
}

func TestDialValidatesInputs(t *testing.T) {
	_, err := Dial(context.Background(), Target{}, Auth{User: "root", Password: "x"})
	if err == nil {
		t.Errorf("Dial with empty host should fail")
	}
	_, err = Dial(context.Background(), Target{Host: "example.com"}, Auth{Password: "x"})
	if err == nil {
		t.Errorf("Dial with empty user should fail")
	}
}

func TestAuthMethodRequiresCredential(t *testing.T) {
	if _, err := authMethod(Auth{}); err == nil {
		t.Errorf("authMethod with empty Auth should fail")
	}
}

func TestAuthMethodWithPassword(t *testing.T) {
	if _, err := authMethod(Auth{Password: "p"}); err != nil {
		t.Errorf("authMethod with password unexpectedly failed: %v", err)
	}
}

func TestAuthMethodWithMissingKeyFile(t *testing.T) {
	if _, err := authMethod(Auth{PrivateKeyPath: "/does/not/exist"}); err == nil {
		t.Errorf("authMethod with missing key file should fail")
	}
}

func TestAuthMethodWithUnencryptedKey(t *testing.T) {
	// Use a known unencrypted ed25519 key generated for testing only.
	keyPEM := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBTNi/oqPgmiq4xq3PFOWy3D0lQbcOFwEMOyDjMa3J6CwAAAJjJEYBkyRGA
ZAAAAAtzc2gtZWQyNTUxOQAAACBTNi/oqPgmiq4xq3PFOWy3D0lQbcOFwEMOyDjMa3J6Cw
AAAEDTPx27WqaqUSV0K6sZqUtcOZSXIH3kIJsf/6jzC9ASvFM2L+io+CaKrjGrc8U5bLcP
SVBtw4XAQw7IOMxrcnoLAAAAEHRlc3RAZXhhbXBsZS5jb20BAgMEBQ==
-----END OPENSSH PRIVATE KEY-----
`
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, []byte(keyPEM), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if _, err := authMethod(Auth{PrivateKeyPath: path}); err != nil {
		t.Errorf("authMethod with valid ed25519 key failed: %v", err)
	}
}

func TestShellEscapePathSpecials(t *testing.T) {
	// Path with semicolons / dollar signs that would otherwise be interpreted.
	got := shellEscape("/tmp; rm -rf /; echo $HOME")
	if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
		t.Errorf("shellEscape should fully quote: %s", got)
	}
}
