package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravityPromptBlockBody = `{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":20578,"totalTokenCount":20578},"modelVersion":"gemini-3.5-flash-low","responseId":"blocked-1"}}`

func newAntigravityPromptBlockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + antigravityPromptBlockBody + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(antigravityPromptBlockBody))
	}))
}

func newAntigravityPromptBlockAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "auth-prompt-block",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func requireAntigravityPromptBlockErr(t *testing.T, err error) {
	t.Helper()
	var se statusErr
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want statusErr", err)
	}
	if se.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", se.StatusCode(), http.StatusBadGateway)
	}
	if !strings.Contains(se.Error(), "upstream blocked the prompt: PROHIBITED_CONTENT") {
		t.Fatalf("error message = %q, want prompt block reason", se.Error())
	}
}

func TestAntigravityExecutePromptBlockReturnsError(t *testing.T) {
	server := newAntigravityPromptBlockServer()
	defer server.Close()

	for _, model := range []string{"gemini-3.5-flash-low", "claude-sonnet-4-6"} {
		t.Run(model, func(t *testing.T) {
			exec := NewAntigravityExecutor(&config.Config{})
			resp, err := exec.Execute(context.Background(), newAntigravityPromptBlockAuth(server.URL), cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
			requireAntigravityPromptBlockErr(t, err)
			if len(resp.Payload) != 0 {
				t.Fatalf("payload = %s, want empty", resp.Payload)
			}
		})
	}
}

func TestAntigravityExecuteStreamPromptBlockReturnsError(t *testing.T) {
	server := newAntigravityPromptBlockServer()
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	result, err := exec.ExecuteStream(context.Background(), newAntigravityPromptBlockAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash-low",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity})
	if err != nil {
		requireAntigravityPromptBlockErr(t, err)
		return
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			requireAntigravityPromptBlockErr(t, chunk.Err)
			return
		}
		if len(chunk.Payload) > 0 {
			t.Fatalf("unexpected payload before error: %s", chunk.Payload)
		}
	}
	t.Fatal("stream ended without a prompt block error")
}
