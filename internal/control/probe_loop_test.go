package control

import (
	"context"
	"testing"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/probes"
)

// TestProbePublishAbortsWhenStopHoldsPubLock guards the stop path: a caller
// such as restore holds pubMu while it waits for the probe loop to exit, so the
// loop's publish step must observe cancellation instead of blocking on pubMu.
func TestProbePublishAbortsWhenStopHoldsPubLock(t *testing.T) {
	s := &Service{}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.applyMeasurements(ctx, []probes.Measurement{{Result: contract.ProbeResult{
			Kind: contract.ProbeTransport, Exit: "x-health", Outcome: contract.OutcomeSuccess,
		}}})
	}()

	select {
	case <-done:
		t.Fatal("published health while pubMu was held")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not abort the blocked publish")
	}
}
