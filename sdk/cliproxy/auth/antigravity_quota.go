package auth

import (
	"fmt"
	"math"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// antigravityQuotaBaseURLDaily and antigravityQuotaBaseURLProd mirror the
// unexported antigravityBaseURLDaily/antigravityBaseURLProd host constants in
// internal/runtime/executor/antigravity_executor.go. This package cannot
// import that one (executors implement this package's ProviderExecutor
// interface, not the other way around), so the hosts are duplicated here for
// the quota probe only. Daily is tried first: it is the only host verified
// (via a manual capture, see antigravity.md) to serve retrieveUserQuotaSummary;
// prod's support for this endpoint is unconfirmed.
const (
	antigravityQuotaBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	antigravityQuotaBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityQuotaPath         = "/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaGroupsKey    = "antigravity_quota_groups"
)

// AntigravityQuotaWindow is one usage bucket (e.g. the "weekly" or "5h" window)
// within an AntigravityQuotaGroup, normalized from a retrieveUserQuotaSummary
// response bucket.
type AntigravityQuotaWindow struct {
	// Utilization is the used percentage (0-100), derived as
	// (1 - remainingFraction) * 100. Nil when the upstream bucket did not
	// report remainingFraction.
	Utilization *int
	// Reset is the bucket's resetTime, normalized to RFC3339 UTC when
	// parsable, otherwise the raw upstream string. Empty when absent.
	Reset string
}

// AntigravityQuotaGroup is one quota group (e.g. "gemini" or "3p") from a
// retrieveUserQuotaSummary response, holding its long ("weekly") and short
// ("5h") windows.
type AntigravityQuotaGroup struct {
	// GroupID is derived from the group's bucket IDs (e.g. "gemini-weekly" ->
	// "gemini"). It is used both as a stable machine identifier and, appended
	// to the auth name, as the usage entry name suffix (see
	// internal/api/handlers/management/auth_usage.go).
	GroupID     string
	DisplayName string
	Long        *AntigravityQuotaWindow
	Short       *AntigravityQuotaWindow
}

// SetAntigravityQuotaGroups stores a freshly probed antigravity quota snapshot
// on auth.RateLimits, applying the same out-of-order protection as
// applyRateLimitHeaders (see rate_limit_headers.go). It reports whether the
// snapshot was applied.
func SetAntigravityQuotaGroups(auth *Auth, groups []AntigravityQuotaGroup, observedAt time.Time) bool {
	if auth == nil || len(groups) == 0 {
		return false
	}
	snapshot := map[string]any{antigravityQuotaGroupsKey: groups}
	return applyRateLimitSnapshot(auth, snapshot, observedAt)
}

// AntigravityQuotaGroups returns the most recently probed antigravity quota
// groups for auth, if any.
func AntigravityQuotaGroups(auth *Auth) []AntigravityQuotaGroup {
	if auth == nil || auth.RateLimits == nil {
		return nil
	}
	groups, _ := auth.RateLimits[antigravityQuotaGroupsKey].([]AntigravityQuotaGroup)
	return groups
}

// antigravityQuotaProjectID returns the Google Cloud project ID discovered for
// this antigravity auth (written to Metadata["project_id"] during login/token
// refresh, see internal/runtime/executor/antigravity_executor_auth.go). Empty
// when not yet known, in which case the probe cannot run.
func antigravityQuotaProjectID(a *Auth) string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	pid, _ := a.Metadata["project_id"].(string)
	return strings.TrimSpace(pid)
}

// antigravityQuotaBaseURLCandidates returns the base URL(s) to try for the
// retrieveUserQuotaSummary probe, in order. When a custom base_url is
// configured for this auth (Attributes or Metadata, matching the resolution
// order in internal/runtime/executor/antigravity_executor_request.go) only
// that URL is tried; otherwise both known hosts are tried, daily first (see
// the package doc comment above).
func antigravityQuotaBaseURLCandidates(a *Auth) []string {
	if a != nil {
		if base := strings.TrimSpace(a.Attributes["base_url"]); base != "" {
			return []string{strings.TrimSuffix(base, "/")}
		}
		if a.Metadata != nil {
			if base, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(base) != "" {
				return []string{strings.TrimSuffix(strings.TrimSpace(base), "/")}
			}
		}
	}
	return []string{antigravityQuotaBaseURLDaily, antigravityQuotaBaseURLProd}
}

// parseAntigravityQuotaSummary parses a retrieveUserQuotaSummary response body
// into normalized quota groups. Each bucket's bucketId is expected to end in
// "-weekly" (long window) or "-5h" (short window); other suffixes are skipped
// with a debug log since callers only render Long/Short. Groups with neither
// window recognized are dropped. It reports ok=false when no group yielded a
// usable window.
func parseAntigravityQuotaSummary(body []byte) ([]AntigravityQuotaGroup, bool) {
	groupsResult := gjson.GetBytes(body, "groups")
	if !groupsResult.IsArray() {
		return nil, false
	}

	result := make([]AntigravityQuotaGroup, 0)
	groupsResult.ForEach(func(_ /* key */, group gjson.Result) bool {
		buckets := group.Get("buckets")
		if !buckets.IsArray() {
			return true
		}
		g := AntigravityQuotaGroup{DisplayName: strings.TrimSpace(group.Get("displayName").String())}
		buckets.ForEach(func(_ /* key */, bucket gjson.Result) bool {
			window := antigravityQuotaWindowFromBucket(bucket)
			bucketID := strings.TrimSpace(bucket.Get("bucketId").String())
			switch {
			case window == nil:
			case strings.HasSuffix(bucketID, "-weekly"):
				g.Long = window
				if g.GroupID == "" {
					g.GroupID = strings.TrimSuffix(bucketID, "-weekly")
				}
			case strings.HasSuffix(bucketID, "-5h"):
				g.Short = window
				if g.GroupID == "" {
					g.GroupID = strings.TrimSuffix(bucketID, "-5h")
				}
			default:
				log.Debugf("antigravity quota: unrecognized bucketId %q, skipping", bucketID)
			}
			return true
		})
		if g.Long == nil && g.Short == nil {
			return true
		}
		if g.GroupID == "" {
			g.GroupID = fmt.Sprintf("group_%d", len(result))
		}
		if g.DisplayName == "" {
			g.DisplayName = g.GroupID
		}
		result = append(result, g)
		return true
	})

	if len(result) == 0 {
		return nil, false
	}
	return result, true
}

// antigravityQuotaWindowFromBucket normalizes one retrieveUserQuotaSummary
// bucket. It returns nil when the bucket has neither a usable
// remainingFraction nor a resetTime, i.e. nothing worth keeping.
func antigravityQuotaWindowFromBucket(bucket gjson.Result) *AntigravityQuotaWindow {
	window := &AntigravityQuotaWindow{}
	// remainingFraction: 0 is a legitimate, meaningful value (quota fully
	// used), so presence must be checked with Exists() rather than treating a
	// zero value as absent.
	if rf := bucket.Get("remainingFraction"); rf.Exists() {
		pct := int(math.Round((1 - rf.Float()) * 100))
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		window.Utilization = &pct
	}
	if resetRaw := strings.TrimSpace(bucket.Get("resetTime").String()); resetRaw != "" {
		if ts, err := time.Parse(time.RFC3339, resetRaw); err == nil {
			window.Reset = ts.UTC().Format(time.RFC3339)
		} else {
			window.Reset = resetRaw
		}
	}
	if window.Utilization == nil && window.Reset == "" {
		return nil
	}
	return window
}
