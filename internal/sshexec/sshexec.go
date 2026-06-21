// Package sshexec wraps golang.org/x/crypto/ssh for the one-shot provisioning
// path: open a session, run a command (or transfer a file), close. SSH is used
// only during initial node setup; afterwards the master and node communicate
// over the internal WireGuard network via the node agent HTTP API.
package sshexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Auth holds the credentials needed to log in to a remote host.
type Auth struct {
	User           string
	Password       string // either Password OR PrivateKeyPath must be set
	PrivateKeyPath string
	Passphrase     string // optional, used only with an encrypted private key
}

// Target identifies a remote host.
type Target struct {
	Host string // hostname or IP
	Port int    // defaults to 22 when zero
}

// Endpoint returns the dial address "host:port".
func (t Target) Endpoint() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// Client is a single connected SSH client. Use Close to release the
// underlying network connection.
type Client struct {
	c      *ssh.Client
	target Target
}

// Dial connects to target and authenticates with auth, returning a Client.
// HostKeyCallback is set to ssh.InsecureIgnoreHostKey: this is a deliberate
// trade-off for one-shot provisioning of a brand-new VM where no host key is
// known in advance. The TUI prompts for and confirms the host fingerprint
// before calling this; afterwards traffic flows over WireGuard, not SSH.
func Dial(ctx context.Context, target Target, auth Auth) (*Client, error) {
	if strings.TrimSpace(target.Host) == "" {
		return nil, fmt.Errorf("ssh target host is empty")
	}
	if strings.TrimSpace(auth.User) == "" {
		return nil, fmt.Errorf("ssh user is empty")
	}
	method, err := authMethod(auth)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            auth.User,
		Auth:            []ssh.AuthMethod{method},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target.Endpoint())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target.Endpoint(), err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, target.Endpoint(), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", target.Endpoint(), err)
	}
	return &Client{c: ssh.NewClient(c, chans, reqs), target: target}, nil
}

// Close releases the SSH connection.
func (c *Client) Close() error {
	if c == nil || c.c == nil {
		return nil
	}
	return c.c.Close()
}

// RunResult is the captured output of one remote command invocation.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes cmd on the remote host and returns its captured output. A
// non-zero exit code is reported via RunResult.ExitCode and not as an error,
// so callers can decide whether the exit status indicates failure for their
// specific command. Real failures (transport errors, signals) come back as
// the second return value.
func (c *Client) Run(ctx context.Context, cmd string) (RunResult, error) {
	session, err := c.c.NewSession()
	if err != nil {
		return RunResult{}, fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return RunResult{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case err := <-done:
		result := RunResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			result.ExitCode = 0
			return result, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitStatus()
			return result, nil
		}
		return result, err
	}
}

// MustRun runs cmd and returns an error if the exit code is non-zero or
// transport failed. Used for commands where any non-zero status is fatal.
func (c *Client) MustRun(ctx context.Context, cmd string) (RunResult, error) {
	result, err := c.Run(ctx, cmd)
	if err != nil {
		return result, err
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("remote %q exited %d: %s", cmd, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result, nil
}

// WriteFile creates remotePath on the remote host with the given content and
// 0600 permissions. Parent directories are created with 0700. The transfer
// goes through `cat > path` over a stdin pipe to avoid requiring SFTP.
func (c *Client) WriteFile(ctx context.Context, remotePath string, content []byte, mode os.FileMode) error {
	dir := path.Dir(remotePath)
	if dir != "" && dir != "." && dir != "/" {
		if _, err := c.MustRun(ctx, fmt.Sprintf("install -d -m 0700 %s", shellEscape(dir))); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	session, err := c.c.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	// `cat` reads until EOF on stdin, then chmod restricts permissions.
	remote := fmt.Sprintf("cat > %s && chmod %o %s", shellEscape(remotePath), mode.Perm(), shellEscape(remotePath))
	if err := session.Start(remote); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if _, err := io.Copy(stdin, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close stdin: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write %s: %w: %s", remotePath, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// authMethod selects the ssh.AuthMethod for the given Auth.
func authMethod(auth Auth) (ssh.AuthMethod, error) {
	if auth.Password != "" {
		return ssh.Password(auth.Password), nil
	}
	if auth.PrivateKeyPath == "" {
		return nil, fmt.Errorf("ssh auth requires either Password or PrivateKeyPath")
	}
	body, err := os.ReadFile(auth.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	var signer ssh.Signer
	if auth.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(body, []byte(auth.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(body)
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return ssh.PublicKeys(signer), nil
}

// shellEscape wraps s in single quotes, escaping any embedded single quotes.
// Suitable for passing arbitrary paths to a remote shell.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
