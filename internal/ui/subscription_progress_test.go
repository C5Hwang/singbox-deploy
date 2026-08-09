package ui

import (
	"context"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// replayPercent feeds recorded events through the shared run state and reports
// where the progress bar ends up, which is what the operator actually sees.
func replayPercent(events []deploy.Event) float64 {
	run := newCommandRun()
	for i := range events {
		run.events = append(run.events, events[i])
	}
	return run.percent()
}

func recordProgress(events *[]deploy.Event) func(deploy.Event) {
	return func(e deploy.Event) { *events = append(*events, e) }
}

// Every flow that shows the shared run screen must report progress. A flow that
// emits nothing leaves the bar at 0% from start to finish, so a slow republish
// looks indistinguishable from a hung one.
func TestRefreshSubscriptionSourcesReportsProgress(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	var events []deploy.Event

	if err := refreshSubscriptionSources(context.Background(), layout, recordProgress(&events)); err != nil {
		t.Fatalf("refreshSubscriptionSources: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("refresh reported no progress; the bar would stay at 0%")
	}
	if got := replayPercent(events); got != 1 {
		t.Fatalf("progress after a completed refresh = %v, want 1", got)
	}
	if last := events[len(events)-1]; last.Status == "running" {
		t.Fatalf("refresh ended on a running event: %+v", last)
	}
}

func TestApplySourceOrderReportsProgress(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	withSubscriptionDeps(t, layout)
	sm := &subscriptionManager{}
	sm.reorder = newReorderForm([]reorderItem{{key: "local", label: "Local"}})
	var events []deploy.Event

	if err := sm.applySourceOrder(context.Background(), recordProgress(&events)); err != nil {
		t.Fatalf("applySourceOrder: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("reorder reported no progress; the bar would stay at 0%")
	}
	if got := replayPercent(events); got != 1 {
		t.Fatalf("progress after a completed reorder = %v, want 1", got)
	}
	if last := events[len(events)-1]; last.Status == "running" {
		t.Fatalf("reorder ended on a running event: %+v", last)
	}
}

// The bar must climb rather than jump: a flow whose steps all report Total=N
// shows the operator which step is running.
func TestSubscriptionRunProgressAdvancesPerStep(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	var events []deploy.Event
	if err := refreshSubscriptionSources(context.Background(), layout, recordProgress(&events)); err != nil {
		t.Fatalf("refreshSubscriptionSources: %v", err)
	}
	seen := map[float64]bool{}
	for i := range events {
		seen[replayPercent(events[:i+1])] = true
	}
	if len(seen) < 2 {
		t.Fatalf("progress never advanced through intermediate values: %v", seen)
	}
}
