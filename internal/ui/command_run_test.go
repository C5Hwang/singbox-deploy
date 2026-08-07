package ui

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// A run whose slowest step is also its last must not render as complete while
// that step is still working, so the bar reports finished steps rather than the
// step currently in flight.
func TestRunPercentCountsFinishedStepsNotStartedOnes(t *testing.T) {
	tests := []struct {
		name  string
		event deploy.Event
		want  float64
	}{
		{"single step running", deploy.Event{Index: 1, Total: 1, Status: "running"}, 0},
		{"single step done", deploy.Event{Index: 1, Total: 1, Status: "ok"}, 1},
		{"first of three running", deploy.Event{Index: 1, Total: 3, Status: "running"}, 0},
		{"first of three done", deploy.Event{Index: 1, Total: 3, Status: "ok"}, 1.0 / 3},
		{"last of three running", deploy.Event{Index: 3, Total: 3, Status: "running"}, 2.0 / 3},
		{"last of three done", deploy.Event{Index: 3, Total: 3, Status: "ok"}, 1},
		{"failed step", deploy.Event{Index: 2, Total: 4, Status: "fail"}, 0.5},
		{"terminal done status", deploy.Event{Index: 1, Total: 1, Status: "done"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCommandRun()
			r.events = []deploy.Event{tt.event}
			if got := r.percent(); got != tt.want {
				t.Fatalf("percent = %v, want %v", got, tt.want)
			}
		})
	}

	r := newCommandRun()
	if got := r.percent(); got != 0 {
		t.Fatalf("percent before any event = %v, want 0", got)
	}
	r.events = []deploy.Event{{Index: 1, Total: 0, Status: "running"}}
	if got := r.percent(); got != 0 {
		t.Fatalf("percent with unknown total = %v, want 0", got)
	}
}
