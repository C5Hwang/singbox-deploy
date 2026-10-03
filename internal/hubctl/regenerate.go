package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// RegenerateResult reports how one node fared in RegenerateConfigs.
type RegenerateResult struct {
	Name string
	Err  error
}

// defaultNginxConfPath is where install puts the managed Nginx config.
const defaultNginxConfPath = "/etc/nginx/conf.d/singbox-deploy.conf"

// RegenerateConfigs rebuilds the Hub (when includeHub is set) and each listed
// spoke from the current templates: sing-box's config.json and the managed
// Nginx config are rewritten and both services restarted, then subscriptions
// are republished. Self-update replaces binaries only, so this is how a
// template change such as a new server DNS setting reaches running nodes.
// The Hub and spokes get the same treatment: a spoke runs its agent's
// reconfigure, and the Hub runs the same steps locally.
//
// A failing node does not stop the run: every selected node is attempted and
// the returned error joins the failures. sing-box only switches to the new
// config after `sing-box check` accepts it, and a failed restart puts the old
// config back, so a failure leaves that node's proxy on its previous config.
func (c *Controller) RegenerateConfigs(ctx context.Context, includeHub bool, spokeIDs []string, log io.Writer) ([]RegenerateResult, error) {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	registered, err := nodes.Load(c.Layout)
	if err != nil {
		return nil, fmt.Errorf("load spoke registry: %w", err)
	}
	byID := make(map[string]nodes.Node, len(registered))
	for _, node := range registered {
		byID[node.ID] = node
	}

	total := len(spokeIDs)
	if includeHub {
		total++
	}
	// Spoke reconfigure reports its own phases through Progress; those would
	// fight this run's per-node bar, so the nested calls run without it.
	inner := *c
	inner.Progress = nil

	var results []RegenerateResult
	index := 0
	run := func(name, detail string, apply func() error) {
		index++
		deploy.EmitProgress(c.Progress, deploy.Event{Index: index, Total: total, Label: name, Detail: detail, Status: "running"})
		err := apply()
		status := "ok"
		if err != nil {
			status = "fail"
			fmt.Fprintf(log, "%s: %v\n", name, err)
		}
		deploy.EmitProgress(c.Progress, deploy.Event{Index: index, Total: total, Label: name, Detail: detail, Status: status, Err: err})
		results = append(results, RegenerateResult{Name: name, Err: err})
	}

	if includeHub {
		run("Hub", "regenerate sing-box and Nginx configs and restart both", func() error {
			return c.regenerateLocal(ctx, log)
		})
	}
	for _, id := range spokeIDs {
		node, ok := byID[id]
		name := id
		if ok {
			name = node.EffectiveAlias()
		}
		run(name, "regenerate sing-box and Nginx configs over WireGuard", func() error {
			switch {
			case !ok:
				return fmt.Errorf("spoke %s is no longer registered", id)
			case !node.Installed:
				return fmt.Errorf("spoke %s is not installed", name)
			}
			return inner.Reconfigure(ctx, node, log)
		})
	}

	var errs []error
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name, r.Err))
		}
	}
	return results, errors.Join(errs...)
}

// regenerateLocal is the Hub's counterpart of a spoke reconfigure: sing-box,
// then Nginx, then subscriptions, all from the Hub's persisted state. Ports
// and settings are unchanged, so the firewall is left alone.
func (c *Controller) regenerateLocal(ctx context.Context, log io.Writer) error {
	cfg, err := deploy.LoadProtocolConfig(c.Layout)
	if err != nil {
		return fmt.Errorf("load hub configuration: %w", err)
	}
	if err := c.regenerateLocalSingBox(cfg, log); err != nil {
		return err
	}
	if err := c.regenerateLocalNginx(cfg, log); err != nil {
		return err
	}
	// Like a spoke reconfigure, a refresh problem is a warning: the node's own
	// configs are already in place and a later refresh repairs the outputs.
	if err := c.RefreshSubscriptions(ctx); err != nil {
		fmt.Fprintf(log, "warning: subscription refresh had issues: %v\n", err)
	}
	return nil
}

// regenerateLocalSingBox renders config.json, validates it, swaps it in and
// restarts sing-box, putting the old config back if the restart fails.
func (c *Controller) regenerateLocalSingBox(cfg deploy.Config, log io.Writer) error {
	oldConfig, err := os.ReadFile(c.Layout.ConfigJSON)
	if err != nil {
		return fmt.Errorf("read current config.json: %w", err)
	}
	candidate := deploy.ProtocolConfigCandidate(c.Layout)
	defer os.Remove(candidate)
	if err := deploy.WriteProtocolConfigCandidate(c.Layout, cfg); err != nil {
		return fmt.Errorf("render config.json: %w", err)
	}
	if err := c.Runner.Run(system.Command{Name: c.Layout.SingBoxBin, Args: []string{"check", "-c", candidate}}); err != nil {
		return fmt.Errorf("validate config.json: %w", err)
	}
	if err := os.Rename(candidate, c.Layout.ConfigJSON); err != nil {
		return fmt.Errorf("activate config.json: %w", err)
	}
	fmt.Fprintln(log, "restarting sing-box on the hub...")
	if err := c.Runner.Run(system.Systemctl("restart", system.SingBoxService)); err != nil {
		restoreErr := deploy.WriteFile(c.Layout.ConfigJSON, oldConfig, 0o600)
		if restoreErr == nil {
			restoreErr = c.Runner.Run(system.Systemctl("restart", system.SingBoxService))
		}
		if restoreErr != nil {
			return errors.Join(fmt.Errorf("restart sing-box: %w", err), fmt.Errorf("restore previous config.json: %w", restoreErr))
		}
		return fmt.Errorf("restart sing-box: %w (previous config.json restored)", err)
	}
	return nil
}

// regenerateLocalNginx rewrites the managed Nginx config and restarts Nginx.
// A config Nginx rejects is replaced by the previous one, so a bad template
// never sits on disk waiting for the next restart to take the Hub's
// subscription and monitor endpoints down.
func (c *Controller) regenerateLocalNginx(cfg deploy.Config, log io.Writer) error {
	oldConf, readErr := os.ReadFile(c.NginxConfPath)
	fmt.Fprintln(log, "rewriting Nginx config and restarting Nginx on the hub...")
	err := deploy.ApplyManagedNginx(c.Runner, c.Layout, cfg, c.NginxConfPath)
	if err == nil {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("apply Nginx config: %w", err)
	}
	restoreErr := deploy.WriteFile(c.NginxConfPath, oldConf, 0o644)
	if restoreErr == nil {
		restoreErr = deploy.RunCommands(c.Runner, system.Systemctl("restart", "nginx"))
	}
	if restoreErr != nil {
		return errors.Join(fmt.Errorf("apply Nginx config: %w", err), fmt.Errorf("restore previous Nginx config: %w", restoreErr))
	}
	return fmt.Errorf("apply Nginx config: %w (previous Nginx config restored)", err)
}
