package deploy

import (
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
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
	if err := ensurePublicLayoutRoot(layout); err != nil {
		return err
	}
	certPath, keyPath := CertificatePaths(layout, cfg.Domain)
	// server_name is matched against the name the client offers, which is the
	// normalized form: lowercase, no trailing dot, punycode for an IDN. Emitting
	// what the operator typed would leave a block Nginx can never select, so
	// both names go through the same normalization the certificate uses.
	siteDomain := ServerName(cfg.Domain)
	monitorDomain := ServerName(cfg.MonitorHost())
	monitorCertPath, monitorKeyPath := CertificatePaths(layout, monitorDomain)
	// The monitor only folds into the camouflage server block when it answers
	// on 443 under the same name. Given its own name it gets its own block,
	// selected by SNI, so the two never share a certificate or a server_name.
	sharesSiteBlock := cfg.MonitorPublicPort == 443 && monitorDomain == siteDomain
	// The subscription is published under the same name its links are spelled
	// with, which is the monitor's once one is deployed. Where /s/ lands follows
	// from that name and the port it answers on: the camouflage block when both
	// belong to the site, the monitor's own block when it is that block's name
	// and port, and a block of its own — with the matching certificate — for
	// every other pairing.
	subscriptionDomain := ServerName(cfg.SubscriptionHost())
	subscriptionCertPath, subscriptionKeyPath := CertificatePaths(layout, subscriptionDomain)
	publicSubscription := !cfg.SpokeMode
	monitorOwnBlock := !cfg.SpokeMode && cfg.DeployMonitor && !sharesSiteBlock
	subscriptionInSiteBlock := publicSubscription && cfg.SubscribePort == 443 && subscriptionDomain == siteDomain
	subscriptionInMonitorBlock := publicSubscription && monitorOwnBlock &&
		cfg.SubscribePort == cfg.MonitorPublicPort && subscriptionDomain == monitorDomain
	subscriptionOwnBlock := publicSubscription && !subscriptionInSiteBlock && !subscriptionInMonitorBlock
	// A port that publishes one name and nothing else gets a catch-all that drops
	// every other name during the handshake, so a bare-address probe learns
	// neither a certificate nor that anything is listening. 443 needs none: the
	// camouflage site is already its default server. Nginx accepts one
	// default_server per port, so a port serving both endpoints is claimed by the
	// monitor's block alone.
	monitorRejectBlock := !cfg.SpokeMode && cfg.DeployMonitor && cfg.MonitorPublicPort != 443
	subscriptionRejectBlock := subscriptionOwnBlock && cfg.SubscribePort != 443 &&
		!(monitorRejectBlock && cfg.SubscribePort == cfg.MonitorPublicPort)
	conf, err := templatefs.Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":          cfg.SubscribePort,
		"MonitorPublicPort":      cfg.MonitorPublicPort,
		"Domain":                 siteDomain,
		"CertificatePath":        certPath,
		"KeyPath":                keyPath,
		"MonitorDomain":          monitorDomain,
		"MonitorCertificatePath": monitorCertPath,
		"MonitorKeyPath":         monitorKeyPath,
		"MonitorSharesSiteBlock": sharesSiteBlock,
		"WebRoot":                layout.WebRoot,
		"SubscribeDir":           layout.SubscribeDir,
		"EnableMonitor":          cfg.DeployMonitor,
		"EnableMonitorFrontend":  cfg.DeployMonitorFrontend,
		"MonitorPort":            cfg.MonitorPort,
		// A spoke serves only the camouflage site, so it emits none of the three
		// subscription placements and no monitor block; the hub also serves the
		// public subscription and monitor endpoints.
		"SubscriptionDomain":          subscriptionDomain,
		"SubscriptionCertificatePath": subscriptionCertPath,
		"SubscriptionKeyPath":         subscriptionKeyPath,
		"SubscriptionInSiteBlock":     subscriptionInSiteBlock,
		"SubscriptionInMonitorBlock":  subscriptionInMonitorBlock,
		"SubscriptionOwnBlock":        subscriptionOwnBlock,
		"SubscriptionRejectBlock":     subscriptionRejectBlock,
		"MonitorRejectBlock":          monitorRejectBlock,
		"PublicMonitor":               !cfg.SpokeMode,
	})
	if err != nil {
		return err
	}
	return WriteFile(nginxConfPath, []byte(conf), 0o644)
}

// ServerName returns the form of domain that Nginx matches an incoming SNI or
// Host against: lowercase, no trailing dot, punycode for an IDN. It is also the
// form to compare two configured names by, and the only form worth reporting to
// an operator, since a URL spelled any other way will not select the block that
// serves it. A name that cannot be normalized is passed through untouched so
// rendering still reports the operator's own value back to them, and the
// configuration test — not a silent rewrite — is what rejects it.
func ServerName(domain string) string {
	normalized, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return strings.TrimSpace(domain)
	}
	return normalized
}
