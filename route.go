package main

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// route.go wires model routing: CPA asks the plugin whether a requested model
// belongs to it, and if so which executor should run it.

// modelRoute answers model.route.
//
// Returning Handled=false for a foreign model is what keeps this plugin from
// swallowing requests meant for CPA's built-in providers. A model this plugin
// knows — by OpenAI name, tool id or underlying model id — is routed here.
func modelRoute(request []byte) ([]byte, error) {
	var req struct {
		RequestedModel string `json:"requested_model"`
		Model          string `json:"model"`
	}
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	model := firstNonEmpty(req.RequestedModel, req.Model)
	if lookupTool(strings.ToLower(strings.TrimSpace(stripProviderPrefix(model)))) == nil {
		return okEnvelope(map[string]any{"handled": false})
	}
	return okEnvelope(map[string]any{
		"handled":        true,
		"target_kind":    "executor",
		"target":         tinkerProviderKey,
		"provider":       tinkerProviderKey,
		"requested_name": model,
	})
}

// stripProviderPrefix removes a "provider/" routing prefix.
func stripProviderPrefix(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

/* ---------- host-driven rotation ---------- */

// hostRotate asks the host for an alternative credential.
//
// The scheduler contract exposes the candidate list to the plugin, but there is
// no "give me the next one" RPC. So rotation reuses host.auth.list and picks the
// first usable credential that is not the one that just failed. When the host
// cannot answer (an older build, or a test process), the caller falls back to the
// plugin's own cache.
func hostRotate(currentAuthID string) (*tinkerCredentials, string, bool) {
	entries := tinkerAuthEntries()
	if len(entries) == 0 {
		return nil, "", false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == currentAuthID || strings.TrimSpace(entry.AuthIndex) == currentAuthID {
			continue
		}
		if entry.Disabled {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(entry.Email))
		if key != "" && !state.accounts.usable(key) {
			continue
		}
		creds := readAuthFileCredentials(entry.Path)
		if creds == nil {
			continue
		}
		if !state.accounts.usable(creds.accountKey()) {
			continue
		}
		return creds, firstNonEmpty(entry.AuthIndex, entry.ID), true
	}
	return nil, "", false
}

// unusedPluginAPIRef keeps the pluginapi import meaningful when a future change
// removes the last direct reference; it also documents the executor target kind.
var _ = pluginapi.ModelRouteTargetKind("")
