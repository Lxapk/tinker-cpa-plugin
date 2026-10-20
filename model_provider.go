package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// model_provider.go wires the Tinker tool catalogue into CPA's model registry.
//
// Both registration paths are served from the same catalogue:
//
//	model.register   (ModelRegistrar) — called once at startup
//	model.static     (ModelProvider)  — asked on demand
//	model.for_auth   (ModelProvider)  — asked per credential
//
// Tinker tools are global: every account sees the same set, so the per-auth
// answer is identical to the static one. Serving it anyway is what lets the
// panel's "models for this credential" view populate.

// modelStatic answers model.register and model.static.
func modelStatic(_ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ModelRegistrationResponse{
		Provider: tinkerProviderKey,
		Models:   catalogueModels(),
	})
}

// modelForAuth answers model.for_auth.
func modelForAuth(request []byte) ([]byte, error) {
	// The credential only matters for a provider whose catalogue depends on the
	// account's plan. Tinker's does not, so the catalogue is returned as-is.
	_ = request
	return okEnvelope(pluginapi.ModelResponse{
		Provider: tinkerProviderKey,
		Models:   catalogueModels(),
	})
}

// catalogueModels renders the tool catalogue as CPA model metadata.
func catalogueModels() []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(toolCatalogue))
	for _, ref := range toolCatalogue {
		info := pluginapi.ModelInfo{
			ID:          ref.OpenAI,
			Object:      "model",
			OwnedBy:     tinkerProviderKey,
			Name:        ref.OpenAI,
			DisplayName: ref.DisplayName,
			Description: ref.DisplayName + " (" + ref.Platform + ")",
			Type:        "chat",
		}
		switch ref.OutputType {
		case "image":
			info.Type = "image"
			info.SupportedOutputModalities = []string{"image"}
			info.SupportedInputModalities = []string{"text"}
		case "video":
			info.Type = "video"
			info.SupportedOutputModalities = []string{"video"}
			info.SupportedInputModalities = []string{"text"}
		default:
			info.SupportedOutputModalities = []string{"text"}
			info.SupportedInputModalities = []string{"text"}
		}
		out = append(out, info)
	}
	return out
}

// candidateMatches keeps only this provider's candidates.
func candidateMatches(candidate pluginapi.SchedulerAuthCandidate) bool {
	provider := strings.TrimSpace(candidate.Provider)
	if provider == "" {
		return true
	}
	return strings.EqualFold(provider, tinkerProviderKey)
}

// candidateKey derives the plugin's account key from a host candidate.
func candidateKey(candidate pluginapi.SchedulerAuthCandidate) string {
	if candidate.Attributes != nil {
		if uid := strings.TrimSpace(candidate.Attributes[tinkerProviderKey+"-uid"]); uid != "" {
			return uid
		}
	}
	if candidate.Metadata != nil {
		if email, okEmail := candidate.Metadata["email"].(string); okEmail {
			if email = strings.TrimSpace(email); email != "" {
				return strings.ToLower(email)
			}
		}
	}
	return strings.TrimSpace(candidate.ID)
}

// nowUnixMilli is the clock used for cooldown comparisons.
func nowUnixMilli() int64 {
	return nowUnix() * 1000
}
