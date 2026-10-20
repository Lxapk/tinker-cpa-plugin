package main

import (
	"encoding/json"
	"fmt"
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

// toolFields describes the optional inputs a tool accepts.
//
// The catalogue is not uniform: gemini_image takes resolution, gpt_image takes
// quality, flux and grok take neither. Sending a field a tool does not declare is
// rejected by the upstream's input validation, so the mapping below checks support
// before adding anything.
type toolFields struct {
	AspectRatio bool
	Resolution  bool // gemini_* image tools
	Quality     bool // gpt_image
	Format      bool
	Enhance     bool
	Duration    bool // video tools
	Audio       bool // video tools that can synthesise audio
}

var toolFieldSupport = map[string]toolFields{
	// Images.
	"gemini_image":     {AspectRatio: true, Resolution: true, Format: true, Enhance: true},
	"gpt_image":        {AspectRatio: true, Quality: true, Format: true, Enhance: true},
	"flux_pro_kontext": {AspectRatio: true, Format: true, Enhance: true},
	"grok_image":       {AspectRatio: true, Format: true, Enhance: true},

	// Video. Note there is no resolution field: the duration and aspect ratio are
	// the only knobs the upstream exposes, so a client asking for 4K video is
	// honoured only in the sense that the request is not rejected.
	"minimax_video":        {AspectRatio: true, Duration: true, Enhance: true},
	"veo_text_to_video":    {AspectRatio: true, Duration: true, Audio: true, Enhance: true},
	"kling_image_to_video": {AspectRatio: true, Duration: true, Audio: true},
	"seedance_video":       {AspectRatio: true, Duration: true, Audio: true, Enhance: true},
}

// videoDurations lists the durations each video tool accepts, in seconds.
//
// The values differ per tool and the upstream rejects anything outside the enum, so
// a requested duration has to be snapped to the nearest allowed value rather than
// passed through.
var videoDurations = map[string][]int{
	"minimax_video":        {5, 6},
	"veo_text_to_video":    {4, 6},
	"kling_image_to_video": {3, 4, 5},
	"seedance_video":       {4, 5, 6},
}

// buildToolInputs turns an OpenAI image request into the tool's input map.
//
// OpenAI clients express size as "1024x1024" or "2K"/"4K", and quality as
// low/medium/high. Tinker instead takes an aspect_ratio string and, on the Gemini
// tools, a resolution enum. Translating here is what makes "4K" work at all —
// before this, only the prompt was forwarded and every image came back at the
// tool's 1K default.
func buildToolInputs(ref toolRef, prompt string, request []byte) map[string]any {
	inputs := map[string]any{"prompt": prompt}
	fields, known := toolFieldSupport[ref.ToolID]
	if !known {
		return inputs
	}

	var body struct {
		Size       string          `json:"size"`
		Quality    string          `json:"quality"`
		Resolution string          `json:"resolution"`
		Aspect     string          `json:"aspect_ratio"`
		Format     string          `json:"output_format"`
		Duration   json.RawMessage `json:"duration"`
		Seconds    json.RawMessage `json:"seconds"`
		Audio      *bool           `json:"generate_audio"`
	}
	_ = json.Unmarshal(request, &body)

	if fields.Duration {
		if d := durationFrom(ref.ToolID, body.Duration, body.Seconds); d > 0 {
			// The upstream declares duration as INTEGER with a string enum, so the
			// value must be a number on the wire.
			inputs["duration"] = d
		}
	}
	if fields.Audio && body.Audio != nil {
		inputs["generate_audio"] = *body.Audio
	}

	if fields.AspectRatio {
		if ratio := aspectRatioFor(ref.ToolID, body.Aspect, body.Size); ratio != "" {
			inputs["aspect_ratio"] = ratio
		}
	}
	if fields.Resolution {
		if res := resolutionFrom(body.Resolution, body.Size); res != "" {
			inputs["resolution"] = res
		}
	}
	if fields.Quality {
		if q := qualityFrom(body.Quality, body.Size); q != "" {
			inputs["quality"] = q
		}
	}
	if fields.Format {
		if f := normalizeFormat(body.Format); f != "" {
			inputs["output_format"] = f
		}
	}
	return inputs
}

// aspectRatioFrom maps an explicit ratio or a WxH size onto Tinker's enum.
func aspectRatioFrom(explicit, size string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" {
		return ""
	}
	// A client may put the ratio itself in size ("16:9"), which is not a pixel
	// dimension; treat it as the ratio rather than failing to parse it.
	if strings.Contains(size, ":") && !strings.Contains(size, "x") {
		return size
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return ""
	}
	var w, h int
	if _, errW := fmt.Sscanf(parts[0], "%d", &w); errW != nil {
		return ""
	}
	if _, errH := fmt.Sscanf(parts[1], "%d", &h); errH != nil {
		return ""
	}
	if w <= 0 || h <= 0 {
		return ""
	}
	// Ratios are width:height, so the width term multiplies the right-hand side:
	// a 9:16 portrait satisfies w*16 == h*9. Getting this backwards silently
	// rotated every landscape request, which is exactly what the test caught.
	switch {
	case w == h:
		return "1:1"
	case w*16 == h*9:
		return "9:16"
	case w*9 == h*16:
		return "16:9"
	case w*4 == h*3:
		return "3:4"
	case w*3 == h*4:
		return "4:3"
	case w*3 == h*2:
		return "2:3"
	case w*2 == h*3:
		return "3:2"
	}
	// Any other shape: report the nearest of the two landscape/portrait families
	// rather than sending a ratio the tool does not accept.
	if w > h {
		return "16:9"
	}
	return "9:16"
}

// resolutionFrom maps a size or explicit resolution onto Tinker's enum.
//
// Gemini's tools accept 0_5K / 1K / 2K / 4K (the newest also 0_5K). A pixel size
// is bucketed by its long edge, which is how the app's own UI describes them.
func resolutionFrom(explicit, size string) string {
	value := strings.ToLower(strings.TrimSpace(explicit))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(size))
	}
	switch value {
	case "0_5k", "512", "0.5k":
		return "0_5K"
	case "1k", "1024":
		return "1K"
	case "2k":
		return "2K"
	case "4k":
		return "4K"
	}
	// A WxH size: bucket by the long edge.
	parts := strings.SplitN(value, "x", 2)
	if len(parts) == 2 {
		var w, h int
		if _, errW := fmt.Sscanf(parts[0], "%d", &w); errW == nil {
			if _, errH := fmt.Sscanf(parts[1], "%d", &h); errH == nil {
				longest := w
				if h > longest {
					longest = h
				}
				switch {
				case longest >= 3000:
					return "4K"
				case longest >= 1800:
					return "2K"
				case longest >= 900:
					return "1K"
				case longest > 0:
					return "0_5K"
				}
			}
		}
	}
	return ""
}

// qualityFrom maps OpenAI's quality onto gpt_image's enum.
func qualityFrom(explicit, size string) string {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "auto", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(explicit))
	case "standard":
		return "medium"
	case "hd":
		return "high"
	}
	// A large size implies a higher quality tier.
	switch resolutionFrom("", size) {
	case "4K":
		return "high"
	case "2K":
		return "medium"
	}
	return ""
}

func normalizeFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "png", "jpeg", "jpg", "webp":
		if strings.EqualFold(value, "jpg") {
			return "jpeg"
		}
		return strings.ToLower(strings.TrimSpace(value))
	}
	return ""
}

// durationFrom snaps a requested clip length to one the tool accepts.
//
// Clients express it differently — "duration": 5, "seconds": "6", or nested in the
// OpenAI video request — so both fields are read and any of them parsed. The result
// is the closest allowed value, because the upstream validates against its enum and
// rejects anything else.
func durationFrom(toolID string, raw ...json.RawMessage) int {
	allowed := videoDurations[toolID]
	if len(allowed) == 0 {
		return 0
	}
	requested := 0
	for _, field := range raw {
		if len(field) == 0 {
			continue
		}
		var asNumber float64
		if errNum := json.Unmarshal(field, &asNumber); errNum == nil && asNumber > 0 {
			requested = int(asNumber + 0.5)
			break
		}
		var asString string
		if errStr := json.Unmarshal(field, &asString); errStr == nil {
			var parsed int
			if _, errScan := fmt.Sscanf(strings.TrimSpace(asString), "%d", &parsed); errScan == nil && parsed > 0 {
				requested = parsed
				break
			}
		}
	}
	if requested <= 0 {
		// No preference: leave it to the tool's default rather than picking one.
		return 0
	}
	best := allowed[0]
	bestGap := absInt(requested - best)
	for _, candidate := range allowed[1:] {
		if gap := absInt(requested - candidate); gap < bestGap {
			best, bestGap = candidate, gap
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// aspectRatios lists the ratios each tool accepts.
//
// The lists differ — Veo offers only 9:16 and 16:9 while MiniMax offers seven — and
// the upstream validates against them, so a requested ratio has to be mapped onto
// the tool's own set rather than forwarded as-is.
var aspectRatios = map[string][]string{
	"gemini_image":         {"auto", "9:16", "4:5", "3:4", "2:3", "1:1", "3:2", "4:3", "5:4", "16:9", "21:9"},
	"gpt_image":            {"auto", "9:16", "3:4", "1:1", "4:3", "16:9"},
	"flux_pro_kontext":     {"auto", "9:16", "3:4", "1:1", "4:3", "16:9"},
	"grok_image":           {"9:16", "3:4", "2:3", "9:20", "1:2", "1:1", "3:2", "4:3", "16:9", "2:1", "20:9"},
	"minimax_video":        {"auto", "9:16", "3:4", "1:1", "4:3", "16:9", "21:9"},
	"veo_text_to_video":    {"9:16", "16:9"},
	"kling_image_to_video": {"auto", "9:16", "1:1", "16:9"},
	"seedance_video":       {"auto", "9:16", "3:4", "1:1", "4:3", "16:9", "21:9"},
}

// aspectRatioFor resolves a requested ratio onto the tool's accepted set.
//
// A client may send "16:9" in size, or "1920x1080"; both are normalised to a ratio
// and then fitted to what the tool declares. Anything the tool cannot express falls
// back to its nearest family (landscape or portrait) instead of being forwarded and
// rejected.
func aspectRatioFor(toolID, explicit, size string) string {
	ratio := aspectRatioFrom(explicit, size)
	if ratio == "" {
		return ""
	}
	allowed := aspectRatios[toolID]
	if len(allowed) == 0 {
		return ratio
	}
	for _, candidate := range allowed {
		if candidate == ratio {
			return ratio
		}
	}
	// Fit to the same orientation when the exact ratio is unavailable.
	landscape := ratioIsLandscape(ratio)
	for _, candidate := range allowed {
		if candidate == "auto" {
			continue
		}
		if ratioIsLandscape(candidate) == landscape {
			return candidate
		}
	}
	return ""
}

// ratioIsLandscape reports whether a "W:H" ratio is wider than it is tall.
func ratioIsLandscape(ratio string) bool {
	parts := strings.SplitN(ratio, ":", 2)
	if len(parts) != 2 {
		return false
	}
	var w, h int
	if _, errW := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &w); errW != nil {
		return false
	}
	if _, errH := fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &h); errH != nil {
		return false
	}
	return w > h
}
