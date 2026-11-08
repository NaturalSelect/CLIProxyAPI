package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiImagesMaxN         = 10
	geminiImagesMaskGuidance = "The next image is an edit mask. Transparent areas of the mask mark the region to edit. Keep all other areas of the original image unchanged."
)

type geminiImagesRequest struct {
	Model       string
	Prompt      string
	Images      []string
	Mask        string
	AspectRatio string
	ImageSize   string
	N           int
}

func isGeminiImagesModel(model string) bool {
	base := imagesModelBase(model)
	return strings.HasPrefix(base, "gemini-") && strings.Contains(base, "-image")
}

func geminiImagesAspectRatio(size string, aspectRatio string) string {
	return xaiImagesAspectRatioFromSize(size, xaiImagesAspectRatio(aspectRatio, ""))
}

func geminiImagesImageSize(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1k":
		return "1K"
	case "2k":
		return "2K"
	case "4k":
		return "4K"
	default:
		return ""
	}
}

func clampGeminiImagesN(n int64) int {
	if n < 1 {
		return 1
	}
	if n > geminiImagesMaxN {
		return geminiImagesMaxN
	}
	return int(n)
}

func validateGeminiImageDataURL(url string) error {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(strings.ToLower(url), "data:") || !strings.Contains(url, ";base64,") {
		return fmt.Errorf("images must be base64 data URLs for Gemini models; remote image URLs are not supported")
	}
	return nil
}

func validateGeminiImagesEditInputs(c *gin.Context, r geminiImagesRequest) bool {
	inputs := append([]string(nil), r.Images...)
	if r.Mask != "" {
		inputs = append(inputs, r.Mask)
	}
	for _, input := range inputs {
		if err := validateGeminiImageDataURL(input); err != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return false
		}
	}
	return true
}

func geminiImagesOptionsFromJSON(rawJSON []byte) (aspectRatio string, imageSize string, n int) {
	aspectRatio = geminiImagesAspectRatio(gjson.GetBytes(rawJSON, "size").String(), gjson.GetBytes(rawJSON, "aspect_ratio").String())
	imageSize = geminiImagesImageSize(firstNonEmpty(gjson.GetBytes(rawJSON, "image_size").String(), gjson.GetBytes(rawJSON, "resolution").String()))
	count := int64(1)
	if v := gjson.GetBytes(rawJSON, "n"); v.Exists() && v.Type == gjson.Number {
		count = v.Int()
	}
	return aspectRatio, imageSize, clampGeminiImagesN(count)
}

func geminiImagesMaskFromJSON(rawJSON []byte) string {
	if mask := gjson.GetBytes(rawJSON, "mask"); mask.Type == gjson.String {
		return strings.TrimSpace(mask.String())
	}
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "mask.image_url").String())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func buildGeminiImagesChatRequest(r geminiImagesRequest) []byte {
	out := []byte(`{"modalities":["image","text"],"messages":[{"role":"user","content":[]}]}`)
	out, _ = sjson.SetBytes(out, "model", r.Model)

	appendPart := func(part []byte) {
		out, _ = sjson.SetRawBytes(out, "messages.0.content.-1", part)
	}
	textPart := func(text string) []byte {
		part := []byte(`{"type":"text"}`)
		part, _ = sjson.SetBytes(part, "text", text)
		return part
	}
	imagePart := func(url string) []byte {
		part := []byte(`{"type":"image_url"}`)
		part, _ = sjson.SetBytes(part, "image_url.url", url)
		return part
	}

	appendPart(textPart(r.Prompt))
	for _, img := range r.Images {
		appendPart(imagePart(img))
	}
	if r.Mask != "" {
		appendPart(textPart(geminiImagesMaskGuidance))
		appendPart(imagePart(r.Mask))
	}

	if r.AspectRatio != "" {
		out, _ = sjson.SetBytes(out, "image_config.aspect_ratio", r.AspectRatio)
	}
	if r.ImageSize != "" {
		out, _ = sjson.SetBytes(out, "image_config.image_size", r.ImageSize)
	}
	return out
}

func parseGeminiImageDataURL(url string) (xaiImageResult, bool) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(strings.ToLower(url), "data:") {
		return xaiImageResult{}, false
	}
	header, b64, found := strings.Cut(url[len("data:"):], ",")
	if !found || strings.TrimSpace(b64) == "" {
		return xaiImageResult{}, false
	}
	mimeType, _, _ := strings.Cut(header, ";")
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	return xaiImageResult{B64JSON: strings.TrimSpace(b64), MimeType: mimeType}, true
}

func extractGeminiChatImages(chatResp []byte) (results []xaiImageResult, promptTokens int64, completionTokens int64, err error) {
	for _, choice := range gjson.GetBytes(chatResp, "choices").Array() {
		for _, img := range choice.Get("message.images").Array() {
			if result, ok := parseGeminiImageDataURL(img.Get("image_url.url").String()); ok {
				results = append(results, result)
			}
		}
	}
	if len(results) == 0 {
		text := strings.TrimSpace(gjson.GetBytes(chatResp, "choices.0.message.content").String())
		if len(text) > 200 {
			text = text[:200]
		}
		if text != "" {
			return nil, 0, 0, fmt.Errorf("upstream did not return image output: %s", text)
		}
		return nil, 0, 0, fmt.Errorf("upstream did not return image output")
	}
	promptTokens = gjson.GetBytes(chatResp, "usage.prompt_tokens").Int()
	completionTokens = gjson.GetBytes(chatResp, "usage.completion_tokens").Int()
	return results, promptTokens, completionTokens, nil
}

func buildGeminiImagesPayload(results []xaiImageResult, promptTokens int64, completionTokens int64) []byte {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	for _, img := range results {
		item := []byte(`{}`)
		item, _ = sjson.SetBytes(item, "b64_json", img.B64JSON)
		item, _ = sjson.SetBytes(item, "mime_type", img.MimeType)
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	out, _ = sjson.SetBytes(out, "usage.input_tokens", promptTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", completionTokens)
	out, _ = sjson.SetBytes(out, "usage.total_tokens", promptTokens+completionTokens)
	return out
}

func (h *OpenAIAPIHandler) executeGeminiImages(ctx context.Context, r geminiImagesRequest) ([]byte, http.Header, *interfaces.ErrorMessage) {
	chatReq := buildGeminiImagesChatRequest(r)

	var (
		all              []xaiImageResult
		promptTokens     int64
		completionTokens int64
		firstHeaders     http.Header
	)
	for i := 0; i < r.N; i++ {
		resp, headers, errMsg := h.ExecuteWithAuthManager(ctx, h.HandlerType(), r.Model, chatReq, "")
		if errMsg != nil {
			return nil, nil, errMsg
		}
		results, pt, ct, err := extractGeminiChatImages(resp)
		if err != nil {
			return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		}
		all = append(all, results...)
		promptTokens += pt
		completionTokens += ct
		if i == 0 {
			firstHeaders = headers
		}
	}
	return buildGeminiImagesPayload(all, promptTokens, completionTokens), firstHeaders, nil
}

func (h *OpenAIAPIHandler) handleGeminiImages(c *gin.Context, r geminiImagesRequest, responseFormat string, streamPrefix string, stream bool) {
	execute := func(ctx context.Context) ([]byte, http.Header, *interfaces.ErrorMessage) {
		return h.executeGeminiImages(ctx, r)
	}
	if stream {
		h.streamImagesWithExecute(c, responseFormat, streamPrefix, execute)
		return
	}
	h.collectImagesWithExecute(c, responseFormat, execute)
}
