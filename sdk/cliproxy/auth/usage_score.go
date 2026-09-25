package auth

import (
	"math"
	"time"
)

// defaultUsageAwareScore is assigned to credentials with no known usage/rate-limit
// snapshot (any provider other than Claude, Codex, or Antigravity, or one whose
// snapshot hasn't been probed yet), so they are neither favored nor starved
// relative to credentials with real headroom data.
const defaultUsageAwareScore = 0.5

// usageWindow is a normalized utilization/reset pair extracted from a credential's
// provider-specific auth.RateLimits snapshot (see rate_limit_headers.go and
// antigravity_quota.go for how that snapshot is populated).
type usageWindow struct {
	utilization int
	resetAt     string
}

// usageAwareScore scores auth in [0, 1] from its most recent rate-limit snapshot:
// higher means more headroom and is safer to route to. When a credential exposes
// several windows (e.g. Claude's 7d and 7d_oi limits), the most constrained window
// wins, since that is the one actually gating the credential's availability.
// Credentials with no snapshot get a neutral defaultUsageAwareScore.
func usageAwareScore(auth *Auth, now time.Time) float64 {
	windows := usageWindowsForAuth(auth)
	if len(windows) == 0 {
		return defaultUsageAwareScore
	}
	worst := -1.0
	for _, window := range windows {
		score := usageWindowScore(window.utilization, window.resetAt, now)
		if worst < 0 || score < worst {
			worst = score
		}
	}
	return worst
}

// usageWindowsForAuth extracts every known utilization window for auth's provider
// from its cached RateLimits snapshot. Only Claude, Codex, and Antigravity
// currently populate that snapshot; every other provider returns nil.
func usageWindowsForAuth(auth *Auth) []usageWindow {
	if auth == nil || len(auth.RateLimits) == 0 {
		return nil
	}
	switch auth.Provider {
	case "claude":
		return claudeUsageWindows(auth.RateLimits)
	case "codex":
		return codexUsageWindows(auth.RateLimits)
	case "antigravity":
		return antigravityUsageWindows(AntigravityQuotaGroups(auth))
	default:
		return nil
	}
}

// claudeUsageWindows reads the 7d and (when present) 7d_oi windows Anthropic
// reports per rate_limit_headers.go's parseClaudeRateLimitHeaders. The 5h window
// is intentionally excluded from scoring: it isn't on the same scale as the 7d
// windows, so folding it into the same "worst window" comparison would penalize
// (or favor) a credential based on a limit that doesn't reflect its real
// remaining headroom.
func claudeUsageWindows(rateLimits map[string]any) []usageWindow {
	var windows []usageWindow
	windows = appendUsageWindow(windows, rateLimits, "7d_utilization", "7d_reset")
	windows = appendUsageWindow(windows, rateLimits, "7d_oi_utilization", "7d_oi_reset")
	return windows
}

// codexUsageWindows reads the primary and secondary windows Codex reports per
// rate_limit_headers.go's parseCodexRateLimitHeaders.
func codexUsageWindows(rateLimits map[string]any) []usageWindow {
	var windows []usageWindow
	windows = appendUsageWindow(windows, rateLimits, "primary_used_percent", "primary_reset_at")
	windows = appendUsageWindow(windows, rateLimits, "secondary_used_percent", "secondary_reset_at")
	return windows
}

// appendUsageWindow reads an integer utilization value at utilizationKey from
// rateLimits and, if present, appends a usageWindow paired with the reset
// timestamp string at resetKey (empty when absent).
func appendUsageWindow(windows []usageWindow, rateLimits map[string]any, utilizationKey, resetKey string) []usageWindow {
	utilization, ok := rateLimitInt(rateLimits[utilizationKey])
	if !ok {
		return windows
	}
	resetAt, _ := rateLimits[resetKey].(string)
	return append(windows, usageWindow{utilization: utilization, resetAt: resetAt})
}

// rateLimitInt normalizes a value stored in auth.RateLimits into an int. Header
// values are normally stored as int (see setRateLimitInt/setRateLimitUtilizationPercent
// in rate_limit_headers.go), but occasionally fall back to a raw string when an
// upstream header didn't parse as numeric; that case is treated as "no data" rather
// than guessed at.
func rateLimitInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// antigravityUsageWindows flattens each quota group's long (weekly) and short (5h)
// windows into the common usageWindow shape.
func antigravityUsageWindows(groups []AntigravityQuotaGroup) []usageWindow {
	var windows []usageWindow
	for _, group := range groups {
		windows = appendAntigravityWindow(windows, group.Long)
		windows = appendAntigravityWindow(windows, group.Short)
	}
	return windows
}

func appendAntigravityWindow(windows []usageWindow, window *AntigravityQuotaWindow) []usageWindow {
	if window == nil || window.Utilization == nil {
		return windows
	}
	return append(windows, usageWindow{utilization: *window.Utilization, resetAt: window.Reset})
}

// fullScoreUtilizationCeiling and fullScoreResetWindow define the automatic
// full-score guarantee applied in usageWindowScore: a window with comfortably
// low utilization that is also about to reset needs no further comparison
// against other credentials, since whatever quota is unused now is about to
// be replenished anyway.
const (
	fullScoreUtilizationCeiling = 90
	fullScoreResetWindow        = 24 * time.Hour
)

// usageWindowScore scores a single utilization/reset window in [0, 1] as the
// largest of three independent signals, so that whichever one shows the
// window is safe to route to wins rather than being diluted by the others:
//   - headroom: the remaining fraction of quota.
//   - resetUrgency (see minutesUntilReset): grows as the reset approaches,
//     which matters most when headroom is poor, since an exhausted window
//     that is about to refresh is as good as a fresh one.
//   - guaranteedFullScore: the product of two [0, 1] factors, one for
//     utilization and one for reset timing. A product only reaches 1 when
//     BOTH factors do, so this expresses "utilization is comfortably under
//     fullScoreUtilizationCeiling AND the reset is within fullScoreResetWindow"
//     without branching on either condition individually; missing either one
//     pulls the product back down and the max falls through to headroom or
//     resetUrgency instead.
func usageWindowScore(utilization int, resetAt string, now time.Time) float64 {
	headroom := math.Min(1, math.Max(0, (100-float64(utilization))/100.0))
	remaining := minutesUntilReset(resetAt, now)

	resetUrgency := math.Min(1, 1.0/(1.0+remaining/60.0))

	utilizationFactor := math.Min(1, math.Max(0, fullScoreUtilizationCeiling-float64(utilization)))
	resetSoonFactor := math.Min(1, fullScoreResetWindow.Minutes()/remaining)
	guaranteedFullScore := utilizationFactor * resetSoonFactor

	return math.Max(headroom, math.Max(resetUrgency, guaranteedFullScore))
}

// minutesUntilReset parses resetAt and returns the minutes remaining until it
// elapses, or +Inf when resetAt is empty, unparsable, or already past. +Inf
// then flows through usageWindowScore's division-based factors and naturally
// resolves them to 0, so a window with no meaningful upcoming reset needs no
// separate case in the scoring formula itself.
func minutesUntilReset(resetAt string, now time.Time) float64 {
	reset, err := time.Parse(time.RFC3339, resetAt)
	if err != nil || !reset.After(now) {
		return math.Inf(1)
	}
	return reset.Sub(now).Minutes()
}
