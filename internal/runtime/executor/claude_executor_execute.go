package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := e.upstreamModel(baseModel)

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	// Use an upstream stream whenever the downstream response needs translation
	// from Claude events. Native Claude responses use the JSON response path.
	upstreamStream := responseFormat != to
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, upstreamStream)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, upstreamStream)
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)

	body, err = applyResolvedClaudeRequestIdentity(body, opts)
	if err != nil {
		return resp, err
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		body = rebuildMidSystemMessagesToTopLevel(body)
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	body, err = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, err = applyResolvedClaudeRequestIdentity(body, opts)
	if err != nil {
		return resp, err
	}
	body = ensureModelMaxTokens(body, baseModel)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeSamplingForUpstream(body)

	if e.claudePromptCacheMode() == config.ClaudePromptCacheModeLegacy {
		body = e.applyLegacyClaudePromptCache(body)
	}
	// Payload rules and other request processing may rewrite stream. Keep the
	// upstream body, transport headers, and response parser on one authority.
	body = helps.SetBoolIfDifferent(body, "stream", upstreamStream)

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	extraBetas = appendClaudeFastModeBeta(body, extraBetas)
	bodyForTranslation := body
	bodyForUpstream := body
	oauthToken := isClaudeOAuthToken(apiKey)
	var oauthToolNamesReverseMap map[string]string
	if oauthToken {
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	bodyForUpstream = sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, bodyForUpstream, baseModel)
	bodyForUpstream, err = applyResolvedClaudeRequestIdentity(bodyForUpstream, opts)
	if err != nil {
		return resp, err
	}
	var promptCachePlan *helps.ClaudePromptCachePlan
	bodyForUpstream, promptCachePlan = e.planAdaptiveClaudePromptCache(
		ctx,
		auth,
		apiKey,
		baseURL,
		baseModel,
		bodyForUpstream,
	)
	var cacheDiagnosticsState *claudeCacheDiagnosticsState
	bodyForUpstream, extraBetas, cacheDiagnosticsState = e.applyClaudeCacheDiagnostics(
		auth,
		apiKey,
		baseURL,
		baseModel,
		bodyForUpstream,
		extraBetas,
	)
	// Enable cch signing by default for OAuth tokens (not just experimental flag).
	// Claude Code always computes cch; missing or invalid cch is a detectable fingerprint.
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}
	reporter.SetTranslatedReasoningEffort(bodyForUpstream, to.String())

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	promptCacheAttempt, err := e.acquireClaudePromptCacheAttempt(ctx, promptCachePlan)
	if err != nil {
		return resp, err
	}
	if promptCacheAttempt != nil {
		defer promptCacheAttempt.Fail()
	}
	var httpResp *http.Response
	httpResp, bodyForUpstream, cacheDiagnosticsState, err = e.executeClaudeMessagesHTTPRequest(
		ctx,
		auth,
		reporter,
		apiKey,
		url,
		upstreamStream,
		extraBetas,
		bodyForUpstream,
		cacheDiagnosticsState,
		oauthToken || experimentalCCHSigningEnabled(e.cfg, auth),
		opts.Headers,
	)
	if err != nil {
		return resp, err
	}
	if promptCacheAttempt != nil {
		promptCacheAttempt.MarkResponseStarted()
	}
	stopPromptCacheHeartbeat := func() {}
	if promptCacheAttempt != nil {
		stopPromptCacheHeartbeat = promptCacheAttempt.StartResponseHeartbeat()
	}
	defer stopPromptCacheHeartbeat()
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	responseBodyReader := io.Reader(decodedBody)
	if promptCacheAttempt != nil {
		responseBodyReader = &claudePromptCacheProgressReadCloser{
			ReadCloser: decodedBody,
			attempt:    promptCacheAttempt,
		}
	}
	data, err := io.ReadAll(responseBodyReader)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if upstreamStream {
		if errValidate := validateClaudeStreamingResponse(data, httpResp.Header.Clone()); errValidate != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, errValidate
		}
		streamUsage := &helps.ClaudeStreamUsageAccumulator{}
		lines := bytes.Split(data, []byte("\n"))
		for i, line := range lines {
			streamUsage.Observe(line)
			lines[i] = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
		}
		data = bytes.Join(lines, []byte("\n"))
		detail := streamUsage.Detail()
		reporter.Publish(ctx, detail)
		cacheMissReason, cacheMissedInputTokens := streamUsage.CacheMissReason()
		e.recordClaudeCacheDiagnostics(
			ctx,
			cacheDiagnosticsState,
			streamUsage.MessageID(),
			cacheMissReason,
			cacheMissedInputTokens,
		)
		if promptCacheAttempt != nil && streamUsage.CompletedSuccessfully() {
			promptCacheAttempt.Complete(detail.CacheReadTokens, detail.CacheCreationTokens)
		}
	} else {
		detail := helps.ParseClaudeUsage(data)
		reporter.Publish(ctx, detail)
		e.recordClaudeCacheDiagnostics(
			ctx,
			cacheDiagnosticsState,
			gjson.GetBytes(data, "id").String(),
			gjson.GetBytes(data, "diagnostics.cache_miss_reason.type").String(),
			gjson.GetBytes(data, "diagnostics.cache_miss_reason.cache_missed_input_tokens").Int(),
		)
		if promptCacheAttempt != nil {
			promptCacheAttempt.Complete(detail.CacheReadTokens, detail.CacheCreationTokens)
		}
		data = restoreClaudeOAuthToolNamesFromResponse(data, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
	}
	data = e.restoreResponseModel(data, req.Model)
	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		to,
		responseFormat,
		req.Model,
		opts.OriginalRequest,
		bodyForTranslation,
		data,
		&param,
	)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}
