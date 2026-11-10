package executor

import (
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

const preservedThinkingSignedHistory = `"messages":[{"role":"user","content":"hi"},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"sig"},{"type":"text","text":"ok"}]},` +
	`{"role":"user","content":"again"}]`

func preservedThinkingBody(model, thinking string) []byte {
	return []byte(`{"model":"` + model + `",` + thinking + preservedThinkingSignedHistory + `}`)
}

func TestApplyClaudePreservedThinkingBinding_InjectsDropBlock(t *testing.T) {
	for _, model := range []string{
		"claude-opus-5-5",
		"claude-sonnet-5-5",
		"claude-haiku-5-5",
		"claude-fable-5-1",
		"claude-fable-5-2",
		"claude-fable-5-2[1m]",
		"claude-fable-5.2",
	} {
		t.Run(model, func(t *testing.T) {
			body := preservedThinkingBody(model, `"thinking":{"type":"adaptive"},`)
			out := applyClaudePreservedThinkingBinding(body)
			if got := gjson.GetBytes(out, "thinking.block_binding.prefix_mismatch_behavior").String(); got != "drop_block" {
				t.Fatalf("prefix_mismatch_behavior = %q, want drop_block; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive preserved", got)
			}
		})
	}
}

func TestApplyClaudePreservedThinkingBinding_RedactedThinkingCountsAsSigned(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[` +
		`{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}]}`)
	out := applyClaudePreservedThinkingBinding(body)
	if got := gjson.GetBytes(out, "thinking.block_binding.prefix_mismatch_behavior").String(); got != "drop_block" {
		t.Fatalf("prefix_mismatch_behavior = %q, want drop_block; body=%s", got, out)
	}
}

func TestApplyClaudePreservedThinkingBinding_ManualThinkingOnlyWhereAccepted(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{model: "claude-opus-5-5", want: true},
		{model: "claude-fable-5-1", want: true},
		{model: "claude-fable-5-2", want: true},
		{model: "claude-sonnet-5-5", want: false},
		{model: "claude-haiku-5-5", want: false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body := preservedThinkingBody(tc.model, `"thinking":{"type":"enabled","budget_tokens":2048},`)
			out := applyClaudePreservedThinkingBinding(body)
			if got := gjson.GetBytes(out, "thinking.block_binding").Exists(); got != tc.want {
				t.Fatalf("block_binding injected = %v, want %v; body=%s", got, tc.want, out)
			}
		})
	}
}

func TestApplyClaudePreservedThinkingBinding_LeavesRequestAloneWhenNotApplicable(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "unrelated model", body: preservedThinkingBody("claude-opus-4-8", `"thinking":{"type":"adaptive"},`)},
		{name: "mythos 5.1 is not fable 5.1", body: preservedThinkingBody("claude-mythos-5-1", `"thinking":{"type":"adaptive"},`)},
		{name: "fable 5.10 is not fable 5.1", body: preservedThinkingBody("claude-fable-5-10", `"thinking":{"type":"adaptive"},`)},
		{name: "no thinking object", body: preservedThinkingBody("claude-opus-5-5", ``)},
		{name: "thinking disabled", body: preservedThinkingBody("claude-opus-5-5", `"thinking":{"type":"disabled"},`)},
		{name: "no signed history", body: []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`)},
		{name: "unsigned thinking block", body: []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":" "}]}]}`)},
		{name: "string content", body: []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":"plain"}]}`)},
		{name: "empty body", body: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := applyClaudePreservedThinkingBinding(tc.body)
			if string(out) != string(tc.body) {
				t.Fatalf("body changed:\n got: %s\nwant: %s", out, tc.body)
			}
		})
	}
}

func TestApplyClaudePreservedThinkingBinding_CallerBehaviorWins(t *testing.T) {
	body := preservedThinkingBody("claude-opus-5-5",
		`"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"error"}},`)
	out := applyClaudePreservedThinkingBinding(body)
	if got := gjson.GetBytes(out, "thinking.block_binding.prefix_mismatch_behavior").String(); got != "error" {
		t.Fatalf("prefix_mismatch_behavior = %q, want caller value error kept", got)
	}
}

func TestApplyClaudeHeaders_BlockBindingBodyAlwaysGetsBindingBeta(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-5-5","thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[{"role":"user","content":"hi"}]}`)
	for _, confirmed := range []bool{false, true} {
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-binding-beta"}}
		req := newClaudeHeaderTestRequest(t, nil)
		if err := applyClaudeHeaders(req, auth, "key-binding-beta", false, nil, body, nil, nil, confirmed); err != nil {
			t.Fatalf("confirmed=%v: applyClaudeHeaders() error = %v", confirmed, err)
		}
		if got := req.Header.Get("Anthropic-Beta"); !strings.Contains(got, claudeThinkingBindingBeta) {
			t.Fatalf("confirmed=%v: Anthropic-Beta = %q, want %s for a block_binding body", confirmed, got, claudeThinkingBindingBeta)
		}
	}
}

func TestClaudeCodeCLIBetas_ThinkingDisplayUpdatesGating(t *testing.T) {
	updates := func(thinking string) []byte {
		return []byte(`{"model":"claude-opus-5-5","thinking":` + thinking + `}`)
	}
	has := func(body []byte, requested map[string]bool, legacy bool) bool {
		return strings.Contains(claudeCodeCLIBetas(body, requested, false, legacy), claudeThinkingDisplayUpdatesBeta)
	}

	if !has(updates(`{"type":"adaptive","display":"updates"}`), nil, false) {
		t.Fatal("display=updates should send thinking-display-updates")
	}
	if !has(updates(`{"type":"enabled","budget_tokens":2048,"display":"UPDATES"}`), nil, false) {
		t.Fatal("display matching is case-insensitive")
	}
	if has(updates(`{"type":"adaptive","display":"summarized"}`), nil, false) {
		t.Fatal("display=summarized must not send thinking-display-updates")
	}
	if has(updates(`{"type":"adaptive"}`), nil, false) {
		t.Fatal("no display must not send thinking-display-updates")
	}
	if has(updates(`{"type":"disabled","display":"updates"}`), nil, false) {
		t.Fatal("thinking disabled must suppress thinking-display-updates")
	}
	if has(updates(`{"type":"disabled"}`), map[string]bool{claudeThinkingDisplayUpdatesBeta: true}, false) {
		t.Fatal("thinking disabled must suppress even a requested thinking-display-updates")
	}
	if !has(updates(`{"type":"adaptive"}`), map[string]bool{claudeThinkingDisplayUpdatesBeta: true}, false) {
		t.Fatal("a requested thinking-display-updates should be forwarded")
	}
	if has(updates(`{"type":"adaptive","display":"updates"}`), nil, true) {
		t.Fatal("legacy wire baselines predate thinking-display-updates")
	}
}

func TestClaudeCodeCLIBetas_ThinkingDisplayUpdatesOmittedForQuotaProbe(t *testing.T) {
	probe := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"thinking":{"type":"adaptive","display":"updates"},` +
		`"messages":[{"role":"user","content":"quota"}],"metadata":{"user_id":"user"}}`)
	if got := claudeCodeCLIBetas(probe, nil, true, false); strings.Contains(got, claudeThinkingDisplayUpdatesBeta) {
		t.Fatalf("quota probe betas = %q, want thinking-display-updates omitted", got)
	}
}

func TestClaudeCodeCLIBetas_ThinkingBetasOrder(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"updates","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`)
	got := claudeCodeCLIBetas(body, nil, false, false)
	want := claudeThinkingBindingBeta + "," + claudeThinkingDisplayUpdatesBeta
	if !strings.Contains(got, want) {
		t.Fatalf("betas = %q, want %q in that relative order", got, want)
	}
}
