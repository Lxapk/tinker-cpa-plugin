package main

import (
	"encoding/json"
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

/* ---------- scheduler ---------- */

// schedulerPick answers scheduler.pick.
//
// The host hands over the candidates it considers eligible. Returning Handled
// with an AuthID pins one; returning Handled=false defers to the host's own
// round-robin. The plugin pins a credential only when it has a concrete
// preference, so the host's ordering stays in charge the rest of the time:
//
//   - credentials the operator disabled or the upstream blocked are skipped;
//   - credentials inside a cooldown are skipped;
//   - among the rest, the least recently used wins, which spreads load across
//     accounts instead of hammering the first one.
func schedulerPick(request []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	best := ""
	bestUsed := int64(-1)
	for _, candidate := range req.Candidates {
		if !candidateMatches(candidate) {
			continue
		}
		key := candidateKey(candidate)
		record := state.accounts.lookup(key, candidate.ID)
		if record != nil {
			if record.Disabled || record.Blocked {
				continue
			}
			if record.CooldownUntil > nowUnixMilli() {
				continue
			}
			if best == "" || (record.LastUsed < bestUsed || bestUsed < 0) {
				best = candidate.ID
				bestUsed = record.LastUsed
			}
			continue
		}
		// A candidate the plugin has never seen is still usable; prefer it over
		// nothing so a freshly added account is not ignored until first use.
		if best == "" {
			best = candidate.ID
			bestUsed = 0
		}
	}

	if best == "" {
		// Everything is filtered out. Defer to the host so it can surface its own
		// "no available auth" error rather than a plugin-specific one.
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: best, Handled: true})
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
