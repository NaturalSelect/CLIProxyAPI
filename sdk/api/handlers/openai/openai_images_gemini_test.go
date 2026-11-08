package openai

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

const geminiTestImageModel = "gemini-2.5-flash-image"

const geminiTestChatImageResponse = `{"choices":[{"index":0,"message":{"role":"assistant","content":"","images":[{"type":"image_url","index":0,"image_url":{"url":"data:image/png;base64,QUJD"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}}`

type geminiImagesCaptureExecutor struct {
	mu       sync.Mutex
	response string
	payloads [][]byte
}

func (e *geminiImagesCaptureExecutor) Identifier() string { return "gemini" }

func (e *geminiImagesCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	response := e.response
	e.mu.Unlock()
	return coreexecutor.Response{Payload: []byte(response)}, nil
}

func (e *geminiImagesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "ExecuteStream not implemented"}
}

func (e *geminiImagesCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *geminiImagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *geminiImagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *geminiImagesCaptureExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	copy(out, e.payloads)
	return out
}

func newGeminiImagesTestHandler(t *testing.T, response string) (*OpenAIAPIHandler, *geminiImagesCaptureExecutor) {
	t.Helper()

	executor := &geminiImagesCaptureExecutor{response: response}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)

	authID := "gemini-images-test-auth"
	auth := &coreauth.Auth{ID: authID, Provider: "gemini", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(%s): %v", authID, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: geminiTestImageModel}})
	manager.RefreshSchedulerEntry(authID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	return NewOpenAIAPIHandler(base), executor
}

func TestIsGeminiImagesModel(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-flash-image",
		"gemini-3-pro-image-preview",
		"gemini-3.1-flash-image",
		"foo/gemini-2.5-flash-image",
		"Gemini-2.5-Flash-Image",
	} {
		if !isGeminiImagesModel(model) {
			t.Fatalf("expected %s to be a Gemini images model", model)
		}
	}
	for _, model := range []string{
		"gemini-2.5-flash",
		"gpt-image-2",
		"imagen-4.0-generate-001",
		"grok-imagine-image",
		"",
	} {
		if isGeminiImagesModel(model) {
			t.Fatalf("expected %s not to be a Gemini images model", model)
		}
	}
}

func TestBuildGeminiImagesChatRequest(t *testing.T) {
	req := buildGeminiImagesChatRequest(geminiImagesRequest{
		Model:       "foo/gemini-2.5-flash-image",
		Prompt:      "a red cube",
		Images:      []string{"data:image/png;base64,AA==", "data:image/jpeg;base64,BB=="},
		Mask:        "data:image/png;base64,CC==",
		AspectRatio: geminiImagesAspectRatio("1792x1024", ""),
		ImageSize:   geminiImagesImageSize("2k"),
		N:           3,
	})

	if got := gjson.GetBytes(req, "model").String(); got != "foo/gemini-2.5-flash-image" {
		t.Fatalf("model = %q", got)
	}
	if got := gjson.GetBytes(req, "modalities").Raw; got != `["image","text"]` {
		t.Fatalf("modalities = %s", got)
	}
	if got := gjson.GetBytes(req, "image_config.aspect_ratio").String(); got != "16:9" {
		t.Fatalf("aspect_ratio = %q, want 16:9", got)
	}
	if got := gjson.GetBytes(req, "image_config.image_size").String(); got != "2K" {
		t.Fatalf("image_size = %q, want 2K", got)
	}
	if gjson.GetBytes(req, "n").Exists() || gjson.GetBytes(req, "stream").Exists() {
		t.Fatalf("n and stream must not be sent upstream: %s", req)
	}

	parts := gjson.GetBytes(req, "messages.0.content").Array()
	wantTypes := []string{"text", "image_url", "image_url", "text", "image_url"}
	if len(parts) != len(wantTypes) {
		t.Fatalf("content parts = %d, want %d: %s", len(parts), len(wantTypes), req)
	}
	for i, want := range wantTypes {
		if got := parts[i].Get("type").String(); got != want {
			t.Fatalf("part %d type = %q, want %q", i, got, want)
		}
	}
	if got := parts[0].Get("text").String(); got != "a red cube" {
		t.Fatalf("prompt part = %q", got)
	}
	if got := parts[3].Get("text").String(); got != geminiImagesMaskGuidance {
		t.Fatalf("mask guidance part = %q", got)
	}
	if got := parts[4].Get("image_url.url").String(); got != "data:image/png;base64,CC==" {
		t.Fatalf("mask part url = %q", got)
	}
}

func TestBuildGeminiImagesChatRequestOmitsEmptyImageConfig(t *testing.T) {
	req := buildGeminiImagesChatRequest(geminiImagesRequest{Model: geminiTestImageModel, Prompt: "hi", N: 1})

	if gjson.GetBytes(req, "image_config").Exists() {
		t.Fatalf("image_config should be omitted: %s", req)
	}
	if got := len(gjson.GetBytes(req, "messages.0.content").Array()); got != 1 {
		t.Fatalf("content parts = %d, want 1", got)
	}
}

func TestGeminiImagesOptionHelpers(t *testing.T) {
	if got := geminiImagesAspectRatio("auto", ""); got != "" {
		t.Fatalf("aspect ratio for auto = %q, want empty", got)
	}
	if got := geminiImagesAspectRatio("", "portrait"); got != "9:16" {
		t.Fatalf("aspect ratio for portrait = %q, want 9:16", got)
	}
	if got := geminiImagesImageSize("8k"); got != "" {
		t.Fatalf("image size for 8k = %q, want empty", got)
	}
	for in, want := range map[int64]int{-5: 1, 0: 1, 4: 4, 99: geminiImagesMaxN} {
		if got := clampGeminiImagesN(in); got != want {
			t.Fatalf("clampGeminiImagesN(%d) = %d, want %d", in, got, want)
		}
	}
	if err := validateGeminiImageDataURL("https://example.com/a.png"); err == nil {
		t.Fatal("expected remote URL to be rejected")
	}
	if err := validateGeminiImageDataURL("data:image/png;base64,AA=="); err != nil {
		t.Fatalf("unexpected error for data URL: %v", err)
	}
}

func TestExtractGeminiChatImages(t *testing.T) {
	chat := []byte(`{"choices":[{"message":{"images":[
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},
		{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,REVG"}}
	]}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)

	results, pt, ct, err := extractGeminiChatImages(chat)
	if err != nil {
		t.Fatalf("extractGeminiChatImages error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].B64JSON != "QUJD" || results[0].MimeType != "image/png" {
		t.Fatalf("first result = %+v", results[0])
	}
	if results[1].B64JSON != "REVG" || results[1].MimeType != "image/jpeg" {
		t.Fatalf("second result = %+v", results[1])
	}
	if pt != 3 || ct != 5 {
		t.Fatalf("usage = %d/%d, want 3/5", pt, ct)
	}
}

func TestExtractGeminiChatImagesTextOnly(t *testing.T) {
	_, _, _, err := extractGeminiChatImages([]byte(`{"choices":[{"message":{"content":"I cannot draw that."}}]}`))
	if err == nil {
		t.Fatal("expected error for text-only response")
	}
	if !strings.Contains(err.Error(), "I cannot draw that.") {
		t.Fatalf("error should include the upstream text, got %q", err.Error())
	}
}

func TestBuildGeminiImagesPayloadFeedsURLResponseFormat(t *testing.T) {
	payload := buildGeminiImagesPayload([]xaiImageResult{{B64JSON: "QUJD", MimeType: "image/png"}}, 4, 6)

	if got := gjson.GetBytes(payload, "usage.total_tokens").Int(); got != 10 {
		t.Fatalf("total_tokens = %d, want 10", got)
	}
	out, err := buildImagesAPIResponseFromXAI(payload, "url")
	if err != nil {
		t.Fatalf("buildImagesAPIResponseFromXAI error: %v", err)
	}
	if got := gjson.GetBytes(out, "data.0.url").String(); got != "data:image/png;base64,QUJD" {
		t.Fatalf("url = %q", got)
	}
}

func TestGeminiImagesGenerationsMultipleImages(t *testing.T) {
	handler, executor := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"a red cube","n":2,"size":"1024x1024"}`),
		handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.Bytes()
	if got := len(gjson.GetBytes(body, "data").Array()); got != 2 {
		t.Fatalf("data items = %d, want 2: %s", got, body)
	}
	if got := gjson.GetBytes(body, "data.0.b64_json").String(); got != "QUJD" {
		t.Fatalf("b64_json = %q", got)
	}
	if got := gjson.GetBytes(body, "usage.input_tokens").Int(); got != 14 {
		t.Fatalf("input_tokens = %d, want 14", got)
	}
	if got := gjson.GetBytes(body, "usage.total_tokens").Int(); got != 36 {
		t.Fatalf("total_tokens = %d, want 36", got)
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("executor calls = %d, want 2", len(payloads))
	}
	if got := gjson.GetBytes(payloads[0], "modalities").Raw; got != `["image","text"]` {
		t.Fatalf("modalities = %s", got)
	}
	if gjson.GetBytes(payloads[0], "n").Exists() {
		t.Fatalf("n must not be sent upstream: %s", payloads[0])
	}
	if got := gjson.GetBytes(payloads[0], "image_config.aspect_ratio").String(); got != "1:1" {
		t.Fatalf("aspect_ratio = %q, want 1:1", got)
	}
}

func TestGeminiImagesGenerationsURLResponseFormat(t *testing.T) {
	handler, _ := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"a red cube","response_format":"url"}`),
		handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("url = %q", got)
	}
}

func TestGeminiImagesGenerationsStream(t *testing.T) {
	handler, _ := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"a red cube","stream":true}`),
		handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event: image_generation.completed") {
		t.Fatalf("missing completed event: %s", body)
	}
	if !strings.Contains(body, `"b64_json":"QUJD"`) {
		t.Fatalf("missing image payload: %s", body)
	}
}

func TestGeminiImagesEditsMultipartWithMask(t *testing.T) {
	handler, executor := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("model", geminiTestImageModel)
	_ = writer.WriteField("prompt", "make it blue")
	for _, field := range []string{"image", "mask"} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", multipart.FileContentDisposition(field, field+".png"))
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("CreatePart(%s): %v", field, err)
		}
		_, _ = part.Write([]byte(field + "-bytes"))
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &buf, handler.ImagesEdits)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "QUJD" {
		t.Fatalf("b64_json = %q", got)
	}

	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(payloads))
	}
	parts := gjson.GetBytes(payloads[0], "messages.0.content").Array()
	wantTypes := []string{"text", "image_url", "text", "image_url"}
	if len(parts) != len(wantTypes) {
		t.Fatalf("content parts = %d, want %d: %s", len(parts), len(wantTypes), payloads[0])
	}
	for i, want := range wantTypes {
		if got := parts[i].Get("type").String(); got != want {
			t.Fatalf("part %d type = %q, want %q", i, got, want)
		}
	}
	if got := parts[1].Get("image_url.url").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("image part url = %q", got)
	}
}

func TestGeminiImagesEditsJSONRejectsRemoteImageURL(t *testing.T) {
	handler, executor := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"make it blue","images":[{"image_url":"https://example.com/a.png"}]}`),
		handler.ImagesEdits)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error type = %q", got)
	}
	if got := len(executor.Payloads()); got != 0 {
		t.Fatalf("executor calls = %d, want 0", got)
	}
}

func TestGeminiImagesEditsJSONAcceptsDataURLWithMask(t *testing.T) {
	handler, executor := newGeminiImagesTestHandler(t, geminiTestChatImageResponse)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"make it blue","images":[{"image_url":"data:image/png;base64,AA=="}],"mask":{"image_url":"data:image/png;base64,BB=="}}`),
		handler.ImagesEdits)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(payloads))
	}
	if got := len(gjson.GetBytes(payloads[0], "messages.0.content").Array()); got != 4 {
		t.Fatalf("content parts = %d, want 4: %s", got, payloads[0])
	}
}

func TestGeminiImagesGenerationsTextOnlyResponseReturnsBadGateway(t *testing.T) {
	handler, _ := newGeminiImagesTestHandler(t, `{"choices":[{"message":{"role":"assistant","content":"I cannot draw that."}}]}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"a red cube"}`),
		handler.ImagesGenerations)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", resp.Code, resp.Body.String())
	}
}
