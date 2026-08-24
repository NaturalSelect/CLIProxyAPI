package logging

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestTimingTrackerRecordsIndependentTurns(t *testing.T) {
	startedAt := time.Now().Add(-time.Second)
	tracker := NewRequestTimingTracker("request-1", "GET /v1/responses", startedAt)

	firstTurn := tracker.BeginTurn("responses_websocket_turn", "gpt-5.6-luna")
	firstTurn.MarkOnce(TimingStageStreamExecutionEntered, RequestTimingEventDetails{})
	firstTurn.MarkOnce(TimingStageFirstVisibleText, RequestTimingEventDetails{})
	firstTurn.Complete("completed")

	secondTurn := tracker.BeginTurn("responses_websocket_turn", "gpt-5.6-luna")
	secondTurn.MarkOnce(TimingStageStreamExecutionEntered, RequestTimingEventDetails{})
	secondTurn.MarkOnce(TimingStageFirstVisibleText, RequestTimingEventDetails{})
	secondTurn.Complete("completed")

	snapshot := tracker.Snapshot()
	if len(snapshot.Turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(snapshot.Turns))
	}
	for index, turn := range snapshot.Turns {
		if !turn.Completed || turn.Outcome != "completed" {
			t.Fatalf("turn %d completion = (%t, %q), want (true, completed)", index+1, turn.Completed, turn.Outcome)
		}
		if _, found := firstTurnEvent(turn, TimingStageFirstVisibleText); !found {
			t.Fatalf("turn %d missing first visible text event", index+1)
		}
	}
}

func TestRequestTimingTurnMarkOnceIsConcurrentSafe(t *testing.T) {
	tracker := NewRequestTimingTracker("request-2", "POST /v1/responses", time.Now())
	turn := tracker.BeginTurn("http_stream", "gpt-5.6-luna")

	var waitGroup sync.WaitGroup
	for workerIndex := 0; workerIndex < 32; workerIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			turn.MarkOnce(TimingStageFirstUpstreamPayload, RequestTimingEventDetails{Outcome: "payload"})
		}()
	}
	waitGroup.Wait()
	turn.Complete("completed")

	snapshot := tracker.Snapshot()
	if len(snapshot.Turns) != 1 {
		t.Fatalf("turn count = %d, want 1", len(snapshot.Turns))
	}
	matchCount := 0
	for _, event := range snapshot.Turns[0].Events {
		if event.Stage == TimingStageFirstUpstreamPayload {
			matchCount++
		}
	}
	if matchCount != 1 {
		t.Fatalf("first upstream payload events = %d, want 1", matchCount)
	}
}

func TestPropagateRequestTimingPreservesTrackerAndTurn(t *testing.T) {
	tracker := NewRequestTimingTracker("request-3", "POST /v1/messages", time.Now())
	turn := tracker.BeginTurn("http_stream", "claude-sonnet")
	source := WithRequestTimingTurn(context.Background(), turn)

	target := PropagateRequestTiming(context.Background(), source)
	if RequestTimingTrackerFromContext(target) != tracker {
		t.Fatal("tracker was not propagated")
	}
	if RequestTimingTurnFromContext(target) != turn {
		t.Fatal("turn was not propagated")
	}
}

func TestWriteRequestTimingSectionContainsOnlyTimingMetadata(t *testing.T) {
	tracker := NewRequestTimingTracker("request-secret", "POST /v1/responses", time.Now())
	turn := tracker.BeginTurn("http_stream", "gpt-5.6-luna")
	turn.Mark(TimingStageAuthSelection, RequestTimingEventDetails{
		Duration: 12 * time.Millisecond,
		Outcome:  "selected",
		Provider: "codex",
		Model:    "gpt-5.6-luna",
		Attempt:  1,
	})
	turn.Complete("completed")

	var output bytes.Buffer
	if err := writeRequestTimingSection(&output, tracker.Snapshot()); err != nil {
		t.Fatalf("writeRequestTimingSection error: %v", err)
	}
	text := output.String()
	for _, expected := range []string{"=== LATENCY TIMELINE ===", "stage=auth.selection", "provider=codex", "model=gpt-5.6-luna"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("timing output missing %q: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"Authorization", "Bearer ", "api-key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("timing output unexpectedly contains %q: %s", forbidden, text)
		}
	}
}
