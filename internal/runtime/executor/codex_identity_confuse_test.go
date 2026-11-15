package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type codexIdentityCapture struct {
	mu      sync.Mutex
	body    []byte
	headers http.Header
}

func newCodexIdentityEchoServer(t *testing.T, captured *codexIdentityCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.mu.Lock()
		captured.body, captured.headers = body, r.Header.Clone()
		captured.mu.Unlock()
		key := ojson.GetBytes(body, "prompt_cache_key").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","prompt_cache_key":"` + key + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
}

func codexIdentityConfuseTestConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
}

func runCodexIdentityRequest(t *testing.T, executor *CodexExecutor, auth *cliproxyauth.Auth, stream bool) []byte {
	t.Helper()
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hi","prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-1"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
	if !stream {
		resp, err := executor.Execute(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		return resp.Payload
	}
	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var out bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		out.Write(chunk.Payload)
	}
	return out.Bytes()
}

func TestCodexExecutorIdentityConfuseRemapsUpstreamAndRestoresClient(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			var captured codexIdentityCapture
			server := newCodexIdentityEchoServer(t, &captured)
			defer server.Close()
			auth := codexOAuthTestAuth(server.URL)
			auth.ID = "auth-1"
			state := helps.ApplyCodexIdentityConfuseDoc(codexIdentityConfuseTestConfig(), auth, []byte(`{"prompt_cache_key":"cache-1"}`), ojson.NewDoc([]byte(`{}`)))
			wantKey := state.PromptCacheKey()

			clientOut := runCodexIdentityRequest(t, NewCodexExecutor(codexIdentityConfuseTestConfig()), auth, stream)

			captured.mu.Lock()
			defer captured.mu.Unlock()
			if got := ojson.GetBytes(captured.body, "prompt_cache_key").String(); got != wantKey {
				t.Fatalf("upstream prompt_cache_key = %q, want %q", got, wantKey)
			}
			if got := ojson.GetBytes(captured.body, "client_metadata.x-codex-installation-id").String(); got == "" || got == "install-1" {
				t.Fatalf("upstream installation id = %q, want a remapped value", got)
			}
			if got := captured.headers.Get("Session-Id"); got != wantKey {
				t.Fatalf("upstream Session-Id = %q, want %q", got, wantKey)
			}
			if got := captured.headers.Get("X-Codex-Window-Id"); got != wantKey+":0" {
				t.Fatalf("upstream X-Codex-Window-Id = %q, want %q", got, wantKey+":0")
			}
			if bytes.Contains(clientOut, []byte(wantKey)) || !bytes.Contains(clientOut, []byte("cache-1")) {
				t.Fatalf("client output did not restore the original prompt_cache_key: %s", clientOut)
			}
		})
	}
}

func TestCodexExecutorIdentityConfuseKeepsPayloadRulesFinal(t *testing.T) {
	var captured codexIdentityCapture
	server := newCodexIdentityEchoServer(t, &captured)
	defer server.Close()
	cfg := codexIdentityConfuseTestConfig()
	cfg.Payload = config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-*", Protocol: "codex"}},
		Params: map[string]any{"prompt_cache_key": "rule-key"},
	}}}
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "auth-1"

	runCodexIdentityRequest(t, NewCodexExecutor(cfg), auth, false)

	captured.mu.Lock()
	defer captured.mu.Unlock()
	if got := ojson.GetBytes(captured.body, "prompt_cache_key").String(); got != "rule-key" {
		t.Fatalf("upstream prompt_cache_key = %q, want payload rule value rule-key", got)
	}
}

func TestApplyCodexWebsocketHeadersIdentityConfuseRemapsSessionHeaders(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-ws-1", Provider: "codex"}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"prompt_cache_key":"cache-ws-1"}`),
	}
	body, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))
	doc := ojson.NewDoc(body)
	state := helps.ApplyCodexIdentityConfuseDoc(cfg, auth, req.Payload, doc)
	ctx := contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1","window_id":"cache-ws-1:0"}`,
	})
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg, false)
	applyCodexIdentityConfuseHeaders(headers, state)

	wantKey := state.PromptCacheKey()
	if got := doc.Get("prompt_cache_key").String(); got != wantKey {
		t.Fatalf("prompt_cache_key = %q, want %q", got, wantKey)
	}
	if got := headers["session_id"]; len(got) != 1 || got[0] != wantKey {
		t.Fatalf("session_id = %#v, want [%q]", got, wantKey)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %q, want empty", got)
	}
	for _, name := range []string{"X-Client-Request-Id", "Thread-Id"} {
		if got := headers.Get(name); got != wantKey {
			t.Fatalf("%s = %q, want %q", name, got, wantKey)
		}
	}
	if got := headers.Get("X-Codex-Window-Id"); got != wantKey+":0" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", got, wantKey+":0")
	}
	metadata := headers.Get("X-Codex-Turn-Metadata")
	if got := ojson.Get(metadata, "prompt_cache_key").String(); got != wantKey {
		t.Fatalf("X-Codex-Turn-Metadata.prompt_cache_key = %q, want %q", got, wantKey)
	}
	if got := ojson.Get(metadata, "turn_id").String(); got == "" || got == "turn-ws-1" {
		t.Fatalf("X-Codex-Turn-Metadata.turn_id = %q, want a remapped value", got)
	}
}

func TestCodexWebsocketsIdentityConfuseRemapsUpstreamAndRestoresClient(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			var captured codexIdentityCapture
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers := r.Header.Clone()
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Errorf("read upstream websocket message: %v", errRead)
					return
				}
				captured.mu.Lock()
				captured.body, captured.headers = bytes.Clone(payload), headers
				captured.mu.Unlock()
				key := ojson.GetBytes(payload, "prompt_cache_key").String()
				completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","object":"response","status":"completed","prompt_cache_key":"` + key + `","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					t.Errorf("write completed websocket message: %v", errWrite)
				}
			}))
			defer server.Close()

			auth := &cliproxyauth.Auth{
				ID:         "auth-ws-1",
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL},
			}
			state := helps.ApplyCodexIdentityConfuseDoc(codexIdentityConfuseTestConfig(), auth, []byte(`{"prompt_cache_key":"cache-ws-1"}`), ojson.NewDoc([]byte(`{}`)))
			wantKey := state.PromptCacheKey()
			exec := NewCodexWebsocketsExecutor(codexIdentityConfuseTestConfig())
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.5",
				Payload: []byte(`{"model":"gpt-5.5","input":"hi","prompt_cache_key":"cache-ws-1"}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}

			var clientOut bytes.Buffer
			if !stream {
				resp, errExecute := exec.Execute(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				clientOut.Write(resp.Payload)
			} else {
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for done := false; !done; {
					select {
					case chunk, ok := <-result.Chunks:
						if !ok {
							done = true
							continue
						}
						if chunk.Err != nil {
							t.Fatalf("stream chunk error = %v", chunk.Err)
						}
						clientOut.Write(chunk.Payload)
					case <-time.After(5 * time.Second):
						t.Fatal("timed out waiting for websocket stream completion")
					}
				}
			}

			captured.mu.Lock()
			defer captured.mu.Unlock()
			if got := ojson.GetBytes(captured.body, "prompt_cache_key").String(); got != wantKey {
				t.Fatalf("upstream prompt_cache_key = %q, want %q", got, wantKey)
			}
			if got := captured.headers.Get("Session_id"); got != wantKey {
				t.Fatalf("upstream session_id = %q, want %q", got, wantKey)
			}
			if bytes.Contains(clientOut.Bytes(), []byte(wantKey)) || !bytes.Contains(clientOut.Bytes(), []byte("cache-ws-1")) {
				t.Fatalf("client output did not restore the original prompt_cache_key: %s", clientOut.Bytes())
			}
		})
	}
}
