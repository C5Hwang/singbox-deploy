package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// packageTargetKey is the form field the node picker of a package grant writes
// to. It is its own key rather than the reset picker's because the two forms
// ask different questions of the same list.
const packageTargetKey = "package_target"

var (
	// grantHubTrafficPackage adds a package to the hub's own store. It writes
	// beside the monitor service, which reads the package afresh on its next
	// sample — see monitor.AddTrafficPackage.
	grantHubTrafficPackage = monitor.AddTrafficPackage
	// grantSpokeTrafficPackage asks one spoke's Agent to add the package over
	// the overlay; the Agent reconciles its quota at once.
	grantSpokeTrafficPackage = func(ctx context.Context, node nodes.Node, grant nodeapi.TrafficPackageGrant) (nodeapi.TrafficUsageUpdate, error) {
		ctrl := &hubctl.Controller{Layout: monitorUILayout(), ExpectedVersion: toolVersion}
		return ctrl.GrantTrafficPackage(ctx, node, grant)
	}
	// readSpokeTrafficCycle reads the cycle a spoke's Agent is in, which is
	// what a grant has to name so it cannot land in the next month.
	readSpokeTrafficCycle = func(ctx context.Context, node nodes.Node) (nodeapi.TrafficUsage, error) {
		return fetchSpokeTrafficUsage(ctx, node)
	}
	// packageGrantNow is the clock a hub grant is stamped with. The stamp
	// decides which cycle the grant counts in, so it is the network's GMT when
	// that can be had, and the host's otherwise — the same fallback the monitor
	// service makes for its own samples.
	packageGrantNow = func(ctx context.Context, logs *logWriter) time.Time {
		now, err := monitor.NetworkGMTNow(ctx)
		if err != nil {
			fmt.Fprintf(logs, "warning: network GMT unavailable, stamping the grant with the host clock: %v\n", err)
			return time.Now().UTC()
		}
		return now
	}
)

// packageGrantResult is what one node came back with: the package now in force
// there, or why the grant did not land.
type packageGrantResult struct {
	label   string
	pkg     monitor.TrafficPackage
	warning string
	err     error
}

// packageGrantFields is the grant form: which nodes, then how much of each
// direction to add.
func (tm *monitorManager) packageGrantFields() []field {
	fields := []field{{
		key:     packageTargetKey,
		label:   "Nodes to grant a traffic package",
		options: resetTargetOptions(tm.trafficSpokes()),
		multi:   true,
		note: "Adds to each node's allowance for the current cycle only; the configured limits are untouched.\n" +
			"The package lapses at the next reset.",
	}}
	return append(fields, fieldsFromParameters(uiparams.MonitorPackageGrantFields())...)
}

// packageGrantTargets expands the ticked options into the nodes to grant to.
func (tm *monitorManager) packageGrantTargets() []resetTarget {
	return expandResetTargets(tm.values[packageTargetKey], tm.trafficSpokes())
}

// packageGrantDelta is the package the form asks to add.
func (tm *monitorManager) packageGrantDelta() monitor.TrafficPackage {
	return uiparams.PackageFromValues(tm.values,
		uiparams.KeyPackageGrantIn, uiparams.KeyPackageGrantOut, uiparams.KeyPackageGrantTotal)
}

// validateField is the monitor screen's form validation: the shared parameter
// rules, then the package rules, which need the screen's knowledge of each
// node's limits.
func (tm *monitorManager) validateField(f field, val string, vals map[string]string) error {
	if err := validateMonitorField(f, val, vals); err != nil {
		return err
	}
	return tm.validatePackageField(f.key, val, vals)
}

// validatePackageField refuses a package for a direction that has no limit. A
// package can only extend an allowance, so on an unlimited direction it would
// be saved and never spent — and an operator who typed it meant something.
func (tm *monitorManager) validatePackageField(key, val string, vals map[string]string) error {
	if key == packageTargetKey {
		return validateResetTarget(val)
	}
	direction, ok := packageDirection(key)
	if !ok {
		return nil
	}
	size, err := uiparams.ParseTrafficSize(val)
	if err != nil || size == 0 {
		return err
	}
	for _, target := range tm.packageTargetsFor(key, vals) {
		if direction.limit(tm.packageLimitsFor(target)) > 0 {
			continue
		}
		return fmt.Errorf("%s has no %s traffic limit, so a package cannot extend it; leave this at 0",
			target.label, direction.name)
	}
	return nil
}

// packageTargetsFor names the nodes a package field applies to: for a grant,
// whichever were ticked; for an adjustment, the node being adjusted.
func (tm *monitorManager) packageTargetsFor(key string, vals map[string]string) []resetTarget {
	switch key {
	case uiparams.KeyPackageGrantIn, uiparams.KeyPackageGrantOut, uiparams.KeyPackageGrantTotal:
		return expandResetTargets(vals[packageTargetKey], tm.trafficSpokes())
	}
	if tm.action == monitorActionSpokeUsage {
		if node, ok := tm.spokeTrafficNode(); ok {
			return []resetTarget{{label: spokeOptionLabel(node), node: node}}
		}
		return nil
	}
	return []resetTarget{{label: resetHubOption, hub: true}}
}

// packageLimitsFor reads the configured limits a package would extend: the
// hub's from its install state, a spoke's from the registry the hub pushed.
func (tm *monitorManager) packageLimitsFor(target resetTarget) monitor.TrafficLimits {
	if target.hub {
		return monitor.TrafficLimits{
			InBytes:    tm.cfg.TrafficInLimitBytes,
			OutBytes:   tm.cfg.TrafficOutLimitBytes,
			TotalBytes: tm.cfg.TrafficTotalLimitBytes,
		}
	}
	return monitor.TrafficLimits{
		InBytes:    target.node.TrafficInLimitBytes,
		OutBytes:   target.node.TrafficOutLimitBytes,
		TotalBytes: target.node.TrafficTotalLimitBytes,
	}
}

// trafficDirection is one of the three figures a limit and a package both
// have, with the words the screens use for it.
type trafficDirection struct {
	name  string
	limit func(monitor.TrafficLimits) uint64
}

var (
	directionIn    = trafficDirection{name: "inbound", limit: func(l monitor.TrafficLimits) uint64 { return l.InBytes }}
	directionOut   = trafficDirection{name: "outbound", limit: func(l monitor.TrafficLimits) uint64 { return l.OutBytes }}
	directionTotal = trafficDirection{name: "total", limit: func(l monitor.TrafficLimits) uint64 { return l.TotalBytes }}
)

func packageDirection(key string) (trafficDirection, bool) {
	switch key {
	case uiparams.KeyPackageIn, uiparams.KeyPackageGrantIn:
		return directionIn, true
	case uiparams.KeyPackageOut, uiparams.KeyPackageGrantOut:
		return directionOut, true
	case uiparams.KeyPackageTotal, uiparams.KeyPackageGrantTotal:
		return directionTotal, true
	}
	return trafficDirection{}, false
}

// grantTrafficPackageRun adds the package on every target, one step each, then
// refreshes the hub's snapshot so the dashboard shows the new allowances
// without waiting for the timer. A node that cannot be reached does not stop
// the rest, for the same reason a fleet-wide clear does not.
func grantTrafficPackageRun(
	ctx context.Context,
	layout paths.Layout,
	cfg deploy.Config,
	targets []resetTarget,
	delta monitor.TrafficPackage,
	logs *logWriter,
	progress func(deploy.Event),
) ([]packageGrantResult, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no node was selected to grant a traffic package to")
	}
	if delta.IsZero() {
		return nil, fmt.Errorf("the package adds nothing; enter a size for at least one direction")
	}
	total := len(targets) + 1
	results := make([]packageGrantResult, 0, len(targets))
	var failures []error
	for i, target := range targets {
		event := deploy.Event{
			Index: i + 1, Total: total,
			Label: "Traffic package", Detail: target.label, Status: "running",
		}
		deploy.EmitProgress(progress, event)
		result := grantOneTarget(ctx, layout, cfg, target, delta, logs)
		results = append(results, result)
		switch {
		case result.err != nil:
			event.Status = "fail"
			event.Err = result.err
			failures = append(failures, fmt.Errorf("grant traffic package on %s: %w", target.label, result.err))
		case result.warning != "":
			event.Status = "warn"
			event.Err = errors.New(result.warning)
			fmt.Fprintf(logs, "warning: the package was granted on %s, but quota reconciliation reported: %s; inspect the Agent service state\n",
				target.label, result.warning)
		default:
			event.Status = "ok"
			fmt.Fprintf(logs, "granted a traffic package on %s; this cycle's package is now in %s, out %s, total %s\n",
				target.label, byteSize(result.pkg.InBytes), byteSize(result.pkg.OutBytes), byteSize(result.pkg.TotalBytes))
		}
		deploy.EmitProgress(progress, event)
	}
	snapshot := deploy.Event{
		Index: total, Total: total,
		Label: "Monitor snapshot", Detail: "refresh the hub dashboard from the spokes", Status: "running",
	}
	deploy.EmitProgress(progress, snapshot)
	if err := refreshSpokeMonitorSnapshot(ctx); err != nil {
		fmt.Fprintf(logs, "warning: the Hub monitor snapshot could not be refreshed: %v; the periodic refresh will retry\n", err)
		snapshot.Status = "warn"
		snapshot.Err = err
	} else {
		snapshot.Status = "ok"
	}
	deploy.EmitProgress(progress, snapshot)
	return results, errors.Join(failures...)
}

func grantOneTarget(
	ctx context.Context,
	layout paths.Layout,
	cfg deploy.Config,
	target resetTarget,
	delta monitor.TrafficPackage,
	logs *logWriter,
) packageGrantResult {
	result := packageGrantResult{label: target.label}
	if target.hub {
		pkg, err := grantHubTrafficPackage(layout, cfg.ResetDay, cfg.ResetHour, packageGrantNow(ctx, logs), delta)
		result.pkg, result.err = pkg, err
		return result
	}
	current, err := readSpokeTrafficCycle(ctx, target.node)
	if err != nil {
		result.err = fmt.Errorf("read the spoke's quota cycle: %w", err)
		return result
	}
	update, err := grantSpokeTrafficPackage(ctx, target.node, nodeapi.TrafficPackageGrant{
		InBytes: delta.InBytes, OutBytes: delta.OutBytes, TotalBytes: delta.TotalBytes,
		ExpectedCycleStart: current.CycleStart,
	})
	if err != nil {
		result.err = err
		return result
	}
	result.pkg = monitor.TrafficPackage{
		InBytes:    update.Applied.Package.InBytes,
		OutBytes:   update.Applied.Package.OutBytes,
		TotalBytes: update.Applied.Package.TotalBytes,
	}
	result.warning = strings.TrimSpace(update.Warning)
	return result
}

// packageSummary words a package for a summary row.
func packageSummary(pkg monitor.TrafficPackage) string {
	if pkg.IsZero() {
		return "none"
	}
	return "in " + byteSize(pkg.InBytes) + " · out " + byteSize(pkg.OutBytes) + " · total " + byteSize(pkg.TotalBytes)
}

// packageFromUsage reads the package a spoke reported back into the monitor's
// own type, which the forms and summaries are written against.
func packageFromUsage(usage nodeapi.TrafficUsage) monitor.TrafficPackage {
	return monitor.TrafficPackage{
		InBytes:    usage.Package.InBytes,
		OutBytes:   usage.Package.OutBytes,
		TotalBytes: usage.Package.TotalBytes,
	}
}
