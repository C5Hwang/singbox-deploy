package relay

import (
	"context"
	"log"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// SingBox is the sing-box half of quota enforcement, as the monitor drives it.
type SingBox interface {
	Start() error
	Stop() error
	IsActive() (bool, error)
}

// QuotaController extends quota enforcement to this node's relay data plane. A
// relay whose quota is exhausted has to stop forwarding as well as stop
// serving: the forwarding rules live in the kernel, entirely independent of
// sing-box, and would otherwise keep spending this node's traffic allowance on
// other nodes' clients for the rest of the cycle.
//
// The relay half is best-effort. Withdrawing the rules must never prevent
// sing-box from being stopped, and the hub independently republishes the
// landing node's own address once it sees the relay's quota is gone, so a
// failure here costs traffic but never connectivity. Failures are logged.
type QuotaController struct {
	SingBox SingBox
	Applier *Applier
	// Logf reports the relay half's failures. nil uses the standard logger.
	Logf func(format string, args ...any)
}

// NewQuotaController pairs singBox with the relay data plane of the node at
// layout, reapplying with bin after a suspension.
func NewQuotaController(singBox SingBox, layout paths.Layout, bin string) QuotaController {
	return QuotaController{
		SingBox: singBox,
		Applier: &Applier{Layout: layout, Bin: bin, Firewall: system.DetectFirewall()},
	}
}

// Stop withdraws the forwarding rules, then stops sing-box.
func (q QuotaController) Stop() error {
	if q.Applier != nil {
		if err := q.Applier.Suspend(context.Background()); err != nil {
			q.logf("monitor: could not withdraw relay forwarding for the quota stop: %v", err)
		}
	}
	return q.SingBox.Stop()
}

// Start restores the forwarding rules, then starts sing-box. A node that never
// relayed has nothing stored, so the restore is a no-op for it.
func (q QuotaController) Start() error {
	if q.Applier != nil {
		if err := q.Applier.Resume(context.Background()); err != nil {
			q.logf("monitor: could not restore relay forwarding after the quota stop: %v", err)
		}
	}
	return q.SingBox.Start()
}

// IsActive reports on sing-box alone: it is what decides whether an exceeded
// quota still has something to stop, and the forwarding rules follow it.
func (q QuotaController) IsActive() (bool, error) { return q.SingBox.IsActive() }

func (q QuotaController) logf(format string, args ...any) {
	if q.Logf != nil {
		q.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
