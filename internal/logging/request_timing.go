package logging

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	maxRequestTimingTurns         = 256
	maxRequestTimingEventsPerTurn = 96
)

const (
	TimingStageStreamExecutionEntered  = "stream.execution_entered"
	TimingStageModelRouter             = "routing.model_router"
	TimingStageBeforeAuthInterceptor   = "interceptor.before_auth"
	TimingStageAuthManagerEntered      = "auth.manager_entered"
	TimingStageAuthSelection           = "auth.selection"
	TimingStageAuthSelectionSubstage   = "auth.selection.substage"
	TimingStageAuthPreparation         = "auth.preparation"
	TimingStageExecutorExecuteStream   = "executor.execute_stream"
	TimingStageUpstreamTTFTStarted     = "upstream.ttft_started"
	TimingStageUpstreamConnection      = "upstream.websocket_connection"
	TimingStageUpstreamRequestSent     = "upstream.request_sent"
	TimingStageFirstUpstreamEvent      = "upstream.first_event"
	TimingStageFirstUpstreamPayload    = "upstream.first_payload"
	TimingStageFirstSemanticContent    = "upstream.first_semantic_content"
	TimingStageFirstHandlerDeliverable = "handler.first_deliverable"
	TimingStageDownstreamHeaders       = "downstream.headers"
	TimingStageFirstDownstreamEvent    = "downstream.first_event"
	TimingStageFirstVisibleText        = "downstream.first_visible_text"
	TimingStageFirstDownstreamWrite    = "downstream.first_write"
	TimingStageDownstreamCompleted     = "downstream.completed"
	TimingStageTurnCompleted           = "turn.completed"
)

type requestTimingTrackerContextKey struct{}
type requestTimingTurnContextKey struct{}

// RequestTimingEventDetails contains low-cardinality metadata for one timing event.
type RequestTimingEventDetails struct {
	Duration time.Duration
	Outcome  string
	Provider string
	Model    string
	Attempt  int
	Items    int
}

// RequestTimingEventSnapshot is an immutable timing event included in request logs.
type RequestTimingEventSnapshot struct {
	Sequence int
	Stage    string
	Offset   time.Duration
	Duration time.Duration
	Outcome  string
	Provider string
	Model    string
	Attempt  int
	Items    int
}

// RequestTimingTurnSnapshot contains one HTTP stream or WebSocket logical turn.
type RequestTimingTurnSnapshot struct {
	ID            int
	Kind          string
	Model         string
	Provider      string
	StartedOffset time.Duration
	Completed     bool
	Outcome       string
	Events        []RequestTimingEventSnapshot
	DroppedEvents int
}

// RequestTimingSnapshot contains all stream turns observed for one HTTP request.
type RequestTimingSnapshot struct {
	RequestID    string
	Endpoint     string
	StartedAt    time.Time
	Turns        []RequestTimingTurnSnapshot
	DroppedTurns int
}

type requestTimingTurnState struct {
	id            int
	kind          string
	model         string
	provider      string
	startedAt     time.Time
	completed     bool
	outcome       string
	events        []RequestTimingEventSnapshot
	seenStages    map[string]struct{}
	droppedEvents int
}

// RequestTimingTracker collects stream latency events without performing I/O on hot paths.
type RequestTimingTracker struct {
	mutex        sync.Mutex
	requestID    string
	endpoint     string
	startedAt    time.Time
	nextSequence int
	activeTurnID int
	turns        []*requestTimingTurnState
	droppedTurns int
}

// RequestTimingTurn identifies one logical streaming operation.
type RequestTimingTurn struct {
	tracker *RequestTimingTracker
	id      int
}

// NewRequestTimingTracker creates a tracker rooted at the HTTP request ingress time.
func NewRequestTimingTracker(requestID string, endpoint string, startedAt time.Time) *RequestTimingTracker {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &RequestTimingTracker{
		requestID: strings.TrimSpace(requestID),
		endpoint:  strings.TrimSpace(endpoint),
		startedAt: startedAt,
	}
}

// WithRequestTimingTracker attaches a tracker to a context.
func WithRequestTimingTracker(ctx context.Context, tracker *RequestTimingTracker) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, requestTimingTrackerContextKey{}, tracker)
}

// RequestTimingTrackerFromContext returns the request tracker, when enabled.
func RequestTimingTrackerFromContext(ctx context.Context) *RequestTimingTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(requestTimingTrackerContextKey{}).(*RequestTimingTracker)
	return tracker
}

// WithRequestTimingTurn attaches the current logical turn and its tracker.
func WithRequestTimingTurn(ctx context.Context, turn *RequestTimingTurn) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if turn == nil {
		return ctx
	}
	ctx = WithRequestTimingTracker(ctx, turn.tracker)
	return context.WithValue(ctx, requestTimingTurnContextKey{}, turn)
}

// RequestTimingTurnFromContext returns the current logical stream turn.
func RequestTimingTurnFromContext(ctx context.Context) *RequestTimingTurn {
	if ctx == nil {
		return nil
	}
	turn, _ := ctx.Value(requestTimingTurnContextKey{}).(*RequestTimingTurn)
	return turn
}

// PropagateRequestTiming copies tracker values from source when target lacks them.
func PropagateRequestTiming(target context.Context, source context.Context) context.Context {
	if target == nil {
		target = context.Background()
	}
	if source == nil {
		return target
	}
	if RequestTimingTrackerFromContext(target) == nil {
		target = WithRequestTimingTracker(target, RequestTimingTrackerFromContext(source))
	}
	if RequestTimingTurnFromContext(target) == nil {
		target = WithRequestTimingTurn(target, RequestTimingTurnFromContext(source))
	}
	return target
}

// EnsureRequestTimingTurn returns the existing turn or creates one for the stream.
func EnsureRequestTimingTurn(ctx context.Context, kind string, model string) (context.Context, *RequestTimingTurn) {
	if existing := RequestTimingTurnFromContext(ctx); existing != nil {
		return ctx, existing
	}
	tracker := RequestTimingTrackerFromContext(ctx)
	if tracker == nil {
		return ctx, nil
	}
	turn := tracker.BeginTurn(kind, model)
	return WithRequestTimingTurn(ctx, turn), turn
}

// BeginTurn creates and activates a logical stream turn.
func (tracker *RequestTimingTracker) BeginTurn(kind string, model string) *RequestTimingTurn {
	if tracker == nil {
		return nil
	}

	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if len(tracker.turns) >= maxRequestTimingTurns {
		tracker.droppedTurns++
		return nil
	}

	turnID := len(tracker.turns) + 1
	state := &requestTimingTurnState{
		id:         turnID,
		kind:       strings.TrimSpace(kind),
		model:      strings.TrimSpace(model),
		startedAt:  time.Now(),
		seenStages: make(map[string]struct{}, 16),
	}
	tracker.turns = append(tracker.turns, state)
	tracker.activeTurnID = turnID
	return &RequestTimingTurn{tracker: tracker, id: turnID}
}

// ActiveTurn returns the turn currently responsible for downstream writes.
func (tracker *RequestTimingTracker) ActiveTurn() *RequestTimingTurn {
	if tracker == nil {
		return nil
	}
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if tracker.activeTurnID <= 0 {
		return nil
	}
	return &RequestTimingTurn{tracker: tracker, id: tracker.activeTurnID}
}

// Mark appends one event. Repeated stages are retained for retries and attempts.
func (turn *RequestTimingTurn) Mark(stage string, details RequestTimingEventDetails) {
	turn.mark(stage, details, false)
}

// MarkOnce appends only the first occurrence of a stage for this turn.
func (turn *RequestTimingTurn) MarkOnce(stage string, details RequestTimingEventDetails) {
	turn.mark(stage, details, true)
}

func (turn *RequestTimingTurn) mark(stage string, details RequestTimingEventDetails, once bool) {
	if turn == nil || turn.tracker == nil || strings.TrimSpace(stage) == "" {
		return
	}

	tracker := turn.tracker
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	state := tracker.turnStateLocked(turn.id)
	if state == nil || state.completed {
		return
	}
	stage = strings.TrimSpace(stage)
	if once {
		if _, exists := state.seenStages[stage]; exists {
			return
		}
	}
	if len(state.events) >= maxRequestTimingEventsPerTurn {
		state.droppedEvents++
		return
	}

	state.seenStages[stage] = struct{}{}
	tracker.nextSequence++
	now := time.Now()
	state.events = append(state.events, RequestTimingEventSnapshot{
		Sequence: tracker.nextSequence,
		Stage:    stage,
		Offset:   nonNegativeDuration(now.Sub(tracker.startedAt)),
		Duration: nonNegativeDuration(details.Duration),
		Outcome:  strings.TrimSpace(details.Outcome),
		Provider: strings.TrimSpace(details.Provider),
		Model:    strings.TrimSpace(details.Model),
		Attempt:  details.Attempt,
		Items:    details.Items,
	})
}

// Measure records a completed duration-bearing stage.
func (turn *RequestTimingTurn) Measure(stage string, startedAt time.Time, details RequestTimingEventDetails) {
	if !startedAt.IsZero() {
		details.Duration = time.Since(startedAt)
	}
	turn.Mark(stage, details)
}

// SetRoute updates low-cardinality route metadata for the turn summary.
func (turn *RequestTimingTurn) SetRoute(provider string, model string) {
	if turn == nil || turn.tracker == nil {
		return
	}
	turn.tracker.mutex.Lock()
	defer turn.tracker.mutex.Unlock()
	state := turn.tracker.turnStateLocked(turn.id)
	if state == nil || state.completed {
		return
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		state.provider = provider
	}
	if model = strings.TrimSpace(model); model != "" {
		state.model = model
	}
}

// Complete closes a turn exactly once and emits one structured summary line.
func (turn *RequestTimingTurn) Complete(outcome string) {
	if turn == nil || turn.tracker == nil {
		return
	}

	tracker := turn.tracker
	tracker.mutex.Lock()
	state := tracker.turnStateLocked(turn.id)
	if state == nil || state.completed {
		tracker.mutex.Unlock()
		return
	}
	state.completed = true
	state.outcome = strings.TrimSpace(outcome)
	tracker.nextSequence++
	state.events = append(state.events, RequestTimingEventSnapshot{
		Sequence: tracker.nextSequence,
		Stage:    TimingStageTurnCompleted,
		Offset:   nonNegativeDuration(time.Since(state.startedAt)),
		Outcome:  state.outcome,
	})
	if tracker.activeTurnID == turn.id {
		tracker.activeTurnID = 0
	}
	snapshot := tracker.turnSnapshotLocked(state)
	requestID := tracker.requestID
	endpoint := tracker.endpoint
	tracker.mutex.Unlock()

	logRequestTimingSummary(requestID, endpoint, snapshot)
}

// Snapshot returns an immutable copy for request-log persistence.
func (tracker *RequestTimingTracker) Snapshot() RequestTimingSnapshot {
	if tracker == nil {
		return RequestTimingSnapshot{}
	}
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	snapshot := RequestTimingSnapshot{
		RequestID:    tracker.requestID,
		Endpoint:     tracker.endpoint,
		StartedAt:    tracker.startedAt,
		DroppedTurns: tracker.droppedTurns,
		Turns:        make([]RequestTimingTurnSnapshot, 0, len(tracker.turns)),
	}
	for _, state := range tracker.turns {
		snapshot.Turns = append(snapshot.Turns, tracker.turnSnapshotLocked(state))
	}
	return snapshot
}

func (tracker *RequestTimingTracker) turnStateLocked(turnID int) *requestTimingTurnState {
	if tracker == nil || turnID <= 0 || turnID > len(tracker.turns) {
		return nil
	}
	return tracker.turns[turnID-1]
}

func (tracker *RequestTimingTracker) turnSnapshotLocked(state *requestTimingTurnState) RequestTimingTurnSnapshot {
	return RequestTimingTurnSnapshot{
		ID:            state.id,
		Kind:          state.kind,
		Model:         state.model,
		Provider:      state.provider,
		StartedOffset: nonNegativeDuration(state.startedAt.Sub(tracker.startedAt)),
		Completed:     state.completed,
		Outcome:       state.outcome,
		Events:        append([]RequestTimingEventSnapshot(nil), state.events...),
		DroppedEvents: state.droppedEvents,
	}
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func logRequestTimingSummary(requestID string, endpoint string, turn RequestTimingTurnSnapshot) {
	fields := log.Fields{
		"request_id":     requestID,
		"endpoint":       endpoint,
		"turn":           turn.ID,
		"kind":           turn.Kind,
		"model":          turn.Model,
		"provider":       turn.Provider,
		"outcome":        turn.Outcome,
		"total_ms":       durationMilliseconds(lastTurnOffset(turn)),
		"dropped_events": turn.DroppedEvents,
	}
	for _, stage := range []string{
		TimingStageModelRouter,
		TimingStageBeforeAuthInterceptor,
		TimingStageAuthSelection,
		TimingStageAuthPreparation,
		TimingStageExecutorExecuteStream,
		TimingStageUpstreamConnection,
		TimingStageFirstUpstreamEvent,
		TimingStageFirstUpstreamPayload,
		TimingStageFirstSemanticContent,
		TimingStageFirstHandlerDeliverable,
		TimingStageFirstDownstreamWrite,
		TimingStageFirstVisibleText,
	} {
		if event, found := firstTurnEvent(turn, stage); found {
			fieldName := strings.ReplaceAll(stage, ".", "_")
			fields[fieldName+"_ms"] = durationMilliseconds(event.Offset)
			if event.Duration > 0 {
				fields[fieldName+"_duration_ms"] = durationMilliseconds(event.Duration)
			}
		}
	}
	log.WithFields(fields).Info("stream latency")
}

func writeRequestTimingSection(writer io.Writer, snapshot RequestTimingSnapshot) error {
	if writer == nil || len(snapshot.Turns) == 0 {
		return nil
	}
	if _, errWrite := io.WriteString(writer, "=== LATENCY TIMELINE ===\n"); errWrite != nil {
		return errWrite
	}
	for _, turn := range snapshot.Turns {
		if _, errWrite := fmt.Fprintf(
			writer,
			"Turn %d: kind=%s model=%s provider=%s outcome=%s started_ms=%.3f dropped_events=%d\n",
			turn.ID,
			turn.Kind,
			turn.Model,
			turn.Provider,
			turn.Outcome,
			durationMilliseconds(turn.StartedOffset),
			turn.DroppedEvents,
		); errWrite != nil {
			return errWrite
		}
		for _, event := range turn.Events {
			if _, errWrite := fmt.Fprintf(
				writer,
				"  +%.3fms stage=%s duration_ms=%.3f attempt=%d items=%d outcome=%s provider=%s model=%s\n",
				durationMilliseconds(event.Offset),
				event.Stage,
				durationMilliseconds(event.Duration),
				event.Attempt,
				event.Items,
				event.Outcome,
				event.Provider,
				event.Model,
			); errWrite != nil {
				return errWrite
			}
		}
	}
	return writeSectionSpacing(writer, 1)
}

func firstTurnEvent(turn RequestTimingTurnSnapshot, stage string) (RequestTimingEventSnapshot, bool) {
	for _, event := range turn.Events {
		if event.Stage == stage {
			return event, true
		}
	}
	return RequestTimingEventSnapshot{}, false
}

func lastTurnOffset(turn RequestTimingTurnSnapshot) time.Duration {
	if len(turn.Events) == 0 {
		return 0
	}
	return turn.Events[len(turn.Events)-1].Offset
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
