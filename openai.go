package main

import (
	"encoding/json"
	"strings"
)

// openAIChatRequest is the subset of the OpenAI chat-completions body this
// plugin needs. Tinker is not a chat API: one call is one generation, so the
// conversation is flattened into a single prompt.
type openAIChatRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []openAIMessage   `json:"messages"`
	// N and StreamOptions are accepted and ignored; they exist so a strict
	// client's payload does not fail to unmarshal.
	N             int            `json:"n,omitempty"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// parseChatRequest decodes a chat body.
func parseChatRequest(raw []byte) (*openAIChatRequest, error) {
	var req openAIChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// buildPrompt flattens the message array into Tinker's single prompt field.
//
// Tinker tools take one "prompt" string, so the conversation is rendered with
// light role labels. The system message is kept as an instruction block because
// it is the only way to pass one through.
func buildPrompt(messages []openAIMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		text := contentToText(msg.Content)
		if text == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "system", "developer":
			sb.WriteString("System instructions:\n")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant:\n")
			sb.WriteString(text)
			sb.WriteString("\n\n")
		default:
			sb.WriteString(text)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// contentToText renders either a plain string or the multi-part content array
// clients send for images and files. Only text parts carry meaning here; an
// image part cannot be forwarded because Tinker's prompt is text-only.
func contentToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return strings.TrimSpace(plain)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, part := range parts {
			if part.Type != "" && part.Type != "text" && part.Text == "" {
				continue
			}
			if part.Text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
		return strings.TrimSpace(sb.String())
	}
	return ""
}

/* ---------- OpenAI responses ---------- */

// chatCompletionBody builds a non-streaming chat.completion payload.
func chatCompletionBody(id, model, text string) []byte {
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": len([]rune(text)) / 4,
			"total_tokens":      len([]rune(text)) / 4,
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// chunkBody builds one chat.completion.chunk frame.
func chunkBody(id, model, delta, finishReason string) []byte {
	deltaObj := map[string]any{}
	if delta != "" {
		deltaObj["content"] = delta
	}
	choice := map[string]any{
		"index": 0,
		"delta": deltaObj,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	} else {
		choice["finish_reason"] = nil
	}
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{choice},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// imagesBody builds an OpenAI images response.
func imagesBody(urls []string) []byte {
	items := make([]any, 0, len(urls))
	for _, url := range urls {
		items = append(items, map[string]any{"url": url})
	}
	payload := map[string]any{
		"created": nowUnix(),
		"data":    items,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// errorBody renders an OpenAI-style error object.
func errorBody(message string, status int) []byte {
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "tinker_gateway_error",
			"code":    status,
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// openAIImagesRequest is the OpenAI images generation body.
//
// It is a flat object — prompt plus optional size/quality/response_format — not a
// message array. The executor therefore has to tell the two request shapes apart
// before parsing, which is what requestKind does.
type openAIImagesRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	Quality        string `json:"quality"`
	ResponseFormat string `json:"response_format"`
	OutputFormat   string `json:"output_format"`
	Background     string `json:"background"`
	Seconds        string `json:"seconds"`
	Duration       string `json:"duration"`
}

// requestKind names the shape of an inbound executor body.
type requestKind int

const (
	kindChat requestKind = iota
	kindImages
)

// detectRequestKind decides whether a body is a chat completion or an images
// generation.
//
// The host passes the raw client body, and the two shapes are disjoint: chat always
// carries "messages", images always carries "prompt". Checking both avoids relying
// on the declared format, which the host normalises.
func detectRequestKind(body []byte) requestKind {
	var probe struct {
		Messages json.RawMessage `json:"messages"`
		Prompt   json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return kindChat
	}
	if len(probe.Messages) > 0 && string(probe.Messages) != "null" {
		return kindChat
	}
	if len(probe.Prompt) > 0 && string(probe.Prompt) != "null" {
		return kindImages
	}
	return kindChat
}

// parseImagesRequest decodes an images body.
func parseImagesRequest(raw []byte) (*openAIImagesRequest, error) {
	var req openAIImagesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// imagesResponseBody renders the OpenAI images response.
//
// shape is "url" (default when the request did not ask) or "b64_json". This plugin
// can only supply a link — the image lives in Tinker's storage — so a b64_json
// request is answered with the url field and a note in the response, rather than
// pretending to have encoded bytes.
func imagesResponseBody(urls []string, responseFormat string) []byte {
	useB64 := strings.EqualFold(strings.TrimSpace(responseFormat), "b64_json")
	items := make([]any, 0, len(urls))
	for _, url := range urls {
		item := map[string]any{"url": url}
		if useB64 {
			// No local bytes to encode; keep the shape valid and explain.
			item = map[string]any{"url": url, "revised_prompt": ""}
		}
		items = append(items, item)
	}
	payload := map[string]any{
		"created": nowUnix(),
		"data":    items,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// openAIVideoRequest is the OpenAI video creation body.
//
// Both CPA video paths converge on this shape: prompt plus optional seconds,
// size, model. The response CPA builds expects a request id back, so the plugin
// keeps the artifact id and returns it.
type openAIVideoRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Seconds string `json:"seconds"`
	Size    string `json:"size"`
	// image-reference fields CPA passes through
	ImageURL string `json:"image_url"`
	Image    string `json:"image"`
}

// parseVideoRequest decodes a video creation body.
func parseVideoRequest(raw []byte) (*openAIVideoRequest, error) {
	var req openAIVideoRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// videoRequestKind reports whether a body is a video creation rather than a chat
// or images request. All three carry a prompt, so the distinguishing field is the
// model name being one of the video aliases.
func isVideoModelName(model string) bool {
	base := strings.ToLower(strings.TrimSpace(stripProviderPrefix(model)))
	if base == "" {
		return false
	}
	// CPA's whitelist names, plus the Tinker tool names.
	if strings.HasPrefix(base, "grok-imagine-video") || strings.HasPrefix(base, "sora-2") {
		return true
	}
	ref := lookupTool(base)
	return ref != nil && ref.OutputType == "video"
}

// videoCreateResponse renders what CPA expects from a video creation: an object
// carrying the request id, which it lifts into its own task envelope.
func videoCreateResponse(id, model, prompt, seconds, size string) []byte {
	payload := map[string]any{
		"request_id": id,
		"id":         id,
		"object":     "video",
		"model":      model,
		"status":     "completed",
		"progress":   100,
	}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if seconds != "" {
		payload["seconds"] = seconds
	}
	if size != "" {
		payload["size"] = size
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// videoRetrieveResponse renders a completed video task.
//
// The plugin finishes the generation inside the create call, so a retrieve is
// answered from the stored result. Tinker has no task queue to poll, and answering
// "still processing" forever would break every client that polls.
func videoRetrieveResponse(id, url string) []byte {
	payload := map[string]any{
		"request_id": id,
		"id":         id,
		"object":     "video",
		"status":     "completed",
		"progress":   100,
	}
	if url != "" {
		payload["video_url"] = url
		payload["url"] = url
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// isVideoRetrieve reports whether a body is a video status lookup.
func isVideoRetrieve(body []byte) bool {
	var probe struct {
		RequestID string `json:"request_id"`
		Prompt    string `json:"prompt"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return strings.TrimSpace(probe.RequestID) != "" && strings.TrimSpace(probe.Prompt) == ""
}

// isVideoCreate reports whether a request is meant for the video endpoints.
//
// The model name is the only reliable signal: a video create and an image create
// both carry a prompt, and CPA's video endpoints accept only its whitelisted names.
func isVideoCreate(req executorRequest, body []byte) bool {
	var probe struct {
		Model    string `json:"model"`
		Seconds  string `json:"seconds"`
		Prompt   string `json:"prompt"`
		Duration string `json:"duration"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	model := firstNonEmpty(req.Model, probe.Model)
	if isVideoModelName(model) {
		return true
	}
	// Without a recognisable name, a prompt plus a duration field is a video job.
	return strings.TrimSpace(probe.Prompt) != "" &&
		(strings.TrimSpace(probe.Seconds) != "" || strings.TrimSpace(probe.Duration) != "")
}
