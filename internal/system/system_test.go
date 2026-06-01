package system

import (
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
	if cmds[0].String() != "firewall-cmd --add-port=8443/tcp --permanent" {
		t.Fatalf("cmd = %q", cmds[0].String())
	}
}

func TestInstallPlanUsesAptOnUbuntu(t *testing.T) {
	plan := BuildInstallPlan(OSRelease{Family: FamilyDebian, PackageManager: "apt"})
	if plan.Commands[0].String() != "apt update" {
		t.Fatalf("first command = %q", plan.Commands[0].String())
	}
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
	bad := Host{OS: OSRelease{Family: FamilyUnknown}, Arch: "amd64"}
	if bad.Supported() {
		t.Fatalf("unknown family must be unsupported")
	}
}
