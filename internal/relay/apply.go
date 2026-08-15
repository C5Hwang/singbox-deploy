package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

// nftCommandTimeout bounds one nft invocation. Loading a ruleset this small is
// measured in milliseconds; anything near this is a stuck host.
const nftCommandTimeout = 10 * time.Second

// clearRuleset is the atomic-replace idiom with nothing to replace it with: it
// declares the table so the delete always has something to remove, whether or
// not the rules were ever installed.
var clearRuleset = "table ip " + Table + " {}\ndelete table ip " + Table + "\n"

// Applier installs this node's relay data plane: the nftables ruleset, the
// firewall rules that let the forwarded packets through, and the boot-time
// systemd unit that puts the ruleset back after a reboot.
//
// Every external effect is a field so the whole thing can be driven by a
// recording fake in tests; the zero value fills each one in with the production
// implementation on first use.
type Applier struct {
	Layout paths.Layout
	// Bin is the binary the boot-time unit reapplies with: the hub's own
	// binary on the hub, the agent binary on a spoke.
	Bin        string
	SystemdDir string
	Firewall   system.Firewall
	Runner     system.Runner

	// NFT runs the nft binary with the rendered ruleset on stdin.
	NFT func(ctx context.Context, stdin string, args ...string) ([]byte, error)
	// Resolve maps a landing node's hostname to its IPv4 address.
	Resolve func(ctx context.Context, host string) (string, error)
	// WriteFile and RemoveFile manage the systemd unit file.
	WriteFile  func(path string, data []byte, perm os.FileMode) error
	RemoveFile func(path string) error
}

func (a *Applier) defaults() {
	if a.Layout.Root == "" {
		a.Layout = paths.DefaultLayout()
	}
	if a.Bin == "" {
		a.Bin = "/usr/bin/singbox-deploy"
	}
	if a.SystemdDir == "" {
		a.SystemdDir = "/etc/systemd/system"
	}
	if a.Runner == nil {
		a.Runner = system.NewExecRunner(nil)
	}
	if a.NFT == nil {
		a.NFT = runNFT
	}
	if a.Resolve == nil {
		a.Resolve = resolveIPv4
	}
	if a.WriteFile == nil {
		a.WriteFile = os.WriteFile
	}
	if a.RemoveFile == nil {
		a.RemoveFile = func(path string) error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
	}
}

// Apply persists cfg as this node's relay job and installs it. An empty
// configuration is a withdrawal, so a relay that loses its last landing node is
// left with no ruleset, no firewall rules and no unit.
//
// The configuration is written before anything is installed: a failure part way
// through then leaves the desired state on disk for the boot-time unit — or the
// next apply — to converge on, rather than a live ruleset nothing remembers.
func (a *Applier) Apply(ctx context.Context, cfg Config) error {
	a.defaults()
	if cfg.Empty() {
		return a.Clear(ctx)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	previous, err := Load(a.Layout)
	if err != nil {
		return err
	}
	if err := Save(a.Layout, cfg); err != nil {
		return err
	}
	a.withdrawFirewall(previous)
	if err := a.install(ctx, cfg); err != nil {
		return err
	}
	return a.installUnit()
}

// Reapply installs whatever this node's stored configuration says, which is
// what the boot-time unit runs. It deliberately does not touch systemd: the
// unit must not manage itself from inside its own ExecStart.
func (a *Applier) Reapply(ctx context.Context) error {
	a.defaults()
	cfg, err := Load(a.Layout)
	if err != nil {
		return err
	}
	if cfg.Empty() {
		return a.clearRules(ctx)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return a.install(ctx, cfg)
}

// Clear withdraws the relay data plane completely and forgets the
// configuration, so this node goes back to being an ordinary one.
func (a *Applier) Clear(ctx context.Context) error {
	a.defaults()
	previous, loadErr := Load(a.Layout)
	if loadErr == nil {
		a.withdrawFirewall(previous)
	}
	if err := a.clearRules(ctx); err != nil {
		return errors.Join(loadErr, err)
	}
	if err := a.removeUnit(); err != nil {
		return errors.Join(loadErr, err)
	}
	return errors.Join(loadErr, Save(a.Layout, Config{}))
}

// install resolves every landing node and loads the resulting ruleset. A
// landing node whose address cannot be found at all is left out and reported,
// so one stale DNS record does not take down the relay's other mappings.
func (a *Applier) install(ctx context.Context, cfg Config) error {
	resolved := make([]ResolvedLanding, 0, len(cfg.Landings))
	var resolveErrs []error
	for _, landing := range cfg.Landings {
		ip, err := a.landingAddress(ctx, landing)
		if err != nil {
			resolveErrs = append(resolveErrs, err)
			continue
		}
		resolved = append(resolved, ResolvedLanding{Landing: landing, IP: ip})
	}
	if len(resolved) == 0 {
		return errors.Join(append(resolveErrs, fmt.Errorf("no relay landing node could be resolved; nothing was forwarded"))...)
	}
	if _, err := a.NFT(ctx, Ruleset(resolved), "-f", "-"); err != nil {
		return errors.Join(append(resolveErrs, fmt.Errorf("load relay ruleset: %w", err))...)
	}
	// Forwarded packets are routed rather than delivered locally, so the kernel
	// drops them outright unless forwarding is on.
	if err := a.Runner.Run(system.Command{Name: "sysctl", Args: []string{"-w", "net.ipv4.ip_forward=1"}}); err != nil {
		return errors.Join(append(resolveErrs, fmt.Errorf("enable IPv4 forwarding: %w", err))...)
	}
	if err := a.grantFirewall(resolved); err != nil {
		return errors.Join(append(resolveErrs, err)...)
	}
	return errors.Join(resolveErrs...)
}

// landingAddress resolves a landing node's hostname, falling back to the
// address the hub recorded when the link was made. Resolving on every apply is
// what follows a landing node that changed address; the recorded fallback is
// what survives a resolver that is unavailable at boot.
func (a *Applier) landingAddress(ctx context.Context, landing Landing) (string, error) {
	ip, err := a.Resolve(ctx, landing.Host)
	if err == nil {
		return ip, nil
	}
	if fallback := strings.TrimSpace(landing.Address); fallback != "" && net.ParseIP(fallback).To4() != nil {
		return fallback, nil
	}
	return "", fmt.Errorf("resolve relay landing node %s (%s): %w", landing.DisplayName(), landing.Host, err)
}

func (a *Applier) clearRules(ctx context.Context) error {
	if _, err := a.NFT(ctx, clearRuleset, "-f", "-"); err != nil {
		return fmt.Errorf("remove relay ruleset: %w", err)
	}
	return nil
}

// grantFirewall opens the relay's listen ports and permits the forwarded flows.
// A DNATed packet is never delivered locally, so an inbound allow is not what
// lets it through — the forward rules are. Both are issued because a host may
// filter either chain.
func (a *Applier) grantFirewall(resolved []ResolvedLanding) error {
	if a.Firewall == system.FirewallNone {
		return nil
	}
	cfg := Config{}
	for _, landing := range resolved {
		cfg.Landings = append(cfg.Landings, landing.Landing)
	}
	cmds := system.FirewallCommands(a.Firewall, cfg.ListenPorts())
	cmds = append(cmds, system.FirewallForwardCommands(a.Firewall, forwardRoutes(resolved), false)...)
	for _, cmd := range cmds {
		if err := a.Runner.Run(cmd); err != nil {
			return fmt.Errorf("open relay firewall rule (%s): %w", cmd, err)
		}
	}
	return nil
}

// withdrawFirewall drops the rules a previous configuration installed. Failures
// are deliberately ignored: a rule that is already gone is the ordinary case
// and both ufw and firewalld report it as an error, which must not block the
// apply that is replacing it.
func (a *Applier) withdrawFirewall(previous Config) {
	if a.Firewall == system.FirewallNone || previous.Empty() {
		return
	}
	cmds := system.FirewallRemoveCommands(a.Firewall, previous.ListenPorts())
	cmds = append(cmds, system.FirewallForwardCommands(a.Firewall, previousRoutes(previous), true)...)
	for _, cmd := range cmds {
		_ = a.Runner.Run(cmd)
	}
}

// forwardRoutes describes the forwarded flows for the host firewall: the
// address and port each mapping was rewritten to.
func forwardRoutes(resolved []ResolvedLanding) []system.ForwardRoute {
	var routes []system.ForwardRoute
	for _, landing := range resolved {
		for _, f := range sortedForwards(landing.Forwards) {
			routes = append(routes, system.ForwardRoute{Proto: f.Network, Address: landing.IP, Port: f.TargetPort})
		}
	}
	return routes
}

// previousRoutes rebuilds the routes a stored configuration granted. The
// address recorded when the link was made is used, because that is what the
// rules were written with.
func previousRoutes(cfg Config) []system.ForwardRoute {
	var routes []system.ForwardRoute
	for _, landing := range cfg.Landings {
		address := strings.TrimSpace(landing.Address)
		if address == "" {
			continue
		}
		for _, f := range sortedForwards(landing.Forwards) {
			routes = append(routes, system.ForwardRoute{Proto: f.Network, Address: address, Port: f.TargetPort})
		}
	}
	return routes
}

func (a *Applier) unitPath() string {
	return filepath.Join(a.SystemdDir, system.RelayService)
}

// installUnit writes and enables the boot-time unit. nftables rules do not
// survive a reboot, so without it a relay would come back up forwarding
// nothing while its subscription endpoints still pointed at it.
func (a *Applier) installUnit() error {
	unit, err := templatefs.Render("service/singbox-deploy-relay.service.tmpl", map[string]any{"Bin": a.Bin})
	if err != nil {
		return err
	}
	if err := a.WriteFile(a.unitPath(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write relay unit: %w", err)
	}
	return runAll(a.Runner,
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", system.RelayService}},
	)
}

func (a *Applier) removeUnit() error {
	if _, err := os.Stat(a.unitPath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := runAll(a.Runner,
		system.Command{Name: "systemctl", Args: []string{"disable", "--now", system.RelayService}},
	); err != nil {
		return err
	}
	if err := a.RemoveFile(a.unitPath()); err != nil {
		return fmt.Errorf("remove relay unit: %w", err)
	}
	return runAll(a.Runner, system.Command{Name: "systemctl", Args: []string{"daemon-reload"}})
}

func runAll(runner system.Runner, cmds ...system.Command) error {
	for _, cmd := range cmds {
		if err := runner.Run(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
	}
	return nil
}

func runNFT(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	binary, err := exec.LookPath("nft")
	if err != nil {
		return nil, fmt.Errorf("nftables is required to relay traffic but nft was not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, nftCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		// nft explains itself on stderr, which Output() folds into the error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// resolveIPv4 returns the first IPv4 address a hostname resolves to. A literal
// address is accepted as itself, so a landing node addressed by IP needs no
// resolver at all.
func resolveIPv4(ctx context.Context, host string) (string, error) {
	host = strings.TrimSpace(host)
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
		return "", fmt.Errorf("relay forwarding needs an IPv4 landing address, but %s is IPv6-only", host)
	}
	ctx, cancel := context.WithTimeout(ctx, nftCommandTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", &net.DNSError{Err: "no IPv4 address", Name: host}
	}
	return addrs[0].Unmap().String(), nil
}
