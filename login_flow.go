package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// This file implements Tinker's login as a self-contained page instead of an
// OAuth round trip.
//
// Why not the OAuth shape the host offers: that flow assumes the browser gets
// redirected back with a code, which the host then forwards to the plugin. Tinker
// has no such redirect — it emails a one-time code and the user types it in. The
// host's dialog has nowhere to put that code, so a login built on it can only
// ever sit at "waiting".
//
// So the plugin owns the whole interaction:
//
//	auth.login.start  -> returns the URL of the plugin's own form
//	the form           sends the code to /tinker/login/verify
//	the plugin         exchanges code -> tokens, stores the credential,
//	                   and marks the pending login successful
//	auth.login.poll    -> reports that result to the host
//
// The page runs on the CPA origin, which also lets it reuse the management key
// the panel already saved, so the operator never retypes it.

// loginSession is one in-flight login, keyed by the state the form carries.
type loginSession struct {
	State     string
	Email     string
	CreatedAt time.Time
	// Status is "waiting" until the form submits a code.
	Status string
	// Creds holds the result once the code was accepted.
	Creds *tinkerCredentials
	// Message is the last user-facing error, if any.
	Message string
}

var loginSessions = &loginSessionStore{items: map[string]*loginSession{}}

type loginSessionStore struct {
	mu    sync.Mutex
	items map[string]*loginSession
}

func (s *loginSessionStore) put(session *loginSession) {
	s.mu.Lock()
	s.items[session.State] = session
	for key, item := range s.items {
		if time.Since(item.CreatedAt) > 30*time.Minute {
			delete(s.items, key)
		}
	}
	s.mu.Unlock()
}

func (s *loginSessionStore) get(state string) *loginSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[strings.TrimSpace(state)]
}

func (s *loginSessionStore) update(state string, mutate func(*loginSession)) {
	s.mu.Lock()
	if session, ok := s.items[strings.TrimSpace(state)]; ok {
		mutate(session)
	}
	s.mu.Unlock()
}

func (s *loginSessionStore) drop(state string) {
	s.mu.Lock()
	delete(s.items, strings.TrimSpace(state))
	s.mu.Unlock()
}

/* ---------- auth.login.start ---------- */

// startLogin issues a session and hands back the form URL.
//
// The host gives no way to collect an address before the flow starts, so the
// address is collected by the form. When the operator did supply one (some
// builds forward query parameters as Metadata), the code is sent immediately and
// the form only asks for the code.
func startLogin(req pluginapi.AuthLoginStartRequest) ([]byte, error) {
	state := randomToken()
	session := &loginSession{
		State:     state,
		CreatedAt: time.Now(),
		Status:    "waiting",
	}

	email := strings.TrimSpace(metaString(req.Metadata, "email", "username", "account"))
	if email != "" && strings.Contains(email, "@") {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, errSend := upstream.requestEmailOTP(ctx, email); errSend != nil {
			return errorEnvelope("login_start_failed", "发送验证码失败: "+errSend.Error(), http.StatusBadGateway), nil
		}
		session.Email = email
		logf("已向 %s 发送登录验证码", email)
	}

	loginSessions.put(session)

	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  tinkerProviderKey,
		URL:       loginPageURL(state),
		State:     state,
		ExpiresAt: time.Now().Add(30 * time.Minute),
		Metadata: map[string]any{
			"display_name": pluginDisplayName,
			"hint":         "在打开的页面中填写邮箱与验证码，完成后回到本窗口点确认即可。",
			"state":        state,
		},
	})
}

// loginPageURL is always relative so a remote browser resolves it against the
// origin it is already using; the host's own base is 127.0.0.1 on a server.
func loginPageURL(state string) string {
	return "/v0/resource/plugins/" + pluginName + "/login?state=" + state
}

/* ---------- the form's endpoints ---------- */

// loginSendCodeRequest is posted by the form to mail a code.
type loginSendCodeRequest struct {
	State string `json:"state"`
	Email string `json:"email"`
}

// handleLoginSendCode mails a code for an existing session.
func handleLoginSendCode(body []byte) ([]byte, error) {
	var req loginSendCodeRequest
	if errUnmarshal := json.Unmarshal(body, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	email := strings.TrimSpace(req.Email)
	if !strings.Contains(email, "@") {
		return loginResultJSON(false, "请填写有效邮箱", "")
	}
	session := loginSessions.get(req.State)
	if session == nil {
		return loginResultJSON(false, "登录会话已过期，请重新从 CPA 发起授权", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, errSend := upstream.requestEmailOTP(ctx, email); errSend != nil {
		return loginResultJSON(false, "发送失败："+errSend.Error(), "")
	}

	loginSessions.update(req.State, func(s *loginSession) {
		s.Email = email
		s.Message = ""
	})
	logf("已向 %s 发送登录验证码", email)
	return loginResultJSON(true, "验证码已发送，请查收邮件", req.State)
}

// loginVerifyRequest is posted by the form with the typed code.
type loginVerifyRequest struct {
	State string `json:"state"`
	Email string `json:"email"`
	Code  string `json:"code"`
}

// handleLoginVerify exchanges the code and stores the credential.
//
// This is the step that makes the login actually finish: the tokens are written
// through the host here, so the account is already in CPA's store by the time
// the host's next poll runs.
func handleLoginVerify(body []byte) ([]byte, error) {
	var req loginVerifyRequest
	if errUnmarshal := json.Unmarshal(body, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return loginResultJSON(false, "请填写验证码", "")
	}
	session := loginSessions.get(req.State)
	if session == nil {
		return loginResultJSON(false, "登录会话已过期，请重新从 CPA 发起授权", "")
	}

	email := strings.TrimSpace(req.Email)
	if email == "" {
		email = session.Email
	}
	if !strings.Contains(email, "@") {
		return loginResultJSON(false, "请填写有效邮箱", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	creds, errVerify := upstream.verifyEmailOTP(ctx, email, code)
	if errVerify != nil {
		loginSessions.update(req.State, func(s *loginSession) { s.Message = errVerify.Error() })
		return loginResultJSON(false, errVerify.Error(), "")
	}

	// The account's own address wins: the operator may have typed a spelling
	// alias, and the label in the panel should match the server.
	if resolved, errUser := upstream.currentUser(ctx, creds.AccessToken); errUser == nil && resolved != "" {
		creds.Email = resolved
	}
	creds = normalizeCredentials(creds)

	auth := tinkerAuthData(creds, "")
	persistErr := saveAuthToHost(auth.FileName, mustJSON(creds))
	if persistErr != nil {
		logf("写入 CPA 账号存储失败: %v", persistErr)
	}

	// A fresh login is proof the credential works: clear anything an earlier
	// failure left behind, or the account would show "需重新登录" the moment it
	// appears.
	state.accounts.clearBlocked(creds.accountKey())
	state.accounts.observe(creds, auth.FileName)

	loginSessions.update(req.State, func(s *loginSession) {
		s.Creds = creds
		s.Email = creds.Email
		s.Status = "success"
		s.Message = ""
	})
	logf("账号 %s 登录成功", creds.label())

	message := "登录成功，回到 CPA 窗口点确认即可"
	if persistErr != nil {
		message = "登录成功，但写入 CPA 账号存储失败：" + persistErr.Error()
	}
	return loginResultJSON(true, message, req.State)
}

// handleLoginStatus reports a session's outcome to the form.
func handleLoginStatus(stateValue string) ([]byte, error) {
	session := loginSessions.get(stateValue)
	if session == nil {
		return loginResultJSON(false, "登录会话已过期", "")
	}
	payload := map[string]any{
		"ok":     session.Creds != nil,
		"state":  session.State,
		"email":  session.Email,
		"status": session.Status,
	}
	if session.Creds != nil {
		payload["message"] = "登录成功"
	} else if session.Message != "" {
		payload["message"] = session.Message
	}
	return json.Marshal(payload)
}

// loginResultJSON renders a form response.
func loginResultJSON(ok bool, message, stateValue string) ([]byte, error) {
	payload := map[string]any{"ok": ok, "message": message}
	if stateValue != "" {
		payload["state"] = stateValue
	}
	return json.Marshal(payload)
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
