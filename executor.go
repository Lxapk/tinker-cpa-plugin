package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// This file implements CPA's ProviderExecutor capability for Tinker.
//
// CPA drives it through:
//
//	executor.identifier     -> stable executor id
//	executor.execute        -> non-streaming completion
//	executor.execute_stream -> streaming completion
//	executor.count_tokens   -> token counting
//	executor.http_request   -> raw upstream bridging
//
// Tinker is not an OpenAI backend: a request becomes a generation job, so the
// executor's job is to
//
//	1. recover the credential from StorageJSON (refreshing it if needed)
//	2. flatten the chat messages into one prompt
//	3. create the generation, poll it, read the content back
//	4. render the result as an OpenAI response (or SSE frames for stream=true)

// executorIdentifier answers executor.identifier.
func executorIdentifier() ([]byte, error) {
	return okEnvelope(identifierResponse{Identifier: tinkerProviderKey})
}

// executorRequest mirrors pluginhost.rpcExecutorRequest: ExecutorRequest plus
// the correlation fields the host adds.
type executorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// decodeExecutorRequest parses an executor call and picks the body to forward.
func decodeExecutorRequest(request []byte) (executorRequest, []byte, *tinkerCredentials, error) {
	var req executorRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return req, nil, nil, errUnmarshal
		}
	}
	body := req.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}
	if len(body) == 0 {
		return req, nil, nil, errors.New("执行请求缺少请求体")
	}
	creds, errParse := parseCredentials(req.StorageJSON)
	if errParse != nil {
		return req, body, nil, errParse
	}
	return req, body, creds, nil
}

/* ---------- executor.execute ---------- */

// executorExecute answers executor.execute (non-streaming).
func executorExecute(request []byte) ([]byte, error) {
	req, body, creds, errDecode := decodeExecutorRequest(request)
	if errDecode != nil {
		return errorEnvelope("invalid_executor_request", errDecode.Error(), 400), nil
	}

	chatReq, errParse := parseChatRequest(body)
	if errParse != nil {
		return errorEnvelope("invalid_request", errParse.Error(), 400), nil
	}
	modelName := firstNonEmpty(req.Model, chatReq.Model)
	ref := resolveTool(modelName)
	prompt := limitPrompt(buildPrompt(chatReq.Messages), ref)
	if prompt == "" {
		return errorEnvelope("invalid_request", "messages 中没有可用的文本内容", 400), nil
	}

	started := time.Now()
	result, used, errRun := runWithAccount(request, creds, req.AuthID, ref, prompt)
	if errRun != nil {
		state.logs.add(callEntry{
			Account:  creds.label(),
			Model:    ref.OpenAI,
			Tool:     ref.ToolID,
			Status:   "error",
			Duration: time.Since(started).Milliseconds(),
			Detail:   errRun.Error(),
		})
		return errorEnvelope("upstream_error", errRun.Error(), httpStatusOf(errRun)), nil
	}

	text := extractText(result.Artifact)
	if text == "" && ref.OutputType != "text" {
		// Image and video tools return a link rather than prose; surfacing the
		// URL is more useful to a chat client than an empty message.
		text = extractMediaURL(result.Artifact)
	}
	if text == "" {
		return errorEnvelope("empty_result", "生成完成但没有返回内容", 502), nil
	}

	state.logs.add(callEntry{
		Account:  used.label(),
		Model:    ref.OpenAI,
		Tool:     ref.ToolID,
		Status:   "ok",
		Duration: time.Since(started).Milliseconds(),
	})

	payload := chatCompletionBody(newCompletionID(), ref.OpenAI, text)
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: payload,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

/* ---------- executor.execute_stream ---------- */

// executorExecuteStream answers executor.execute_stream.
//
// Tinker has no incremental output: a generation either is or is not finished.
// The frames are therefore emitted once the result exists, chunked by rune count
// so a client that renders progressively still shows text appearing. Sending a
// bare JSON body instead would be silently dropped — OpenAI streaming clients
// only parse text/event-stream.
func executorExecuteStream(request []byte) ([]byte, error) {
	req, body, creds, errDecode := decodeExecutorRequest(request)
	if errDecode != nil {
		return errorEnvelope("invalid_executor_request", errDecode.Error(), 400), nil
	}

	chatReq, errParse := parseChatRequest(body)
	if errParse != nil {
		return errorEnvelope("invalid_request", errParse.Error(), 400), nil
	}
	modelName := firstNonEmpty(req.Model, chatReq.Model)
	ref := resolveTool(modelName)
	prompt := limitPrompt(buildPrompt(chatReq.Messages), ref)
	if prompt == "" {
		return errorEnvelope("invalid_request", "messages 中没有可用的文本内容", 400), nil
	}

	// Run the generation before returning so the first frame already carries
	// content: a client that times out waiting for its first byte gets nothing
	// from an empty preface.
	started := time.Now()
	result, used, errRun := runWithAccount(request, creds, req.AuthID, ref, prompt)
	if errRun != nil {
		return errorEnvelope("upstream_error", errRun.Error(), httpStatusOf(errRun)), nil
	}

	text := extractText(result.Artifact)
	if text == "" && ref.OutputType != "text" {
		text = extractMediaURL(result.Artifact)
	}
	if text == "" {
		return errorEnvelope("empty_result", "生成完成但没有返回内容", 502), nil
	}

	state.logs.add(callEntry{
		Account:  used.label(),
		Model:    ref.OpenAI,
		Tool:     ref.ToolID,
		Status:   "ok",
		Duration: time.Since(started).Milliseconds(),
	})

	id := newCompletionID()
	return okEnvelope(streamChunkEnvelope{
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Chunks:  buildSSEFrames(id, ref.OpenAI, text),
	})
}

// streamChunkEnvelope mirrors pluginhost.rpcExecutorStreamResponse.
//
// pluginapi.ExecutorStreamResponse cannot be used across the RPC boundary: its
// Chunks field is a channel for in-process consumers, and JSON cannot carry one
// ("unsupported type: <-chan pluginapi.ExecutorStreamChunk"). The wire shape
// carries a concrete slice instead.
type streamChunkEnvelope struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// buildSSEFrames renders the whole completion as SSE frames.
func buildSSEFrames(id, model, text string) []pluginapi.ExecutorStreamChunk {
	frames := make([]pluginapi.ExecutorStreamChunk, 0, 8)
	appendFrame := func(payload []byte) {
		frames = append(frames, pluginapi.ExecutorStreamChunk{Payload: payload})
	}

	// Bare JSON objects, not SSE frames.
	//
	// CPA wraps every chunk in "data: ...\n\n" itself and appends the terminal
	// "data: [DONE]" (executorStreamDonePayload). Sending pre-framed text here put
	// "data: data: {...}" on the wire, and a client parsing the stream as JSON
	// failed at the second colon — the reported
	// "Expected EOF after parsing, but had : instead".
	appendFrame(chunkBody(id, model, "", ""))
	const step = 120
	runes := []rune(text)
	for idx := 0; idx < len(runes); idx += step {
		end := idx + step
		if end > len(runes) {
			end = len(runes)
		}
		appendFrame(chunkBody(id, model, string(runes[idx:end]), ""))
	}
	appendFrame(chunkBody(id, model, "", "stop"))
	return frames
}

/* ---------- account selection ---------- */

// runWithAccount runs one generation, rotating to another account when the
// selected credential is unusable or the upstream rejects it.
//
// The host picks the credential (see scheduler.go); this function only decides
// whether to retry. isAuthError means the token is dead and a different account
// may well work; a quota error means the same. A transport failure is retried
// once on the same account, since it says nothing about the credential.
func runWithAccount(request []byte, creds *tinkerCredentials, authID string, ref toolRef, prompt string) (*generationResult, *tinkerCredentials, error) {
	cfg := state.settings.get()
	maxAttempts := cfg.MaxRotate
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	current := creds
	currentAuthID := authID
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		hCtx, cancel := context.WithTimeout(context.Background(),
			time.Duration(cfg.GenerationTimeoutSeconds+60)*time.Second)

		usable, errToken := withToken(hCtx, current, currentAuthID)
		if errToken != nil {
			cancel()
			lastErr = errToken
			state.accounts.markFailure(current.accountKey(), errToken.Error(), failureAuth)
			next, nextID := rotateAccount(request, current, currentAuthID)
			if next == nil {
				break
			}
			current, currentAuthID = next, nextID
			continue
		}

		result, errRun := upstream.generate(hCtx, usable.AccessToken, ref.ToolID, ref.ModelID, prompt)
		cancel()

		if errRun == nil {
			state.accounts.observe(usable, currentAuthID)
			state.accounts.markUsed(usable.accountKey())
			return result, usable, nil
		}

		lastErr = errRun
		kind := classifyFailure(errRun)
		state.accounts.markFailure(usable.accountKey(), errRun.Error(), kind)

		if kind == failureTransient || kind == failureRate {
			// A blip says nothing about the account; retrying on a different one
			// would just multiply the failure.
			break
		}
		next, nextID := rotateAccount(request, current, currentAuthID)
		if next == nil {
			break
		}
		current, currentAuthID = next, nextID
	}

	if lastErr == nil {
		lastErr = errors.New("没有可用的 Tinker 账号")
	}
	return nil, current, lastErr
}

// rotateAccount asks the host for another credential.
//
// CPA exposes the candidates through the scheduler contract; when the host does
// not answer, the plugin falls back to its own account cache so a second account
// can still be tried.
func rotateAccount(request []byte, current *tinkerCredentials, currentAuthID string) (*tinkerCredentials, string) {
	if next, nextID, ok := hostRotate(currentAuthID); ok {
		return next, nextID
	}

	currentKey := ""
	if current != nil {
		currentKey = current.accountKey()
	}
	for _, record := range state.accounts.list() {
		if record.Disabled {
			continue
		}
		if record.Key == currentKey {
			continue
		}
		if !state.accounts.usable(record.Key) {
			continue
		}
		if creds := credsForAuthIndex(record.AuthIndex); creds != nil {
			return creds, record.AuthIndex
		}
	}
	return nil, ""
}

// credsForAuthIndex reads one credential out of the host's auth store.
func credsForAuthIndex(authIndex string) *tinkerCredentials {
	if strings.TrimSpace(authIndex) == "" {
		return nil
	}
	raw, errGet := callHost("host.auth.get", map[string]any{"auth_id": authIndex})
	if errGet != nil || len(raw) == 0 {
		return nil
	}
	var resp struct {
		Auth *struct {
			StorageJSON json.RawMessage `json:"storage_json"`
		} `json:"auth"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil || resp.Auth == nil {
		return nil
	}
	creds, errParse := parseCredentials(resp.Auth.StorageJSON)
	if errParse != nil {
		return nil
	}
	return creds
}

/* ---------- count_tokens / http_request ---------- */

// executorCountTokens answers executor.count_tokens.
//
// Tinker bills per generation, not per token, and exposes no tokenizer. The
// estimate keeps clients that pre-flight a request working; it is not billing
// data.
func executorCountTokens(request []byte) ([]byte, error) {
	_, body, _, errDecode := decodeExecutorRequest(request)
	if errDecode != nil {
		return errorEnvelope("invalid_executor_request", errDecode.Error(), 400), nil
	}
	chatReq, errParse := parseChatRequest(body)
	if errParse != nil {
		return errorEnvelope("invalid_request", errParse.Error(), 400), nil
	}
	prompt := buildPrompt(chatReq.Messages)
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: []byte(`{"input_tokens":` + itoa(len([]rune(prompt))/4) + `}`),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

// executorHTTPRequest answers executor.http_request.
//
// This is CPA's raw bridging path. Tinker has no generic HTTP surface to mirror,
// so the request is answered with an explicit error instead of silently
// returning an empty body.
func executorHTTPRequest(request []byte) ([]byte, error) {
	return errorEnvelope("not_supported", "Tinker 不支持原始 HTTP 透传", http.StatusNotImplemented), nil
}

/* ---------- helpers ---------- */

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// itoa avoids pulling strconv into the payload builders.
func itoa(v int) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// decodeBodyString reads a body as a string, used for logging.
func decodeBodyString(raw []byte) string {
	return strings.TrimSpace(string(bytes.TrimSpace(raw)))
}

// classifyFailure maps an execution error onto a cooldown class.
//
// Ported from the reference plugin: the status code alone is not enough because
// Tinker surfaces semantics in the body, and because a 5xx may be a throttle.
func classifyFailure(err error) failureKind {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		// Transport failure, timeout, JSON decode problem: nothing about the
		// credential.
		return failureTransient
	}
	lowered := strings.ToLower(apiErr.Message)

	// An expired access token is not a broken credential: CPA refreshes it on its
	// own schedule, and Tinker's tokens live only 15 minutes, so a request that
	// lands just before that refresh fails with a perfectly good account behind
	// it. Parking the account for the hard window here is what turned a routine
	// refresh gap into "需重新登录".
	if strings.Contains(lowered, "expired") && !strings.Contains(lowered, "refresh token") {
		return failureTransient
	}

	switch {
	case apiErr.isAuthError():
		return failureAuth
	case apiErr.isQuotaError():
		return failureQuota
	case apiErr.Status == 429,
		strings.Contains(lowered, "rate limit"),
		strings.Contains(lowered, "too many requests"),
		strings.Contains(lowered, "频率"),
		strings.Contains(lowered, "限流"):
		return failureRate
	default:
		return failureTransient
	}
}
