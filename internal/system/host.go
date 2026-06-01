package system

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Host describes the detected machine: OS family, architecture, and firewall.
type Host struct {
	OS       OSRelease
	Arch     string // normalized: amd64 or arm64
	Firewall Firewall
	IsRoot   bool
	SELinux  bool // true when SELinux is enforcing
}

// DetectHost probes the running machine.
func DetectHost() (Host, error) {
	h := Host{
		Arch:     normalizeArch(runtime.GOARCH),
		Firewall: DetectFirewall(),
		IsRoot:   os.Geteuid() == 0,
		SELinux:  selinuxEnforcing(),
	}
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return h, err
	}
	osr, err := ParseOSRelease(string(content))
	if err != nil {
		return h, err
	}
	h.OS = osr
	return h, nil
}

// normalizeArch maps Go arch names to the project's canonical names.
func normalizeArch(goarch string) string {
	switch goarch {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return goarch
	}
}

// DetectFirewall returns the first available firewall front-end.
func DetectFirewall() Firewall {
	if _, err := exec.LookPath("ufw"); err == nil {
		return FirewallUFW
	}
	if _, err := exec.LookPath("firewall-cmd"); err == nil {
		return FirewallFirewalld
	}
	return FirewallNone
}

// selinuxEnforcing reports whether SELinux is in enforcing mode.
func selinuxEnforcing() bool {
	out, err := exec.Command("getenforce").Output()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "Enforcing")
}

// Supported reports whether the host can be installed onto: supported OS family
// and architecture, root privileges, and SELinux not enforcing.
func (h Host) Supported() bool {
	return h.OS.Supported() && (h.Arch == "amd64" || h.Arch == "arm64")
}
