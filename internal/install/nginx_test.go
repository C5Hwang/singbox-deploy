package install

import (
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func TestAptNginxInstallCommandIsNoninteractive(t *testing.T) {
	cmds := NginxInstallCommands(system.OSRelease{PackageManager: "apt"})
	if len(cmds) != 1 {
		t.Fatalf("commands = %#v", cmds)
	}
	script := strings.Join(cmds[0].Args, " ")
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"NEEDRESTART_MODE=a",
		"Dpkg::Options::=--force-confdef",
		"gpg --batch --yes --no-tty --dearmor",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("nginx apt script missing %q:\n%s", want, script)
		}
	}
}
