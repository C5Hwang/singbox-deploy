package system

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SingBoxConflictCheck detects existing sing-box services or binaries that are
// not managed by this project. Managed resources are allowed so reinstall can
// proceed against the canonical layout.
type SingBoxConflictCheck struct {
	ServicePaths   []string
	ExpectedBinary string
	ExpectedConfig string
	LookPath       func(string) (string, error)
}

// Check reports a conflict when sing-box.service exists but does not point to
// the managed binary/config, or when a sing-box binary already exists in PATH.
func (c SingBoxConflictCheck) Check() error {
	for _, path := range c.servicePaths() {
		body, err := os.ReadFile(path)
		switch {
		case err == nil:
			if !managedSingBoxUnit(string(body), c.ExpectedBinary, c.ExpectedConfig) {
				return fmt.Errorf("existing %s is not managed by singbox-deploy; remove or disable it before installation", path)
			}
		case os.IsNotExist(err):
			continue
		default:
			return fmt.Errorf("check %s: %w", path, err)
		}
	}

	lookPath := c.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	found, err := lookPath("sing-box")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return nil
		}
		return fmt.Errorf("check sing-box in PATH: %w", err)
	}
	if !samePath(found, c.ExpectedBinary) {
		return fmt.Errorf("existing sing-box binary %s conflicts with managed binary %s; remove it from PATH before installation", found, c.ExpectedBinary)
	}
	return nil
}

func (c SingBoxConflictCheck) servicePaths() []string {
	if len(c.ServicePaths) > 0 {
		return uniqueStrings(c.ServicePaths)
	}
	return []string{
		"/etc/systemd/system/" + SingBoxService,
		"/usr/lib/systemd/system/" + SingBoxService,
		"/lib/systemd/system/" + SingBoxService,
	}
}

func managedSingBoxUnit(body, expectedBinary, expectedConfig string) bool {
	return expectedBinary != "" && expectedConfig != "" &&
		strings.Contains(body, expectedBinary) && strings.Contains(body, expectedConfig)
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = normalizePath(left)
	right = normalizePath(right)
	return left == right
}

func normalizePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// PortCheckTimeout is the per-port timeout used for public reachability probes.
var PortCheckTimeout = 2 * time.Second

// CheckPorts verifies that every required port can be bound locally. Ports with
// Public=true are also probed through host:port while the temporary listener is
// active, which catches common firewall/security-group failures.
func CheckPorts(ctx context.Context, host string, ports []Port) error {
	var failures []string
	for _, p := range uniquePorts(ports) {
		if p.Number <= 0 {
			continue
		}
		if err := checkPort(ctx, host, p); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", portLabel(p), err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("port check failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func uniquePorts(ports []Port) []Port {
	seen := map[string]bool{}
	var out []Port
	for _, p := range ports {
		key := fmt.Sprintf("%s/%d", strings.ToLower(p.Proto), p.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

func checkPort(ctx context.Context, host string, p Port) error {
	switch strings.ToLower(p.Proto) {
	case "tcp":
		return checkTCPPort(ctx, host, p)
	case "udp":
		return checkUDPPort(ctx, host, p)
	default:
		return fmt.Errorf("unsupported protocol %q", p.Proto)
	}
}

func checkTCPPort(ctx context.Context, host string, p Port) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("", fmt.Sprint(p.Number)))
	if err != nil {
		return fmt.Errorf("local bind failed: %w", err)
	}
	defer ln.Close()
	if !p.Public {
		return nil
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("public probe host is empty")
	}
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	dialCtx, cancel := context.WithTimeout(ctx, PortCheckTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(host, fmt.Sprint(p.Number)))
	if err != nil {
		return fmt.Errorf("public TCP probe failed: %w", err)
	}
	_ = conn.Close()
	select {
	case <-accepted:
	case <-time.After(PortCheckTimeout):
		return fmt.Errorf("public TCP probe connected but was not accepted")
	}
	return nil
}

func checkUDPPort(ctx context.Context, host string, p Port) error {
	pc, err := net.ListenPacket("udp", net.JoinHostPort("", fmt.Sprint(p.Number)))
	if err != nil {
		return fmt.Errorf("local bind failed: %w", err)
	}
	defer pc.Close()
	if !p.Public {
		return nil
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("public probe host is empty")
	}

	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generate UDP probe token: %w", err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, PortCheckTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "udp", net.JoinHostPort(host, fmt.Sprint(p.Number)))
	if err != nil {
		return fmt.Errorf("public UDP probe setup failed: %w", err)
	}
	defer conn.Close()
	if err := pc.SetReadDeadline(time.Now().Add(PortCheckTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write(token); err != nil {
		return fmt.Errorf("public UDP probe send failed: %w", err)
	}
	buf := make([]byte, len(token))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		return fmt.Errorf("public UDP probe failed: %w", err)
	}
	if n != len(token) || string(buf) != string(token) {
		return fmt.Errorf("public UDP probe received unexpected payload")
	}
	return nil
}

func portLabel(p Port) string {
	label := strings.TrimSpace(p.Label)
	if label == "" {
		return fmt.Sprintf("%s/%d", strings.ToLower(p.Proto), p.Number)
	}
	return fmt.Sprintf("%s %s/%d", label, strings.ToLower(p.Proto), p.Number)
}
