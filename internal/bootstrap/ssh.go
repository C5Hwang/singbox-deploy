package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshRunner runs commands over an established SSH connection, opening a fresh
// session per command (SSH sessions are single-use).
type sshRunner struct {
	client *ssh.Client
}

const sshDialTimeout = 20 * time.Second

var errHostKeyCaptured = errors.New("SSH host key captured")

// HostKeyInfo is the identity presented by a server during the SSH handshake.
// Fingerprint uses OpenSSH's SHA256:<base64> representation.
type HostKeyInfo struct {
	Algorithm   string
	Fingerprint string
}

// scanSSHHostKey performs an unauthenticated handshake solely to obtain the
// server key. The callback deliberately aborts before credentials are sent;
// the operator must confirm the returned fingerprint before a real connection.
func scanSSHHostKey(ctx context.Context, target Target) (HostKeyInfo, error) {
	if err := validateRootUser(target.User); err != nil {
		return HostKeyInfo{}, err
	}
	if strings.TrimSpace(target.Host) == "" {
		return HostKeyInfo{}, fmt.Errorf("SSH host is required")
	}
	address := sshTargetAddress(target)
	var captured HostKeyInfo
	cfg := &ssh.ClientConfig{
		User: defaultSSHUser(target.User),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = HostKeyInfo{
				Algorithm:   key.Type(),
				Fingerprint: ssh.FingerprintSHA256(key),
			}
			return errHostKeyCaptured
		},
		Timeout: sshDialTimeout,
	}
	dialer := net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return HostKeyInfo{}, fmt.Errorf("dial %s: %w", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sshDialTimeout))
	_, _, _, err = ssh.NewClientConn(conn, address, cfg)
	if captured.Fingerprint != "" && (err == nil || errors.Is(err, errHostKeyCaptured)) {
		return captured, nil
	}
	if captured.Fingerprint != "" {
		// Some x/crypto/ssh versions wrap callback failures without preserving
		// the sentinel. A captured key is still safe to display because this
		// handshake never accepted it and therefore never reached authentication.
		return captured, nil
	}
	if err == nil {
		return HostKeyInfo{}, fmt.Errorf("SSH handshake with %s returned no host key", address)
	}
	return HostKeyInfo{}, fmt.Errorf("scan SSH host key from %s: %w", address, err)
}

// dialSSH connects using a fingerprint that the operator confirmed during the
// preceding scan. Connections without a pin, or to a server presenting a
// different key, are rejected before any authentication material is sent.
func dialSSH(ctx context.Context, target Target) (Runner, error) {
	if err := validateRootUser(target.User); err != nil {
		return nil, err
	}
	expectedFingerprint := strings.TrimSpace(target.HostKeyFingerprint)
	if expectedFingerprint == "" {
		return nil, fmt.Errorf("SSH host key fingerprint is required; scan and confirm the server key first")
	}
	authMethods, err := authMethods(target.Auth)
	if err != nil {
		return nil, err
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH authentication method provided")
	}
	address := sshTargetAddress(target)
	cfg := &ssh.ClientConfig{
		User:            defaultSSHUser(target.User),
		Auth:            authMethods,
		HostKeyCallback: pinnedHostKeyCallback(expectedFingerprint),
		Timeout:         sshDialTimeout,
	}
	dialer := net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	_ = conn.SetDeadline(time.Now().Add(sshDialTimeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", address, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return &sshRunner{client: ssh.NewClient(sshConn, chans, reqs)}, nil
}

func pinnedHostKeyCallback(expectedFingerprint string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if actual != expectedFingerprint {
			return fmt.Errorf("SSH host key mismatch: expected %s, server presented %s", expectedFingerprint, actual)
		}
		return nil
	}
}

func defaultSSHUser(user string) string {
	if strings.TrimSpace(user) == "" {
		return "root"
	}
	return strings.TrimSpace(user)
}

func validateRootUser(user string) error {
	if user = defaultSSHUser(user); user != "root" {
		return fmt.Errorf("SSH bootstrap requires the root user (got %q)", user)
	}
	return nil
}

func sshTargetAddress(target Target) string {
	port := target.Port
	if port == 0 {
		port = 22
	}
	host := strings.TrimSpace(target.Host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func authMethods(auth Auth) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if len(auth.PrivateKeyPEM) > 0 {
		var signer ssh.Signer
		var err error
		if auth.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(auth.PrivateKeyPEM, []byte(auth.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(auth.PrivateKeyPEM)
		}
		if err != nil {
			return nil, fmt.Errorf("parse SSH private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if auth.Password != "" {
		methods = append(methods, ssh.Password(auth.Password))
		// Some servers require keyboard-interactive for password auth.
		methods = append(methods, ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = auth.Password
			}
			return answers, nil
		}))
	}
	return methods, nil
}

// Run opens a session, feeds stdin, and returns combined stdout/stderr.
func (r *sshRunner) Run(ctx context.Context, cmd string, stdin []byte) (string, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out
	if len(stdin) > 0 {
		session.Stdin = bytes.NewReader(stdin)
	}

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return out.String(), ctx.Err()
	case err := <-done:
		return out.String(), err
	}
}

func (r *sshRunner) Close() error {
	return r.client.Close()
}
