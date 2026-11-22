package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// canceledUpstreamErrorStreamExecutor emits one payload and holds the error chunk back until release
// is closed, so a test can cancel the request after the payload reached the consumer.
type canceledUpstreamErrorStreamExecutor struct {
	release chan struct{}
	err     error
}

func (*canceledUpstreamErrorStreamExecutor) Identifier() string { return "canceledupstreamerr" }

func (*canceledUpstreamErrorStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *canceledUpstreamErrorStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	go func() {
		<-e.release
		chunks <- cliproxyexecutor.StreamChunk{Err: e.err}
		close(chunks)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*canceledUpstreamErrorStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*canceledUpstreamErrorStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*canceledUpstreamErrorStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestWrapStreamResultRecordsUpstreamErrorDeliveredAfterCancel(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantFailed bool
	}{
		{
			name:       "upstream error is recorded",
			err:        &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failed"},
			wantFailed: true,
		},
		{
			name:       "plain cancellation is dropped",
			err:        context.Canceled,
			wantFailed: false,
		},
		{
			name:       "wrapped cancellation is dropped",
			err:        fmt.Errorf("read upstream: %w", context.Canceled),
			wantFailed: false,
		},
		{
			name:       "deadline exceeded is dropped",
			err:        context.DeadlineExceeded,
			wantFailed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			release := make(chan struct{})
			model := "canceled-upstream-error-model-" + uuid.NewString()
			auth := &Auth{ID: "canceled-upstream-error-auth-" + uuid.NewString(), Provider: "canceledupstreamerr", Status: StatusActive}
			hook := &captureResultHook{}
			manager := NewManager(nil, nil, hook)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(&canceledUpstreamErrorStreamExecutor{release: release, err: tc.err})
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			streamResult, errStream := manager.ExecuteStream(ctx, []string{"canceledupstreamerr"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			if _, ok := <-streamResult.Chunks; !ok {
				t.Fatal("stream closed before the first payload")
			}
			cancel()
			close(release)
			for range streamResult.Chunks {
				// drain
			}

			hook.mu.Lock()
			defer hook.mu.Unlock()
			failed := 0
			for _, result := range hook.results {
				if result.AuthID == auth.ID && !result.Success {
					failed++
				}
			}
			if tc.wantFailed && failed == 0 {
				t.Fatalf("upstream error delivered after cancel was not recorded as a failure; results=%d", len(hook.results))
			}
			if !tc.wantFailed && failed != 0 {
				t.Fatalf("cancellation was recorded as %d failure(s), want none", failed)
			}
		})
	}
}
