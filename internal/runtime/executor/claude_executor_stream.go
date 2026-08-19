package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
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
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, true)
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)

	body, err = applyResolvedClaudeRequestIdentity(body, opts)
	if err != nil {
		return nil, err
	}

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		body = rebuildMidSystemMessagesToTopLevel(body)
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	body, err = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, err = applyResolvedClaudeRequestIdentity(body, opts)
	if err != nil {
		return nil, err
	}
	body = ensureModelMaxTokens(body, baseModel)
	body = raiseMaxTokensForConversationCompaction(body, baseModel)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeSamplingForUpstream(body)

	if e.claudePromptCacheMode() == config.ClaudePromptCacheModeLegacy {
		body = e.applyLegacyClaudePromptCache(body)
	}

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
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
		return nil, err
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
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}
	reporter.SetTranslatedReasoningEffort(bodyForUpstream, to.String())

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	promptCacheAttempt, err := e.acquireClaudePromptCacheAttempt(ctx, promptCachePlan)
	if err != nil {
		return nil, err
	}
	var httpResp *http.Response
	httpResp, bodyForUpstream, cacheDiagnosticsState, err = e.executeClaudeMessagesHTTPRequest(
		ctx,
		auth,
		reporter,
		apiKey,
		url,
		true,
		extraBetas,
		bodyForUpstream,
		cacheDiagnosticsState,
		oauthToken || experimentalCCHSigningEnabled(e.cfg, auth),
		opts.Headers,
	)
	if err != nil {
		if promptCacheAttempt != nil {
			promptCacheAttempt.Fail()
		}
		return nil, err
	}
	if promptCacheAttempt != nil {
		promptCacheAttempt.MarkResponseStarted()
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		if promptCacheAttempt != nil {
			promptCacheAttempt.Fail()
		}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		stopPromptCacheHeartbeat := func() {}
		if promptCacheAttempt != nil {
			stopPromptCacheHeartbeat = promptCacheAttempt.StartResponseHeartbeat()
		}
		defer stopPromptCacheHeartbeat()
		defer func() {
			if promptCacheAttempt != nil {
				promptCacheAttempt.Fail()
			}
		}()
		defer func() {
			if ctx != nil && ctx.Err() != nil {
				reporter.PublishFailure(ctx, ctx.Err())
			}
		}()
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()
		streamUsage := &helps.ClaudeStreamUsageAccumulator{}
		streamReader := io.Reader(decodedBody)
		if promptCacheAttempt != nil {
			streamReader = &claudePromptCacheProgressReadCloser{
				ReadCloser: decodedBody,
				attempt:    promptCacheAttempt,
			}
		}

		// If the response target is Claude, directly forward complete SSE events without translation.
		if responseFormat == to {
			scanner := bufio.NewScanner(streamReader)
			scanner.Buffer(nil, 52_428_800) // 50MB
			var event bytes.Buffer
			flushEvent := func() bool {
				if event.Len() == 0 {
					return true
				}
				cloned := bytes.Clone(event.Bytes())
				event.Reset()
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
					return true
				case <-ctx.Done():
					return false
				}
			}
			for scanner.Scan() {
				line := scanner.Bytes()
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				streamUsage.Observe(line)
				line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
				line = e.restoreResponseModel(line, req.Model)
				reporter.ObserveSemanticResponse(to, line)
				event.Write(line)
				event.WriteByte('\n')
				if len(bytes.TrimSpace(line)) == 0 && !flushEvent() {
					return
				}
			}
			if !flushEvent() {
				return
			}
			if errScan := scanner.Err(); errScan != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
				case <-ctx.Done():
				}
				return
			}
			if errFinalize := finalizeClaudeStreamUsage(ctx, e, reporter, promptCacheAttempt, cacheDiagnosticsState, streamUsage); errFinalize != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errFinalize)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errFinalize}:
				case <-ctx.Done():
				}
			}
			return
		}

		// For other formats, use translation
		scanner := bufio.NewScanner(streamReader)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.Observe(line)
			line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
			line = e.restoreResponseModel(line, req.Model)
			reporter.ObserveSemanticResponse(to, line)
			chunks := sdktranslator.TranslateStream(
				ctx,
				to,
				responseFormat,
				req.Model,
				opts.OriginalRequest,
				bodyForTranslation,
				bytes.Clone(line),
				&param,
			)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		if errFinalize := finalizeClaudeStreamUsage(ctx, e, reporter, promptCacheAttempt, cacheDiagnosticsState, streamUsage); errFinalize != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errFinalize)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errFinalize}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func finalizeClaudeStreamUsage(
	ctx context.Context,
	executor *ClaudeExecutor,
	reporter *helps.UsageReporter,
	promptCacheAttempt *helps.ClaudePromptCacheAttempt,
	cacheDiagnosticsState *claudeCacheDiagnosticsState,
	streamUsage *helps.ClaudeStreamUsageAccumulator,
) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if streamUsage == nil || !streamUsage.CompletedSuccessfully() {
		errIncomplete := io.ErrUnexpectedEOF
		if reporter != nil {
			reporter.PublishFailure(ctx, errIncomplete)
		}
		return errIncomplete
	}
	detail := streamUsage.Detail()
	if reporter != nil {
		reporter.Publish(ctx, detail)
	}
	if promptCacheAttempt != nil {
		promptCacheAttempt.Complete()
	}
	if executor != nil {
		cacheMissReason, cacheMissedInputTokens := streamUsage.CacheMissReason()
		executor.recordClaudeCacheDiagnostics(
			ctx,
			cacheDiagnosticsState,
			streamUsage.MessageID(),
			cacheMissReason,
			cacheMissedInputTokens,
		)
	}
	return nil
}

func validateClaudeStreamingResponse(data []byte, headers http.Header) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 52_428_800)

	hasData := false
	hasMessageStart := false
	hasMessageDelta := false
	hasTerminalMarker := false

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			hasTerminalMarker = true
			continue
		}
		hasData = true
		if !gjson.ValidBytes(payload) {
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned malformed stream data", headers: headers}
		}

		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "error":
			message := strings.TrimSpace(root.Get("error.message").String())
			if message == "" {
				message = strings.TrimSpace(root.Get("error.type").String())
			}
			if message == "" {
				message = "unknown upstream error"
			}
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned error event: " + message, headers: headers}
		case "message_start":
			message := root.Get("message")
			if strings.TrimSpace(message.Get("id").String()) == "" || strings.TrimSpace(message.Get("model").String()) == "" {
				return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream message_start is missing id or model", headers: headers}
			}
			hasMessageStart = true
		case "message_delta":
			hasMessageDelta = true
		case "message_stop":
			hasTerminalMarker = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	if !hasData {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned empty stream response", headers: headers}
	}
	if !hasMessageStart {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response is missing message_start", headers: headers}
	}
	if !hasMessageDelta {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response ended before message completion", headers: headers}
	}
	if !hasTerminalMarker {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response is missing a terminal marker", headers: headers}
	}
	return nil
}
