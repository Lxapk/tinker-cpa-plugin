package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
			{Method: http.MethodGet, Path: "/tinker/status", Description: "插件运行状态与账号汇总。"},
			{Method: http.MethodGet, Path: "/tinker/models", Description: "Tinker 工具目录。"},
			{Method: http.MethodGet, Path: "/tinker/calls", Description: "最近的调用记录。"},
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

	case path == "/tinker/calls/clear" && method == http.MethodPost:
		state.logs.clear()
		return jsonManagementResponse(map[string]any{"success": true})

	case path == "/tinker/account/toggle" && method == http.MethodPost:
		return toggleAccount(req)

	case path == "/tinker/account/remove" && method == http.MethodPost:
		return removeAccount(req)
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
	records := state.accounts.list()
	byKey := map[string]*accountRecord{}
	for idx := range records {
		byKey[strings.ToLower(records[idx].Key)] = &records[idx]
	}

	rows := make([]map[string]any, 0)
	seen := map[string]bool{}

	for _, entry := range tinkerAuthEntries() {
		creds := readAuthFileCredentials(entry.Path)
		key := ""
		email := strings.TrimSpace(entry.Email)
		if creds != nil {
			key = creds.accountKey()
			if creds.Email != "" {
				email = creds.Email
			}
		}
		if key == "" {
			key = strings.ToLower(email)
		}
		if key == "" {
			key = strings.TrimSpace(entry.ID)
		}
		if key == "" {
			continue
		}
		seen[strings.ToLower(key)] = true

		row := map[string]any{
			"key":        key,
			"email":      email,
			"auth_id":    entry.ID,
			"auth_index": entry.AuthIndex,
			"name":       entry.Name,
			"disabled":   entry.Disabled,
			"blocked":    false,
		}
		if record, ok := byKey[strings.ToLower(key)]; ok {
			row["disabled"] = record.Disabled
			row["blocked"] = record.Blocked
			row["blocked_reason"] = record.BlockedWhy
			row["failures"] = record.Failures
			row["last_error"] = record.LastError
			row["last_used"] = formatUnix(record.LastUsed)
			row["cooldown_until"] = formatUnixMilli(record.CooldownUntil)
		}
		if creds != nil {
			row["expires_at"] = formatUnix(creds.ExpiresAt)
		}
		rows = append(rows, row)
	}

	// Accounts the plugin knows about but the host no longer lists (a file was
	// deleted outside the panel) still deserve a row, marked as orphaned —
	// unless nothing identifies the account, in which case the row would be a
	// nameless ghost left over from an earlier failed import.
	for _, record := range records {
		if seen[strings.ToLower(record.Key)] {
			continue
		}
		if strings.TrimSpace(record.Email) == "" && strings.TrimSpace(record.AuthIndex) == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"key":            record.Key,
			"email":          record.Email,
			"auth_id":        record.AuthIndex,
			"disabled":       record.Disabled,
			"blocked":        record.Blocked,
			"blocked_reason": record.BlockedWhy,
			"failures":       record.Failures,
			"last_error":     record.LastError,
			"last_used":      formatUnix(record.LastUsed),
			"orphaned":       true,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		left, _ := rows[i]["email"].(string)
		right, _ := rows[j]["email"].(string)
		return strings.ToLower(left) < strings.ToLower(right)
	})

	enabled := 0
	for _, row := range rows {
		if disabled, _ := row["disabled"].(bool); !disabled {
			enabled++
		}
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
// It is deliberately a single self-contained page: CPA serves it from
// /v0/resource/plugins/<id>/home, and every number it shows comes from the JSON
// routes above, so the panel and the API can never disagree.
func pageHTML() string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Tinker</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         background:#f6f6f7; color:#1d1d1f; }
  header { padding:20px 18px 12px; background:#fff; border-bottom:1px solid #e6e6e8; }
  h1 { margin:0 0 4px; font-size:19px; }
  .sub { color:#6e6e73; font-size:13px; }
  .cards { display:flex; gap:10px; padding:14px 18px; flex-wrap:wrap; }
  .card { flex:1 1 96px; background:#fff; border:1px solid #e6e6e8; border-radius:12px; padding:12px; }
  .card b { display:block; font-size:20px; }
  .card span { color:#6e6e73; font-size:12px; }
  section { padding:0 18px 24px; }
  h2 { font-size:14px; text-transform:uppercase; letter-spacing:.04em; color:#6e6e73; margin:18px 0 8px; }
  .row { display:flex; align-items:center; gap:12px; background:#fff; border:1px solid #e6e6e8;
         border-radius:12px; padding:12px; margin-bottom:8px; }
  .row .who { flex:1; min-width:0; }
  .row .mail { font-size:14px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .row .meta { color:#6e6e73; font-size:12px; margin-top:2px; }
  .pill { font-size:11px; padding:2px 8px; border-radius:999px; }
  .on { background:#e3f6e5; color:#1a7f37; }
  .off { background:#fdeaea; color:#c62828; }
  button { border:1px solid #d2d2d7; background:#fff; border-radius:8px; padding:6px 12px;
           font-size:13px; cursor:pointer; }
  button:hover { background:#f0f0f2; }
  table { width:100%%; border-collapse:collapse; font-size:13px; }
  th,td { text-align:left; padding:8px 6px; border-bottom:1px solid #ececee; }
  th { color:#6e6e73; font-weight:500; font-size:12px; }
  .err { color:#c62828; }
  .empty { color:#6e6e73; font-size:13px; padding:12px; }
  @media (prefers-color-scheme: dark) {
    body { background:#121212; color:#f2f2f2; }
    header,.card,.row { background:#1c1c1e; border-color:#2c2c2e; }
    button { background:#1c1c1e; color:#f2f2f2; border-color:#3a3a3c; }
    th,td { border-color:#2c2c2e; }
  }
</style>
</head>
<body>
<header>
  <h1>Tinker</h1>
  <div class="sub" id="sub">加载中…</div>
</header>
<div class="cards">
  <div class="card"><b id="c-total">-</b><span>账号</span></div>
  <div class="card"><b id="c-enabled">-</b><span>启用</span></div>
  <div class="card"><b id="c-models">-</b><span>模型</span></div>
</div>
<section>
  <h2>账号</h2>
  <div id="accounts"><div class="empty">加载中…</div></div>
</section>
<section>
  <h2>最近调用</h2>
  <div id="calls"><div class="empty">加载中…</div></div>
</section>
<script>
const base = "/v0/management";
async function api(path, opts) {
  const resp = await fetch(base + path, Object.assign({headers:{"Content-Type":"application/json"}}, opts));
  const body = await resp.json();
  if (!body.ok) throw new Error((body.error && body.error.message) || "请求失败");
  return body.result;
}
function esc(s) { return String(s == null ? "" : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
async function loadStatus() {
  const st = await api("/tinker/status");
  document.getElementById("sub").textContent = st.name + " · " + st.version + " · " + st.upstream;
  document.getElementById("c-total").textContent = st.accounts;
  document.getElementById("c-enabled").textContent = st.enabled;
  document.getElementById("c-models").textContent = st.models;
}
async function loadAccounts() {
  const data = await api("/tinker/accounts");
  const host = document.getElementById("accounts");
  if (!data.accounts.length) { host.innerHTML = '<div class="empty">还没有账号。在 CPA 的登录页用 Tinker 邮箱登录。</div>'; return; }
  host.innerHTML = data.accounts.map(a => {
    const on = !a.disabled;
    const flags = [];
    if (a.blocked) flags.push('<span class="pill off">需重新登录</span>');
    if (a.orphaned) flags.push('<span class="pill off">文件已删除</span>');
    return '<div class="row">' +
      '<div class="who"><div class="mail">' + esc(a.email || a.key) + '</div>' +
      '<div class="meta">' + (a.last_used ? '最近使用 ' + esc(a.last_used) : '尚未使用') +
        (a.last_error ? ' · <span class="err">' + esc(a.last_error) + '</span>' : '') + '</div></div>' +
      flags.join('') +
      '<span class="pill ' + (on ? 'on' : 'off') + '">' + (on ? '已启用' : '已禁用') + '</span>' +
      '<button data-key="' + esc(a.key) + '" data-disabled="' + (on ? "true" : "false") + '">' + (on ? '禁用' : '启用') + '</button>' +
      '</div>';
  }).join("");
  host.querySelectorAll("button").forEach(btn => {
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await api("/tinker/account/toggle", {method:"POST", body: JSON.stringify({
          key: btn.dataset.key, disabled: btn.dataset.disabled === "true" })});
        await Promise.all([loadStatus(), loadAccounts()]);
      } catch (e) { alert(e.message); btn.disabled = false; }
    });
  });
}
async function loadCalls() {
  const data = await api("/tinker/calls");
  const host = document.getElementById("calls");
  if (!data.calls.length) { host.innerHTML = '<div class="empty">还没有调用记录。</div>'; return; }
  host.innerHTML = '<table><thead><tr><th>时间</th><th>账号</th><th>模型</th><th>状态</th><th>耗时</th></tr></thead><tbody>' +
    data.calls.map(c => '<tr><td>' + esc(c.time) + '</td><td>' + esc(c.account) + '</td><td>' + esc(c.model) +
      '</td><td class="' + (c.status === 'ok' ? '' : 'err') + '">' + esc(c.status) +
      '</td><td>' + (c.duration_ms || 0) + 'ms</td></tr>').join("") +
    '</tbody></table>';
}
(async () => {
  try { await Promise.all([loadStatus(), loadAccounts(), loadCalls()]); }
  catch (e) { document.getElementById("sub").textContent = "加载失败：" + e.message; }
})();
</script>
</body>
</html>`)
}
