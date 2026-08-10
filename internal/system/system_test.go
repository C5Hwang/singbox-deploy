package system

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectOSUbuntu(t *testing.T) {
	osr, err := ParseOSRelease("ID=ubuntu\nVERSION_ID=\"22.04\"\n")
	if err != nil {
		t.Fatalf("ParseOSRelease error: %v", err)
	}
	if osr.Family != FamilyDebian || osr.PackageManager != "apt" {
		t.Fatalf("osr = %+v", osr)
	}
}

func TestDetectOSRocky(t *testing.T) {
	osr, err := ParseOSRelease("ID=\"rocky\"\nVERSION_ID=\"9.3\"\nID_LIKE=\"rhel centos fedora\"\n")
	if err != nil {
		t.Fatalf("ParseOSRelease error: %v", err)
	}
	if osr.Family != FamilyRHEL {
		t.Fatalf("expected rhel family, got %+v", osr)
	}
	if osr.PackageManager != "dnf" {
		t.Fatalf("package manager = %q", osr.PackageManager)
	}
}

func TestDetectOSIDLikeRHEL(t *testing.T) {
	osr, err := ParseOSRelease("ID=customos\nID_LIKE=\"rhel\"\n")
	if err != nil {
		t.Fatalf("ParseOSRelease error: %v", err)
	}
	if osr.Family != FamilyRHEL {
		t.Fatalf("expected rhel via ID_LIKE, got %+v", osr)
	}
}

func TestFirewallCommands(t *testing.T) {
	cmds := FirewallCommands(FirewallUFW, []Port{{Number: 443, Proto: "tcp"}, {Number: 443, Proto: "udp"}})
	want := []string{"ufw allow 443/tcp", "ufw allow 443/udp"}
	if len(cmds) != len(want) {
		t.Fatalf("cmds = %#v", cmds)
	}
	for i := range want {
		if cmds[i].String() != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmds[i].String(), want[i])
		}
	}
}

func TestFirewallCommandsFirewalld(t *testing.T) {
	cmds := FirewallCommands(FirewallFirewalld, []Port{{Number: 8443, Proto: "tcp"}})
	want := []string{"firewall-cmd --add-port=8443/tcp --permanent", "firewall-cmd --reload"}
	if len(cmds) != len(want) {
		t.Fatalf("cmds = %#v", cmds)
	}
	for i := range want {
		if cmds[i].String() != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmds[i].String(), want[i])
		}
	}
}

func TestFirewallRemoveCommands(t *testing.T) {
	ufw := FirewallRemoveCommands(FirewallUFW, []Port{{Number: 9443, Proto: "udp"}})
	if len(ufw) != 1 || ufw[0].String() != "ufw delete allow 9443/udp" {
		t.Fatalf("ufw remove cmds = %#v", ufw)
	}
	fd := FirewallRemoveCommands(FirewallFirewalld, []Port{{Number: 9443, Proto: "udp"}})
	want := []string{"firewall-cmd --remove-port=9443/udp --permanent", "firewall-cmd --reload"}
	if len(fd) != len(want) {
		t.Fatalf("firewalld remove cmds = %#v", fd)
	}
	for i := range want {
		if fd[i].String() != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, fd[i].String(), want[i])
		}
	}
	if len(FirewallRemoveCommands(FirewallNone, []Port{{Number: 9443, Proto: "udp"}})) != 0 {
		t.Fatal("no firewall should yield no commands")
	}
}

func TestInstallPlanUsesAptOnUbuntu(t *testing.T) {
	plan := BuildInstallPlan(OSRelease{Family: FamilyDebian, PackageManager: "apt"})
	if plan.Commands[0].String() != "apt-get update" {
		t.Fatalf("first command = %q", plan.Commands[0].String())
	}
	if !containsEnv(plan.Commands[0].Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Fatalf("apt command missing noninteractive env: %#v", plan.Commands[0].Env)
	}
}

func containsEnv(env []string, want string) bool {
	for _, got := range env {
		if got == want {
			return true
		}
	}
	return false
}

func TestSystemctlCommand(t *testing.T) {
	if Systemctl("enable", SingBoxService).String() != "systemctl enable sing-box.service" {
		t.Fatalf("unexpected systemctl command")
	}
	if MonitorService != "singbox-deploy-monitor.service" {
		t.Fatalf("monitor service = %q", MonitorService)
	}
}

type recordingRunner struct{ commands []string }

func (r *recordingRunner) Run(c Command) error {
	r.commands = append(r.commands, c.String())
	return nil
}

func TestRunInstallPlanRecordsCommands(t *testing.T) {
	r := &recordingRunner{}
	plan := InstallPlan{Commands: []Command{
		{Name: "apt", Args: []string{"update"}},
		{Name: "systemctl", Args: []string{"enable", "sing-box.service"}},
	}}
	if err := RunInstallPlan(r, plan); err != nil {
		t.Fatalf("RunInstallPlan error: %v", err)
	}
	if len(r.commands) != 2 {
		t.Fatalf("commands = %#v", r.commands)
	}
}

func TestExecRunnerStreamsOutput(t *testing.T) {
	var buf strings.Builder
	r := NewExecRunner(&buf)
	if err := r.Run(Command{Name: "printf", Args: []string{"hello"}}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if buf.String() != "hello" {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestExecRunnerHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewExecRunnerContext(ctx, nil).Run(Command{Name: "sh", Args: []string{"-c", "exit 0"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{"amd64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Fatalf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostSupported(t *testing.T) {
	h := Host{OS: OSRelease{Family: FamilyDebian}, Arch: "amd64"}
	if !h.Supported() {
		t.Fatalf("ubuntu/amd64 should be supported")
	}
	bad := Host{OS: OSRelease{}, Arch: "amd64"}
	if bad.Supported() {
		t.Fatalf("unknown family must be unsupported")
	}
}

func TestSingBoxConflictAllowsManagedService(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, SingBoxService)
	expectedBin := filepath.Join(root, "sing-box", "sing-box")
	expectedConfig := filepath.Join(root, "sing-box", "conf", "config.json")
	unit := "[Service]\nExecStart=" + expectedBin + " run -c " + expectedConfig + "\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	err := SingBoxConflictCheck{
		ServicePaths:   []string{unitPath},
		ExpectedBinary: expectedBin,
		ExpectedConfig: expectedConfig,
		LookPath:       func(string) (string, error) { return "", exec.ErrNotFound },
	}.Check()
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
}

func TestSingBoxConflictBlocksForeignService(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, SingBoxService)
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/usr/bin/sing-box run -c /etc/sing-box/config.json\n"), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	err := SingBoxConflictCheck{
		ServicePaths:   []string{unitPath},
		ExpectedBinary: filepath.Join(root, "managed", "sing-box"),
		ExpectedConfig: filepath.Join(root, "managed", "config.json"),
		LookPath:       func(string) (string, error) { return "", exec.ErrNotFound },
	}.Check()
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("expected unmanaged service conflict, got %v", err)
	}
}

func TestSingBoxConflictBlocksPathBinary(t *testing.T) {
	root := t.TempDir()
	err := SingBoxConflictCheck{
		ServicePaths:   []string{filepath.Join(root, "missing.service")},
		ExpectedBinary: filepath.Join(root, "managed", "sing-box"),
		ExpectedConfig: filepath.Join(root, "managed", "config.json"),
		LookPath:       func(string) (string, error) { return "/usr/local/bin/sing-box", nil },
	}.Check()
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected PATH binary conflict, got %v", err)
	}
}

func TestCheckPortsProbesTCPReachability(t *testing.T) {
	port := freeTCPPort(t)
	err := CheckPorts(context.Background(), "127.0.0.1", []Port{{Number: port, Proto: "tcp", Label: "test", Public: true}})
	if err != nil {
		t.Fatalf("CheckPorts error: %v", err)
	}
}

func TestCheckPortsDetectsOccupiedTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	err = CheckPorts(context.Background(), "127.0.0.1", []Port{{Number: port, Proto: "tcp", Label: "occupied", Public: true}})
	if err == nil || !strings.Contains(err.Error(), "local bind failed") {
		t.Fatalf("expected local bind failure, got %v", err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// The monitor shells out to nft and ping; a minimal cloud image ships neither,
// and without them the per-IP and latency metrics disable themselves.
func TestInstallPlanInstallsMonitorProbeDependencies(t *testing.T) {
	for _, tc := range []struct {
		packageManager string
		ping           string
	}{
		{packageManager: "apt", ping: "iputils-ping"},
		{packageManager: "dnf", ping: "iputils"},
		{packageManager: "yum", ping: "iputils"},
	} {
		t.Run(tc.packageManager, func(t *testing.T) {
			plan := BuildInstallPlan(OSRelease{PackageManager: tc.packageManager})
			install := plan.Commands[len(plan.Commands)-1].String()
			for _, want := range []string{"nftables", tc.ping} {
				if !strings.Contains(install, " "+want) {
					t.Fatalf("install command %q does not install %q", install, want)
				}
			}
		})
	}
}
