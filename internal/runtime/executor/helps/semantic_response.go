package helps

import (
	"bytes"
	"strings"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// HasSemanticResponseContent reports whether an upstream response payload contains
// the first non-empty model output that should complete TTFT measurement.
func HasSemanticResponseContent(format sdktranslator.Format, payload []byte) bool {
	for _, jsonPayload := range semanticJSONPayloads(payload) {
		if hasSemanticJSONContent(format, jsonPayload) {
			return true
		}
	}
	return false
}

func semanticJSONPayloads(payload []byte) [][]byte {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 || bytes.Equal(trimmedPayload, []byte("[DONE]")) {
		return nil
	}
	if ojson.ValidBytes(trimmedPayload) {
		return [][]byte{trimmedPayload}
	}

	lines := bytes.Split(trimmedPayload, []byte("\n"))
	payloads := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmedLine := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
			continue
		}
		jsonPayload := bytes.TrimSpace(trimmedLine[len("data:"):])
		if len(jsonPayload) == 0 || bytes.Equal(jsonPayload, []byte("[DONE]")) || !ojson.ValidBytes(jsonPayload) {
			continue
		}
		payloads = append(payloads, jsonPayload)
	}
	return payloads
}

func hasSemanticJSONContent(format sdktranslator.Format, payload []byte) bool {
	switch format {
	case sdktranslator.FormatClaude:
		return hasClaudeSemanticContent(payload)
	case sdktranslator.FormatGemini, sdktranslator.FormatAntigravity:
		return hasGeminiSemanticContent(payload)
	case sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse:
		return hasOpenAIResponsesSemanticContent(payload)
	case sdktranslator.FormatInteractions:
		return hasInteractionsSemanticContent(payload)
	case sdktranslator.FormatOpenAI:
		return hasOpenAISemanticContent(payload) || hasOpenAIResponsesSemanticContent(payload)
	default:
		return hasOpenAISemanticContent(payload) ||
			hasOpenAIResponsesSemanticContent(payload) ||
			hasClaudeSemanticContent(payload) ||
			hasGeminiSemanticContent(payload)
	}
}

func hasOpenAISemanticContent(payload []byte) bool {
	root := ojson.ParseBytes(payload)
	semantic := false
	root.Get("choices").ForEach(func(_, choice ojson.Result) bool {
		delta := choice.Get("delta")
		message := choice.Get("message")
		if hasOpenAIMessageContent(delta.Get("content")) ||
			hasTrimmedResult(delta.Get("reasoning_content")) ||
			hasTrimmedResult(delta.Get("reasoning")) ||
			hasOpenAIMessageContent(message.Get("content")) ||
			hasTrimmedResult(message.Get("reasoning_content")) ||
			hasTrimmedResult(message.Get("reasoning")) ||
			hasOpenAIToolCalls(delta.Get("tool_calls")) ||
			hasOpenAIToolCalls(message.Get("tool_calls")) ||
			hasOpenAIMedia(delta) ||
			hasOpenAIMedia(message) {
			semantic = true
			return false
		}
		return true
	})
	if semantic {
		return true
	}
	return hasTrimmedResult(root.Get("output_text")) ||
		hasOpenAIOutputItems(root.Get("output")) ||
		hasOpenAIImageData(root.Get("data")) ||
		hasTrimmedResult(root.Get("url")) ||
		hasTrimmedResult(root.Get("image.url")) ||
		hasTrimmedResult(root.Get("video.url"))
}

func hasOpenAIResponsesSemanticContent(payload []byte) bool {
	root := ojson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	switch eventType {
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return hasTrimmedResult(root.Get("delta"))
	case "response.output_text.done":
		return hasTrimmedResult(root.Get("text"))
	case "response.reasoning_summary_text.done":
		return hasTrimmedResult(root.Get("text"))
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return hasTrimmedResult(root.Get("part.text"))
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return hasRawStringResult(root.Get("delta"))
	case "response.function_call_arguments.done":
		return hasRawResult(root.Get("arguments"))
	case "response.custom_tool_call_input.done":
		return hasRawResult(root.Get("input"))
	case "response.image_generation_call.partial_image":
		return hasRawResult(root.Get("partial_image_b64"))
	case "response.audio.delta", "response.output_audio.delta":
		return hasRawStringResult(root.Get("delta"))
	case "response.output_item.added", "response.output_item.done":
		return hasOpenAIOutputItem(root.Get("item"))
	case "response.content_part.added", "response.content_part.done":
		return hasOpenAIContentPart(root.Get("part"))
	case "response.completed", "response.done", "response.incomplete":
		return hasOpenAIOutputItems(root.Get("response.output"))
	case "", "response.created", "response.in_progress", "response.queued", "error":
		return hasOpenAIOutputItems(root.Get("output"))
	default:
		return hasOpenAIOutputItems(root.Get("response.output"))
	}
}

func hasClaudeSemanticContent(payload []byte) bool {
	root := ojson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	switch eventType {
	case "content_block_delta":
		delta := root.Get("delta")
		return hasTrimmedResult(delta.Get("text")) ||
			hasTrimmedResult(delta.Get("thinking")) ||
			hasRawResult(delta.Get("partial_json"))
	case "content_block_start":
		return hasClaudeContentBlock(root.Get("content_block"))
	case "message_start":
		return hasClaudeContentArray(root.Get("message.content"))
	case "message_delta", "message_stop", "content_block_stop", "ping", "error":
		return false
	default:
		return hasClaudeContentArray(root.Get("content"))
	}
}

func hasGeminiSemanticContent(payload []byte) bool {
	root := ojson.ParseBytes(payload)
	if hasGeminiCandidates(root.Get("candidates")) || hasGeminiCandidates(root.Get("response.candidates")) {
		return true
	}
	return hasGeminiParts(root.Get("parts")) || hasGeminiParts(root.Get("content.parts"))
}

func hasInteractionsSemanticContent(payload []byte) bool {
	root := ojson.ParseBytes(payload)
	switch strings.TrimSpace(root.Get("event_type").String()) {
	case "step.delta":
		delta := root.Get("delta")
		return hasTrimmedResult(delta.Get("text")) ||
			hasTrimmedResult(delta.Get("content.text")) ||
			hasRawResult(delta.Get("arguments")) ||
			hasRawResult(delta.Get("content.data"))
	case "step.start", "step.stop":
		step := root.Get("step")
		return hasTrimmedResult(step.Get("name")) ||
			hasRawResult(step.Get("arguments")) ||
			hasInteractionsStepContent(step)
	case "interaction.completed", "finish":
		steps := root.Get("interaction.steps")
		if !steps.Exists() {
			steps = root.Get("steps")
		}
		semantic := false
		steps.ForEach(func(_, step ojson.Result) bool {
			if hasInteractionsStepContent(step) {
				semantic = true
				return false
			}
			return true
		})
		return semantic
	default:
		steps := root.Get("steps")
		semantic := false
		steps.ForEach(func(_, step ojson.Result) bool {
			if hasInteractionsStepContent(step) {
				semantic = true
				return false
			}
			return true
		})
		return semantic
	}
}

func hasInteractionsStepContent(step ojson.Result) bool {
	if hasTrimmedResult(step.Get("name")) || hasRawResult(step.Get("arguments")) {
		return true
	}
	content := step.Get("content")
	if content.Type == ojson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	semantic := false
	content.ForEach(func(_, part ojson.Result) bool {
		if hasTrimmedResult(part.Get("text")) ||
			hasRawResult(part.Get("data")) ||
			hasTrimmedResult(part.Get("uri")) ||
			hasTrimmedResult(part.Get("url")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIToolCalls(toolCalls ojson.Result) bool {
	semantic := false
	toolCalls.ForEach(func(_, call ojson.Result) bool {
		if hasTrimmedResult(call.Get("function.name")) ||
			hasRawResult(call.Get("function.arguments")) ||
			hasTrimmedResult(call.Get("name")) ||
			hasRawResult(call.Get("arguments")) ||
			hasRawResult(call.Get("input")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIMedia(node ojson.Result) bool {
	return hasRawResult(node.Get("audio.data")) ||
		hasTrimmedResult(node.Get("audio.id")) ||
		hasRawResult(node.Get("image_url.url")) ||
		hasRawResult(node.Get("b64_json")) ||
		hasRawResult(node.Get("images.0.image_url.url"))
}

func hasOpenAIMessageContent(content ojson.Result) bool {
	if !content.Exists() {
		return false
	}
	if content.Type == ojson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	return hasOpenAIContentParts(content)
}

func hasOpenAIImageData(data ojson.Result) bool {
	semantic := false
	data.ForEach(func(_, item ojson.Result) bool {
		if hasRawResult(item.Get("b64_json")) ||
			hasTrimmedResult(item.Get("url")) ||
			hasRawResult(item.Get("image_url.url")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIOutputItems(items ojson.Result) bool {
	semantic := false
	items.ForEach(func(_, item ojson.Result) bool {
		if hasOpenAIOutputItem(item) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIOutputItem(item ojson.Result) bool {
	itemType := strings.TrimSpace(item.Get("type").String())
	switch itemType {
	case "function_call", "custom_tool_call", "computer_call", "web_search_call", "file_search_call":
		return hasTrimmedResult(item.Get("name")) || hasRawResult(item.Get("arguments")) || hasRawResult(item.Get("input"))
	case "image_generation_call":
		return hasRawResult(item.Get("result")) || hasRawResult(item.Get("partial_image_b64"))
	case "audio":
		return hasRawResult(item.Get("data")) || hasTrimmedResult(item.Get("id"))
	case "reasoning":
		return hasOpenAIReasoningSummary(item.Get("summary"))
	default:
		return hasOpenAIContentParts(item.Get("content")) || hasTrimmedResult(item.Get("text"))
	}
}

func hasOpenAIReasoningSummary(summary ojson.Result) bool {
	semantic := false
	summary.ForEach(func(_, part ojson.Result) bool {
		if hasTrimmedResult(part.Get("text")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIContentParts(parts ojson.Result) bool {
	semantic := false
	parts.ForEach(func(_, part ojson.Result) bool {
		if hasOpenAIContentPart(part) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasOpenAIContentPart(part ojson.Result) bool {
	return hasTrimmedResult(part.Get("text")) ||
		hasTrimmedResult(part.Get("transcript")) ||
		hasRawResult(part.Get("image_url.url")) ||
		hasRawResult(part.Get("b64_json")) ||
		hasRawResult(part.Get("audio.data"))
}

func hasClaudeContentArray(content ojson.Result) bool {
	semantic := false
	content.ForEach(func(_, block ojson.Result) bool {
		if hasClaudeContentBlock(block) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasClaudeContentBlock(block ojson.Result) bool {
	blockType := strings.TrimSpace(block.Get("type").String())
	switch blockType {
	case "text":
		return hasTrimmedResult(block.Get("text"))
	case "thinking", "redacted_thinking":
		return hasTrimmedResult(block.Get("thinking")) || hasRawResult(block.Get("data"))
	case "tool_use", "server_tool_use":
		return hasTrimmedResult(block.Get("name")) || hasRawResult(block.Get("input"))
	case "image":
		return hasRawResult(block.Get("source.data")) || hasTrimmedResult(block.Get("source.url"))
	default:
		return false
	}
}

func hasGeminiCandidates(candidates ojson.Result) bool {
	semantic := false
	candidates.ForEach(func(_, candidate ojson.Result) bool {
		if hasGeminiParts(candidate.Get("content.parts")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasGeminiParts(parts ojson.Result) bool {
	semantic := false
	parts.ForEach(func(_, part ojson.Result) bool {
		if hasTrimmedResult(part.Get("text")) ||
			hasTrimmedResult(part.Get("functionCall.name")) ||
			hasRawResult(part.Get("functionCall.args")) ||
			hasRawResult(part.Get("inlineData.data")) ||
			hasTrimmedResult(part.Get("fileData.fileUri")) ||
			hasTrimmedResult(part.Get("executableCode.code")) ||
			hasTrimmedResult(part.Get("codeExecutionResult.output")) {
			semantic = true
			return false
		}
		return true
	})
	return semantic
}

func hasTrimmedResult(result ojson.Result) bool {
	return result.Exists() && result.Type == ojson.String && strings.TrimSpace(result.String()) != ""
}

func hasRawResult(result ojson.Result) bool {
	if !result.Exists() {
		return false
	}
	if result.Type == ojson.String {
		return result.String() != ""
	}
	if result.Type != ojson.JSON {
		return false
	}
	nonEmpty := false
	result.ForEach(func(_, _ ojson.Result) bool {
		nonEmpty = true
		return false
	})
	return nonEmpty
}

func hasRawStringResult(result ojson.Result) bool {
	return result.Exists() && result.Type == ojson.String && result.String() != ""
}
