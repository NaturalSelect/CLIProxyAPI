package helps

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestApplyCodexIdentityConfuseDocDisabled(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	payload := []byte(`{"prompt_cache_key":"cache-1"}`)
	for name, cfg := range map[string]*config.Config{
		"flag off":    {Routing: config.RoutingConfig{Strategy: "fill-first"}},
		"round robin": {Routing: config.RoutingConfig{Strategy: "round-robin"}, Codex: config.CodexConfig{IdentityConfuse: true}},
	} {
		t.Run(name, func(t *testing.T) {
			doc := ojson.NewDoc(payload)
			if state := ApplyCodexIdentityConfuseDoc(cfg, auth, payload, doc); state != nil {
				t.Fatalf("state = %+v, want nil", state)
			}
			if got := doc.Get("prompt_cache_key").String(); got != "cache-1" {
				t.Fatalf("prompt_cache_key = %q, want cache-1", got)
			}
		})
	}
}

func TestApplyCodexIdentityConfuseDocRemapsAndExposes(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	userPayload := []byte(`{"prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-1"}}`)
	doc := ojson.NewDoc([]byte(`{"model":"gpt-5-codex","prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-1","x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-1\",\"turn_id\":\"turn-1\",\"window_id\":\"cache-1:0\"}","x-codex-window-id":"cache-1:0"}}`))

	state := ApplyCodexIdentityConfuseDoc(cfg, auth, userPayload, doc)
	wantKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-1")
	wantTurnID := codexIdentityConfuseUUID("auth-1", "turn", "turn-1")
	if state.PromptCacheKey() != wantKey {
		t.Fatalf("PromptCacheKey() = %q, want %q", state.PromptCacheKey(), wantKey)
	}
	for path, want := range map[string]string{
		"prompt_cache_key":                        wantKey,
		"client_metadata.x-codex-installation-id": codexIdentityConfuseUUID("auth-1", "installation", "install-1"),
		"client_metadata.x-codex-window-id":       wantKey + ":0",
	} {
		if got := doc.Get(path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	turnMetadata := doc.Get("client_metadata.x-codex-turn-metadata").String()
	for path, want := range map[string]string{"prompt_cache_key": wantKey, "turn_id": wantTurnID, "window_id": wantKey + ":0"} {
		if got := ojson.Get(turnMetadata, path).String(); got != want {
			t.Errorf("turn metadata %s = %q, want %q", path, got, want)
		}
	}
	if again := state.ConfuseTurnMetadata(turnMetadata); again != turnMetadata {
		t.Errorf("ConfuseTurnMetadata is not idempotent: %s -> %s", turnMetadata, again)
	}

	stripped := ojson.NewDoc([]byte(`{"type":"response.steer","input":[]}`))
	ApplyCodexIdentityConfuseDoc(cfg, auth, userPayload, stripped)
	if got := stripped.String(); got != `{"type":"response.steer","input":[]}` {
		t.Errorf("remapping added fields the body did not carry: %s", got)
	}

	upstream := []byte(`data: {"type":"response.completed","response":{"prompt_cache_key":"` + wantKey + `","turn_id":"` + wantTurnID + `"}}`)
	client := state.ExposeResponse(upstream)
	if bytes.Contains(client, []byte(wantKey)) || bytes.Contains(client, []byte(wantTurnID)) {
		t.Fatalf("client payload still carries remapped identifiers: %s", client)
	}
	if !bytes.Contains(client, []byte(`"prompt_cache_key":"cache-1"`)) || !bytes.Contains(client, []byte(`"turn_id":"turn-1"`)) {
		t.Fatalf("client payload did not restore original identifiers: %s", client)
	}
}

func TestCodexIdentityConfuseStateNilIsNoOp(t *testing.T) {
	var state *CodexIdentityConfuseState
	payload := []byte(`{"prompt_cache_key":"cache-1"}`)
	if got := state.ExposeResponse(payload); !bytes.Equal(got, payload) {
		t.Fatalf("ExposeResponse() = %s, want unchanged", got)
	}
	if state.PromptCacheKey() != "" || state.WindowID() != "" {
		t.Fatal("nil state returned identifiers")
	}
	if got := state.ConfuseTurnMetadata(`{"turn_id":"turn-1"}`); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("ConfuseTurnMetadata() = %s, want unchanged", got)
	}
}
