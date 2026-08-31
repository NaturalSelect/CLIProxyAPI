package auth

import (
	"testing"
	"time"
)

func floatsClose(a, b, tolerance float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

func TestUsageWindowScore_FullHeadroom(t *testing.T) {
	t.Parallel()

	now := time.Now()
	got := usageWindowScore(0, "", now)
	if !floatsClose(got, usageHeadroomWeight, 1e-9) {
		t.Fatalf("usageWindowScore(0, \"\", now) = %v, want %v", got, usageHeadroomWeight)
	}
}

func TestUsageWindowScore_ExhaustedNoReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := usageWindowScore(100, "", now); got != 0 {
		t.Fatalf("usageWindowScore(100, \"\", now) = %v, want 0", got)
	}
}

func TestUsageWindowScore_ExhaustedWithImminentReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	resetAt := now.Add(2 * time.Minute).UTC().Format(time.RFC3339)
	got := usageWindowScore(100, resetAt, now)
	if got <= 0.20 || got >= usageResetWeight {
		t.Fatalf("usageWindowScore(100, +2m, now) = %v, want in (0.20, %v)", got, usageResetWeight)
	}
}

func TestUsageWindowScore_DistantResetBarelyHelps(t *testing.T) {
	t.Parallel()

	now := time.Now()
	resetAt := now.Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	got := usageWindowScore(100, resetAt, now)
	if got <= 0 || got >= 0.01 {
		t.Fatalf("usageWindowScore(100, +30d, now) = %v, want in (0, 0.01)", got)
	}
}

func TestUsageWindowScore_ClampsOverage(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := usageWindowScore(113, "", now); got != 0 {
		t.Fatalf("usageWindowScore(113, \"\", now) = %v, want 0 (clamped)", got)
	}
}

func TestUsageWindowScore_PastResetNoBonus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	resetAt := now.Add(-time.Minute).UTC().Format(time.RFC3339)
	if got := usageWindowScore(100, resetAt, now); got != 0 {
		t.Fatalf("usageWindowScore(100, past reset, now) = %v, want 0", got)
	}
}

func TestUsageAwareScore_NoData_ReturnsFallback(t *testing.T) {
	t.Parallel()

	auth := &Auth{ID: "a", Provider: "gemini"}
	if got := usageAwareScore(auth, time.Now()); got != defaultUsageAwareScore {
		t.Fatalf("usageAwareScore(no data) = %v, want %v", got, defaultUsageAwareScore)
	}
}

func TestUsageAwareScore_UnparsedRateLimitValue_ReturnsFallback(t *testing.T) {
	t.Parallel()

	auth := &Auth{ID: "a", Provider: "claude", RateLimits: map[string]any{"5h_utilization": "n/a"}}
	if got := usageAwareScore(auth, time.Now()); got != defaultUsageAwareScore {
		t.Fatalf("usageAwareScore(unparsed) = %v, want %v", got, defaultUsageAwareScore)
	}
}

func TestUsageAwareScore_ClaudeMultiWindowTakesWorst(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{
		ID:       "a",
		Provider: "claude",
		RateLimits: map[string]any{
			"5h_utilization": 20,
			"7d_utilization": 80,
		},
	}
	got := usageAwareScore(auth, now)
	want := usageWindowScore(80, "", now)
	if got != want {
		t.Fatalf("usageAwareScore(mixed windows) = %v, want worst window score %v", got, want)
	}
}

func TestUsageAwareScore_CodexWindows(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{
		ID:       "a",
		Provider: "codex",
		RateLimits: map[string]any{
			"primary_used_percent":   10,
			"secondary_used_percent": 5,
		},
	}
	got := usageAwareScore(auth, now)
	// primary (10%) has less headroom than secondary (5%), so it is the
	// binding/worst window and should determine the score.
	want := usageWindowScore(10, "", now)
	if got != want {
		t.Fatalf("usageAwareScore(codex) = %v, want %v", got, want)
	}
}

func TestUsageAwareScore_AntigravityWindows(t *testing.T) {
	t.Parallel()

	now := time.Now()
	weeklyUtil := 30
	shortUtil := 90
	auth := &Auth{ID: "a", Provider: "antigravity"}
	groups := []AntigravityQuotaGroup{
		{
			GroupID: "gemini",
			Long:    &AntigravityQuotaWindow{Utilization: &weeklyUtil},
			Short:   &AntigravityQuotaWindow{Utilization: &shortUtil},
		},
	}
	if !SetAntigravityQuotaGroups(auth, groups, now) {
		t.Fatalf("SetAntigravityQuotaGroups() = false, want true")
	}
	got := usageAwareScore(auth, now)
	want := usageWindowScore(shortUtil, "", now)
	if got != want {
		t.Fatalf("usageAwareScore(antigravity) = %v, want worst window score %v", got, want)
	}
}

func TestUsageAwareScore_AntigravityWindowsWithNilUtilization(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{ID: "a", Provider: "antigravity"}
	groups := []AntigravityQuotaGroup{
		{GroupID: "gemini", Long: &AntigravityQuotaWindow{Reset: now.Add(time.Hour).UTC().Format(time.RFC3339)}},
	}
	if !SetAntigravityQuotaGroups(auth, groups, now) {
		t.Fatalf("SetAntigravityQuotaGroups() = false, want true")
	}
	// The only window has no Utilization value, so it contributes nothing and the
	// credential must fall back to the neutral score rather than treating a
	// reset-only window as 0% or 100% utilized.
	if got := usageAwareScore(auth, now); got != defaultUsageAwareScore {
		t.Fatalf("usageAwareScore(nil utilization) = %v, want %v", got, defaultUsageAwareScore)
	}
}
