package main

import (
	"context"
	"fmt"
	"io"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/relay"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// ApplyRelay installs the relay job the hub pushed. The whole job is replaced
// on every request, so a landing node the hub withdrew stops being forwarded
// without needing a second call, and an empty request retires the data plane.
//
// It deliberately does not take the mutation gate the install path uses: the
// forwarding rules share no state with the sing-box deployment, and a relay
// that has to wait for a long install before it stops carrying an exhausted
// node's traffic would defeat the point of the quota fallback.
func (h *agentHandler) ApplyRelay(ctx context.Context, req nodeapi.RelayRequest, log io.Writer) error {
	cfg := relayConfigFromRequest(req)
	applier := &relay.Applier{
		Layout:     h.layout,
		Bin:        agentBinaryPath,
		SystemdDir: h.systemdDir,
		Firewall:   system.DetectFirewall(),
		Runner:     h.commandRunner(ctx, log),
	}
	if cfg.Empty() {
		fmt.Fprintln(log, "withdrawing relay forwarding")
		return applier.Clear(ctx)
	}
	fmt.Fprintf(log, "forwarding for %d landing node(s)\n", len(cfg.Landings))
	return applier.Apply(ctx, cfg)
}

func relayConfigFromRequest(req nodeapi.RelayRequest) relay.Config {
	cfg := relay.Config{Landings: make([]relay.Landing, 0, len(req.Landings))}
	for _, landing := range req.Landings {
		forwards := make([]relay.Forward, 0, len(landing.Forwards))
		for _, f := range landing.Forwards {
			forwards = append(forwards, relay.Forward{
				Protocol:   f.Protocol,
				Network:    f.Network,
				ListenPort: f.ListenPort,
				TargetPort: f.TargetPort,
			})
		}
		cfg.Landings = append(cfg.Landings, relay.Landing{
			NodeID:   landing.NodeID,
			Name:     landing.Name,
			Host:     landing.Host,
			Address:  landing.Address,
			Forwards: forwards,
		})
	}
	return cfg
}
