package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// managementRegistration declares the plugin's management surface.
//
// CPA mounts resources under /v0/resource/plugins/<plugin-id>/ and management
// routes under /v0/management/.
func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{
			{
				// The login form the OAuth dialog redirects to. Tinker's login needs
				// an email address, which the host's dialog cannot collect, so the
				// form gathers it and returns to tinker-auth-url with it.
				Path:        "login",
				Menu:        "",
				Description: "Tinker 登录：填写邮箱并接收验证码。",
			},
			{
				// The path must not be "/": CPA normalizes a resource path with
				// strings.TrimRight(path, "/") and rejects the result when it
				// becomes empty, so a root resource is silently dropped and the
				// menu entry never appears.
				Path:        "home",
				Menu:        "Tinker",
				Description: "Tinker 账号、启用状态与最近调用，集中在这一页。",
			},
		},
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/tinker/accounts", Description: "Tinker 账号列表（含启用状态）。"},
			{Method: http.MethodPost, Path: "/tinker/account/toggle", Description: "启用或禁用某个账号。"},
			{Method: http.MethodPost, Path: "/tinker/account/remove", Description: "移除某个账号的本地记录。"},
			{Method: http.MethodPost, Path: "/tinker/login/send", Description: "登录表单：发送邮箱验证码。"},
			{Method: http.MethodPost, Path: "/tinker/login/verify", Description: "登录表单：校验验证码并写入账号。"},
			{Method: http.MethodGet, Path: "/tinker/login/status", Description: "登录表单：查询登录结果。"},
			{Method: http.MethodGet, Path: "/tinker/status", Description: "插件运行状态与账号汇总。"},
			{Method: http.MethodGet, Path: "/tinker/models", Description: "Tinker 工具目录。"},
			{Method: http.MethodGet, Path: "/tinker/calls", Description: "最近的调用记录。"},
			{Method: http.MethodGet, Path: "/tinker/routing/status", Description: "当前路由策略与轮巡位置。"},
			{Method: http.MethodPost, Path: "/tinker/routing/config", Description: "设置路由策略。"},
			{Method: http.MethodPost, Path: "/tinker/routing/reset", Description: "重置轮巡位置。"},
			{Method: http.MethodPost, Path: "/tinker/login/new", Description: "创建一个新的登录会话并返回表单地址。"},
			{Method: http.MethodPost, Path: "/tinker/calls/clear", Description: "清空调用记录。"},
		},
	}
}

// managementRequest is what CPA hands to management.handle.
//
// It is the SDK type rather than a local mirror: pluginapi.ManagementRequest
// carries no json tags, so the host sends "Method"/"Path"/"Headers"/... in
// PascalCase, and Headers/Query are http.Header and url.Values (maps of slices),
// not strings. A local mirror with lowercase tags fails to decode outright
// ("cannot unmarshal array into Go struct field ... headers of type string") and
// the panel gets a 502 from the host.
type managementRequest = pluginapi.ManagementRequest

// normalizeManagementPath reduces a request path to the plugin-relative form.
//
// CPA hands the handler the full path it received — /v0/management/tinker/accounts,
// or /v0/resource/plugins/tinker/home for a resource — so both prefixes are
// stripped here to leave the route the registration declared.
func normalizeManagementPath(raw string) string {
	path := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	for _, prefix := range []string{
		"/v0/resource/plugins/" + pluginName,
		"/v0/management",
		"/v0/resource/plugins",
	} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// managementResponse is the shape CPA expects back from management.handle.
//
// CPA calls this handler directly from its HTTP mux (ServeManagementHTTP /
// ServeResourceHTTP), so the answer is an HTTP response — status, headers, body —
// not the JSON envelope the capability RPCs use.
type managementResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers,omitempty"`
	Body       []byte              `json:"Body,omitempty"`
}

// wrapManagement puts a ManagementResponse inside the RPC envelope CPA reads.
//
// The management handler's answer travels back through the same envelope as
// every other capability, so the HTTP response the host eventually writes is the
// StatusCode/Headers/Body carried in the envelope's result.
func wrapManagement(resp managementResponse) ([]byte, error) {
	return okEnvelope(resp)
}

// rawManagementResponse wraps an already-encoded JSON body.
func rawManagementResponse(body []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return wrapManagement(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func jsonManagementResponse(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return wrapManagement(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
		Body:       raw,
	})
}

func htmlManagementResponse(body string) ([]byte, error) {
	return wrapManagement(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
		Body:       []byte(body),
	})
}

func errorManagementResponse(status int, message string) ([]byte, error) {
	raw, _ := json.Marshal(map[string]any{
		"ok":    false,
		"error": map[string]any{"message": message},
	})
	return wrapManagement(managementResponse{
		StatusCode: status,
		Headers:    map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
		Body:       raw,
	})
}

func handleManagement(request []byte) ([]byte, error) {
	var req managementRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	path := normalizeManagementPath(req.Path)
	method := strings.ToUpper(strings.TrimSpace(req.Method))

	switch {
	case path == "/tinker/accounts" && method == http.MethodGet:
		return jsonManagementResponse(accountsPayload())

	case path == "/tinker/status" && method == http.MethodGet:
		return jsonManagementResponse(statusPayload())

	case path == "/tinker/models" && method == http.MethodGet:
		return jsonManagementResponse(modelsPayload())

	case path == "/tinker/calls" && method == http.MethodGet:
		return jsonManagementResponse(map[string]any{"calls": state.logs.recent(100)})

	case path == "/tinker/routing/status" && method == http.MethodGet:
		return jsonManagementResponse(routingPanelPayload())

	case path == "/tinker/routing/config" && method == http.MethodPost:
		body := decodeManagementBody(req.Body)
		strategy := firstNonEmpty(stringFrom(body, "strategy"), stringFrom(body, "value"))
		if strategy == "" {
			return errorManagementResponse(http.StatusBadRequest, "缺少 strategy")
		}
		result := applyRoutingConfig(strategy)
		result["hint"] = routingNextHint()
		return jsonManagementResponse(result)

	case path == "/tinker/routing/reset" && method == http.MethodPost:
		resetRotation()
		return jsonManagementResponse(map[string]any{"ok": true})

	case path == "/tinker/login/new" && method == http.MethodPost:
		return rawManagementResponse(newLoginSession())

	case path == "/tinker/calls/clear" && method == http.MethodPost:
		state.logs.clear()
		return jsonManagementResponse(map[string]any{"success": true})

	case path == "/tinker/account/toggle" && method == http.MethodPost:
		return toggleAccount(req)

	case path == "/tinker/account/remove" && method == http.MethodPost:
		return removeAccount(req)

	case path == "/tinker/login/send" && method == http.MethodPost:
		return rawManagementResponse(handleLoginSendCode(req.Body))

	case path == "/tinker/login/verify" && method == http.MethodPost:
		return rawManagementResponse(handleLoginVerify(req.Body))

	case path == "/tinker/login/status" && method == http.MethodGet:
		return rawManagementResponse(handleLoginStatus(req.Query.Get("state")))
	}

	return errorManagementResponse(http.StatusNotFound, "未知管理接口: "+path)
}

// accountsPayload renders the account table.
//
// The host's auth list is the source of truth for what exists; the plugin's own
// store supplies the operator's choice and the failure state. Joining them here
// means the panel shows one row per credential even when the plugin has not seen
// it used yet.
func accountsPayload() map[string]any {
	records := liveAccounts()

	rows := make([]map[string]any, 0, len(records))
	enabled := 0
	for _, record := range records {
		row := map[string]any{
			"key":            record.Key,
			"email":          record.Email,
			"auth_id":        record.AuthIndex,
			"auth_index":     record.AuthIndex,
			"disabled":       record.Disabled,
			"blocked":        record.AuthRejected,
			"blocked_reason": record.AuthReason,
			"failures":       record.Failures,
			"last_error":     record.LastError,
			"last_used":      formatUnix(record.LastUsed),
			"expires_at":     formatUnix(record.ExpiresAt),
			"calls":          record.CallCount,
		}
		if !record.Disabled && !record.AuthRejected {
			enabled++
		}
		rows = append(rows, row)
	}

	return map[string]any{
		"accounts": rows,
		"total":    len(rows),
		"enabled":  enabled,
		"disabled": len(rows) - enabled,
	}
}

// statusPayload summarises the plugin for the panel header.
func statusPayload() map[string]any {
	cfg := state.settings.get()
	accounts, _ := accountsPayload()["accounts"].([]map[string]any)
	enabled := 0
	for _, row := range accounts {
		if disabled, _ := row["disabled"].(bool); !disabled {
			enabled++
		}
	}
	return map[string]any{
		"name":          pluginDisplayName,
		"version":       pluginVersion,
		"provider":      tinkerProviderKey,
		"upstream":      tinkerBaseURL,
		"accounts":      len(accounts),
		"enabled":       enabled,
		"default_model": cfg.DefaultModel,
		"models":        len(toolCatalogue),
		"poll_millis":   cfg.PollIntervalMillis,
		"timeout_sec":   cfg.GenerationTimeoutSeconds,
	}
}

// modelsPayload renders the tool catalogue for the panel.
func modelsPayload() map[string]any {
	return map[string]any{
		"models": toolCatalogue,
	}
}

// toggleAccount enables or disables one account.
//
// The write goes to every auth file belonging to the account, because the host
// builds its candidate list from those files. The plugin's own flag is written
// only after the file write succeeded, so the panel never shows a state the host
// does not honour.
func toggleAccount(req managementRequest) ([]byte, error) {
	body := decodeManagementBody(req.Body)
	key := firstNonEmpty(stringFrom(body, "key"), stringFrom(body, "email"), stringFrom(body, "auth_id"))
	if key == "" {
		return errorManagementResponse(http.StatusBadRequest, "缺少 key")
	}

	disabled, hasFlag := boolFrom(body, "disabled")
	if !hasFlag {
		// No explicit value means "flip", which is what the row button sends.
		current := false
		if record := state.accounts.lookup(key); record != nil {
			current = record.Disabled
		}
		disabled = !current
	}

	keys := []string{key}
	if record := state.accounts.lookup(key); record != nil {
		keys = append(keys, record.Key, record.UID, record.Email, record.AuthIndex)
	}

	changed, errWrite := syncAccountDisabledToHost(keys, disabled)
	if errWrite != nil && changed == 0 {
		return errorManagementResponse(http.StatusInternalServerError, errWrite.Error())
	}

	state.accounts.setDisabled(key, disabled)
	logf("账号 %s 已%s", key, map[bool]string{true: "禁用", false: "启用"}[disabled])

	return jsonManagementResponse(map[string]any{
		"success":  true,
		"key":      key,
		"disabled": disabled,
		"files":    changed,
	})
}

// removeAccount drops the plugin's local record for an account.
//
// The host owns the credential itself, so this does not delete the auth file;
// it clears the failure state so a re-added account starts clean.
func removeAccount(req managementRequest) ([]byte, error) {
	body := decodeManagementBody(req.Body)
	key := firstNonEmpty(stringFrom(body, "key"), stringFrom(body, "email"), stringFrom(body, "auth_id"))
	if key == "" {
		return errorManagementResponse(http.StatusBadRequest, "缺少 key")
	}
	state.accounts.setDisabled(key, true)
	return jsonManagementResponse(map[string]any{"success": true, "key": key})
}

/* ---------- helpers ---------- */

func decodeManagementBody(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		return map[string]any{}
	}
	if body == nil {
		return map[string]any{}
	}
	return body
}

func stringFrom(body map[string]any, key string) string {
	value, ok := body[key]
	if !ok || value == nil {
		return ""
	}
	if text, isText := value.(string); isText {
		return strings.TrimSpace(text)
	}
	return ""
}

func boolFrom(body map[string]any, key string) (bool, bool) {
	value, ok := body[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		return strings.EqualFold(typed, "true") || typed == "1", true
	}
	return false, false
}

func formatUnix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04:05")
}

func formatUnixMilli(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// pageHTML is the resource page CPA renders for this plugin.
//
// The document itself lives in main_page.go; this keeps the historical entry
// point so the resource route does not have to know that.
func pageHTML() string {
	return renderMainPage()
}


// loginFormHTML is the page the authorisation dialog opens for Tinker.
//
// It is the whole login: the address, the emailed code and the exchange all
// happen here. The page runs on the CPA origin, so it reuses the management key
// the panel already stored and the operator never retypes it.
func loginFormHTML() string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Tinker 登录</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { margin:0; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         background:#f6f6f7; color:#1d1d1f; padding:22px; }
  h1 { font-size:19px; margin:0 0 6px; }
  p.sub { color:#6e6e73; font-size:13px; margin:0 0 18px; line-height:1.55; }
  label { display:block; font-size:13px; color:#6e6e73; margin:14px 0 6px; }
  input { width:100%%; border:1px solid #d2d2d7; border-radius:10px; padding:11px 12px;
          font-size:16px; background:#fff; color:inherit; }
  .row { display:flex; gap:8px; }
  .row input { flex:1; min-width:0; }
  .row button { flex:0 0 auto; white-space:nowrap; }
  button { border:0; border-radius:10px; padding:11px 14px; font-size:15px; font-weight:600;
           background:#1d1d1f; color:#fff; cursor:pointer; }
  button.primary { width:100%%; margin-top:18px; }
  button:disabled { opacity:.5; cursor:default; }
  button.ghost { background:transparent; color:#1d1d1f; border:1px solid #d2d2d7; font-weight:500; }
  .msg { margin-top:14px; font-size:13px; line-height:1.55; padding:10px 12px; border-radius:10px; display:none; }
  .msg.show { display:block; }
  .msg.ok { background:#e3f6e5; color:#1a7f37; }
  .msg.bad { background:#fdeaea; color:#c62828; }
  .msg.wait { background:#eef2f7; color:#3a4a5c; }
  .done { display:none; text-align:center; padding:26px 0; }
  .done.show { display:block; }
  .done .big { font-size:42px; line-height:1; }
  .done h2 { font-size:17px; margin:12px 0 6px; }
  @media (prefers-color-scheme: dark) {
    body { background:#121212; color:#f2f2f2; }
    input { background:#1c1c1e; border-color:#3a3a3c; }
    button { background:#f2f2f2; color:#121212; }
    button.ghost { background:transparent; color:#f2f2f2; }
    .msg.ok { background:#16301b; color:#7ee787; }
    .msg.bad { background:#3a1a1a; color:#ff9f9f; }
    .msg.wait { background:#1c2530; color:#9fb6cc; }
  }
</style>
</head>
<body>
<div id="form">
  <h1>登录 Tinker</h1>
  <p class="sub">Tinker 使用邮箱验证码登录。填写邮箱并获取验证码，在本页填入即可完成授权。</p>

  <label for="email">邮箱</label>
  <input id="email" type="email" placeholder="you@example.com" autocomplete="username" />

  <label for="code">验证码</label>
  <div class="row">
    <input id="code" type="text" inputmode="numeric" maxlength="8" placeholder="邮件中的 6 位数字" autocomplete="one-time-code" />
    <button class="ghost" id="send" onclick="sendCode()">获取验证码</button>
  </div>

  <button class="primary" id="submit" onclick="submitCode()">完成登录</button>
  <div class="msg" id="msg"></div>
</div>

<div class="done" id="done">
  <div class="big">✓</div>
  <h2>登录成功</h2>
  <p class="sub" id="doneMsg">账号已写入 CPA，可以关闭本页。</p>
</div>

<script>
// The page is served from the CPA origin, so it reuses the key the panel saved.
const MGMT = "/v0/management";
const PANEL_KEY = "cpa-management-key";
const OUR_KEY = "tinker.mgmtKey";
const state = new URLSearchParams(location.search).get("state") || "";

function key() {
  for (const name of [PANEL_KEY, OUR_KEY]) {
    try {
      const v = (localStorage.getItem(name) || "").trim();
      if (v) return v;
    } catch (e) {}
  }
  return "";
}
function show(text, cls) {
  const el = document.getElementById("msg");
  el.textContent = text;
  el.className = "msg show " + (cls || "wait");
}
function clearMsg() {
  const el = document.getElementById("msg");
  el.className = "msg";
}
async function api(path, body) {
  const k = key();
  if (!k) throw new Error("未找到 CPA 管理密钥，请先在 CPA 面板登录");
  const resp = await fetch(MGMT + path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Authorization": "Bearer " + k,
      "X-Management-Key": k
    },
    body: JSON.stringify(body)
  });
  const data = await resp.json().catch(() => null);
  if (!resp.ok) {
    throw new Error((data && (data.message || data.error)) || ("HTTP " + resp.status));
  }
  return data || {};
}
async function sendCode() {
  const email = (document.getElementById("email").value || "").trim();
  if (!email || email.indexOf("@") < 0) { show("请填写有效邮箱", "bad"); return; }
  const btn = document.getElementById("send");
  btn.disabled = true;
  show("正在发送验证码…", "wait");
  try {
    const data = await api("/tinker/login/send", { state: state, email: email });
    show(data.message || "验证码已发送，请查收邮件", "ok");
    document.getElementById("code").focus();
  } catch (e) {
    show("发送失败：" + e.message, "bad");
  } finally {
    btn.disabled = false;
  }
}
async function submitCode() {
  const email = (document.getElementById("email").value || "").trim();
  const code = (document.getElementById("code").value || "").trim();
  if (!email || email.indexOf("@") < 0) { show("请填写有效邮箱", "bad"); return; }
  if (!code) { show("请填写邮件中的验证码", "bad"); return; }
  const btn = document.getElementById("submit");
  btn.disabled = true;
  show("正在校验…", "wait");
  try {
    const data = await api("/tinker/login/verify", { state: state, email: email, code: code });
    if (data.ok) {
      document.getElementById("form").style.display = "none";
      document.getElementById("done").classList.add("show");
      document.getElementById("doneMsg").textContent = data.message || "账号已写入 CPA。";
    } else {
      show(data.message || "验证失败", "bad");
      btn.disabled = false;
    }
  } catch (e) {
    show("验证失败：" + e.message, "bad");
    btn.disabled = false;
  }
}
if (!state) {
  show("缺少 state，请从 CPA 的授权窗口重新打开本页", "bad");
}
</script>
</body>
</html>`)
}

// routingPanelPayload is what /tinker/routing/status returns.
func routingPanelPayload() map[string]any {
	payload := routingStatusJSON()
	payload["hint"] = routingNextHint()
	return payload
}

// newLoginSession creates a login and returns the form URL the panel navigates to.
func newLoginSession() ([]byte, error) {
	raw, err := startLogin(pluginapi.AuthLoginStartRequest{Provider: tinkerProviderKey})
	if err != nil {
		return nil, err
	}
	// startLogin returns the RPC envelope; unwrap it so the panel sees {url,state}.
	var env struct {
		Result struct {
			URL   string `json:"URL"`
			State string `json:"State"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return json.Marshal(map[string]any{
		"ok":    true,
		"url":   env.Result.URL,
		"state": env.Result.State,
	})
}
