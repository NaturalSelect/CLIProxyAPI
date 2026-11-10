package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	claudeDiagnosticsTTL            = time.Hour
	claudeDiagnosticsCleanupPeriod  = 15 * time.Minute
	claudeDiagnosticsMaxEntries     = 4096
	claudeDiagnosticsEvictBatchSize = 256
)

type claudeDiagnosticsEntry struct {
	previousMessageID string
	minimumSequence   uint64
	committedSequence uint64
	lastAccess        uint64
	expiresAt         time.Time
}

var claudeDiagnosticsState = struct {
	sync.Mutex
	entries      map[string]claudeDiagnosticsEntry
	lastCleanup  time.Time
	nextSequence uint64
	nextAccess   uint64
}{entries: make(map[string]claudeDiagnosticsEntry)}

// BeginClaudeDiagnostics starts one request generation for a stable credential
// identity and Claude conversation. It returns the last successfully completed
// upstream message ID, if any. Only a SHA-256 digest of the credential identity
// and session is retained as the cache key, so access-token rotation does not
// interrupt continuity.
func BeginClaudeDiagnostics(credentialIdentity, sessionID string) (key string, sequence uint64, previousMessageID string) {
	credentialIdentity = strings.TrimSpace(credentialIdentity)
	sessionID = strings.TrimSpace(sessionID)
	if credentialIdentity == "" || sessionID == "" {
		return "", 0, ""
	}
	digest := sha256.Sum256([]byte(credentialIdentity + "\x00" + sessionID))
	key = hex.EncodeToString(digest[:])
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	cleanupClaudeDiagnosticsLocked(now)

	entry, found := claudeDiagnosticsState.entries[key]
	newGeneration := !found || (!entry.expiresAt.IsZero() && now.After(entry.expiresAt))
	if newGeneration && !found {
		evictClaudeDiagnosticsLocked()
	}

	claudeDiagnosticsState.nextSequence++
	sequence = claudeDiagnosticsState.nextSequence
	if newGeneration {
		entry = claudeDiagnosticsEntry{minimumSequence: sequence}
	}
	claudeDiagnosticsState.nextAccess++
	entry.lastAccess = claudeDiagnosticsState.nextAccess
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
	return key, sequence, entry.previousMessageID
}

// CommitClaudeDiagnostics advances continuity only after a response completes.
// A response from an older concurrently-started request cannot overwrite a
// newer committed generation, including after TTL expiry or capacity eviction.
func CommitClaudeDiagnostics(key string, sequence uint64, messageID string) {
	key = strings.TrimSpace(key)
	messageID = strings.TrimSpace(messageID)
	if key == "" || sequence == 0 || messageID == "" {
		return
	}
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	entry, ok := claudeDiagnosticsState.entries[key]
	if !ok || sequence < entry.minimumSequence || sequence < entry.committedSequence {
		return
	}
	claudeDiagnosticsState.nextAccess++
	entry.previousMessageID = messageID
	entry.committedSequence = sequence
	entry.lastAccess = claudeDiagnosticsState.nextAccess
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
}

func cleanupClaudeDiagnosticsLocked(now time.Time) {
	if !claudeDiagnosticsState.lastCleanup.IsZero() && now.Sub(claudeDiagnosticsState.lastCleanup) < claudeDiagnosticsCleanupPeriod {
		return
	}
	for key, entry := range claudeDiagnosticsState.entries {
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			delete(claudeDiagnosticsState.entries, key)
		}
	}
	claudeDiagnosticsState.lastCleanup = now
}

func evictClaudeDiagnosticsLocked() {
	if len(claudeDiagnosticsState.entries) < claudeDiagnosticsMaxEntries {
		return
	}
	type candidate struct {
		key        string
		lastAccess uint64
	}
	candidates := make([]candidate, 0, len(claudeDiagnosticsState.entries))
	for key, entry := range claudeDiagnosticsState.entries {
		candidates = append(candidates, candidate{key: key, lastAccess: entry.lastAccess})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastAccess < candidates[j].lastAccess
	})
	count := min(claudeDiagnosticsEvictBatchSize, len(candidates))
	for _, candidate := range candidates[:count] {
		delete(claudeDiagnosticsState.entries, candidate.key)
	}
}

func resetClaudeDiagnosticsForTest() {
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	claudeDiagnosticsState.entries = make(map[string]claudeDiagnosticsEntry)
	claudeDiagnosticsState.lastCleanup = time.Time{}
	claudeDiagnosticsState.nextSequence = 0
	claudeDiagnosticsState.nextAccess = 0
}

// NOTE: IsClaudeQuotaProbeRequest reports the max_tokens=1 quota probe Claude Code sends to read account limits.
func IsClaudeQuotaProbeRequest(body []byte) bool {
	if gjson.GetBytes(body, "max_tokens").Int() != 1 {
		return false
	}
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && len(tools.Array()) > 0 {
		return false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return true
	}
	if len(messages.Array()) != 1 {
		return false
	}
	first := messages.Array()[0]
	if first.Get("role").String() != "user" {
		return false
	}
	isProbeText := func(text string) bool {
		return text == "quota" || text == "test" || text == "." || text == "probe"
	}
	content := first.Get("content")
	if content.Type == gjson.String {
		return isProbeText(strings.TrimSpace(content.String()))
	}
	if !content.IsArray() {
		return false
	}
	nonReminderCount := 0
	matched := false
	for _, part := range content.Array() {
		text := strings.TrimSpace(part.Get("text").String())
		if strings.Contains(text, "<system-reminder>") {
			continue
		}
		nonReminderCount++
		if isProbeText(text) || (text == "Hi" && part.Get("cache_control").Exists()) {
			matched = true
		}
	}
	return nonReminderCount == 1 && matched
}
