package cluster

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/sshexec"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/wireguard"
)

// redirectWGConfig redirects wireguard.ConfigPath to a temp file for the
// duration of the test, restoring the production path on cleanup.
func redirectWGConfig(t *testing.T) {
	t.Helper()
	prev := wireguard.ConfigPath
	wireguard.ConfigPath = filepath.Join(t.TempDir(), "wg-sdeploy.conf")
	t.Cleanup(func() { wireguard.ConfigPath = prev })
}

// fakeSSHClient implements sshClient by replaying canned responses.
type fakeSSHClient struct {
	runs      []string
	writes    map[string][]byte
	responses map[string]sshexec.RunResult // prefix match against the cmd
}

func newFakeSSHClient() *fakeSSHClient {
	return &fakeSSHClient{
		writes: map[string][]byte{},
		responses: map[string]sshexec.RunResult{
			"command -v apt-get":          {ExitCode: 0},
			"command -v dnf":              {ExitCode: 1},
			"command -v yum":              {ExitCode: 1},
			"apt-get update":              {ExitCode: 0},
			"uname -m":                    {Stdout: "x86_64\n", ExitCode: 0},
			"curl -fsSL":                  {ExitCode: 0},
			"systemctl enable --now wg":   {ExitCode: 0},
			"systemctl daemon-reload &&":  {ExitCode: 0},
			"/usr/bin/singbox-node setup": {ExitCode: 0},
		},
	}
}

func (f *fakeSSHClient) Run(_ context.Context, cmd string) (sshexec.RunResult, error) {
	f.runs = append(f.runs, cmd)
	for prefix, res := range f.responses {
		if strings.HasPrefix(cmd, prefix) || strings.Contains(cmd, prefix) {
			return res, nil
		}
	}
	return sshexec.RunResult{ExitCode: 0}, nil
}

func (f *fakeSSHClient) MustRun(ctx context.Context, cmd string) (sshexec.RunResult, error) {
	return f.Run(ctx, cmd)
}

func (f *fakeSSHClient) WriteFile(_ context.Context, path string, content []byte, _ uint32) error {
	f.writes[path] = content
	return nil
}

func (f *fakeSSHClient) Close() error { return nil }

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// fakeRunner records commands without executing them.
type fakeRunner struct {
	cmds []system.Command
	err  error
}

func (f *fakeRunner) Run(c system.Command) error {
	f.cmds = append(f.cmds, c)
	return f.err
}

func newTestOrchestrator(t *testing.T, client *fakeSSHClient) (*Orchestrator, Registry) {
	t.Helper()
	dir := t.TempDir()
	registry := NewRegistry(paths.LayoutForRoot(dir))
	o := &Orchestrator{
		Registry: registry,
		Runner:   &fakeRunner{},
		SSHDial: func(ctx context.Context, t sshexec.Target, a sshexec.Auth) (sshClient, error) {
			return client, nil
		},
	}
	return o, registry
}

func TestAddNodeRequestValidate(t *testing.T) {
	good := AddNodeRequest{
		Alias:                "Tokyo",
		SSHTarget:            sshexec.Target{Host: "1.2.3.4"},
		Domain:               "jp.example.com",
		EnabledProtocols:     []config.Protocol{config.ProtocolRealityVision},
		MasterPublicEndpoint: "5.6.7.8:51820",
		Version:              "v1.0.0",
		CoreVersion:          "1.12.0",
	}
	if err := good.Validate(); err != nil {
		t.Errorf("good request rejected: %v", err)
	}

	for _, mutate := range []struct {
		name string
		fn   func(*AddNodeRequest)
	}{
		{"no alias", func(r *AddNodeRequest) { r.Alias = "" }},
		{"no host", func(r *AddNodeRequest) { r.SSHTarget = sshexec.Target{} }},
		{"no domain", func(r *AddNodeRequest) { r.Domain = "" }},
		{"no protocols", func(r *AddNodeRequest) { r.EnabledProtocols = nil }},
		{"no master endpoint", func(r *AddNodeRequest) { r.MasterPublicEndpoint = "" }},
		{"no version", func(r *AddNodeRequest) { r.Version = "" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			req := good
			mutate.fn(&req)
			if err := req.Validate(); err == nil {
				t.Errorf("invalid request should fail validation")
			}
		})
	}
}

func TestCanonicalProtocols(t *testing.T) {
	got := canonicalProtocols([]config.Protocol{
		config.ProtocolHysteria2, config.ProtocolRealityVision, config.ProtocolHysteria2,
	})
	want := []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v want %v", i, got[i], want[i])
		}
	}
}

func TestAddNodeFlowSucceeds(t *testing.T) {
	redirectWGConfig(t)
	client := newFakeSSHClient()
	o, registry := newTestOrchestrator(t, client)
	o.VerifyConnectivity = func(ctx context.Context, n Node) error { return nil }
	o.PostRegister = func(context.Context, Node) error { return nil }

	req := AddNodeRequest{
		Alias:                "Tokyo",
		PublicIP:             "203.0.113.10",
		SSHTarget:            sshexec.Target{Host: "203.0.113.10"},
		SSHAuth:              sshexec.Auth{User: "root", Password: "x"},
		Domain:               "jp.example.com",
		EnabledProtocols:     []config.Protocol{config.ProtocolRealityVision},
		Ports:                config.Ports{RealityVision: 443},
		RealityServerName:    "www.microsoft.com",
		MasterPublicEndpoint: "198.51.100.1:51820",
		Version:              "v1.2.3",
		CoreVersion:          "1.12.0",
	}

	node, err := o.AddNode(context.Background(), req)
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if node.ID != "001" {
		t.Errorf("expected first node ID 001 got %q", node.ID)
	}
	if node.WGIP != "10.10.0.2" {
		t.Errorf("expected WGIP 10.10.0.2 got %q", node.WGIP)
	}

	// WireGuard config file was written to the node at the (possibly
	// redirected) config path.
	if _, ok := client.writes[wireguard.ConfigPath]; !ok {
		t.Errorf("node WireGuard config was not written at %s; writes: %v", wireguard.ConfigPath, mapKeys(client.writes))
	}
	// singbox-node systemd unit was deployed.
	if _, ok := client.writes[`/etc/systemd/system/singbox-node.service`]; !ok {
		t.Errorf("singbox-node systemd unit was not written")
	}
	// Apt installation was attempted.
	foundApt := false
	for _, cmd := range client.runs {
		if strings.Contains(cmd, "apt-get install") {
			foundApt = true
			break
		}
	}
	if !foundApt {
		t.Errorf("apt install command was not invoked")
	}
	// Master key pair was generated and persisted.
	if _, err := registry.MasterKeys(); err != nil {
		t.Errorf("master keys not generated: %v", err)
	}
}

func TestRemoveNodeForceContinuesPastError(t *testing.T) {
	redirectWGConfig(t)
	o, registry := newTestOrchestrator(t, newFakeSSHClient())
	must(t, registry.Save(Node{ID: "001", Alias: "Tokyo", WGIP: "10.10.0.2", WGPublicKey: "PUB", APIToken: "tok"}))
	if _, err := registry.EnsureMasterKeys(); err != nil {
		t.Fatalf("ensure master keys: %v", err)
	}

	// Agent is not reachable (no real listener), and the Runner returns nil
	// so WG sync "succeeds" silently. With force=true we expect the node to
	// be removed from the registry even though Teardown errored.
	if err := o.RemoveNode(context.Background(), "001", true); err != nil {
		t.Fatalf("RemoveNode force=true unexpectedly failed: %v", err)
	}

	nodes, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected empty registry after force remove; got %d", len(nodes))
	}
}
