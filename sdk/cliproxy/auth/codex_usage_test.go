package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const codexWhamUsageFixture = `{
  "user_id": "user-1",
  "account_id": "acct-1",
  "plan_type": "plus",
  "rate_limit": {
    "allowed": true,
    "limit_reached": false,
    "primary_window": {
      "used_percent": 0,
      "limit_window_seconds": 18000,
      "reset_after_seconds": 18000,
      "reset_at": 1791238831
    },
    "secondary_window": {
      "used_percent": 5,
      "limit_window_seconds": 604800,
      "reset_after_seconds": 398579,
      "reset_at": 1791619409
    }
  }
}`

func TestParseCodexUsageResponseMapsBothWindows(t *testing.T) {
	snapshot, ok := parseCodexUsageResponse([]byte(codexWhamUsageFixture))
	if !ok {
		t.Fatal("parseCodexUsageResponse() ok = false, want true")
	}

	want := map[string]any{
		"primary_used_percent":          0,
		"primary_window_minutes":        300,
		"primary_reset_after_seconds":   18000,
		"primary_reset_at":              time.Unix(1791238831, 0).UTC().Format(time.RFC3339),
		"secondary_used_percent":        5,
		"secondary_window_minutes":      10080,
		"secondary_reset_after_seconds": 398579,
		"secondary_reset_at":            time.Unix(1791619409, 0).UTC().Format(time.RFC3339),
	}
	for key, wantValue := range want {
		if got := snapshot[key]; got != wantValue {
			t.Errorf("snapshot[%q] = %v, want %v", key, got, wantValue)
		}
	}
}

func TestParseCodexUsageResponseKeepsZeroUsedPercent(t *testing.T) {
	snapshot, ok := parseCodexUsageResponse([]byte(codexWhamUsageFixture))
	if !ok {
		t.Fatal("parseCodexUsageResponse() ok = false, want true")
	}
	if _, present := snapshot["primary_used_percent"]; !present {
		t.Fatal("primary_used_percent = 0 was dropped, want it kept as a real measurement")
	}
}

func TestParseCodexUsageResponseWithoutWindows(t *testing.T) {
	for name, body := range map[string]string{
		"empty object":       `{}`,
		"null rate limit":    `{"rate_limit":null}`,
		"not json":           `<html>blocked</html>`,
		"windows are scalar": `{"rate_limit":{"primary_window":1,"secondary_window":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if snapshot, ok := parseCodexUsageResponse([]byte(body)); ok {
				t.Fatalf("parseCodexUsageResponse() = (%v, true), want ok = false", snapshot)
			}
		})
	}
}

func TestCodexUsageSnapshotFeedsScoring(t *testing.T) {
	snapshot, _ := parseCodexUsageResponse([]byte(codexWhamUsageFixture))
	auth := &Auth{ID: "a", Provider: "codex", RateLimits: snapshot}

	windows := usageWindowsForAuth(auth)
	if len(windows) != 2 {
		t.Fatalf("usageWindowsForAuth() returned %d windows, want 2", len(windows))
	}
	if windows[0].utilization != 0 || windows[1].utilization != 5 {
		t.Fatalf("utilizations = (%d, %d), want (0, 5)", windows[0].utilization, windows[1].utilization)
	}
}

func TestCodexUsageAccountID(t *testing.T) {
	testCases := []struct {
		name string
		auth *Auth
		want string
	}{
		{
			name: "oauth credential uses account_id metadata",
			auth: &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "t", "account_id": " acct-1 "}},
			want: "acct-1",
		},
		{
			name: "oauth credential without account_id",
			auth: &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "t"}},
			want: "",
		},
		{
			name: "api key credential is never probed",
			auth: &Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-1"}, Metadata: map[string]any{"account_id": "acct-1"}},
			want: "",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := codexUsageAccountID(testCase.auth); got != testCase.want {
				t.Fatalf("codexUsageAccountID() = %q, want %q", got, testCase.want)
			}
		})
	}
}

type codexUsageProbeExecutor struct {
	mu         sync.Mutex
	requests   []*http.Request
	executeHit int
	status     int
	body       string
}

func (e *codexUsageProbeExecutor) Identifier() string { return "codex" }
func (e *codexUsageProbeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeHit++
	return cliproxyexecutor.Response{}, nil
}
func (e *codexUsageProbeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *codexUsageProbeExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (e *codexUsageProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *codexUsageProbeExecutor) HttpRequest(_ context.Context, _ *Auth, req *http.Request) (*http.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, req)
	return &http.Response{
		StatusCode: e.status,
		Body:       io.NopCloser(strings.NewReader(e.body)),
	}, nil
}

func newCodexProbeManager(exec *codexUsageProbeExecutor, auth *Auth) *Manager {
	manager := NewManager(nil, nil, nil)
	manager.executors["codex"] = exec
	manager.auths[auth.ID] = auth
	return manager
}

func TestProbeUsageFetchesCodexWhamUsage(t *testing.T) {
	exec := &codexUsageProbeExecutor{status: http.StatusOK, body: codexWhamUsageFixture}
	manager := newCodexProbeManager(exec, &Auth{
		ID:       "codex-wham",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "t", "account_id": "acct-1"},
	})

	manager.probeUsage(context.Background(), "codex-wham")

	if exec.executeHit != 0 {
		t.Fatalf("Execute calls = %d, want 0: codex must not spend a model call", exec.executeHit)
	}
	if len(exec.requests) != 1 {
		t.Fatalf("HttpRequest calls = %d, want 1", len(exec.requests))
	}
	req := exec.requests[0]
	if req.Method != http.MethodGet || req.URL.String() != codexUsageURL {
		t.Fatalf("request = %s %s, want GET %s", req.Method, req.URL, codexUsageURL)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-1" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-1", got)
	}
	if got := req.Header.Get("User-Agent"); got != codexUsageUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, codexUsageUserAgent)
	}

	current, ok := manager.GetByID("codex-wham")
	if !ok || current == nil {
		t.Fatal("auth missing after probe")
	}
	if got := current.RateLimits["secondary_used_percent"]; got != 5 {
		t.Fatalf("secondary_used_percent = %v, want 5", got)
	}
	if _, ok := parseRateLimitObservedAt(current.RateLimits); !ok {
		t.Fatal("observed_at was not stamped on the snapshot")
	}
}

func TestProbeUsageSkipsCodexWithoutAccountID(t *testing.T) {
	exec := &codexUsageProbeExecutor{status: http.StatusOK, body: codexWhamUsageFixture}
	manager := newCodexProbeManager(exec, &Auth{
		ID:         "codex-api-key",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-1"},
	})

	manager.probeUsage(context.Background(), "codex-api-key")

	if len(exec.requests) != 0 || exec.executeHit != 0 {
		t.Fatalf("requests = %d, Execute calls = %d, want none for an api key credential", len(exec.requests), exec.executeHit)
	}
}

func TestProbeUsageIgnoresFailedCodexUsageResponse(t *testing.T) {
	exec := &codexUsageProbeExecutor{status: http.StatusForbidden, body: codexWhamUsageFixture}
	manager := newCodexProbeManager(exec, &Auth{
		ID:       "codex-forbidden",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "t", "account_id": "acct-1"},
	})

	manager.probeUsage(context.Background(), "codex-forbidden")

	current, _ := manager.GetByID("codex-forbidden")
	if current == nil || len(current.RateLimits) != 0 {
		t.Fatalf("RateLimits = %v, want none after a non-2xx response", current)
	}
}
