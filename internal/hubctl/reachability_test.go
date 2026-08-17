package hubctl

import (
	"fmt"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
)

func TestReachabilityRecordsOnlyTransportFailures(t *testing.T) {
	reach := newReachability()

	reach.observe("node-a", fmt.Errorf("dial tcp 10.90.0.2:19091: i/o timeout"))
	if _, ok := reach.lastFailure("node-a"); !ok {
		t.Fatal("a transport failure must mark the node unreachable")
	}

	// The agent answered, so the hub can reach it; its own error is the useful
	// one and arrives just as fast on the next call.
	reach.observe("node-b", &nodeapi.StatusError{Code: 502, Body: "no monitor"})
	if _, ok := reach.lastFailure("node-b"); ok {
		t.Fatal("an agent status answer must leave the node reachable")
	}

	// Recovery is one successful probe away.
	reach.observe("node-a", nil)
	if _, ok := reach.lastFailure("node-a"); ok {
		t.Fatal("a successful probe must clear the failure")
	}

	// A node nobody has probed is not a verdict either way: the caller dials.
	if _, ok := reach.lastFailure("node-unknown"); ok {
		t.Fatal("an unprobed node must not count as unreachable")
	}
}

// The refresh timer is what keeps observations current. If it stops, a stale
// failure must not keep a recovered node dark forever.
func TestReachabilityExpiresAStaleFailure(t *testing.T) {
	now := time.Now()
	reach := newReachability()
	reach.now = func() time.Time { return now }
	reach.observe("node-a", fmt.Errorf("overlay unreachable"))

	now = now.Add(unreachableTTL - time.Second)
	if _, ok := reach.lastFailure("node-a"); !ok {
		t.Fatal("a failure must hold for at least the refresh interval")
	}

	now = now.Add(2 * time.Second)
	if _, ok := reach.lastFailure("node-a"); ok {
		t.Fatal("a failure older than the TTL must stop deciding for the caller")
	}
}
