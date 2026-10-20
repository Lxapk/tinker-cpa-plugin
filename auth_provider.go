package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// This file wires Tinker authentication into CPA's AuthProvider capability.
//
// CPA drives the flow through five RPCs:
//
//	auth.identifier   -> stable provider key
//	auth.parse        -> turn uploaded credential material into an AuthData
//	auth.login.start  -> begin a login, return the user-facing URL + state
//	auth.login.poll   -> poll until success, return the AuthData
//	auth.refresh      -> refresh an existing credential
//
// Tinker has no OAuth redirect: a login is an emailed one-time code. So the
// login flow is modelled as "start sends the code, poll waits for the operator
// to type it in". The panel's login dialog collects the code and posts it as the
// poll's Metadata["code"], which is why the state carries the email address.

const tinkerProviderKey = "tinker"

/* ---------- auth.identifier ---------- */

// authIdentifier answers auth.identifier.
//
// It must return the provider key, not a display spelling: CPA compares this
// value against auth.Provider when deciding which models an auth can reach
// (ModelsForAuth). Returning a pretty name silently empties the account list and
// /v1/models for that provider.
func authIdentifier() ([]byte, error) {
	return okEnvelope(identifierResponse{Identifier: tinkerProviderKey})
}

/* ---------- auth.parse ---------- */

// authParse answers auth.parse: accept credential material the operator pasted
// or uploaded and turn it into a CPA auth record.
func authParse(request []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	// Provider filter: only handle our own provider, let others pass.
	if req.Provider != "" && !strings.EqualFold(req.Provider, tinkerProviderKey) {
		return okEnvelope(map[string]any{"Handled": false})
	}

	creds, errExtract := parseCredentialInput(req.RawJSON)
	if errExtract != nil {
		return okEnvelope(map[string]any{
			"Handled": true,
			"Error":   errExtract.Error(),
		})
	}

	return okEnvelope(map[string]any{
		"Handled": true,
		"Auth":    tinkerAuthData(creds, req.FileName),
	})
}

// tinkerAuthData converts credentials into the host's auth record shape.
func tinkerAuthData(creds *tinkerCredentials, fileName string) pluginapi.AuthData {
	storage, _ := json.Marshal(creds)
	auth := pluginapi.AuthData{
		Provider:    tinkerProviderKey,
		FileName:    fileName,
		Label:       creds.label(),
		StorageJSON: storage,
		Metadata: map[string]any{
			"email":      creds.Email,
			"account_id": creds.AccountID,
		},
		Attributes: map[string]string{tinkerProviderKey + "-uid": creds.accountKey()},
	}
	if auth.FileName == "" {
		auth.FileName = credsFileName(creds)
	}
	// Tell the host when this credential will need a refresh so the pool does
	// not have to guess from an opaque storage blob.
	if creds.ExpiresAt > 0 {
		auth.NextRefreshAfter = time.Unix(creds.ExpiresAt, 0).
			Add(-time.Duration(state.settings.get().RefreshSkewSeconds) * time.Second)
	}
	return auth
}

// credsFileName builds a stable auth file name from the account identity.
func credsFileName(creds *tinkerCredentials) string {
	base := creds.accountKey()
	if base == "" {
		base = "account"
	}
	replacer := strings.NewReplacer("@", "_at_", ".", "_", "/", "_", "\\", "_", ":", "_")
	return tinkerProviderKey + "-" + replacer.Replace(strings.ToLower(base)) + ".json"
}

/* ---------- auth.login.start / poll ---------- */

// authLoginStart answers auth.login.start.
//
// The flow is owned by this plugin's own form page (login_flow.go) because Tinker
// needs an emailed one-time code and the host's dialog has nowhere to collect it.
func authLoginStart(request []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	if req.Provider != "" && !strings.EqualFold(req.Provider, tinkerProviderKey) {
		return errorEnvelope("unsupported_provider",
			"本插件仅支持 Tinker（"+tinkerProviderKey+"）登录", http.StatusBadRequest), nil
	}
	return startLogin(req)
}

// authLoginPoll answers auth.login.poll.
//
// The code is submitted to the plugin's form rather than to the host, so this
// only reports what that form already did.
func authLoginPoll(request []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	session := loginSessions.get(req.State)
	if session == nil {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录会话已过期，请重新发起授权",
		})
	}
	if session.Creds != nil {
		auth := tinkerAuthData(session.Creds, "")
		loginSessions.drop(req.State)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusSuccess,
			Message: "Tinker 账号登录成功",
			Auth:    auth,
		})
	}
	if session.Message != "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: session.Message,
		})
	}
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusPending,
		Message: "请在打开的页面中填写邮箱与验证码",
	})
}


/* ---------- helpers ---------- */

// metaString reads the first non-empty string from a metadata map.
func metaString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok || value == nil {
			continue
		}
		if text, isText := value.(string); isText && strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

// withToken returns a usable access token, refreshing first when the credential
// is near expiry.
//
// The refreshed pair is written back to the auth file before returning: the old
// refresh token dies the moment it is used, so keeping the new one only in memory
// would make the next call fail.
// withToken returns the credential to use for one generation.
//
// CPA owns refreshing. Its auto-refresh loop re-evaluates every 5 seconds against
// NextRefreshAfter and calls auth.refresh when it is due, and the executor is
// handed CPA's current in-memory credential in StorageJSON. There is therefore
// nothing to do here but hand the credential back.
//
// Refreshing here was actively harmful. Tinker's refresh token is single-use: the
// server rotates it on every call, so a second refresh with the spent token
// answers "invalid JWT refresh token". The plugin's on-demand refresh raced CPA's
// scheduled one, lost, and then marked a perfectly good account as blocked — the
// account appeared to die within minutes while the official app kept working
// indefinitely.
func withToken(_ context.Context, creds *tinkerCredentials, _ string) (*tinkerCredentials, error) {
	if creds == nil {
		return nil, errors.New("缺少凭据")
	}
	return creds, nil
}

/* ---------- auth.refresh ---------- */

// authRefresh answers auth.refresh.
//
// Tinker's access token lives 15 minutes, so this runs often. A refresh needs
// the current access token in Authorization even after it expired; the client
// handles that, and here the only job is to persist the new pair.
func authRefresh(request []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	creds, errParse := parseCredentials(req.StorageJSON)
	if errParse != nil {
		return nil, errParse
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	next, errRefresh := upstream.refresh(ctx, creds)
	if errRefresh != nil {
		state.accounts.markBlocked(creds.accountKey(), errRefresh.Error())
		return nil, fmt.Errorf("刷新失败，请重新登录: %w", errRefresh)
	}

	key := creds.accountKey()
	state.accounts.clearBlocked(key)
	state.accounts.observe(next, req.AuthID)
	// The host persists what this call returns, but writing through keeps the
	// file correct even when the host's own save path is unavailable.
	persistRefreshedCredential(req.AuthID, next)

	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth:             tinkerAuthData(next, ""),
		NextRefreshAfter: time.Unix(next.ExpiresAt, 0).Add(-time.Duration(state.settings.get().RefreshSkewSeconds) * time.Second),
	})
}

/* ---------- helpers ---------- */

// metaString reads the first non-empty string from a metadata map.

// withToken returns a usable access token, refreshing first when the credential
// is near expiry.
//
// The refreshed pair is written back to the auth file before returning: the old
// refresh token dies the moment it is used, so keeping the new one only in memory
// would make the next call fail.
// httpStatusOf pulls a status out of an error chain for the panel.
func httpStatusOf(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return http.StatusBadGateway
}
