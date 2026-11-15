package helps

import (
	"bytes"
	"context"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// NOTE: A nil state means identity confusion is disabled; every method accepts nil.
type CodexIdentityConfuseState struct {
	authID                 string
	originalPromptCacheKey string
	promptCacheKey         string
	turnIDs                []codexIdentityReplacement
}

type codexIdentityReplacement struct {
	original string
	confused string
}

type codexIdentityConfuseKey struct{}

func WithCodexIdentityConfuse(ctx context.Context, state *CodexIdentityConfuseState) context.Context {
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, codexIdentityConfuseKey{}, state)
}

func CodexIdentityConfuseFromContext(ctx context.Context) *CodexIdentityConfuseState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(codexIdentityConfuseKey{}).(*CodexIdentityConfuseState)
	return state
}

// NOTE: Remapping only helps when routing pins a session to one auth.
func CodexIdentityConfuseEnabled(cfg *config.Config) bool {
	if cfg == nil || !cfg.Codex.IdentityConfuse {
		return false
	}
	strategy := strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy))
	return cfg.Routing.SessionAffinity || strategy == "fill-first" || strategy == "fillfirst" || strategy == "ff"
}

// NOTE: Must run before payload rules are applied so configured values stay final.
func ApplyCodexIdentityConfuseDoc(cfg *config.Config, auth *cliproxyauth.Auth, userPayload []byte, doc *ojson.Doc) *CodexIdentityConfuseState {
	if !CodexIdentityConfuseEnabled(cfg) || auth == nil || strings.TrimSpace(auth.ID) == "" || doc == nil {
		return nil
	}
	state := &CodexIdentityConfuseState{authID: strings.TrimSpace(auth.ID)}
	// NOTE: Only rewrite fields doc already carries; earlier stages may strip them on purpose.
	if promptCacheKey := strings.TrimSpace(ojson.GetBytesNoCopy(userPayload, "prompt_cache_key").String()); promptCacheKey != "" {
		state.originalPromptCacheKey = strings.Clone(promptCacheKey)
		state.promptCacheKey = codexIdentityConfuseUUID(state.authID, "prompt-cache", promptCacheKey)
		if doc.Get("prompt_cache_key").Exists() {
			SetStringIfDifferentDoc(doc, "prompt_cache_key", state.promptCacheKey)
		}
	}
	if installationID := strings.TrimSpace(doc.Get("client_metadata.x-codex-installation-id").String()); installationID != "" {
		_ = doc.Set("client_metadata.x-codex-installation-id", codexIdentityConfuseUUID(state.authID, "installation", installationID))
	}
	if turnMetadata := strings.Clone(strings.TrimSpace(doc.Get("client_metadata.x-codex-turn-metadata").String())); turnMetadata != "" {
		_ = doc.Set("client_metadata.x-codex-turn-metadata", state.ConfuseTurnMetadata(turnMetadata))
	}
	if state.promptCacheKey != "" && strings.TrimSpace(doc.Get("client_metadata.x-codex-window-id").String()) != "" {
		_ = doc.Set("client_metadata.x-codex-window-id", state.WindowID())
	}
	return state
}

func (s *CodexIdentityConfuseState) PromptCacheKey() string {
	if s == nil {
		return ""
	}
	return s.promptCacheKey
}

func (s *CodexIdentityConfuseState) WindowID() string {
	if s == nil || s.promptCacheKey == "" {
		return ""
	}
	return s.promptCacheKey + ":0"
}

func (s *CodexIdentityConfuseState) ConfuseTurnMetadata(rawTurnMetadata string) string {
	if s == nil {
		return rawTurnMetadata
	}
	updated := rawTurnMetadata
	hasPromptCacheKey := ojson.Get(rawTurnMetadata, "prompt_cache_key").Exists()
	if s.promptCacheKey != "" && !hasPromptCacheKey && s.originalPromptCacheKey != "" {
		updated = strings.ReplaceAll(updated, s.originalPromptCacheKey, s.promptCacheKey)
	}
	if !ojson.Valid(updated) {
		return updated
	}
	doc := ojson.NewDocString(updated)
	changed := false
	if s.promptCacheKey != "" && hasPromptCacheKey {
		_ = doc.Set("prompt_cache_key", s.promptCacheKey)
		changed = true
	}
	if turnID := strings.TrimSpace(ojson.Get(rawTurnMetadata, "turn_id").String()); turnID != "" {
		_ = doc.Set("turn_id", s.confuseTurnID(turnID))
		changed = true
	}
	if s.promptCacheKey != "" && ojson.Get(rawTurnMetadata, "window_id").Exists() {
		_ = doc.Set("window_id", s.WindowID())
		changed = true
	}
	if !changed {
		return updated
	}
	return string(doc.Bytes())
}

func (s *CodexIdentityConfuseState) ExposeResponse(payload []byte) []byte {
	if s == nil {
		return payload
	}
	payload = replaceCodexIdentity(payload, s.promptCacheKey, s.originalPromptCacheKey)
	for _, turnID := range s.turnIDs {
		payload = replaceCodexIdentity(payload, turnID.confused, turnID.original)
	}
	return payload
}

func (s *CodexIdentityConfuseState) confuseTurnID(turnID string) string {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return turnID
	}
	for _, replacement := range s.turnIDs {
		if replacement.original == turnID || replacement.confused == turnID {
			return replacement.confused
		}
	}
	confusedTurnID := codexIdentityConfuseUUID(s.authID, "turn", turnID)
	s.turnIDs = append(s.turnIDs, codexIdentityReplacement{original: strings.Clone(turnID), confused: confusedTurnID})
	return confusedTurnID
}

func replaceCodexIdentity(payload []byte, from string, to string) []byte {
	if len(payload) == 0 || from == "" || to == "" || from == to || !bytes.Contains(payload, []byte(from)) {
		return payload
	}
	return bytes.ReplaceAll(payload, []byte(from), []byte(to))
}

func codexIdentityConfuseUUID(authID string, kind string, value string) string {
	name := strings.Join([]string{"cli-proxy-api", "codex", "identity-confuse", kind, strings.TrimSpace(authID), strings.TrimSpace(value)}, ":")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}
