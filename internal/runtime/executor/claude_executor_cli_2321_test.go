package executor

import (
	"strings"
	"testing"
)

// claudeCode2321MainBody keeps only the fields of a captured 2.1.321 main-thread
// body that gate betas: a role=system turn with an inline tool_addition, a
// deferred tool, the advisor tool, safeguards, thread, diagnostics and a 1h
// cache TTL.
func claudeCode2321MainBody(model string) []byte {
	return []byte(`{"model":"` + model + `","max_tokens":128000,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"say ok"}]},` +
		`{"role":"system","content":[{"type":"text","text":"tools changed"},` +
		`{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__guide","input_schema":{"type":"object"}}}}]}],` +
		`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},` +
		`{"type":"text","text":"prompt","cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}}],` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}},` +
		`{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"defer_loading":true},` +
		`{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5","defer_loading":true}],` +
		`"thinking":{"type":"adaptive","display":"updates"},` +
		`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},` +
		`"safeguards":{"type":"dangerous_tool_use"},"output_config":{"effort":"medium"},` +
		`"thread":{"type":"create"},"diagnostics":{"previous_message_id":null},` +
		`"metadata":{"user_id":"user"},"stream":true}`)
}

// The expected value is the Claude Code 2.1.321 capture (OAuth, cli entrypoint)
// restricted to the betas this proxy models, plus the fallback-credit trailer
// the proxy adds for every OAuth credential.
func TestClaudeCodeCLIBetas_Matches2321MainThreadOrder(t *testing.T) {
	const want = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14," +
		"context-management-2025-06-27,prompt-caching-scope-2026-01-05,per-turn-control-2026-07-01," +
		"mid-conversation-tool-changes-2026-07-01,inline-tools-2026-09-15,advisor-tool-2026-03-01," +
		"thinking-token-count-2026-05-13,advanced-tool-use-2025-11-20,effort-2025-11-24," +
		"dangerous-tool-use-2026-09-03,fallback-credit-2026-06-01,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11," +
		"cache-diagnosis-2026-04-07,message-threads-2026-08-12"

	for _, model := range []string{"claude-fable-5-2", "claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5"} {
		t.Run(model, func(t *testing.T) {
			if got := claudeCodeCLIBetas(claudeCode2321MainBody(model), nil, true, false); got != want {
				t.Fatalf("claudeCodeCLIBetas() =\n %s\nwant\n %s", got, want)
			}
		})
	}
}

func TestClaudeCodeCLIBetas_Haiku55PutsClaudeCodeAfterPromptCachingScope(t *testing.T) {
	const want = "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27," +
		"prompt-caching-scope-2026-01-05,claude-code-20250219,per-turn-control-2026-07-01," +
		"mid-conversation-tool-changes-2026-07-01,inline-tools-2026-09-15,advisor-tool-2026-03-01," +
		"thinking-token-count-2026-05-13,advanced-tool-use-2025-11-20,effort-2025-11-24," +
		"dangerous-tool-use-2026-09-03,fallback-credit-2026-06-01,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11," +
		"cache-diagnosis-2026-04-07,message-threads-2026-08-12"
	if got := claudeCodeCLIBetas(claudeCode2321MainBody("claude-haiku-5-5"), nil, true, false); got != want {
		t.Fatalf("claudeCodeCLIBetas() =\n %s\nwant\n %s", got, want)
	}
}

func TestClaudeCodeCLIBetas_QuotaProbeOmitsClaudeCodeAndThinkingTokenCount(t *testing.T) {
	probe := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"quota"}],"metadata":{"user_id":"user"}}`)
	got := claudeCodeCLIBetas(probe, nil, true, false)
	for _, beta := range []string{claudeCodeBeta, claudeThinkingTokenCountBeta} {
		if strings.Contains(got, beta) {
			t.Fatalf("quota probe betas = %q, want %s omitted", got, beta)
		}
	}
	if requested := claudeCodeCLIBetas(probe, map[string]bool{claudeThinkingTokenCountBeta: true}, true, false); !strings.Contains(requested, claudeThinkingTokenCountBeta) {
		t.Fatalf("quota probe betas = %q, want requested thinking-token-count kept", requested)
	}
}

func TestClaudeCodeCLIBetas_MidConversationSystemOnlyWhenRequested(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5"}`)
	if got := claudeCodeCLIBetas(body, nil, false, false); strings.Contains(got, claudeMidConvSystemBeta) {
		t.Fatalf("betas = %q, want mid-conversation-system omitted unless requested", got)
	}
	got := claudeCodeCLIBetas(body, map[string]bool{claudeMidConvSystemBeta: true}, false, false)
	if !strings.Contains(got, claudeMidConvSystemBeta+",per-turn-control-2026-07-01") {
		t.Fatalf("betas = %q, want requested mid-conversation-system ahead of per-turn-control", got)
	}
}

func TestClaudeCodeCLIBetas_MessageThreadsFollowsBodyOrRequest(t *testing.T) {
	if got := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5"}`), nil, false, false); strings.Contains(got, claudeMessageThreadsBeta) {
		t.Fatalf("betas = %q, want message-threads omitted without a thread object", got)
	}
	if got := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5","thread":{"type":"create"}}`), nil, false, false); !strings.HasSuffix(got, claudeMessageThreadsBeta) {
		t.Fatalf("betas = %q, want message-threads last for a thread body", got)
	}
	if got := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5"}`), map[string]bool{claudeMessageThreadsBeta: true}, false, false); !strings.HasSuffix(got, claudeMessageThreadsBeta) {
		t.Fatalf("betas = %q, want requested message-threads last", got)
	}
}

func TestClaudeCodeCLIBetas_EffortPrecedesDangerousToolUse(t *testing.T) {
	got := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5","safeguards":{"type":"dangerous_tool_use"}}`), nil, false, false)
	if !strings.Contains(got, claudeEffortBeta+","+claudeDangerousToolUseBeta) {
		t.Fatalf("betas = %q, want effort ahead of dangerous-tool-use", got)
	}
	legacy := claudeCodeCLIBetas([]byte(`{"model":"claude-opus-5-5","safeguards":{"type":"dangerous_tool_use"}}`), nil, false, true)
	if !strings.Contains(legacy, claudeDangerousToolUseBeta+","+claudeEffortBeta) {
		t.Fatalf("legacy betas = %q, want dangerous-tool-use ahead of effort", legacy)
	}
}

// The 2.1.321 capture of the prompt "say ok" carried cc_version=2.1.321.20d.
func TestComputeFingerprintMatchesClaudeCode2321Capture(t *testing.T) {
	if got := computeFingerprint("say ok", "2.1.321"); got != "20d" {
		t.Fatalf("computeFingerprint() = %q, want 20d", got)
	}
}

func TestClaudeIncludePerTurnControl_2321CatalogModels(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{model: "claude-fable-5-2", want: true},
		{model: "claude-fable-5-2[1m]", want: true},
		{model: "claude-fable-5-1", want: true},
		{model: "claude-opus-5-5", want: true},
		{model: "claude-sonnet-5-5", want: true},
		{model: "claude-haiku-5-5", want: true},
		{model: "claude-fable-5", want: false},
		{model: "claude-sonnet-5", want: false},
		{model: "claude-opus-4-8", want: false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `"}`)
			if got := claudeIncludePerTurnControl(body, nil); got != tc.want {
				t.Fatalf("claudeIncludePerTurnControl(%s) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestClaudeIncludePerTurnTiming_2321CatalogModels(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{model: "claude-fable-5-2", want: true},
		{model: "claude-fable-5-2[1m]", want: true},
		{model: "claude-fable-5-1", want: true},
		{model: "claude-opus-5-5", want: true},
		{model: "claude-sonnet-5-5", want: true},
		{model: "claude-mythos-5-2", want: true},
		{model: "claude-haiku-5-5", want: false},
		{model: "claude-fable-5", want: false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","output_config":{"timing":{"type":"enabled"}}}`)
			if got := claudeIncludePerTurnTiming(body, nil); got != tc.want {
				t.Fatalf("claudeIncludePerTurnTiming(%s) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
