// Package bootstrap performs the one-time SSH provisioning that turns a fresh
// server into a spoke: it pushes the embedded agent binary, installs the
// WireGuard tools and overlay config, writes the agent's config and systemd
// unit, and starts everything. After this runs the hub reaches the spoke purely
// over the overlay; SSH is never used again.
package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/agentfirewall"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

const (
	// AgentBinaryPath is where the agent binary is installed on a spoke.
	AgentBinaryPath     = "/usr/bin/singbox-deploy-agent"
	spokeLayoutRoot     = "/etc/singbox-deploy"
	spokeAgentConfigDir = spokeLayoutRoot + "/state/agent"
)

const spokePrivateKeyMarker = "__SINGBOX_DEPLOY_SPOKE_PRIVATE_KEY__"

// Auth carries SSH authentication material. Password and key auth may both be
// supplied; key auth is tried first.
type Auth struct {
	Password      string
	PrivateKeyPEM []byte
	Passphrase    string
}

// Target identifies a server to bootstrap over SSH.
type Target struct {
	Host               string
	Port               int
	User               string
	HostKeyFingerprint string // confirmed OpenSSH SHA256 fingerprint
	Auth               Auth
}

// Plan is the non-secret material to install on the spoke. The spoke completes
// it with its locally generated WireGuard private key during Provision.
type Plan struct {
	AgentBinary  []byte // arch-matched agent binary bytes
	AgentVersion string // exact version the installed binary must report
	WGAddress    string // spoke overlay address with prefix
	HubIP        string // hub overlay address allowed to reach the agent
	HubPublicKey string
	HubEndpoint  string
	Subnet       string
	AgentUnit    string // singbox-deploy-agent.service contents
	Token        string
	ListenIP     string // spoke overlay address the agent binds to
	AgentPort    int
	Interface    string // WireGuard interface name (sbwg0)
}

// Result contains only material a spoke may return to the hub. In particular,
// the WireGuard private key is generated and retained on the spoke; the hub
// receives only its public key.
type Result struct {
	WGPublicKey  string
	AgentVersion string
}

// Runner is a connected SSH session abstraction: each Run executes one command
// with optional stdin and returns its combined output. It is an interface so the
// provisioning sequence can be tested without a real SSH server.
type Runner interface {
	Run(ctx context.Context, cmd string, stdin []byte) (string, error)
	Close() error
}

// Bootstrapper provisions spokes over SSH. Dial is injectable for testing.
type Bootstrapper struct {
	Dial func(ctx context.Context, target Target) (Runner, error)
	Scan func(ctx context.Context, target Target) (HostKeyInfo, error)
	Log  io.Writer
}

func (b *Bootstrapper) dial(ctx context.Context, target Target) (Runner, error) {
	if b.Dial != nil {
		return b.Dial(ctx, target)
	}
	return dialSSH(ctx, target)
}

func (b *Bootstrapper) logf(format string, args ...any) {
	if b.Log != nil {
		fmt.Fprintf(b.Log, format, args...)
	}
}

// ScanHostKey obtains a server's key without authenticating. Callers must show
// the SHA256 fingerprint to the operator and copy it into Target only after an
// explicit confirmation.
func (b *Bootstrapper) ScanHostKey(ctx context.Context, target Target) (HostKeyInfo, error) {
	if err := validateRootUser(target.User); err != nil {
		return HostKeyInfo{}, err
	}
	if b.Scan != nil {
		return b.Scan(ctx, target)
	}
	return scanSSHHostKey(ctx, target)
}

// DetectArch connects and returns the spoke's normalized architecture
// ("amd64" or "arm64"), so the hub can pick the matching embedded agent binary.
func (b *Bootstrapper) DetectArch(ctx context.Context, target Target) (string, error) {
	if err := validateRootUser(target.User); err != nil {
		return "", err
	}
	if err := requireConfirmedHostKey(target); err != nil {
		return "", err
	}
	runner, err := b.dial(ctx, target)
	if err != nil {
		return "", err
	}
	defer runner.Close()
	out, err := runner.Run(ctx, "uname -m", nil)
	if err != nil {
		return "", fmt.Errorf("detect architecture: %w", err)
	}
	return normalizeArch(strings.TrimSpace(out))
}

// normalizeArch maps uname -m output to the project's canonical names.
func normalizeArch(machine string) (string, error) {
	switch strings.TrimSpace(machine) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", machine)
	}
}

// Provision runs the full SSH provisioning sequence. It is idempotent: re-running
// it re-pushes the binary and configs and restarts the units.
func (b *Bootstrapper) Provision(ctx context.Context, target Target, plan Plan) (result Result, retErr error) {
	if err := validateRootUser(target.User); err != nil {
		return Result{}, err
	}
	if err := requireConfirmedHostKey(target); err != nil {
		return Result{}, err
	}
	if len(plan.AgentBinary) == 0 {
		return Result{}, fmt.Errorf("agent binary is empty")
	}
	if strings.TrimSpace(plan.AgentVersion) == "" {
		return Result{}, fmt.Errorf("expected agent version is required")
	}
	if plan.AgentPort <= 0 || plan.AgentPort > 65535 {
		return Result{}, fmt.Errorf("agent port must be between 1 and 65535")
	}
	if strings.TrimSpace(plan.HubIP) == "" {
		plan.HubIP = wgnet.HubAddress
	}
	iface := plan.Interface
	if iface == "" {
		iface = wgnet.InterfaceName
	}
	if err := (agentfirewall.Rule{
		Backend: system.FirewallUFW, Interface: iface, HubIP: plan.HubIP,
		ListenIP: plan.ListenIP, Port: plan.AgentPort,
	}).Validate(); err != nil {
		return Result{}, err
	}
	runner, err := b.dial(ctx, target)
	if err != nil {
		return Result{}, err
	}
	defer runner.Close()
	// Refuse to overwrite the overlay of a server that is itself managing
	// spokes. This check runs before the failed-provision cleanup defer because
	// no artifacts from this attempt exist yet and the current Hub must remain
	// untouched.
	const childHubCheck = "if [ -d '/etc/singbox-deploy/state/nodes' ] && find '/etc/singbox-deploy/state/nodes' -mindepth 1 -maxdepth 1 -type d -print -quit | grep -q .; then echo 'server already manages spoke nodes' >&2; exit 1; fi"
	if out, err := runner.Run(ctx, childHubCheck, nil); err != nil {
		return Result{}, fmt.Errorf("refuse to convert an active hub into a spoke: %w: %s", err, out)
	}

	// Any error after the SSH session is established may have left a key,
	// uploaded binary, config, or running unit behind. Clean those artifacts on
	// the same pinned connection before returning the provisioning error.
	defer func() {
		if retErr == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := cleanupProvisionedArtifacts(cleanupCtx, runner, iface); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	// 1. Install the WireGuard userspace tools using the spoke's package manager.
	b.logf("installing WireGuard tools...\n")
	if err := b.installWireGuard(ctx, runner); err != nil {
		return Result{}, fmt.Errorf("install wireguard-tools: %w", err)
	}

	// The spoke owns its private key. Reuse an existing key on retries and send
	// only the derived public key back over the SSH channel.
	b.logf("generating spoke WireGuard identity locally...\n")
	publicKey, err := generateSpokeKey(ctx, runner, iface)
	if err != nil {
		return Result{}, err
	}

	// 2. Push the agent binary.
	b.logf("uploading agent binary (%d bytes)...\n", len(plan.AgentBinary))
	if err := uploadFile(ctx, runner, AgentBinaryPath, plan.AgentBinary, "0755"); err != nil {
		return Result{}, fmt.Errorf("upload agent binary: %w", err)
	}
	digest := sha256.Sum256(plan.AgentBinary)
	expectedDigest := hex.EncodeToString(digest[:])
	verifyDigest := fmt.Sprintf("printf '%%s  %%s\\n' %s %s | sha256sum --check --status -",
		shellQuote(expectedDigest), shellQuote(AgentBinaryPath))
	if out, err := runner.Run(ctx, verifyDigest, nil); err != nil {
		return Result{}, fmt.Errorf("verify uploaded agent SHA-256 %s: %w: %s", expectedDigest, err, out)
	}

	// 3. Overlay config.
	b.logf("writing overlay config...\n")
	wgConfig := wgnet.RenderSpokeConfig(wgnet.SpokeConfig{
		PrivateKey:   spokePrivateKeyMarker,
		Address:      plan.WGAddress,
		HubPublicKey: plan.HubPublicKey,
		HubEndpoint:  plan.HubEndpoint,
		Subnet:       plan.Subnet,
	})
	if err := installSpokeConfig(ctx, runner, iface, wgConfig); err != nil {
		return Result{}, fmt.Errorf("upload wireguard config: %w", err)
	}

	// 4. Agent config (token, bind address, port).
	agentDir := spokeAgentConfigDir
	if err := prepareAgentConfigDir(ctx, runner, spokeLayoutRoot, agentDir); err != nil {
		return Result{}, fmt.Errorf("prepare agent config directory: %w", err)
	}
	files := map[string]struct {
		content string
		mode    string
	}{
		agentDir + "/token":                          {plan.Token + "\n", "0600"},
		agentDir + "/" + agentfirewall.ListenIPFile:  {plan.ListenIP + "\n", "0600"},
		agentDir + "/" + agentfirewall.AgentPortFile: {fmt.Sprintf("%d\n", plan.AgentPort), "0600"},
		agentDir + "/" + agentfirewall.HubIPFile:     {plan.HubIP + "\n", "0600"},
		agentDir + "/" + agentfirewall.InterfaceFile: {iface + "\n", "0600"},
	}
	for path, f := range files {
		if err := uploadFile(ctx, runner, path, []byte(f.content), f.mode); err != nil {
			return Result{}, fmt.Errorf("write agent config %s: %w", path, err)
		}
	}

	// 5. Agent systemd unit.
	b.logf("installing agent service...\n")
	if err := uploadFile(ctx, runner, "/etc/systemd/system/singbox-deploy-agent.service", []byte(plan.AgentUnit), "0644"); err != nil {
		return Result{}, fmt.Errorf("upload agent unit: %w", err)
	}

	// 6. Bring up the overlay, admit the Hub to the Agent API through an active
	// host firewall, then start the agent. The firewall rule is scoped to the
	// managed interface, Hub source address, spoke destination address, and
	// Agent TCP port; the API is never opened globally.
	b.logf("starting overlay and agent...\n")
	startOverlay := strings.Join([]string{
		"systemctl daemon-reload",
		"systemctl enable wg-quick@" + iface + ".service",
		"systemctl restart wg-quick@" + iface + ".service",
	}, " && ")
	if out, err := runner.Run(ctx, startOverlay, nil); err != nil {
		return Result{}, fmt.Errorf("start overlay: %w: %s", err, out)
	}
	if err := configureAgentFirewall(ctx, runner, iface, plan.HubIP, plan.ListenIP, plan.AgentPort); err != nil {
		return Result{}, err
	}
	startAgent := strings.Join([]string{
		"systemctl daemon-reload",
		"systemctl enable singbox-deploy-agent.service",
		"systemctl restart singbox-deploy-agent.service",
	}, " && ")
	if out, err := runner.Run(ctx, startAgent, nil); err != nil {
		return Result{}, fmt.Errorf("start agent: %w: %s", err, out)
	}
	versionOut, err := runner.Run(ctx, shellQuote(AgentBinaryPath)+" --version", nil)
	if err != nil {
		return Result{}, fmt.Errorf("verify running agent version: %w: %s", err, versionOut)
	}
	actualVersion := strings.TrimSpace(versionOut)
	if actualVersion != strings.TrimSpace(plan.AgentVersion) {
		return Result{}, fmt.Errorf("agent version mismatch after startup: expected %q, got %q", strings.TrimSpace(plan.AgentVersion), actualVersion)
	}
	return Result{WGPublicKey: publicKey, AgentVersion: actualVersion}, nil
}

// Cleanup removes artifacts created by Provision after a failed add-node
// transaction. It is used only as part of that initial SSH bootstrap attempt;
// routine management and successful-node removal continue to use WireGuard.
// Service stop failures are tolerated because a partially provisioned unit may
// not exist, while removal and daemon-reload failures are reported.
func (b *Bootstrapper) Cleanup(ctx context.Context, target Target, iface string) error {
	if err := validateRootUser(target.User); err != nil {
		return err
	}
	if err := requireConfirmedHostKey(target); err != nil {
		return err
	}
	runner, err := b.dial(ctx, target)
	if err != nil {
		return err
	}
	defer runner.Close()
	if strings.TrimSpace(iface) == "" {
		iface = wgnet.InterfaceName
	}
	return cleanupProvisionedArtifacts(ctx, runner, iface)
}

func cleanupProvisionedArtifacts(ctx context.Context, runner Runner, iface string) error {
	unit := "wg-quick@" + iface + ".service"
	paths := []string{
		AgentBinaryPath,
		"/etc/systemd/system/singbox-deploy-agent.service",
		"/etc/wireguard/" + iface + ".conf",
		"/etc/wireguard/" + iface + ".conf.singbox-deploy.template",
		"/etc/wireguard/" + iface + ".key",
		"/etc/wireguard/" + iface + ".key.singbox-deploy.tmp",
	}
	quotedPaths := make([]string, len(paths))
	for i, path := range paths {
		quotedPaths[i] = shellQuote(path)
	}
	cmd := strings.Join([]string{
		"systemctl disable --now singbox-deploy-agent.service >/dev/null 2>&1 || true",
		"systemctl disable --now " + unit + " >/dev/null 2>&1 || true",
		AgentFirewallCleanupShell(spokeAgentConfigDir),
		"rm -f " + strings.Join(quotedPaths, " "),
		"rm -rf '/etc/singbox-deploy/state/agent'",
		"systemctl daemon-reload",
	}, "; ")
	if out, err := runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("clean failed spoke bootstrap: %w: %s", err, out)
	}
	return nil
}

// configureAgentFirewall opens the authenticated Agent API only on the
// WireGuard control-plane path. It records the selected backend/zone beside the
// Agent config so both failed-bootstrap cleanup and a later Agent uninstall can
// remove the exact rule.
func configureAgentFirewall(ctx context.Context, runner Runner, iface, hubIP, listenIP string, port int) error {
	rule := agentfirewall.Rule{Interface: iface, HubIP: hubIP, ListenIP: listenIP, Port: port}
	richRule := rule.RichRule()
	backendPath := spokeAgentConfigDir + "/" + agentfirewall.BackendFile
	zonePath := spokeAgentConfigDir + "/" + agentfirewall.ZoneFile
	cmd := strings.Join([]string{
		"if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state 2>/dev/null | grep -qi '^running$'; then",
		"zone=$(firewall-cmd --get-zone-of-interface=" + shellQuote(iface) + " 2>/dev/null || true)",
		"[ \"$zone\" = 'no zone' ] && zone=''",
		"[ -n \"$zone\" ] || zone=$(firewall-cmd --get-default-zone)",
		"printf '%s\\n' 'firewalld' > " + shellQuote(backendPath),
		"printf '%s\\n' \"$zone\" > " + shellQuote(zonePath),
		"firewall-cmd --permanent --zone=\"$zone\" --add-rich-rule=" + shellQuote(richRule),
		"firewall-cmd --reload",
		"elif command -v ufw >/dev/null 2>&1; then",
		"printf '%s\\n' 'ufw' > " + shellQuote(backendPath),
		"ufw allow in on " + shellQuote(iface) + " from " + shellQuote(hubIP) + " to " + shellQuote(listenIP) +
			" port " + fmt.Sprintf("%d", port) + " proto tcp comment 'singbox-deploy-agent'",
		"else",
		"printf '%s\\n' 'none' > " + shellQuote(backendPath),
		"fi",
	}, "\n")
	if out, err := runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("open scoped agent firewall rule: %w: %s", err, out)
	}
	return nil
}

// AgentFirewallCleanupShell returns an idempotent shell fragment that removes
// the exact scoped rule described by the root-only Agent state files.
func AgentFirewallCleanupShell(agentDir string) string {
	statePath := func(name string) string { return agentDir + "/" + name }
	return strings.Join([]string{
		"if [ -r " + shellQuote(statePath(agentfirewall.BackendFile)) + " ]; then",
		"backend=$(cat " + shellQuote(statePath(agentfirewall.BackendFile)) + ")",
		"hub_ip=$(cat " + shellQuote(statePath(agentfirewall.HubIPFile)) + " 2>/dev/null || true)",
		"listen_ip=$(cat " + shellQuote(statePath(agentfirewall.ListenIPFile)) + " 2>/dev/null || true)",
		"agent_port=$(cat " + shellQuote(statePath(agentfirewall.AgentPortFile)) + " 2>/dev/null || true)",
		"iface=$(cat " + shellQuote(statePath(agentfirewall.InterfaceFile)) + " 2>/dev/null || true)",
		"if [ \"$backend\" = 'ufw' ] && [ -n \"$hub_ip\" ] && [ -n \"$listen_ip\" ] && [ -n \"$agent_port\" ] && [ -n \"$iface\" ]; then",
		"ufw --force delete allow in on \"$iface\" from \"$hub_ip\" to \"$listen_ip\" port \"$agent_port\" proto tcp >/dev/null 2>&1 || true",
		"elif [ \"$backend\" = 'firewalld' ] && [ -n \"$hub_ip\" ] && [ -n \"$listen_ip\" ] && [ -n \"$agent_port\" ]; then",
		"zone=$(cat " + shellQuote(statePath(agentfirewall.ZoneFile)) + " 2>/dev/null || true)",
		"[ -n \"$zone\" ] || zone=$(firewall-cmd --get-default-zone 2>/dev/null || true)",
		"family=ipv4",
		"prefix=32",
		"case \"$hub_ip\" in *:*) family=ipv6; prefix=128 ;; esac",
		`rule="rule family=\"$family\" source address=\"$hub_ip/$prefix\" destination address=\"$listen_ip/$prefix\" port port=\"$agent_port\" protocol=\"tcp\" accept"`,
		"firewall-cmd --permanent --zone=\"$zone\" --remove-rich-rule=\"$rule\" >/dev/null 2>&1 || true",
		"firewall-cmd --reload >/dev/null 2>&1 || true",
		"fi",
		"fi",
	}, "\n")
}

func requireConfirmedHostKey(target Target) error {
	if strings.TrimSpace(target.HostKeyFingerprint) == "" {
		return fmt.Errorf("SSH host key fingerprint is required; scan and explicitly confirm it first")
	}
	return nil
}

func generateSpokeKey(ctx context.Context, runner Runner, iface string) (string, error) {
	keyPath := "/etc/wireguard/" + iface + ".key"
	tmpPath := keyPath + ".singbox-deploy.tmp"
	cmd := fmt.Sprintf("umask 077 && mkdir -p %s && if [ ! -s %s ]; then wg genkey > %s && chmod 0600 %s && mv %s %s; fi && chmod 0600 %s && wg pubkey < %s",
		shellQuote(parentDir(keyPath)), shellQuote(keyPath), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(keyPath), shellQuote(keyPath), shellQuote(keyPath))
	out, err := runner.Run(ctx, cmd, nil)
	if err != nil {
		return "", fmt.Errorf("generate spoke WireGuard key: %w: %s", err, out)
	}
	publicKey := strings.TrimSpace(out)
	if !wgnet.ValidKey(publicKey) {
		return "", fmt.Errorf("spoke returned an invalid WireGuard public key")
	}
	return publicKey, nil
}

func installSpokeConfig(ctx context.Context, runner Runner, iface, config string) error {
	if strings.Count(config, spokePrivateKeyMarker) != 1 {
		return fmt.Errorf("wireguard config must contain exactly one private-key marker")
	}
	configPath := "/etc/wireguard/" + iface + ".conf"
	templatePath := configPath + ".singbox-deploy.template"
	if err := uploadFile(ctx, runner, templatePath, []byte(config), "0600"); err != nil {
		return err
	}
	keyPath := "/etc/wireguard/" + iface + ".key"
	tmpPath := configPath + ".singbox-deploy.tmp"
	cmd := fmt.Sprintf("private_key=$(cat %s) && sed \"s|%s|${private_key}|\" %s > %s && chmod 0600 %s && ! grep -F %s %s && mv %s %s && rm -f %s",
		shellQuote(keyPath), spokePrivateKeyMarker, shellQuote(templatePath), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(spokePrivateKeyMarker), shellQuote(tmpPath), shellQuote(tmpPath), shellQuote(configPath), shellQuote(templatePath))
	if out, err := runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("materialize spoke-local private key: %w: %s", err, out)
	}
	return nil
}

// installWireGuard detects the spoke's package manager from /etc/os-release and
// installs wireguard-tools with it.
func (b *Bootstrapper) installWireGuard(ctx context.Context, runner Runner) error {
	osRelease, err := runner.Run(ctx, "cat /etc/os-release", nil)
	if err != nil {
		return err
	}
	osr, err := system.ParseOSRelease(osRelease)
	if err != nil {
		return err
	}
	cmds := wgnet.InstallCommands(osr)
	if len(cmds) == 0 {
		return fmt.Errorf("unsupported package manager for %s", osr.ID)
	}
	for _, cmd := range cmds {
		shell := renderShellCommand(cmd)
		if out, err := runner.Run(ctx, shell, nil); err != nil {
			return fmt.Errorf("%s: %w: %s", shell, err, out)
		}
	}
	return nil
}

// renderShellCommand renders a system.Command as a shell line, prefixing its
// per-command environment so it survives sudo/non-login shells.
func renderShellCommand(cmd system.Command) string {
	var b strings.Builder
	for _, env := range cmd.Env {
		b.WriteString(env)
		b.WriteByte(' ')
	}
	b.WriteString(cmd.Name)
	for _, arg := range cmd.Args {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	return b.String()
}

// prepareAgentConfigDir gives Nginx traverse access to the managed layout root
// while keeping all state, including the agent token, root-only. Explicit
// chmod calls make the result independent of the remote root user's umask and
// repair directories left by an interrupted earlier bootstrap.
func prepareAgentConfigDir(ctx context.Context, runner Runner, layoutRoot, agentDir string) error {
	stateDir := parentDir(agentDir)
	cmd := fmt.Sprintf("mkdir -p %s %s %s && chmod 0755 %s && chmod 0700 %s %s",
		shellQuote(layoutRoot), shellQuote(stateDir), shellQuote(agentDir),
		shellQuote(layoutRoot), shellQuote(stateDir), shellQuote(agentDir))
	if out, err := runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// uploadFile writes content to remotePath with the given octal mode, creating
// parent directories. It streams the bytes over stdin so no size limit or
// escaping of the payload is needed. Removing a fixed stale temporary file and
// restricting umask only around its creation prevent an old 0644 inode from
// exposing a secret without changing the permissions of newly-created parents.
func uploadFile(ctx context.Context, runner Runner, remotePath string, content []byte, mode string) error {
	dir := parentDir(remotePath)
	tmp := remotePath + ".singbox-deploy.tmp"
	cmd := fmt.Sprintf("mkdir -p %s && rm -f %s && (umask 077 && cat > %s) && chmod %s %s && mv %s %s",
		shellQuote(dir), shellQuote(tmp), shellQuote(tmp), mode, shellQuote(tmp), shellQuote(tmp), shellQuote(remotePath))
	if out, err := runner.Run(ctx, cmd, content); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func parentDir(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "/"
}

// shellQuote single-quotes a string for safe use in a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
