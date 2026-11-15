package responses

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

const testResponsesSecondGeminiThoughtSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsE"

func geminiResponsesOutputItems(t *testing.T, parts []string, stream bool) []ojson.Result {
	t.Helper()
	body := `{"candidates":[{"content":{"parts":[` + strings.Join(parts, ",") + `]},"finishReason":"STOP"}],"responseId":"probe"}`
	if !stream {
		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(body), nil)
		return ojson.GetBytes(out, "output").Array()
	}
	var param any
	var items []ojson.Result
	for _, line := range []string{body, "[DONE]"} {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			for _, raw := range strings.Split(string(chunk), "\n") {
				if !strings.HasPrefix(raw, "data: ") {
					continue
				}
				event := ojson.Parse(strings.TrimPrefix(raw, "data: "))
				if event.Get("type").String() == "response.completed" {
					items = event.Get("response.output").Array()
				}
			}
		}
	}
	return items
}

// geminiResponsesCarrierSignatures returns every signature the converter kept,
// either as a visible reasoning carrier or, for signatures that trail visible
// text, in the replay cache that backs the message ID.
func geminiResponsesCarrierSignatures(t *testing.T, items []ojson.Result) []string {
	t.Helper()
	var signatures []string
	for _, item := range items {
		if item.Get("type").String() == "message" {
			cached, _ := cache.GetAntigravityReasoningReplayItems("gemini-3.6-flash-high", "gemini-responses-text:"+item.Get("id").String())
			for _, raw := range cached {
				signatures = append(signatures, ojson.GetBytes(raw, "thoughtSignature").String())
			}
			continue
		}
		if item.Get("type").String() != "reasoning" {
			continue
		}
		signature, _, _, marked, ok := decodeGeminiResponsesCarrier(item.Get("encrypted_content").String())
		if !marked || !ok {
			t.Fatalf("reasoning item is not a valid carrier: %s", item.Raw)
		}
		signatures = append(signatures, signature)
	}
	return signatures
}

func geminiResponsesVisibleTypes(items []ojson.Result) []string {
	var types []string
	for _, item := range items {
		if item.Get("type").String() != "reasoning" {
			types = append(types, item.Get("type").String())
		}
	}
	return types
}

func TestGeminiResponsesUnsupportedPayloadDoesNotRetargetSignature(t *testing.T) {
	// The replay cache must be local to this test so cached text signatures from
	// one case cannot leak into the next one that reuses the same message ID.
	previousHome := homekv.Current()
	homekv.SetCurrent(nil)
	t.Cleanup(func() {
		homekv.SetCurrent(previousHome)
		cache.ClearAntigravityReasoningReplayCache()
	})
	sig := testResponsesGeminiThoughtSignature
	second := testResponsesSecondGeminiThoughtSignature
	function := `{"functionCall":{"name":"lookup","args":{}}}`
	text := `{"text":"answer"}`
	file := `{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"}}`
	code := `{"executableCode":{"language":"PYTHON","code":"1"}}`
	codeResult := `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}}`
	emptyThought := `{"text":"","thought":true}`
	tail := func(signature string) string { return `{"text":"","thoughtSignature":"` + signature + `"}` }
	detached := func(signature string) string { return `{"thoughtSignature":"` + signature + `"}` }
	signedUnsupported := func(payload, signature string) string {
		return strings.TrimSuffix(payload, "}") + `,"thoughtSignature":"` + signature + `"}`
	}

	for _, tt := range []struct {
		name        string
		parts       []string
		wantVisible []string
		wantSigs    []string
	}{
		{"function then tail", []string{function, tail(sig)}, []string{"function_call"}, []string{sig}},
		{"text then tail", []string{text, tail(sig)}, []string{"message"}, []string{sig}},
		{"function, file, tail", []string{function, file, tail(sig)}, []string{"function_call"}, nil},
		{"function, code result, detached", []string{function, codeResult, detached(sig)}, []string{"function_call"}, nil},
		{"function, code, tail", []string{function, code, tail(sig)}, []string{"function_call"}, nil},
		{"text, file, tail", []string{text, file, tail(sig)}, []string{"message"}, nil},
		{"text, code result, detached", []string{text, codeResult, detached(sig)}, []string{"message"}, nil},
		{"function, file, empty thought, tail", []string{function, file, emptyThought, tail(sig)}, []string{"function_call"}, nil},
		{"function, signed file", []string{function, signedUnsupported(file, sig)}, []string{"function_call"}, nil},
		{"text, signed code", []string{text, signedUnsupported(code, sig)}, []string{"message"}, nil},
		{"signed text, file, tail keeps own signature", []string{`{"text":"answer","thoughtSignature":"` + sig + `"}`, file, tail(second)}, []string{"message"}, []string{sig}},
		{"function, file, signed function", []string{function, file, `{"functionCall":{"name":"other","args":{}},"thoughtSignature":"` + sig + `"}`}, []string{"function_call", "function_call"}, []string{sig}},
	} {
		for _, stream := range []bool{false, true} {
			name := tt.name + "/nonstream"
			if stream {
				name = tt.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				cache.ClearAntigravityReasoningReplayCache()
				items := geminiResponsesOutputItems(t, tt.parts, stream)
				if got := geminiResponsesVisibleTypes(items); !reflect.DeepEqual(got, tt.wantVisible) {
					t.Fatalf("visible items = %v, want %v; output=%v", got, tt.wantVisible, items)
				}
				if got := geminiResponsesCarrierSignatures(t, items); !reflect.DeepEqual(got, tt.wantSigs) {
					t.Fatalf("carrier signatures = %v, want %v; output=%v", got, tt.wantSigs, items)
				}
			})
		}
	}
}
