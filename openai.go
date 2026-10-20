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
