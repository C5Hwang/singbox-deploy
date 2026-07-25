package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

// fakeRunner records commands and returns scripted output.
type fakeRunner struct {
	commands []string
	stdins   [][]byte
	outputs  map[string]string
	errors   map[string]error
}

func (f *fakeRunner) Run(_ context.Context, cmd string, stdin []byte) (string, error) {
	f.commands = append(f.commands, cmd)
	f.stdins = append(f.stdins, stdin)
	for prefix, out := range f.outputs {
		if strings.Contains(cmd, prefix) {
			return out, f.errors[prefix]
		}
	}
	for prefix, err := range f.errors {
		if strings.Contains(cmd, prefix) {
			return "", err
		}
	}
	return "", nil
}

func (f *fakeRunner) Close() error { return nil }

type localShellRunner struct{}

func (localShellRunner) Run(ctx context.Context, command string, stdin []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (localShellRunner) Close() error { return nil }

func TestDetectArch(t *testing.T) {
	cases := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
	for uname, want := range cases {
		runner := &fakeRunner{outputs: map[string]string{"uname -m": uname + "\n"}}
		b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return runner, nil }}
		got, err := b.DetectArch(context.Background(), Target{HostKeyFingerprint: "SHA256:test"})
		if err != nil {
			t.Fatalf("DetectArch(%s): %v", uname, err)
		}
		if got != want {
			t.Fatalf("DetectArch(%s) = %q, want %q", uname, got, want)
		}
	}
	runner := &fakeRunner{outputs: map[string]string{"uname -m": "mips\n"}}
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return runner, nil }}
	if _, err := b.DetectArch(context.Background(), Target{HostKeyFingerprint: "SHA256:test"}); err == nil {
		t.Fatalf("expected error for unsupported arch")
	}
}

func TestProvisionSequence(t *testing.T) {
	keyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{outputs: map[string]string{
		"cat /etc/os-release": "ID=ubuntu\nVERSION_ID=\"22.04\"\nID_LIKE=debian\n",
		"wg pubkey":           keyPair.PublicKey + "\n",
		"--version":           "v1.2.3\n",
	}}
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return runner, nil }}
	plan := Plan{
		AgentBinary:  []byte("BINARY-CONTENT"),
		AgentVersion: "v1.2.3",
		WGAddress:    "10.90.0.2/24",
		HubIP:        "10.90.0.1",
		HubPublicKey: keyPair.PublicKey,
		HubEndpoint:  "hub.example.com:51820",
		Subnet:       wgnet.DefaultSubnet,
		AgentUnit:    "[Unit]\n",
		Token:        "tok123",
		ListenIP:     "10.90.0.2",
		AgentPort:    19091,
		Interface:    "sbwg0",
	}
	result, err := b.Provision(context.Background(), Target{Host: "h", User: "root", HostKeyFingerprint: "SHA256:test"}, plan)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if result.WGPublicKey != keyPair.PublicKey || result.AgentVersion != "v1.2.3" {
		t.Fatalf("unexpected result: %+v", result)
	}
	joined := strings.Join(runner.commands, "\n")

	for _, want := range []string{
		"apt-get",
		"wireguard-tools",
		"wg genkey",
		"wg pubkey",
		"/usr/bin/singbox-deploy-agent",
		"sha256sum --check --status",
		"/etc/wireguard/sbwg0.conf",
		"/etc/singbox-deploy/state/agent/token",
		"/etc/singbox-deploy/state/agent/firewall_backend",
		"/etc/systemd/system/singbox-deploy-agent.service",
		"systemctl restart wg-quick@sbwg0.service",
		"ufw allow in on 'sbwg0' from '10.90.0.1' to '10.90.0.2' port 19091 proto tcp",
		`source address="10.90.0.1/32" destination address="10.90.0.2/32"`,
		"systemctl restart singbox-deploy-agent.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("provision sequence missing %q:\n%s", want, joined)
		}
	}

	// The agent binary content is streamed over stdin, not embedded in the command.
	foundBinary := false
	for _, in := range runner.stdins {
		if string(in) == "BINARY-CONTENT" {
			foundBinary = true
		}
	}
	if !foundBinary {
		t.Fatalf("agent binary was not streamed over stdin")
	}
	// The token file gets the token as its content over stdin.
	foundToken := false
	for i, cmd := range runner.commands {
		if strings.Contains(cmd, "/etc/singbox-deploy/state/agent/token") && strings.Contains(string(runner.stdins[i]), "tok123") {
			foundToken = true
		}
	}
	if !foundToken {
		t.Fatalf("token content not streamed to token file")
	}

	// The hub uploads only a marker; the private key is substituted from the
	// spoke-local key file and never crosses back to the hub.
	foundTemplate := false
	for _, in := range runner.stdins {
		if strings.Contains(string(in), spokePrivateKeyMarker) {
			foundTemplate = true
		}
		if strings.Contains(string(in), keyPair.PrivateKey) {
			t.Fatal("spoke private key appeared in hub-uploaded content")
		}
	}
	if !foundTemplate {
		t.Fatal("wireguard config template was not uploaded")
	}

	firewallIndex, agentIndex := -1, -1
	for i, command := range runner.commands {
		if strings.Contains(command, "firewall_backend") && strings.Contains(command, "ufw allow in") {
			firewallIndex = i
		}
		if strings.Contains(command, "systemctl restart singbox-deploy-agent.service") {
			agentIndex = i
		}
	}
	if firewallIndex < 0 || agentIndex < 0 || firewallIndex >= agentIndex {
		t.Fatalf("scoped firewall must be configured before Agent startup: firewall=%d agent=%d", firewallIndex, agentIndex)
	}
	checkShellSyntax(t, runner.commands[firewallIndex])
}

func TestProvisionRejectsChecksumAndVersionMismatch(t *testing.T) {
	keyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	baseOutputs := map[string]string{
		"cat /etc/os-release": "ID=ubuntu\nID_LIKE=debian\n",
		"wg pubkey":           keyPair.PublicKey + "\n",
		"--version":           "wrong\n",
	}
	plan := Plan{
		AgentBinary: []byte("agent"), AgentVersion: "expected",
		WGAddress: "10.90.0.2/24", HubPublicKey: keyPair.PublicKey,
		HubEndpoint: "hub.example.com:51820", Subnet: wgnet.DefaultSubnet,
		AgentUnit: "unit", Token: "token", ListenIP: "10.90.0.2", AgentPort: 19091,
	}

	checksumRunner := &fakeRunner{outputs: baseOutputs, errors: map[string]error{"sha256sum --check --status": errors.New("bad digest")}}
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return checksumRunner, nil }}
	if _, err := b.Provision(context.Background(), Target{HostKeyFingerprint: "SHA256:test"}, plan); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum failure, got %v", err)
	}

	versionRunner := &fakeRunner{outputs: baseOutputs}
	b.Dial = func(context.Context, Target) (Runner, error) { return versionRunner, nil }
	if _, err := b.Provision(context.Background(), Target{HostKeyFingerprint: "SHA256:test"}, plan); err == nil || !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("expected version mismatch, got %v", err)
	}
}

func TestProvisionRefusesServerThatAlreadyManagesSpokesBeforeMutation(t *testing.T) {
	runner := &fakeRunner{errors: map[string]error{"server already manages spoke nodes": errors.New("active child registry")}}
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return runner, nil }}
	_, err := b.Provision(context.Background(), Target{HostKeyFingerprint: "SHA256:test"}, Plan{
		AgentBinary: []byte("agent"), AgentVersion: "v1",
		ListenIP: "10.90.0.2", AgentPort: 19091,
	})
	if err == nil || !strings.Contains(err.Error(), "active hub") {
		t.Fatalf("expected active-Hub refusal, got %v", err)
	}
	if len(runner.commands) != 1 || strings.Contains(strings.Join(runner.commands, "\n"), "rm -f") {
		t.Fatalf("active Hub was mutated during refusal: %v", runner.commands)
	}
}

func TestBootstrapRequiresRoot(t *testing.T) {
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) {
		t.Fatal("non-root target must be rejected before dialing")
		return nil, nil
	}}
	if _, err := b.DetectArch(context.Background(), Target{User: "ubuntu"}); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("expected root-only error, got %v", err)
	}
}

func TestCleanupRemovesProvisionedAgentAndWireGuardSecrets(t *testing.T) {
	runner := &fakeRunner{}
	b := &Bootstrapper{Dial: func(context.Context, Target) (Runner, error) { return runner, nil }}
	err := b.Cleanup(context.Background(), Target{
		Host: "spoke.example.com", User: "root", HostKeyFingerprint: "SHA256:pinned",
	}, "sbwg0")
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("cleanup commands = %d, want one transaction", len(runner.commands))
	}
	command := runner.commands[0]
	for _, want := range []string{
		"disable --now singbox-deploy-agent.service",
		"disable --now wg-quick@sbwg0.service",
		"/usr/bin/singbox-deploy-agent",
		"/etc/wireguard/sbwg0.conf",
		"/etc/wireguard/sbwg0.key",
		"/etc/singbox-deploy/state/agent",
		"ufw --force delete allow in",
		"--remove-rich-rule",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("cleanup command missing %q:\n%s", want, command)
		}
	}
	checkShellSyntax(t, command)
}

func checkShellSyntax(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid generated shell: %v: %s\n%s", err, out, script)
	}
}

func TestUploadFileUsesStdin(t *testing.T) {
	runner := &fakeRunner{}
	if err := uploadFile(context.Background(), runner, "/tmp/x", []byte("data'with'quotes"), "0644"); err != nil {
		t.Fatalf("uploadFile: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(runner.commands))
	}
	cmd := runner.commands[0]
	for _, want := range []string{"mkdir -p '/tmp'", "rm -f '/tmp/x.singbox-deploy.tmp'", "(umask 077 && cat > '/tmp/x.singbox-deploy.tmp')", "chmod 0644"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("upload command missing %q: %s", want, cmd)
		}
	}
	if strings.HasPrefix(cmd, "umask 077") {
		t.Fatalf("unexpected upload command: %s", cmd)
	}
	if string(runner.stdins[0]) != "data'with'quotes" {
		t.Fatalf("stdin content mismatch: %q", runner.stdins[0])
	}
}

func TestUploadFileReplacesStaleWorldReadableTempPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public-parent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "token")
	tmp := dest + ".singbox-deploy.tmp"
	if err := os.WriteFile(tmp, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := uploadFile(context.Background(), localShellRunner{}, dest, []byte("secret\n"), "0600"); err != nil {
		t.Fatalf("uploadFile: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("destination mode = %04o, want 0600", got)
	}
	if parent, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := parent.Mode().Perm(); got != 0o755 {
		t.Fatalf("parent mode = %04o, want unchanged 0755", got)
	}
	if body, err := os.ReadFile(dest); err != nil {
		t.Fatal(err)
	} else if string(body) != "secret\n" {
		t.Fatalf("destination body = %q", body)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temporary file still exists: %v", err)
	}
}

func TestPrepareAgentConfigDirEnforcesPublicRootAndPrivateState(t *testing.T) {
	layoutRoot := filepath.Join(t.TempDir(), "singbox-deploy")
	agentDir := filepath.Join(layoutRoot, "state", "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Model the bad permissions left by the prefix-wide umask fix: the layout
	// root is not traversable by Nginx, while pre-existing state may be too open.
	for path, mode := range map[string]os.FileMode{
		layoutRoot:                         0o700,
		filepath.Join(layoutRoot, "state"): 0o755,
		agentDir:                           0o755,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareAgentConfigDir(context.Background(), localShellRunner{}, layoutRoot, agentDir); err != nil {
		t.Fatalf("prepareAgentConfigDir: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		layoutRoot:                         0o755,
		filepath.Join(layoutRoot, "state"): 0o700,
		agentDir:                           0o700,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
