package auth

import (
	"testing"
	"time"
)

// antigravityQuotaSummaryFixture mirrors a real retrieveUserQuotaSummary
// response (see antigravity.md), with the 3p-5h bucket filled in to match the
// shape of gemini-5h since the captured sample was truncated before it.
const antigravityQuotaSummaryFixture = `{
  "groups": [
    {
      "buckets": [
        {
          "bucketId": "gemini-weekly",
          "displayName": "Weekly Limit Remaining",
          "window": "weekly",
          "resetTime": "2026-08-17T05:29:20Z",
          "description": "You have used some of your weekly limit.",
          "remainingFraction": 0.9737029
        },
        {
          "bucketId": "gemini-5h",
          "displayName": "Five Hour Limit Remaining",
          "window": "5h",
          "resetTime": "2026-08-10T20:22:39Z",
          "description": "You have used some of your 5-hour limit.",
          "remainingFraction": 0.9405329
        }
      ],
      "displayName": "Gemini Models",
      "description": "Models within this group: Gemini Flash, Gemini Pro"
    },
    {
      "buckets": [
        {
          "bucketId": "3p-weekly",
          "displayName": "Weekly Limit Remaining",
          "window": "weekly",
          "resetTime": "2026-08-15T13:17:37Z",
          "description": "You have hit your weekly limit.",
          "remainingFraction": 0
        },
        {
          "bucketId": "3p-5h",
          "displayName": "Five Hour Limit Remaining",
          "window": "5h",
          "resetTime": "2026-08-10T21:00:00Z",
          "description": "You have used some of your 5-hour limit.",
          "remainingFraction": 0.5
        }
      ],
      "displayName": "Third-Party Models",
      "description": "Models within this group: Claude, GPT"
    }
  ]
}`

func TestParseAntigravityQuotaSummary_RealCapture(t *testing.T) {
	groups, ok := parseAntigravityQuotaSummary([]byte(antigravityQuotaSummaryFixture))
	if !ok {
		t.Fatalf("parseAntigravityQuotaSummary: ok = false, want true")
	}
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}

	gemini := groups[0]
	if gemini.GroupID != "gemini" {
		t.Fatalf("gemini.GroupID = %q, want %q", gemini.GroupID, "gemini")
	}
	if gemini.DisplayName != "Gemini Models" {
		t.Fatalf("gemini.DisplayName = %q, want %q", gemini.DisplayName, "Gemini Models")
	}
	if gemini.Long == nil || gemini.Long.Utilization == nil || *gemini.Long.Utilization != 3 {
		t.Fatalf("gemini.Long = %+v, want Utilization=3 (round((1-0.9737029)*100))", gemini.Long)
	}
	if gemini.Long.Reset != "2026-08-17T05:29:20Z" {
		t.Fatalf("gemini.Long.Reset = %q, want %q", gemini.Long.Reset, "2026-08-17T05:29:20Z")
	}
	if gemini.Short == nil || gemini.Short.Utilization == nil || *gemini.Short.Utilization != 6 {
		t.Fatalf("gemini.Short = %+v, want Utilization=6 (round((1-0.9405329)*100))", gemini.Short)
	}

	thirdParty := groups[1]
	if thirdParty.GroupID != "3p" {
		t.Fatalf("thirdParty.GroupID = %q, want %q", thirdParty.GroupID, "3p")
	}
	// remainingFraction: 0 is a legitimate value (quota fully used) and must
	// not be treated the same as "field absent".
	if thirdParty.Long == nil || thirdParty.Long.Utilization == nil || *thirdParty.Long.Utilization != 100 {
		t.Fatalf("thirdParty.Long = %+v, want Utilization=100 (remainingFraction=0 must not be dropped)", thirdParty.Long)
	}
	if thirdParty.Short == nil || thirdParty.Short.Utilization == nil || *thirdParty.Short.Utilization != 50 {
		t.Fatalf("thirdParty.Short = %+v, want Utilization=50", thirdParty.Short)
	}
}

func TestParseAntigravityQuotaSummary_EmptyOrMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty body":       []byte(``),
		"no groups field":  []byte(`{}`),
		"groups not array": []byte(`{"groups": "oops"}`),
		"empty groups":     []byte(`{"groups": []}`),
		"buckets missing":  []byte(`{"groups": [{"displayName": "X"}]}`),
		"bucket has neither field": []byte(`{"groups": [{"buckets": [
			{"bucketId": "gemini-weekly"}
		]}]}`),
		"unrecognized bucket suffix": []byte(`{"groups": [{"buckets": [
			{"bucketId": "gemini-monthly", "remainingFraction": 0.5}
		]}]}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			groups, ok := parseAntigravityQuotaSummary(body)
			if ok || len(groups) != 0 {
				t.Fatalf("parseAntigravityQuotaSummary(%s) = (%v, %v), want (nil, false)", name, groups, ok)
			}
		})
	}
}

func TestAntigravityQuotaProjectID(t *testing.T) {
	if got := antigravityQuotaProjectID(nil); got != "" {
		t.Fatalf("nil auth: got %q, want empty", got)
	}
	if got := antigravityQuotaProjectID(&Auth{}); got != "" {
		t.Fatalf("no metadata: got %q, want empty", got)
	}
	a := &Auth{Metadata: map[string]any{"project_id": "  helical-parity-106wv  "}}
	if got := antigravityQuotaProjectID(a); got != "helical-parity-106wv" {
		t.Fatalf("got %q, want trimmed project id", got)
	}
	a = &Auth{Metadata: map[string]any{"project_id": 123}}
	if got := antigravityQuotaProjectID(a); got != "" {
		t.Fatalf("wrong type: got %q, want empty", got)
	}
}

func TestAntigravityQuotaBaseURLCandidates(t *testing.T) {
	if got := antigravityQuotaBaseURLCandidates(nil); len(got) != 2 || got[0] != antigravityQuotaBaseURLDaily || got[1] != antigravityQuotaBaseURLProd {
		t.Fatalf("default candidates = %v, want [daily, prod]", got)
	}
	a := &Auth{Attributes: map[string]string{"base_url": "https://custom.example.com/"}}
	if got := antigravityQuotaBaseURLCandidates(a); len(got) != 1 || got[0] != "https://custom.example.com" {
		t.Fatalf("attributes base_url candidates = %v, want [https://custom.example.com]", got)
	}
	a = &Auth{Metadata: map[string]any{"base_url": "https://custom2.example.com"}}
	if got := antigravityQuotaBaseURLCandidates(a); len(got) != 1 || got[0] != "https://custom2.example.com" {
		t.Fatalf("metadata base_url candidates = %v, want [https://custom2.example.com]", got)
	}
}

func TestSetAndGetAntigravityQuotaGroups(t *testing.T) {
	utilization := 42
	groups := []AntigravityQuotaGroup{{GroupID: "gemini", Long: &AntigravityQuotaWindow{Utilization: &utilization}}}

	auth := &Auth{Provider: "antigravity"}
	now := time.Now()
	if !SetAntigravityQuotaGroups(auth, groups, now) {
		t.Fatalf("SetAntigravityQuotaGroups = false, want true")
	}
	got := AntigravityQuotaGroups(auth)
	if len(got) != 1 || got[0].GroupID != "gemini" {
		t.Fatalf("AntigravityQuotaGroups = %+v, want the stored group", got)
	}

	// Out-of-order protection: a stale probe must not clobber a fresher snapshot.
	older := now.Add(-time.Minute)
	staleGroups := []AntigravityQuotaGroup{{GroupID: "stale"}}
	if SetAntigravityQuotaGroups(auth, staleGroups, older) {
		t.Fatalf("SetAntigravityQuotaGroups with older observedAt = true, want false (should be rejected)")
	}
	got = AntigravityQuotaGroups(auth)
	if len(got) != 1 || got[0].GroupID != "gemini" {
		t.Fatalf("stale write clobbered snapshot: %+v", got)
	}
}

func TestAntigravityQuotaGroups_NilAndEmpty(t *testing.T) {
	if got := AntigravityQuotaGroups(nil); got != nil {
		t.Fatalf("nil auth: got %v, want nil", got)
	}
	if got := AntigravityQuotaGroups(&Auth{}); got != nil {
		t.Fatalf("no RateLimits: got %v, want nil", got)
	}
	if got := SetAntigravityQuotaGroups(&Auth{}, nil, time.Now()); got {
		t.Fatalf("SetAntigravityQuotaGroups with empty groups = true, want false")
	}
}
