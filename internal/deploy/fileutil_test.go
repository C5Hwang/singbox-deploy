package deploy

import (
	"context"
	"errors"
	"testing"
)

func TestRunStepsStopsBeforeNextStepWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ranSecond bool
	err := RunSteps(ctx, nil, []Step{
		{
			Label: "first",
			Run: func(context.Context) error {
				cancel()
				return nil
			},
		},
		{
			Label: "second",
			Run: func(context.Context) error {
				ranSecond = true
				return nil
			},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSteps error = %v, want context.Canceled", err)
	}
	if ranSecond {
		t.Fatal("RunSteps started a new mutation step after cancellation")
	}
}
