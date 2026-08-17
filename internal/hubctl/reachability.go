package hubctl

import (
	"errors"
	"sync"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
)

// unreachableTTL is how long one failed liveness probe keeps the hub from
// dialing a spoke again on a dashboard's behalf. It is longer than the monitor
// refresh interval that produces those probes, so a node that is down stays
// marked down between rounds, and shorter than a multiple of it, so a node that
// comes back is dialed again within one round of its recovery.
const unreachableTTL = 90 * time.Second

// fleetReachability is the tracker every Controller in this process reports to
// unless it was given its own. See Controller.reach.
var fleetReachability = newReachability()

// reachability remembers whether the hub could reach each spoke at all, as
// observed by the liveness probes the refresh timers already run. Drill-down
// reads consult it so a powered-off node costs a lookup rather than a dial that
// waits out its whole timeout: dropped packets inside the tunnel look exactly
// like a slow host, and only a previous observation can tell them apart.
type reachability struct {
	mu    sync.Mutex
	state map[string]reachObservation
	// now is a seam for tests; nil means time.Now.
	now func() time.Time
}

type reachObservation struct {
	at time.Time
	// err is nil once the hub has reached the node, and otherwise the transport
	// failure that says it could not.
	err error
}

func newReachability() *reachability {
	return &reachability{state: map[string]reachObservation{}}
}

func (r *reachability) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// observe records what one authenticated liveness probe learned about whether
// the hub can talk to a spoke. An agent that answered with a status code is
// reachable, however unwelcome its answer — its own error is the useful one and
// it will arrive just as fast on the next call — so only a transport failure
// counts against the node.
func (r *reachability) observe(nodeID string, probeErr error) {
	if r == nil || nodeID == "" {
		return
	}
	var status *nodeapi.StatusError
	if probeErr != nil && errors.As(probeErr, &status) {
		probeErr = nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[nodeID] = reachObservation{at: r.clock(), err: probeErr}
}

// lastFailure reports the transport failure that last kept the hub from
// reaching nodeID, while that observation is recent enough to act on. An
// expired or missing one is not a verdict: the caller dials and finds out.
func (r *reachability) lastFailure(nodeID string) (error, bool) {
	if r == nil || nodeID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	observation, ok := r.state[nodeID]
	if !ok || observation.err == nil {
		return nil, false
	}
	if r.clock().Sub(observation.at) > unreachableTTL {
		return nil, false
	}
	return observation.err, true
}
