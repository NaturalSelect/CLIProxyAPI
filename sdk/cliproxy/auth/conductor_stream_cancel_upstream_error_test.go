package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWrapStreamResultRecordsReadyUpstreamErrorWhenCanceled(t *testing.T) {
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
			cancel()
			model := "canceled-upstream-error-model-" + uuid.NewString()
			auth := &Auth{ID: "canceled-upstream-error-auth-" + uuid.NewString(), Provider: "canceledupstreamerr", Status: StatusActive}
			hook := &captureResultHook{}
			manager := NewManager(nil, nil, hook)
			manager.SetRetryConfig(0, 0, 0)
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			// NOTE: The error is queued before the wrapper starts, so the wrapper reads it while the context is already canceled.
			remaining := make(chan cliproxyexecutor.StreamChunk, 1)
			remaining <- cliproxyexecutor.StreamChunk{Err: tc.err}
			close(remaining)

			result := manager.wrapStreamResult(ctx, auth, auth.Provider, model, model, nil, time.Time{}, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, 1)
			for range result.Chunks {
				// drain
			}

			hook.mu.Lock()
			defer hook.mu.Unlock()
			failed := 0
			for _, recorded := range hook.results {
				if recorded.AuthID == auth.ID && !recorded.Success {
					failed++
				}
			}
			if tc.wantFailed && failed == 0 {
				t.Fatalf("ready upstream error was not recorded as a failure; results=%d", len(hook.results))
			}
			if !tc.wantFailed && failed != 0 {
				t.Fatalf("cancellation was recorded as %d failure(s), want none", failed)
			}
		})
	}
}
