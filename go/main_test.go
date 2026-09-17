package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitForTelemetryShutdownCollectsAllFailures(t *testing.T) {
	first, second := errors.New("trace shutdown failed"), errors.New("log shutdown failed")
	results := make(chan error, 3)
	results <- first
	results <- nil
	results <- second
	got := waitForTelemetryShutdown(context.Background(), results, 3)
	if !errors.Is(got, first) || !errors.Is(got, second) {
		t.Fatalf("shutdown dropped errors: %v", got)
	}
	if err := waitForTelemetryShutdown(context.Background(), nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForTelemetryShutdownStopsAtDeadlineWhenAProviderBlocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	results := make(chan error, 1)
	done := make(chan error, 1)
	go func() { done <- waitForTelemetryShutdown(ctx, results, 1) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait exceeded the shared shutdown deadline")
	}
	// A shutdown finishing after the deadline must not block its sending goroutine.
	results <- nil
}
