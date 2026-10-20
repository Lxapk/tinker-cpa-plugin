package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"errors"
	"net/http"
	"strings"
	"sync"
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
//
// Two request shapes arrive here: chat completions (a message array) and images
// generations (a flat prompt object). They are parsed separately and answered in
// their own format, so an OpenAI client can use either endpoint.
func executorExecute(request []byte) ([]byte, error) {
	req, body, creds, errDecode := decodeExecutorRequest(request)
	if errDecode != nil {
		return errorEnvelope("invalid_executor_request", errDecode.Error(), 400), nil
	}
	// A retrieval carries only {"request_id": ...} and is answered from the stored
	// result; a create carries a prompt.
	if isVideoRetrieve(body) {
		return executeVideoRetrieve(body)
	}
	if isVideoCreate(req, body) {
		return executeVideo(req, body, creds)
	}
	if detectRequestKind(body) == kindImages {
		return executeImages(req, body, creds)
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
	result, used, errRun := runWithAccount(request, creds, req.AuthID, ref,
		buildToolInputs(ref, prompt, body), collectImageURLs(chatReq.Messages))
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

// executeImages answers /v1/images/generations.
//
// The response is the images shape — {"created":…,"data":[{"url":…}]} — not a chat
// completion, because that is what the caller's endpoint promises. Returning a chat
// body here would leave an images client with nothing it can parse.
func executeImages(req executorRequest, body []byte, creds *tinkerCredentials) ([]byte, error) {
	imgReq, errParse := parseImagesRequest(body)
	if errParse != nil {
		return errorEnvelope("invalid_request", errParse.Error(), 400), nil
	}
	prompt := strings.TrimSpace(imgReq.Prompt)
	if prompt == "" {
		return errorEnvelope("invalid_request", "prompt is required", 400), nil
	}

	modelName := firstNonEmpty(req.Model, imgReq.Model)
	ref := resolveTool(modelName)
	if ref.OutputType != "image" {
		// The endpoint is for images; a text model named here is a client mistake.
		ref = defaultToolForType("image")
	}
	prompt = limitPrompt(prompt, ref)

	started := time.Now()
	result, used, errRun := runWithAccount(nil, creds, req.AuthID, ref,
		buildToolInputs(ref, prompt, body), imagesFromImagesRequest(imgReq))
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

	url := extractMediaURL(result.Artifact)
	if url == "" {
		return errorEnvelope("empty_result", "生成完成但没有返回图片", 502), nil
	}

	state.logs.add(callEntry{
		Account:  used.label(),
		Model:    ref.OpenAI,
		Tool:     ref.ToolID,
		Status:   "ok",
		Duration: time.Since(started).Milliseconds(),
	})

	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: imagesResponseBody([]string{url}, imgReq.ResponseFormat),
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

	// Images with stream=true arrive here too: the host routes them through its
	// streaming image path. The body still produces one result, so it is answered
	// with that result in the images shape and a single terminal frame.
	if detectRequestKind(body) == kindImages {
		raw, errImages := executeImages(req, body, creds)
		if errImages != nil {
			return nil, errImages
		}
		return raw, nil
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
	result, used, errRun := runWithAccount(request, creds, req.AuthID, ref,
		buildToolInputs(ref, prompt, body), collectImageURLs(chatReq.Messages))
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
func runWithAccount(request []byte, creds *tinkerCredentials, authID string, ref toolRef, inputs map[string]any, imageURLs []string) (*generationResult, *tinkerCredentials, error) {
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

		// Reference images must be uploaded first: Tinker's tools take a blob
		// reference, not a URL, so a client's image_url is downloaded and re-sent
		// through uploadArtifactFile.
		runInputs := inputs
		if len(imageURLs) > 0 {
			if field := referenceFieldFor(ref.ToolID); field != "" {
				if refs, errRefs := buildReferenceInputs(hCtx, usable.AccessToken, ref.ToolID, imageURLs); errRefs != nil {
					cancel()
					lastErr = errRefs
					state.accounts.markFailure(usable.accountKey(), errRefs.Error(), failureTransient)
					break
				} else if len(refs) > 0 {
					runInputs = cloneInputs(inputs)
					runInputs[field] = refs
				}
			}
		}

		result, errRun := upstream.generate(hCtx, usable.AccessToken, ref.ToolID, ref.ModelID, runInputs)
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

// videoResults holds completed video URLs by task id so a retrieve can answer.
//
// Tinker finishes a generation synchronously — there is no queue to poll — so the
// create call already has the link. CPA's video protocol is task-based, though: it
// returns an id and the client then GETs /v1/videos/{id}. Keeping the result lets
// that GET succeed instead of reporting "still processing" forever.
var videoResults = struct {
	sync.Map
}{}

func rememberVideo(id, url string) {
	if id == "" {
		return
	}
	videoResults.Store(id, url)
}

func lookupVideo(id string) string {
	if value, ok := videoResults.Load(id); ok {
		if url, isText := value.(string); isText {
			return url
		}
	}
	return ""
}

// executeVideo answers a video creation request.
func executeVideo(req executorRequest, body []byte, creds *tinkerCredentials) ([]byte, error) {
	videoReq, errParse := parseVideoRequest(body)
	if errParse != nil {
		return errorEnvelope("invalid_request", errParse.Error(), 400), nil
	}
	prompt := strings.TrimSpace(videoReq.Prompt)
	if prompt == "" {
		return errorEnvelope("invalid_request", "prompt is required", 400), nil
	}

	modelName := firstNonEmpty(req.Model, videoReq.Model)
	ref := resolveTool(modelName)
	if ref.OutputType != "video" {
		ref = defaultToolForType("video")
	}
	prompt = limitPrompt(prompt, ref)

	// A "WxH" size is meaningful for video only as an orientation hint; the tools
	// take aspect_ratio.
	inputs := buildToolInputs(ref, prompt, body)
	if videoReq.Size != "" {
		if _, has := inputs["aspect_ratio"]; !has {
			if ratio := aspectRatioFor(ref.ToolID, "", videoReq.Size); ratio != "" {
				inputs["aspect_ratio"] = ratio
			}
		}
	}
	if videoReq.Seconds != "" {
		if _, has := inputs["duration"]; !has {
			if d := durationFrom(ref.ToolID, json.RawMessage(`"`+videoReq.Seconds+`"`)); d > 0 {
				inputs["duration"] = d
			}
		}
	}

	started := time.Now()
	result, used, errRun := runWithAccount(nil, creds, req.AuthID, ref, inputs,
		imagesFromVideoRequest(videoReq))
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

	url := extractMediaURL(result.Artifact)
	if url == "" {
		return errorEnvelope("empty_result", "生成完成但没有返回视频", 502), nil
	}

	state.logs.add(callEntry{
		Account:  used.label(),
		Model:    ref.OpenAI,
		Tool:     ref.ToolID,
		Status:   "ok",
		Duration: time.Since(started).Milliseconds(),
	})

	taskID := result.ArtifactID
	rememberVideo(taskID, url)

	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: videoCreateResponse(taskID, ref.OpenAI, prompt, videoReq.Seconds, videoReq.Size),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

// executeVideoRetrieve answers a video status lookup from the stored result.
func executeVideoRetrieve(body []byte) ([]byte, error) {
	var probe struct {
		RequestID string `json:"request_id"`
		ID        string `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(body, &probe); errUnmarshal != nil {
		return errorEnvelope("invalid_request", errUnmarshal.Error(), 400), nil
	}
	id := firstNonEmpty(probe.RequestID, probe.ID)
	url := lookupVideo(id)
	if url == "" {
		// Unknown id: report a failure rather than an eternal "queued" that no
		// client can ever resolve.
		return errorEnvelope("not_found", "找不到该视频任务（插件重启后任务记录会丢失，请重新生成）", 404), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: videoRetrieveResponse(id, url),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

// buildReferenceInputs downloads and uploads every image the client attached.
//
// Tinker's reference fields take a blob reference produced by its own upload
// mutation, so each URL becomes one {image_blob_id, previewUrl, name} entry. The
// tool's own field name and count limit are applied by the caller.
func buildReferenceInputs(ctx context.Context, accessToken, toolID string, urls []string) ([]any, error) {
	limit := referenceLimit(toolID)
	if len(urls) > limit {
		urls = urls[:limit]
	}
	out := make([]any, 0, len(urls))
	for _, rawURL := range urls {
		data, contentType, name, errFetch := fetchImageBytes(ctx, rawURL)
		if errFetch != nil {
			return nil, errFetch
		}
		ref, errUpload := upstream.uploadImage(ctx, accessToken, data, name, contentType)
		if errUpload != nil {
			return nil, errUpload
		}
		out = append(out, ref)
	}
	return out, nil
}

// fetchImageBytes handles both http(s) URLs and data: URLs.
func fetchImageBytes(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	if strings.HasPrefix(rawURL, "data:") {
		// data:image/png;base64,…  — decoded locally, no download needed.
		comma := strings.Index(rawURL, ",")
		if comma < 0 {
			return nil, "", "", errors.New("data URL 格式不正确")
		}
		meta := rawURL[len("data:"):comma]
		contentType := strings.SplitN(meta, ";", 2)[0]
		if contentType == "" {
			contentType = "image/png"
		}
		payload, errDecode := base64.StdEncoding.DecodeString(rawURL[comma+1:])
		if errDecode != nil {
			return nil, "", "", fmt.Errorf("data URL 解码失败: %w", errDecode)
		}
		return payload, contentType, "inline." + formatExt(contentType), nil
	}
	data, contentType, errFetch := upstream.fetchImage(ctx, rawURL)
	if errFetch != nil {
		return nil, "", "", errFetch
	}
	name := "reference." + formatExt(contentType)
	if idx := strings.LastIndex(rawURL, "/"); idx >= 0 && idx < len(rawURL)-1 {
		candidate := rawURL[idx+1:]
		if q := strings.Index(candidate, "?"); q > 0 {
			candidate = candidate[:q]
		}
		if strings.Contains(candidate, ".") {
			name = candidate
		}
	}
	return data, contentType, name, nil
}

// formatExt maps a content type to a file extension.
func formatExt(contentType string) string {
	switch {
	case strings.Contains(contentType, "png"):
		return "png"
	case strings.Contains(contentType, "webp"):
		return "webp"
	case strings.Contains(contentType, "gif"):
		return "gif"
	default:
		return "jpg"
	}
}

// cloneInputs copies an inputs map so a retry starts from a clean slate.
func cloneInputs(inputs map[string]any) map[string]any {
	out := make(map[string]any, len(inputs)+1)
	for key, value := range inputs {
		out[key] = value
	}
	return out
}
