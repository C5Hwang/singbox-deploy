package ipban

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/templatefs"
)

const nftCommandTimeout = 10 * time.Second

// clearRuleset declares the table so the delete always has something to remove,
// whether or not the rules were ever installed.
var clearRuleset = "table inet " + Table + " {}\ndelete table inet " + Table + "\n"

// Applier installs this node's ban rules and the boot-time unit that puts them
// back after a reboot. Every external effect is a field so tests can record it;
// the zero value fills each in with the production implementation.
type Applier struct {
	Layout paths.Layout
	// Bin is the binary the boot-time unit reapplies with: the hub's own binary
	// on the hub, the agent binary on a spoke.
	Bin        string
	SystemdDir string
	Runner     system.Runner

	// NFT runs the nft binary with the rendered ruleset on stdin.
	NFT        func(ctx context.Context, stdin string, args ...string) ([]byte, error)
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

// Apply persists cfg as this node's rule set and installs it. An empty
// configuration lifts every ban and removes the unit.
//
// The configuration is written before the ruleset is loaded, so a failure part
// way through leaves the desired state on disk for the boot-time unit or the
// next apply to converge on.
func (a *Applier) Apply(ctx context.Context, cfg Config) error {
	a.defaults()
	if cfg.Empty() {
		return a.Clear(ctx)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := Save(a.Layout, cfg); err != nil {
		return err
	}
	if _, err := a.NFT(ctx, Ruleset(cfg), "-f", "-"); err != nil {
		return fmt.Errorf("load IP ban ruleset: %w", err)
	}
	return a.installUnit()
}

// Reapply installs whatever the stored configuration says, which is what the
// boot-time unit runs. It does not touch systemd: the unit must not manage
// itself from inside its own ExecStart.
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
	if _, err := a.NFT(ctx, Ruleset(cfg), "-f", "-"); err != nil {
		return fmt.Errorf("load IP ban ruleset: %w", err)
	}
	return nil
}

// Clear lifts every ban on this node and forgets the configuration.
func (a *Applier) Clear(ctx context.Context) error {
	a.defaults()
	if err := a.clearRules(ctx); err != nil {
		return err
	}
	if err := a.removeUnit(); err != nil {
		return err
	}
	return Save(a.Layout, Config{})
}

func (a *Applier) clearRules(ctx context.Context) error {
	if _, err := a.NFT(ctx, clearRuleset, "-f", "-"); err != nil {
		return fmt.Errorf("remove IP ban ruleset: %w", err)
	}
	return nil
}

func (a *Applier) unitPath() string {
	return filepath.Join(a.SystemdDir, system.IPBanService)
}

// installUnit writes and enables the boot-time unit. nftables rules do not
// survive a reboot, so without it every ban would silently lapse.
func (a *Applier) installUnit() error {
	unit, err := templatefs.Render("service/singbox-deploy-ipban.service.tmpl", map[string]any{"Bin": a.Bin})
	if err != nil {
		return err
	}
	if err := a.WriteFile(a.unitPath(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write IP ban unit: %w", err)
	}
	return runAll(a.Runner,
		system.Command{Name: "systemctl", Args: []string{"daemon-reload"}},
		system.Command{Name: "systemctl", Args: []string{"enable", system.IPBanService}},
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
		system.Command{Name: "systemctl", Args: []string{"disable", "--now", system.IPBanService}},
	); err != nil {
		return err
	}
	if err := a.RemoveFile(a.unitPath()); err != nil {
		return fmt.Errorf("remove IP ban unit: %w", err)
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
		return nil, fmt.Errorf("nftables is required to enforce IP bans but nft was not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, nftCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// Command dispatches the "ipban" subcommand both the hub and the agent binary
// expose. "apply" is what the boot-time unit runs; "clear" is the manual escape
// hatch for lifting every ban on a node the hub can no longer reach.
func Command(ctx context.Context, args []string, bin string) error {
	applier := &Applier{Bin: bin}
	if len(args) != 1 {
		return usage()
	}
	switch args[0] {
	case "apply":
		return applier.Reapply(ctx)
	case "clear":
		return applier.Clear(ctx)
	default:
		return usage()
	}
}

func usage() error { return fmt.Errorf("usage: ipban apply|clear") }
