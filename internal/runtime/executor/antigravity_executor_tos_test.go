package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const antigravityTOSBody = `{
	"error": {
		"code": 403,
		"message": "This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.",
		"status": "PERMISSION_DENIED",
		"details": [
			{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "TOS_VIOLATION",
				"domain": "cloudcode-pa.googleapis.com",
				"metadata": {
					"uiMessage": "true",
					"appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9",
					"appeal_url_link_text": "Submit Appeal"
				}
			}
		]
	}
}`

func resetAntigravityTOSPause(t *testing.T) {
	t.Helper()
	antigravityTOSPauseUntil.Store(0)
	t.Cleanup(func() { antigravityTOSPauseUntil.Store(0) })
}

func TestAntigravityTOSViolationParsing(t *testing.T) {
	t.Run("matches tos signature and extracts appeal url", func(t *testing.T) {
		violation := parseAntigravityTOSViolation([]byte(antigravityTOSBody))
		if violation == nil {
			t.Fatal("parseAntigravityTOSViolation() = nil, want TOS violation match")
		}
		if violation.AppealURL != "https://forms.gle/hGzM9MEUv2azZsrb9" {
			t.Fatalf("AppealURL = %q", violation.AppealURL)
		}
		if violation.AppealURLText != "Submit Appeal" {
			t.Fatalf("AppealURLText = %q", violation.AppealURLText)
		}
	})

	t.Run("ignores 403 permission denied with another reason", func(t *testing.T) {
		body := `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SOME_OTHER_REASON"}]}}`
		if violation := parseAntigravityTOSViolation([]byte(body)); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation() = %+v, want nil", violation)
		}
	})

	t.Run("ignores 429 resource exhausted", func(t *testing.T) {
		body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION"}]}}`
		if violation := parseAntigravityTOSViolation([]byte(body)); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation() = %+v, want nil for 429", violation)
		}
	})

	t.Run("ignores empty and invalid bodies", func(t *testing.T) {
		if violation := parseAntigravityTOSViolation(nil); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation(nil) = %+v, want nil", violation)
		}
		if violation := parseAntigravityTOSViolation([]byte("not json")); violation != nil {
			t.Fatalf("parseAntigravityTOSViolation(invalid) = %+v, want nil", violation)
		}
	})
}

func TestAntigravityTOSStatusErrIsTerminalAndCredentialScoped(t *testing.T) {
	err := newAntigravityStatusErr(http.StatusForbidden, []byte(antigravityTOSBody))
	if !err.IsTerminalTOSViolation() {
		t.Fatal("IsTerminalTOSViolation() = false, want true")
	}
	if !err.IsCredentialScoped() {
		t.Fatal("IsCredentialScoped() = false, want true")
	}
	meta := err.TOSAppealMetadata()
	if meta["tos_appeal_url"] != "https://forms.gle/hGzM9MEUv2azZsrb9" || meta["tos_appeal_url_text"] != "Submit Appeal" {
		t.Fatalf("TOSAppealMetadata() = %v, want appeal url and text", meta)
	}

	plain := newAntigravityStatusErr(http.StatusForbidden, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED"}}`))
	if plain.IsTerminalTOSViolation() || plain.IsCredentialScoped() || plain.TOSAppealMetadata() != nil {
		t.Fatal("plain 403 must not be marked as a TOS violation")
	}
}

func TestAntigravityTOSPoolPause(t *testing.T) {
	resetAntigravityTOSPause(t)

	cfg := &config.Config{}
	if antigravityInTOSPause(cfg) {
		t.Fatal("antigravityInTOSPause() = true before any TOS violation")
	}
	markAntigravityTOSPause(cfg)
	if !antigravityInTOSPause(cfg) {
		t.Fatal("antigravityInTOSPause() = false after TOS violation, want pool-wide pause")
	}
}

func TestAntigravityTOSPauseWindowConfigurable(t *testing.T) {
	resetAntigravityTOSPause(t)

	disabledCfg := &config.Config{}
	disabledCfg.Antigravity.TOSPauseWindow = "off"
	markAntigravityTOSPause(disabledCfg)
	if antigravityInTOSPause(disabledCfg) {
		t.Fatal("antigravityInTOSPause() = true with the pause disabled")
	}

	window, enabled := antigravityTOSPauseWindow(&config.Config{})
	if !enabled || window != 6*time.Hour {
		t.Fatalf("default window = %s enabled=%v, want 6h enabled", window, enabled)
	}
	customCfg := &config.Config{}
	customCfg.Antigravity.TOSPauseWindow = "2h"
	if window, enabled = antigravityTOSPauseWindow(customCfg); !enabled || window != 2*time.Hour {
		t.Fatalf("custom window = %s enabled=%v, want 2h enabled", window, enabled)
	}
	invalidCfg := &config.Config{}
	invalidCfg.Antigravity.TOSPauseWindow = "soon"
	if window, enabled = antigravityTOSPauseWindow(invalidCfg); !enabled || window != 6*time.Hour {
		t.Fatalf("invalid window = %s enabled=%v, want fallback to 6h", window, enabled)
	}
}

func TestAntigravityTOSPauseExpires(t *testing.T) {
	resetAntigravityTOSPause(t)

	antigravityTOSPauseUntil.Store(time.Now().Add(-time.Minute).UnixNano())
	if antigravityInTOSPause(&config.Config{}) {
		t.Fatal("antigravityInTOSPause() = true for an expired deadline")
	}
}

func TestAntigravityRateLimitNeverArmsTOSPause(t *testing.T) {
	resetAntigravityTOSPause(t)

	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`)
	exec := NewAntigravityExecutor(&config.Config{})
	exec.applyAntigravityTOSState(exec.cfg, &cliproxyauth.Auth{ID: "rate-limited"}, body)
	if antigravityInTOSPause(exec.cfg) {
		t.Fatal("a 429 body armed the TOS pause, want no pause")
	}
}

func TestAntigravityExecuteTOSResponseDeactivatesViaErrorAndPausesPool(t *testing.T) {
	resetAntigravityTOSPause(t)

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(antigravityTOSBody))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "tos-exec",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity}

	_, err := exec.Execute(context.Background(), auth, req, opts)
	sErr, ok := err.(statusErr)
	if !ok || !sErr.IsTerminalTOSViolation() {
		t.Fatalf("Execute() error = %v (%T), want terminal TOS statusErr", err, err)
	}
	if !antigravityInTOSPause(exec.cfg) {
		t.Fatal("pool pause not armed after a TOS response")
	}

	hitsAfterFirst := hits
	_, err = exec.Execute(context.Background(), auth, req, opts)
	paused, ok := err.(statusErr)
	if !ok || paused.StatusCode() != http.StatusServiceUnavailable || !strings.Contains(paused.Error(), "paused") {
		t.Fatalf("second Execute() error = %v, want 503 pool-paused error", err)
	}
	if hits != hitsAfterFirst {
		t.Fatalf("upstream hit %d times after the pause, want no further requests", hits-hitsAfterFirst)
	}

	_, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if pausedStream, ok := errStream.(statusErr); !ok || pausedStream.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteStream() error = %v, want 503 pool-paused error", errStream)
	}
	_, errCount := exec.CountTokens(context.Background(), auth, req, opts)
	if pausedCount, ok := errCount.(statusErr); !ok || pausedCount.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("CountTokens() error = %v, want 503 pool-paused error", errCount)
	}
}
