package deploy

import (
	"fmt"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// aptNginxScript sets up the nginx.org mainline apt repository and installs
// nginx. It is run via `bash -c` because it requires pipes, heredoc-free key
// dearmoring, and sourcing /etc/os-release for the distro and codename.
const aptNginxScript = `set -e
export DEBIAN_FRONTEND=noninteractive
export APT_LISTCHANGES_FRONTEND=none
export NEEDRESTART_MODE=a
apt-get install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold gnupg2 ca-certificates curl
curl -fsSL https://nginx.org/keys/nginx_signing.key | gpg --batch --yes --no-tty --dearmor -o /usr/share/keyrings/nginx-archive-keyring.gpg
. /etc/os-release
echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] http://nginx.org/packages/mainline/${ID} ${VERSION_CODENAME} nginx" > /etc/apt/sources.list.d/nginx.list
apt-get update
apt-get install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold nginx`

// dnfNginxScript writes the nginx.org mainline yum repo and installs nginx. The
// quoted heredoc keeps $releasever/$basearch literal for yum to expand.
const dnfNginxScript = `set -e
cat > /etc/yum.repos.d/nginx.repo <<'REPO'
[nginx-mainline]
name=nginx mainline repo
baseurl=http://nginx.org/packages/mainline/centos/$releasever/$basearch/
gpgcheck=1
enabled=1
gpgkey=https://nginx.org/keys/nginx_signing.key
module_hotfixes=true
REPO
%s install -y nginx`

// NginxInstallCommands returns the commands to install Nginx from the nginx.org
// mainline repository for the detected OS family.
func NginxInstallCommands(osr system.OSRelease) []system.Command {
	switch osr.PackageManager {
	case "apt":
		return []system.Command{{Name: "bash", Args: []string{"-c", aptNginxScript}}}
	case "dnf", "yum":
		script := fmt.Sprintf(dnfNginxScript, osr.PackageManager)
		return []system.Command{{Name: "bash", Args: []string{"-c", script}}}
	default:
		return nil
	}
}

// WriteManagedNginxConfig renders and writes the managed Nginx configuration.
func WriteManagedNginxConfig(layout paths.Layout, cfg Config, nginxConfPath string) error {
	certPath := filepath.Join(layout.TLSDir, cfg.Domain+".crt")
	keyPath := filepath.Join(layout.TLSDir, cfg.Domain+".key")
	conf, err := templatefs.Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":     cfg.SubscribePort,
		"MonitorPublicPort": cfg.MonitorPublicPort,
		"Domain":            cfg.Domain,
		"CertificatePath":   certPath,
		"KeyPath":           keyPath,
		"WebRoot":           layout.WebRoot,
		"SubscribeDir":      layout.SubscribeDir,
		"EnableMonitor":     cfg.DeployMonitor,
		"MonitorPort":       cfg.MonitorPort,
	})
	if err != nil {
		return err
	}
	return WriteFile(nginxConfPath, []byte(conf), 0o644)
}
