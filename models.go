package main

import (
	"encoding/json"
	"strings"
)

// toolRef maps an OpenAI-facing model name onto a Tinker tool.
//
// Tinker has no "models" in the OpenAI sense: each entry is a tool that wraps a
// model. The catalogue below is the full set the app exposes — four text, four
// image and four video tools.
type toolRef struct {
	// OpenAI name is what a client asks for.
	OpenAI string `json:"id"`
	// ToolID is Tinker's tool key (the flow node "type").
	ToolID string `json:"tool_id"`
	// ModelID is the tool's default model, empty when the tool has a single
	// implicit model.
	ModelID string `json:"model_id,omitempty"`
	// OutputType is "text", "image" or "video".
	OutputType string `json:"output_type"`
	// DisplayName mirrors what the Tinker app shows.
	DisplayName string `json:"display_name"`
	// Platform is the vendor behind the tool.
	Platform string `json:"platform"`
	// ToolPromptKey is the input field carrying the prompt. Every text, image
	// and video tool in Tinker uses "prompt", but the field is named here so a
	// future tool that differs is a one-line change.
	ToolPromptKey string `json:"-"`
}

var toolCatalogue = []toolRef{
	// ---- text ----
	{OpenAI: "claude-opus-4-8", ToolID: "claude_text", ModelID: "anthropic:claude-opus-4-8",
		OutputType: "text", DisplayName: "Anthropic Claude Text Generator", Platform: "Anthropic"},
	{OpenAI: "gemini-3-pro", ToolID: "gemini_text", ModelID: "google:gemini-3-pro",
		OutputType: "text", DisplayName: "Google Gemini Text Generator", Platform: "Google"},
	{OpenAI: "grok-4-1-fast-reasoning", ToolID: "grok_text", ModelID: "xai:grok-4-1-fast-reasoning",
		OutputType: "text", DisplayName: "xAI Grok Text Generator", Platform: "xAI"},
	{OpenAI: "gpt-5.2", ToolID: "openai_text", ModelID: "gpt-5.2",
		OutputType: "text", DisplayName: "OpenAI GPT Text Generator", Platform: "OpenAI"},

	// ---- image ----
	{OpenAI: "gemini-3.1-flash-image", ToolID: "gemini_image", ModelID: "gemini_31_flash",
		OutputType: "image", DisplayName: "Google Gemini Image Generator", Platform: "Google"},
	{OpenAI: "gpt-image", ToolID: "gpt_image", ModelID: "default",
		OutputType: "image", DisplayName: "OpenAI GPT Image Generator", Platform: "OpenAI"},
	{OpenAI: "flux-image", ToolID: "flux_pro_kontext", ModelID: "default",
		OutputType: "image", DisplayName: "Flux Image", Platform: "Black Forest Labs"},
	{OpenAI: "grok-image", ToolID: "grok_image", ModelID: "default",
		OutputType: "image", DisplayName: "xAI Grok Image Generator", Platform: "xAI"},

	// ---- video ----
	{OpenAI: "minimax-video", ToolID: "minimax_video", ModelID: "minimax-h3",
		OutputType: "video", DisplayName: "MiniMax H3 Video Generator", Platform: "MiniMax"},
	{OpenAI: "veo-video", ToolID: "veo_text_to_video", ModelID: "veo-3.1-generate-preview",
		OutputType: "video", DisplayName: "Google Veo Video Generator", Platform: "Google"},
	{OpenAI: "kling-video", ToolID: "kling_image_to_video", ModelID: "kling-o3-pro",
		OutputType: "video", DisplayName: "Kling Video Generator", Platform: "Kling AI"},
	{OpenAI: "seedance-video", ToolID: "seedance_video", ModelID: "seedance-2.0",
		OutputType: "video", DisplayName: "Seedance Video Generator", Platform: "ByteDance"},
}

// resolveTool maps a requested model name onto a tool.
//
// Resolution order is forgiving on purpose: clients send the OpenAI name, the
// Tinker tool id, or the underlying model id depending on how the user typed it
// into their client.
func resolveTool(requested string) toolRef {
	wanted := strings.ToLower(strings.TrimSpace(requested))
	if wanted == "" {
		wanted = strings.ToLower(state.settings.get().DefaultModel)
	}
	// A "tinker/" or "provider/" prefix is a routing hint, not part of the name.
	if idx := strings.LastIndex(wanted, "/"); idx >= 0 && idx < len(wanted)-1 {
		candidate := wanted[idx+1:]
		if lookupTool(candidate) != nil {
			wanted = candidate
		}
	}
	if found := lookupTool(wanted); found != nil {
		return *found
	}
	// Unknown names fall back to the configured text default so a typo still
	// produces an answer rather than a hard failure.
	fallback := state.settings.get().DefaultModel
	if found := lookupTool(strings.ToLower(fallback)); found != nil {
		return *found
	}
	return toolCatalogue[0]
}

func lookupTool(name string) *toolRef {
	for i := range toolCatalogue {
		ref := &toolCatalogue[i]
		if strings.EqualFold(ref.OpenAI, name) ||
			strings.EqualFold(ref.ToolID, name) ||
			(ref.ModelID != "" && strings.EqualFold(ref.ModelID, name)) {
			return ref
		}
	}
	return nil
}

// defaultToolForType returns the first tool of a type, used by the image and
// video endpoints when the client names no model.
func defaultToolForType(outputType string) toolRef {
	for _, ref := range toolCatalogue {
		if ref.OutputType == outputType {
			return ref
		}
	}
	return toolCatalogue[0]
}

// promptLimit is Tinker's own per-type ceiling. Exceeding it is a hard upstream
// error ("Your prompt is too long ... under 4,000 characters"), so the prompt is
// trimmed before the call instead of surfacing that failure to the client.
func promptLimit(ref toolRef) int {
	cfg := state.settings.get()
	if ref.OutputType == "text" {
		return cfg.TextPromptLimit
	}
	return cfg.ImagePromptLimit
}

// limitPrompt trims a prompt to the tool's ceiling.
func limitPrompt(prompt string, ref toolRef) string {
	runes := []rune(prompt)
	limit := promptLimit(ref)
	if len(runes) <= limit {
		return prompt
	}
	logf("prompt 超过 %d 字符上限，已截断（工具 %s）", limit, ref.ToolID)
	return string(runes[:limit])
}

/* ---------- content extraction ---------- */

// artifactHasOutput reports whether the artifact carries anything renderable.
func artifactHasOutput(artifact map[string]any) bool {
	return extractText(artifact) != "" || extractMediaURL(artifact) != ""
}

// extractText flattens every text-bearing content type into one string.
//
// Tinker stores generation output as typed content: text lands in
// BookContent.pages[].content, and the other types carry their own fields.
func extractText(artifact map[string]any) string {
	contents := contentsOf(artifact)
	if len(contents) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, content := range contents {
		kind, _ := content["__typename"].(string)
		switch kind {
		case "BookContent":
			pages, _ := content["pages"].([]any)
			for _, pageRaw := range pages {
				page, _ := pageRaw.(map[string]any)
				text, _ := page["content"].(string)
				if text == "" {
					continue
				}
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(text)
			}
			if sb.Len() == 0 {
				if description, _ := content["description"].(string); description != "" {
					sb.WriteString(description)
				}
			}
		case "MarkdownContent":
			if text, _ := content["text"].(string); text != "" {
				sb.WriteString(text)
			}
		case "LandingPageContent", "WebsiteContent":
			if html, _ := content["htmlContent"].(string); html != "" {
				sb.WriteString(html)
			}
		case "CsvContent":
			if headers, ok := content["headers"]; ok {
				sb.WriteString(stringify(headers))
			}
			if records, ok := content["records"]; ok {
				sb.WriteString("\n")
				sb.WriteString(stringify(records))
			}
		case "JsonContent":
			if data, ok := content["data"]; ok {
				sb.WriteString(stringify(data))
			}
		case "ImageContent":
			if url := imageURL(content); url != "" {
				sb.WriteString("![image](" + url + ")")
			}
		case "VideoContent", "PdfContent":
			if url := mediaURL(content); url != "" {
				sb.WriteString(url)
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// extractMediaURL returns the first downloadable media link.
func extractMediaURL(artifact map[string]any) string {
	for _, content := range contentsOf(artifact) {
		if url := imageURL(content); url != "" {
			return url
		}
		kind, _ := content["__typename"].(string)
		if kind == "VideoContent" || kind == "PdfContent" || kind == "AudioContent" {
			if url := mediaURL(content); url != "" {
				return url
			}
		}
	}
	return ""
}

// imageURL prefers the largest rendition Tinker offers.
func imageURL(content map[string]any) string {
	if urls, ok := content["displayUrls"].(map[string]any); ok {
		for _, key := range []string{"original", "large", "medium", "thumbnail"} {
			if url, _ := urls[key].(string); url != "" {
				return url
			}
		}
	}
	url, _ := content["url"].(string)
	return url
}

// mediaURL covers the field names the video/audio/pdf content types use.
func mediaURL(content map[string]any) string {
	for _, key := range []string{"videoUrl", "completeAudioUrl", "url"} {
		if url, _ := content[key].(string); url != "" {
			return url
		}
	}
	if urls, ok := content["displayUrls"].(map[string]any); ok {
		for _, key := range []string{"full", "original", "poster", "preview"} {
			if url, _ := urls[key].(string); url != "" {
				return url
			}
		}
	}
	return ""
}

// contentsOf reads artifact.outputContents defensively: GraphQL decodes into
// []any, but a hand-edited fixture may carry a different shape.
func contentsOf(artifact map[string]any) []map[string]any {
	if artifact == nil {
		return nil
	}
	raw, ok := artifact["outputContents"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if entry, okEntry := item.(map[string]any); okEntry {
			out = append(out, entry)
		}
	}
	return out
}

// stringify renders an arbitrary JSON value for the text extractor.
func stringify(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
