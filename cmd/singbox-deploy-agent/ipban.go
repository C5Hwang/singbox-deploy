package main

import (
	"context"
	"fmt"
	"io"

	"github.com/C5Hwang/singbox-deploy/internal/ipban"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
)

// ApplyIPBan installs the ban rule set the hub pushed. The whole set is
// replaced on every request, so a lifted ban stops being enforced without a
// second call, and an empty request removes the table and its unit.
//
// Like the relay job it does not take the install mutation gate: the drop
// rules share no state with the sing-box deployment, and a ban should not wait
// behind a long install to take effect.
func (h *agentHandler) ApplyIPBan(ctx context.Context, req nodeapi.IPBanRequest, log io.Writer) error {
	cfg := ipBanConfigFromRequest(req)
	applier := &ipban.Applier{
		Layout:     h.layout,
		Bin:        agentBinaryPath,
		SystemdDir: h.systemdDir,
		Runner:     h.commandRunner(ctx, log),
	}
	if cfg.Empty() {
		fmt.Fprintln(log, "lifting every IP ban on this node")
		return applier.Clear(ctx)
	}
	for _, rule := range cfg.Rules {
		fmt.Fprintf(log, "banning %d address(es) on %s\n", len(rule.Addresses), rule.Label)
	}
	return applier.Apply(ctx, cfg)
}

func ipBanConfigFromRequest(req nodeapi.IPBanRequest) ipban.Config {
	cfg := ipban.Config{Rules: make([]ipban.Rule, 0, len(req.Rules))}
	for _, rule := range req.Rules {
		cfg.Rules = append(cfg.Rules, ipban.Rule{
			Label:     rule.Label,
			Addresses: append([]string(nil), rule.Addresses...),
			TCP:       append([]int(nil), rule.TCP...),
			UDP:       append([]int(nil), rule.UDP...),
		})
	}
	return cfg
}
