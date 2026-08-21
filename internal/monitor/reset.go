package monitor

import (
	"errors"
	"fmt"
	"os"
)

// ResetScope names one body of recorded history an operator can clear. The
// three are deliberately separate: the dashboard shows them on three pages, and
// clearing one because another became misleading is the whole point.
type ResetScope string

const (
	// ResetScopeClients clears the per-address traffic history — every tier of
	// it, and every strand: what the node terminated itself and what it relayed
	// for a client to a landing node.
	ResetScopeClients ResetScope = "clients"
	// ResetScopeLatency clears the carrier probe history. It deliberately
	// leaves the relay probes alone: those belong to a relay link, and a link
	// is cleared through the relay it belongs to.
	ResetScopeLatency ResetScope = "latency"
	// ResetScopeRelayLatency clears the probe history of one relay link, named
	// by its target. An empty target clears every relay probe on the node.
	ResetScopeRelayLatency ResetScope = "relay-latency"
)

// Valid reports whether scope names something this release can clear.
func (s ResetScope) Valid() bool {
	switch s {
	case ResetScopeClients, ResetScopeLatency, ResetScopeRelayLatency:
		return true
	}
	return false
}

// ResetHistory clears one scope from the store at dbPath.
//
// It opens the database rather than taking a running Monitor, because the
// process asking is rarely the process sampling: on the hub the TUI asks while
// the monitor service holds the store, and on a spoke the agent asks while its
// own in-process monitor does. SQLite serializes the two through the file lock
// and the store's busy timeout, and the sampler needs no telling: its next
// round records the traffic since its last read, which is exactly the history
// that survives a reset.
//
// A database that was never created has nothing to clear and is not an error —
// a node whose monitor has never run is already in the state being asked for.
func ResetHistory(dbPath string, scope ResetScope, target string) error {
	if !scope.Valid() {
		return fmt.Errorf("unknown monitor reset scope %q", scope)
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat monitor store: %w", err)
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		return fmt.Errorf("open monitor store: %w", err)
	}
	defer store.Close()
	switch scope {
	case ResetScopeClients:
		return store.ResetIPTraffic()
	case ResetScopeLatency:
		return store.ResetCarrierPingSamples()
	default:
		return store.ResetRelayPingSamples(target)
	}
}
