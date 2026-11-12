package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestGeminiClaudeCarrierPlacement(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	carrier := `{"type":"thinking","thinking":"","signature":"` + signature + `"}`
	tool := `{"type":"tool_use","id":"call-1","name":"lookup","input":{}}`
	text := `{"type":"text","text":"answer"}`
	marked := func(direction, target string) string {
		return `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:` + direction + `:` + target + `:` + base64.RawStdEncoding.EncodeToString([]byte(signature)) + `"}`
	}
	for _, tt := range []struct {
		name       string
		content    string
		signatures []string
	}{
		{"leading function", carrier + `,` + tool, []string{signature}},
		{"trailing function", tool + `,` + carrier, []string{signature}},
		{"signed text before tool", carrier + `,` + text + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"text only", carrier + `,` + text, []string{signature}},
		{"trailing text", text + `,` + carrier, []string{signature}},
		{"standalone", carrier, []string{signature}},
		{"marked next function", marked("next", "function") + `,` + tool, []string{signature}},
		{"prefixed signature", `{"type":"thinking","thinking":"","signature":"gemini#` + signature + `"},` + tool, []string{signature}},
		{"consecutive carriers", carrier + `,{"type":"thinking","thinking":"","signature":"EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"},` + tool, []string{signature, "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"}},
		{"marked previous text", text + `,` + marked("previous", "text") + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"marked standalone", marked("standalone", "any") + `,` + tool, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"wrong target", marked("next", "function") + `,` + text + `,` + tool, []string{"", sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"foreign carrier", `{"type":"thinking","thinking":"","signature":"claude#invalid"},` + tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"malformed carrier", `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:next:function:invalid"},` + tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"no carrier", tool, []string{sigcompat.GeminiSkipThoughtSignatureValidator}},
		{"parallel sibling", carrier + `,` + tool + `,{"type":"tool_use","id":"call-2","name":"lookup","input":{}}`, []string{signature, sigcompat.GeminiSkipThoughtSignatureValidator}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := ConvertClaudeRequestToGemini("gemini-4-carbon", claudeReplayRequest(`[`+tt.content+`]`), false)
			parts := gjson.GetBytes(output, "contents.0.parts").Array()
			if len(parts) != len(tt.signatures) {
				t.Fatalf("got %d parts, want %d: %s", len(parts), len(tt.signatures), output)
			}
			for i, want := range tt.signatures {
				if got := parts[i].Get("thoughtSignature").String(); got != want {
					t.Errorf("part %d signature = %q, want %q; output=%s", i, got, want, output)
				}
			}
		})
	}
}

func TestGeminiClaudeCarrierStaysInAssistantMessage(t *testing.T) {
	carrier := `{"type":"thinking","thinking":"","signature":"` + capturedGeminiThinkingSignature + `"}`
	request := []byte(`{"messages":[{"role":"assistant","content":[` + carrier + `]},{"role":"user","content":[` + carrier + `,{"type":"text","text":"continue"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
	output := ConvertClaudeRequestToGemini("gemini-4-carbon", request, false)
	var sawCall bool
	for _, content := range gjson.GetBytes(output, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if content.Get("role").String() == "user" && part.Get("thoughtSignature").Exists() {
				t.Fatalf("user message replayed assistant signature: %s", output)
			}
			if part.Get("functionCall").Exists() {
				sawCall = true
				if part.Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
					t.Fatalf("carrier leaked between messages: %s", output)
				}
			}
		}
	}
	if !sawCall {
		t.Fatalf("functionCall missing: %s", output)
	}
}

func TestGeminiClaudePartMetadataPreservesSupportedPayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		part string
		tool bool
	}{
		{"text", `{"text":"answer","partMetadata":{"source":"model"}}`, false},
		{"tool", `{"functionCall":{"name":"lookup","args":{}},"partMetadata":{"source":"model"}}`, true},
	} {
		for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
			for _, signed := range []bool{false, true} {
				t.Run(tt.name+"/"+mode+"/signed="+strconv.FormatBool(signed), func(t *testing.T) {
					part := []byte(tt.part)
					if signed {
						part, _ = sjson.SetBytes(part, "thoughtSignature", capturedGeminiThinkingSignature)
					}
					content := geminiClaudeContentForParts(t, []string{string(part)}, mode)
					output := replayGeminiClaudeContent(t, content)
					parts := gjson.GetBytes(output, "contents.0.parts").Array()
					if gjson.GetBytes(output, "contents.0.role").String() != "model" || len(parts) != 1 {
						t.Fatalf("supported payload with metadata was dropped: %s; content=%s", output, content)
					}
					if tt.tool {
						if parts[0].Get("functionCall.name").String() != "lookup" || parts[0].Get("functionCall.args").Raw != `{}` {
							t.Errorf("metadata changed tool payload: %s", output)
						}
					} else if parts[0].Get("text").String() != "answer" {
						t.Errorf("metadata changed text payload: %s", output)
					}
					if signed && parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
						t.Errorf("metadata dropped native signature: %s", output)
					}
				})
			}
		}
	}
}

func TestGeminiClaudeDelayedTextSignaturePartBoundaryRoundTrip(t *testing.T) {
	parts := []string{`{"text":"unsigned"}`, `{"text":"signed"}`, `{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}`}
	for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
		t.Run(mode, func(t *testing.T) {
			content := geminiClaudeContentForParts(t, parts, mode)
			output := replayGeminiClaudeContent(t, content)
			actual := gjson.GetBytes(output, "contents.0.parts").Array()
			if len(actual) != 2 || actual[0].Get("text").String() != "unsigned" || actual[0].Get("thoughtSignature").Exists() || actual[1].Get("text").String() != "signed" || actual[1].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
				t.Fatalf("delayed signature changed native text part contents: %s; content=%s", output, content)
			}
		})
	}
}

func TestGeminiClaudeUnsupportedPayloadDoesNotRetargetSignature(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	for _, tt := range []struct {
		name  string
		parts []string
	}{
		{"inline data", []string{`{"inlineData":{"mimeType":"image/png","data":"eA=="},"thoughtSignature":"` + signature + `"}`}},
		{"file data", []string{`{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"},"thoughtSignature":"` + signature + `"}`}},
		{"code", []string{`{"executableCode":{"language":"PYTHON","code":"1"},"thoughtSignature":"` + signature + `"}`}},
		{"delayed image signature", []string{`{"inlineData":{"mimeType":"image/png","data":"eA=="}}`, `{"thoughtSignature":"` + signature + `"}`}},
		{"delayed file signature", []string{`{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"}}`, `{"text":"","thought":true}`, `{"thoughtSignature":"` + signature + `"}`}},
	} {
		for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
			for _, prefix := range []string{`{"text":"before"}`, `{"functionCall":{"name":"lookup","args":{}}}`} {
				t.Run(tt.name+"/"+mode+"/"+prefix, func(t *testing.T) {
					parts := append([]string{prefix}, tt.parts...)
					parts = append(parts, `{"text":"after"}`)
					content := geminiClaudeContentForParts(t, parts, mode)
					output := replayGeminiClaudeContent(t, content)
					for _, part := range gjson.GetBytes(output, "contents.0.parts").Array() {
						if got := part.Get("thoughtSignature").String(); got != "" && got != sigcompat.GeminiSkipThoughtSignatureValidator {
							t.Errorf("unsupported payload signature was attached to a different part: %s; content=%s", output, content)
						}
					}
				})
			}
		}
	}
}

func geminiClaudeContentForParts(t *testing.T, parts []string, mode string) string {
	t.Helper()
	full := `{"candidates":[{"content":{"parts":[` + strings.Join(parts, ",") + `]},"finishReason":"STOP"}]}`
	if mode == "nonstream" {
		response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, []byte(full), nil)
		return gjson.GetBytes(response, "content").Raw
	}
	chunks := []string{full}
	if mode == "stream split chunks" {
		chunks = nil
		for _, part := range parts {
			chunks = append(chunks, `{"candidates":[{"content":{"parts":[`+part+`]}}]}`)
		}
		chunks = append(chunks, `{"candidates":[{"finishReason":"STOP"}]}`)
	}
	chunks = append(chunks, "[DONE]")
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	return claudeContentFromGeminiStream(t, events)
}

func TestGeminiClaudeSignedTextPartBoundaryRoundTrip(t *testing.T) {
	signed := `{"text":"signed","thoughtSignature":"` + capturedGeminiThinkingSignature + `"}`
	unsigned := `{"text":"unsigned"}`
	full := `{"candidates":[{"content":{"parts":[` + signed + `,` + unsigned + `]},"finishReason":"STOP"}]}`
	for _, mode := range []string{"nonstream", "stream same chunk", "stream split chunks"} {
		t.Run(mode, func(t *testing.T) {
			var content string
			if mode == "nonstream" {
				response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, []byte(full), nil)
				content = gjson.GetBytes(response, "content").Raw
			} else {
				chunks := []string{full, "[DONE]"}
				if mode == "stream split chunks" {
					chunks = []string{`{"candidates":[{"content":{"parts":[` + signed + `]}}]}`, `{"candidates":[{"content":{"parts":[` + unsigned + `]},"finishReason":"STOP"}]}`, "[DONE]"}
				}
				var param any
				var events []byte
				for _, chunk := range chunks {
					events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
				}
				content = claudeContentFromGeminiStream(t, events)
			}
			output := replayGeminiClaudeContent(t, content)
			parts := gjson.GetBytes(output, "contents.0.parts").Array()
			if len(parts) != 2 || parts[0].Get("text").String() != "signed" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("text").String() != "unsigned" || parts[1].Get("thoughtSignature").Exists() {
				t.Fatalf("signed text was merged with another native part: %s; content=%s", output, content)
			}
		})
	}
}

func TestGeminiClaudeCarrierDoesNotCrossNonEmptyThinking(t *testing.T) {
	signature := capturedGeminiThinkingSignature
	previous := `{"type":"thinking","thinking":"","signature":"cpa-gemini-carrier-v1:previous:text:` + base64.RawStdEncoding.EncodeToString([]byte(signature)) + `"}`
	for _, content := range []string{
		`[{"type":"text","text":"visible"},{"type":"thinking","thinking":"reasoning"},` + previous + `]`,
		`[{"type":"text","text":"visible"},{"type":"thinking","thinking":"","signature":"` + signature + `"},{"type":"thinking","thinking":"reasoning"},{"type":"text","text":"unrelated"}]`,
	} {
		for _, compat := range []bool{false, true} {
			request := []byte(`{"messages":[{"role":"assistant","content":` + content + `},{"role":"user","content":"continue"}]}`)
			output := convertClaudeRequestToGemini("gemini-4-carbon", request, false, compat)
			for _, part := range gjson.GetBytes(output, "contents.0.parts").Array() {
				if !part.Get("thought").Bool() && part.Get("thoughtSignature").Exists() {
					t.Errorf("compat=%t: signature crossed nonempty thought: %s", compat, output)
				}
			}
		}
	}
}

func TestGeminiClaudeDroppedThoughtSignatureDoesNotReachVisibleText(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"visible"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"reasoning","thought":true}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":""}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	content := claudeContentFromGeminiStream(t, events)
	output := replayGeminiClaudeContent(t, content)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 1 || parts[0].Get("text").String() != "visible" || parts[0].Get("thoughtSignature").Exists() {
		t.Fatalf("dropped thought signature rebound to visible text: %s; content=%s", output, content)
	}
}

func TestGeminiClaudeCarrierDoesNotConsumeWhitespaceThinking(t *testing.T) {
	request := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":" ","signature":"` + capturedGeminiThinkingSignature + `"},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
	for _, compat := range []bool{false, true} {
		output := convertClaudeRequestToGemini("gemini-4-carbon", request, false, compat)
		parts := gjson.GetBytes(output, "contents.0.parts").Array()
		wantParts := 1
		if compat {
			wantParts = 2
		}
		if len(parts) != wantParts {
			t.Fatalf("compat=%t: nonempty thinking was consumed as a carrier: %s", compat, output)
		}
		if compat && (parts[0].Get("text").String() != " " || !parts[0].Get("thought").Bool() || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature) {
			t.Errorf("compat thinking changed: %s", output)
		}
		if parts[len(parts)-1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Errorf("compat=%t: nonempty thought signature leaked to tool: %s", compat, output)
		}
	}
}

func TestGeminiClaudeStreamingEmptyBlockDoesNotRetargetSignature(t *testing.T) {
	for _, empty := range []string{`{"text":""}`, `{"text":"","thought":true}`} {
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first","args":{}}}]}}]}`,
			`{"candidates":[{"content":{"parts":[` + empty + `]}}]}`,
			`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second","args":{}}}]},"finishReason":"STOP"}]}`,
			`[DONE]`,
		}
		var param any
		var events []byte
		for _, chunk := range chunks {
			events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
		}
		content := claudeContentFromGeminiStream(t, events)
		output := replayGeminiClaudeContent(t, content)
		parts := gjson.GetBytes(output, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Errorf("empty block %s retargeted tool signature: %s; content=%s", empty, output, content)
		}
	}
}

func TestGeminiClaudeNonStreamSignatureOnlyThoughtPartRoundTrip(t *testing.T) {
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"},{"text":"","thought":true,"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`)
	response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
	output := replayGeminiClaudeContent(t, gjson.GetBytes(response, "content").Raw)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("empty thought part retargeted visible text signature: %s; response=%s", output, response)
	}
}

func TestGeminiClaudeNonStreamConsecutiveTrailingSignatures(t *testing.T) {
	second := "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}},{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"thoughtSignature":"` + second + `"}]},"finishReason":"STOP"}]}`)
	response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
	output := replayGeminiClaudeContent(t, gjson.GetBytes(response, "content").Raw)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != second || parts[1].Get("thoughtSignature").String() != capturedGeminiThinkingSignature {
		t.Fatalf("consecutive signatures must retain their function target: %s; response=%s", output, response)
	}
}

// claudeReplayRequest wraps assistant content in a request and answers every tool_use with a tool_result.
func claudeReplayRequest(content string) []byte {
	request := []byte(`{"messages":[{"role":"assistant","content":` + content + `},{"role":"user","content":[]}]}`)
	for _, block := range gjson.Parse(content).Array() {
		if block.Get("type").String() == "tool_use" {
			result := []byte(`{"type":"tool_result","tool_use_id":"","content":"ok"}`)
			result, _ = sjson.SetBytes(result, "tool_use_id", block.Get("id").String())
			request, _ = sjson.SetRawBytes(request, "messages.1.content.-1", result)
		}
	}
	if gjson.GetBytes(request, "messages.1.content.#").Int() == 0 {
		request, _ = sjson.SetRawBytes(request, "messages.1.content", []byte(`[{"type":"text","text":"continue"}]`))
	}
	return request
}

func replayGeminiClaudeContent(t *testing.T, content string) []byte {
	t.Helper()
	return ConvertClaudeRequestToGemini("gemini-4-carbon", claudeReplayRequest(content), false)
}

func TestGeminiClaudeStreamingCompletedToolSignatureRoundTrip(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first","args":{}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second","args":{}}}]},"finishReason":"STOP"}]}`,
		`[DONE]`,
	}
	var param any
	var events []byte
	for _, chunk := range chunks {
		events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte(chunk), &param), nil)...)
	}
	content := claudeContentFromGeminiStream(t, events)
	output := replayGeminiClaudeContent(t, content)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("signature after a closed tool block must bind backward: %s; content=%s", output, content)
	}
}

func claudeContentFromGeminiStream(t *testing.T, events []byte) string {
	t.Helper()
	content := []byte(`[]`)
	arguments := make(map[int]string)
	for _, line := range strings.Split(string(events), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		index := int(event.Get("index").Int())
		switch event.Get("type").String() {
		case "content_block_start":
			content, _ = sjson.SetRawBytes(content, "-1", []byte(event.Get("content_block").Raw))
		case "content_block_delta":
			switch event.Get("delta.type").String() {
			case "signature_delta":
				content, _ = sjson.SetBytes(content, strconv.Itoa(index)+".signature", event.Get("delta.signature").String())
			case "input_json_delta":
				arguments[index] += event.Get("delta.partial_json").String()
			case "text_delta":
				path := strconv.Itoa(index) + ".text"
				content, _ = sjson.SetBytes(content, path, gjson.GetBytes(content, path).String()+event.Get("delta.text").String())
			case "thinking_delta":
				path := strconv.Itoa(index) + ".thinking"
				content, _ = sjson.SetBytes(content, path, gjson.GetBytes(content, path).String()+event.Get("delta.thinking").String())
			}
		case "content_block_stop":
			if args := arguments[index]; args != "" {
				if !gjson.Valid(args) {
					t.Fatalf("invalid streamed tool arguments: %q", args)
				}
				content, _ = sjson.SetRawBytes(content, strconv.Itoa(index)+".input", []byte(args))
			}
		}
	}
	return string(content)
}

func TestGeminiClaudeTrailingTextSignatureRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		upstream := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"},{"thoughtSignature":"` + capturedGeminiThinkingSignature + `"},{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`)
		var content string
		if stream {
			var param any
			events := bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, upstream, &param), nil)
			events = append(events, bytes.Join(ConvertGeminiResponseToClaude(context.Background(), "gemini-4-carbon", nil, nil, []byte("[DONE]"), &param), nil)...)
			content = claudeContentFromGeminiStream(t, events)
		} else {
			response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
			content = gjson.GetBytes(response, "content").Raw
		}
		output := replayGeminiClaudeContent(t, content)
		parts := gjson.GetBytes(output, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != capturedGeminiThinkingSignature || parts[1].Get("thoughtSignature").String() != sigcompat.GeminiSkipThoughtSignatureValidator {
			t.Fatalf("stream=%t: text signature was rebound to a later tool: %s", stream, output)
		}
	}
}

func TestGeminiClaudeThoughtSignatureRoundTrip(t *testing.T) {
	for _, signature := range []string{capturedGeminiThinkingSignature} {
		t.Run(signature[:8], func(t *testing.T) {
			upstream := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"Paris"}}}]},"finishReason":"STOP"}]}`)
			upstream, _ = sjson.SetBytes(upstream, "candidates.0.content.parts.0.thoughtSignature", signature)
			response := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
			content := gjson.GetBytes(response, "content")
			toolID := content.Get("1.id").String()
			if toolID == "" {
				t.Fatalf("missing tool_use after signature carrier: %s", response)
			}
			request := []byte(`{"messages":[{"role":"assistant","content":[]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":"ok"}]}]}`)
			request, _ = sjson.SetRawBytes(request, "messages.0.content", []byte(content.Raw))
			request, _ = sjson.SetBytes(request, "messages.1.content.0.tool_use_id", toolID)
			for _, compat := range []bool{false, true} {
				output := convertClaudeRequestToGemini("gemini-4-carbon", request, false, compat)
				output = sigcompat.SanitizeGeminiRequestThoughtSignatures(output, "contents")
				parts := gjson.GetBytes(output, "contents.0.parts").Array()
				if len(parts) != 1 || !parts[0].Get("functionCall").Exists() {
					t.Fatalf("compat=%t: carrier must be attached to functionCall, got %s", compat, output)
				}
				if got := parts[0].Get("thoughtSignature").String(); got != signature {
					t.Fatalf("compat=%t: replayed signature = %q, want original %q", compat, got, signature)
				}
			}
		})
	}
}
