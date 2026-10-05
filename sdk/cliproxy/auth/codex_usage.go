package auth

import (
	"math"
	"time"

	"github.com/tidwall/gjson"
)

const (
	codexUsageURL       = "https://chatgpt.com/backend-api/wham/usage"
	codexUsageUserAgent = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
)

// codexUsageAccountID returns the ChatGPT account ID that wham/usage requires.
// It is empty for API-key credentials, which have no ChatGPT account and must
// never be sent to chatgpt.com.
func codexUsageAccountID(a *Auth) string {
	if a.AuthKind() == AuthKindAPIKey {
		return ""
	}
	return authMetadataString(a, "account_id")
}

// parseCodexUsageResponse converts a wham/usage response body into a RateLimits
// snapshot. It reports ok=false when the body carries neither window.
//
// NOTE: Keys deliberately match parseCodexRateLimitHeaders so scoring and the
// management API read one shape regardless of where the snapshot came from.
func parseCodexUsageResponse(body []byte) (map[string]any, bool) {
	snapshot := make(map[string]any)
	setCodexUsageWindow(snapshot, "primary", gjson.GetBytes(body, "rate_limit.primary_window"))
	setCodexUsageWindow(snapshot, "secondary", gjson.GetBytes(body, "rate_limit.secondary_window"))
	if len(snapshot) == 0 {
		return nil, false
	}
	return snapshot, true
}

func setCodexUsageWindow(snapshot map[string]any, prefix string, window gjson.Result) {
	if !window.IsObject() {
		return
	}
	if used := window.Get("used_percent"); used.Exists() {
		snapshot[prefix+"_used_percent"] = int(math.Round(used.Float()))
	}
	if seconds := window.Get("limit_window_seconds"); seconds.Exists() {
		snapshot[prefix+"_window_minutes"] = int(seconds.Int() / 60)
	}
	if seconds := window.Get("reset_after_seconds"); seconds.Exists() {
		snapshot[prefix+"_reset_after_seconds"] = int(seconds.Int())
	}
	if resetAt := window.Get("reset_at"); resetAt.Exists() {
		snapshot[prefix+"_reset_at"] = time.Unix(resetAt.Int(), 0).UTC().Format(time.RFC3339)
	}
}
