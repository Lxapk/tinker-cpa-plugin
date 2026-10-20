package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// pendingLogin is one in-flight email-OTP login.
type pendingLogin struct {
	Email     string
	CreatedAt time.Time
	// AttemptedAt records when the code was first submitted, so a wrong code
	// returns an error instead of polling forever.
	AttemptedAt time.Time
	LastError   string
	// Creds caches the successful result so a repeated poll returns it too.
	Creds *tinkerCredentials
	// Done marks a login that will never succeed again.
	Done bool
}

var pendingLogins = newPendingLoginStore()

type pendingLoginStore struct {
	mu    sync.Mutex
	items map[string]*pendingLogin
}

func newPendingLoginStore() *pendingLoginStore {
	return &pendingLoginStore{items: map[string]*pendingLogin{}}
}

func (s *pendingLoginStore) put(state string, login *pendingLogin) {
	s.mu.Lock()
	s.items[state] = login
	// Drop anything older than 15 minutes; a login never legitimately takes
	// longer and an unbounded map would leak across a long-running process.
	for key, item := range s.items {
		if time.Since(item.CreatedAt) > 15*time.Minute {
			delete(s.items, key)
		}
	}
	s.mu.Unlock()
}

func (s *pendingLoginStore) get(state string) *pendingLogin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[state]
}

func (s *pendingLoginStore) delete(state string) {
	s.mu.Lock()
	delete(s.items, state)
	s.mu.Unlock()
}

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

/* ---------- auth.login.start ---------- */

// authLoginStart answers auth.login.start: mail a code to the given address.
//
// The email arrives as Metadata["email"] (the panel's login form field), and the
// flow state handed back is what the poll call presents together with the code.
func authLoginStart(request []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	if req.Provider != "" && !strings.EqualFold(req.Provider, tinkerProviderKey) {
		return nil, fmt.Errorf("unsupported provider %q", req.Provider)
	}

	email := strings.TrimSpace(metaString(req.Metadata, "email", "username", "account"))
	if email == "" {
		return nil, errors.New("请填写 Tinker 账号邮箱")
	}
	if !strings.Contains(email, "@") {
		return nil, errors.New("邮箱格式不正确")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resendAfter, errSend := upstream.requestEmailOTP(ctx, email)
	if errSend != nil {
		return nil, errSend
	}

	stateToken := randomToken()
	pendingLogins.put(stateToken, &pendingLogin{
		Email:     email,
		CreatedAt: time.Now(),
	})

	logf("已向 %s 发送登录验证码", email)
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  tinkerProviderKey,
		URL:       tinkerBaseURL,
		State:     stateToken,
		ExpiresAt: resendAfter.Add(14 * time.Minute),
		Metadata: map[string]any{
			"email": email,
			// The panel renders this as the instruction above the input.
			"message": "验证码已发送到 " + email + "，请在下方填入邮件中的 6 位验证码。",
		},
	})
}

/* ---------- auth.login.poll ---------- */

// authLoginPoll answers auth.login.poll: exchange the typed code for tokens.
//
// CPA calls this repeatedly. The code is supplied by the operator through
// Metadata["code"]; until it arrives the poll reports "pending", and a rejected
// code returns an error status with the server's message.
func authLoginPoll(request []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	login := pendingLogins.get(req.State)
	if login == nil {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录会话已过期，请重新发起登录",
		})
	}
	if login.Creds != nil {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status: pluginapi.AuthLoginStatusSuccess,
			Auth:   tinkerAuthData(login.Creds, ""),
		})
	}

	code := strings.TrimSpace(metaString(req.Metadata, "code", "otp", "password"))
	if code == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "等待输入验证码",
		})
	}
	if login.LastError != "" && login.LastError == code {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "验证码不正确，请重新输入",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, errVerify := upstream.verifyEmailOTP(ctx, login.Email, code)
	if errVerify != nil {
		login.AttemptedAt = time.Now()
		login.LastError = code
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "登录失败：" + errVerify.Error(),
		})
	}

	// Resolve the email when the operator typed a different spelling than the
	// account's own address; failure here is not fatal, it only affects a label.
	if resolved, errUser := upstream.currentUser(ctx, creds.AccessToken); errUser == nil && resolved != "" {
		creds.Email = resolved
	}
	creds = normalizeCredentials(creds)
	login.Creds = creds
	state.accounts.observe(creds, "")
	logf("账号 %s 登录成功", creds.label())

	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   tinkerAuthData(creds, ""),
	})
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
func withToken(ctx context.Context, creds *tinkerCredentials, authID string) (*tinkerCredentials, error) {
	if creds == nil {
		return nil, errors.New("缺少凭据")
	}
	skew := time.Duration(state.settings.get().RefreshSkewSeconds) * time.Second
	if !creds.expired(skew) {
		return creds, nil
	}
	if creds.RefreshToken == "" {
		return creds, nil
	}
	next, err := upstream.refresh(ctx, creds)
	if err != nil {
		state.accounts.markBlocked(creds.accountKey(), err.Error())
		return nil, err
	}
	state.accounts.clearBlocked(creds.accountKey())
	persistRefreshedCredential(authID, next)
	return next, nil
}

// httpStatusOf pulls a status out of an error chain for the panel.
func httpStatusOf(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return http.StatusBadGateway
}
