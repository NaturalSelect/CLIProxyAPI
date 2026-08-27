package auth

import (
	"net/http"
	"testing"
)

func TestParseClaudeRateLimitHeaders_FableWindow(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.40")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1788100000")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.12")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1788170000")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "0.09")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "1788177600")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "allowed")

	snapshot, ok := parseClaudeRateLimitHeaders(headers, nil)
	if !ok {
		t.Fatalf("parseClaudeRateLimitHeaders ok = false, want true")
	}
	if got := snapshot["7d_oi_utilization"]; got != 9 {
		t.Fatalf("7d_oi_utilization = %v, want 9", got)
	}
	if got := snapshot["7d_oi_reset"]; got != "2026-08-31T12:00:00Z" {
		t.Fatalf("7d_oi_reset = %v, want 2026-08-31T12:00:00Z", got)
	}
	if got := snapshot["7d_oi_status"]; got != "allowed" {
		t.Fatalf("7d_oi_status = %v, want allowed", got)
	}
	if got := snapshot["7d_utilization"]; got != 12 {
		t.Fatalf("7d_utilization = %v, want 12", got)
	}
	if got := snapshot["5h_utilization"]; got != 40 {
		t.Fatalf("5h_utilization = %v, want 40", got)
	}
}

func TestParseClaudeRateLimitHeaders_OnlyFableWindow(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.13")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "1788177600")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")

	snapshot, ok := parseClaudeRateLimitHeaders(headers, nil)
	if !ok {
		t.Fatalf("parseClaudeRateLimitHeaders ok = false, want true")
	}
	if got := snapshot["7d_oi_utilization"]; got != 113 {
		t.Fatalf("7d_oi_utilization = %v, want 113", got)
	}
	if got := snapshot["7d_oi_status"]; got != "rejected" {
		t.Fatalf("7d_oi_status = %v, want rejected", got)
	}
	if _, exists := snapshot["7d_utilization"]; exists {
		t.Fatalf("unexpected 7d_utilization in snapshot: %+v", snapshot)
	}
}

func TestParseClaudeRateLimitHeaders_MissingFableWindowCarriesForwardPrevious(t *testing.T) {
	t.Parallel()

	existing := map[string]any{
		"7d_oi_utilization": 77,
		"7d_oi_reset":       "2026-09-01T00:00:00Z",
		"7d_oi_status":      "allowed_warning",
	}

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.40")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1788100000")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.12")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1788170000")

	snapshot, ok := parseClaudeRateLimitHeaders(headers, existing)
	if !ok {
		t.Fatalf("parseClaudeRateLimitHeaders ok = false, want true")
	}
	if got := snapshot["7d_oi_utilization"]; got != 77 {
		t.Fatalf("7d_oi_utilization = %v, want carried-forward 77", got)
	}
	if got := snapshot["7d_oi_reset"]; got != "2026-09-01T00:00:00Z" {
		t.Fatalf("7d_oi_reset = %v, want carried-forward value", got)
	}
	if got := snapshot["7d_oi_status"]; got != "allowed_warning" {
		t.Fatalf("7d_oi_status = %v, want carried-forward value", got)
	}
	if got := snapshot["7d_utilization"]; got != 12 {
		t.Fatalf("7d_utilization = %v, want fresh value 12", got)
	}
}

func TestParseClaudeRateLimitHeaders_FreshFableWindowOverridesPrevious(t *testing.T) {
	t.Parallel()

	existing := map[string]any{
		"7d_oi_utilization": 77,
		"7d_oi_reset":       "2026-09-01T00:00:00Z",
		"7d_oi_status":      "allowed_warning",
	}

	headers := http.Header{}
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "0.05")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "1788177600")
	headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "allowed")

	snapshot, ok := parseClaudeRateLimitHeaders(headers, existing)
	if !ok {
		t.Fatalf("parseClaudeRateLimitHeaders ok = false, want true")
	}
	if got := snapshot["7d_oi_utilization"]; got != 5 {
		t.Fatalf("7d_oi_utilization = %v, want fresh value 5, not carried-forward 77", got)
	}
	if got := snapshot["7d_oi_status"]; got != "allowed" {
		t.Fatalf("7d_oi_status = %v, want fresh value allowed", got)
	}
}

func TestParseClaudeRateLimitHeaders_NoHeaders(t *testing.T) {
	t.Parallel()

	if _, ok := parseClaudeRateLimitHeaders(http.Header{}, nil); ok {
		t.Fatalf("parseClaudeRateLimitHeaders ok = true, want false for empty headers")
	}
}
