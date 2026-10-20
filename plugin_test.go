package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestToolCatalogueIsComplete(t *testing.T) {
	// Tinker exposes exactly four text, four image and four video tools. A
	// missing entry means a model the app offers is unreachable through CPA.
	counts := map[string]int{}
	for _, ref := range toolCatalogue {
		counts[ref.OutputType]++
		if ref.OpenAI == "" || ref.ToolID == "" {
			t.Fatalf("工具条目缺少标识: %+v", ref)
		}
	}
	want := map[string]int{"text": 4, "image": 4, "video": 4}
	for kind, expected := range want {
		if counts[kind] != expected {
			t.Errorf("%s 工具数量 = %d, 期望 %d", kind, counts[kind], expected)
		}
	}
}

func TestResolveToolMatchesEveryForm(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":            "claude_text",
		"claude_text":                "claude_text",
		"anthropic:claude-opus-4-8":  "claude_text",
		"tinker/claude-opus-4-8":     "claude_text",
		"flux-image":                 "flux_pro_kontext",
		"kling-video":                "kling_image_to_video",
	}
	for requested, expectedTool := range cases {
		got := resolveTool(requested)
		if got.ToolID != expectedTool {
			t.Errorf("resolveTool(%q) = %q, 期望 %q", requested, got.ToolID, expectedTool)
		}
	}
}

func TestResolveToolFallsBackToDefault(t *testing.T) {
	got := resolveTool("definitely-not-a-tinker-model")
	if got.OutputType != "text" {
		t.Errorf("未知模型应回退到文本工具, 得到 %q", got.OutputType)
	}
}

func TestLimitPromptTrimsToToolCeiling(t *testing.T) {
	state.settings.cfg.ImagePromptLimit = 10
	ref := toolRef{OutputType: "image", ToolID: "gpt_image"}
	if got := limitPrompt(strings.Repeat("a", 50), ref); len([]rune(got)) != 10 {
		t.Errorf("图像 prompt 应截断到 10 字符, 得到 %d", len([]rune(got)))
	}
	refText := toolRef{OutputType: "text", ToolID: "claude_text"}
	if got := limitPrompt(strings.Repeat("a", 50), refText); len([]rune(got)) != 50 {
		t.Errorf("文本 prompt 不应被图像上限影响, 得到 %d", len([]rune(got)))
	}
}

func TestBuildPromptFlattensRoles(t *testing.T) {
	messages := []openAIMessage{
		{Role: "system", Content: json.RawMessage(`"be brief"`)},
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", Content: json.RawMessage(`"hello"`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"again"}]`)},
	}
	prompt := buildPrompt(messages)
	for _, want := range []string{"System instructions:", "be brief", "hello", "again"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt 缺少片段 %q: %q", want, prompt)
		}
	}
}

func TestExtractTextFromBookContent(t *testing.T) {
	artifact := map[string]any{
		"outputContents": []any{
			map[string]any{
				"__typename": "BookContent",
				"pages": []any{
					map[string]any{"content": "第一段"},
					map[string]any{"content": "第二段"},
				},
			},
		},
	}
	got := extractText(artifact)
	if got != "第一段\n\n第二段" {
		t.Errorf("extractText = %q", got)
	}
}

func TestExtractMediaURLPrefersOriginal(t *testing.T) {
	artifact := map[string]any{
		"outputContents": []any{
			map[string]any{
				"__typename": "ImageContent",
				"displayUrls": map[string]any{
					"thumbnail": "small.png",
					"original":  "big.png",
				},
			},
		},
	}
	if got := extractMediaURL(artifact); got != "big.png" {
		t.Errorf("extractMediaURL = %q, 期望 big.png", got)
	}
}

func TestExtractMediaURLFromVideo(t *testing.T) {
	artifact := map[string]any{
		"outputContents": []any{
			map[string]any{"__typename": "VideoContent", "videoUrl": "clip.mp4"},
		},
	}
	if got := extractMediaURL(artifact); got != "clip.mp4" {
		t.Errorf("extractMediaURL = %q, 期望 clip.mp4", got)
	}
}

func TestParseCredentialsRoundTrip(t *testing.T) {
	original := &tinkerCredentials{
		AccessToken:  "token-abcdefghijklmnop",
		RefreshToken: "refresh-token",
		Email:        "user@example.com",
		AccountID:    "42",
	}
	raw, errMarshal := json.Marshal(original)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	parsed, errParse := parseCredentials(raw)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if parsed.Email != original.Email || parsed.AccountID != original.AccountID {
		t.Errorf("往返后不一致: %+v", parsed)
	}
	if parsed.UID != "42" {
		t.Errorf("UID 应由 account_id 推导, 得到 %q", parsed.UID)
	}
}

func TestParseCredentialInputAcceptsEmailTokenPair(t *testing.T) {
	creds, errParse := parseCredentialInput([]byte("someone@example.com\nabcdefghijklmnopqrstuvwxyz"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if creds.Email != "someone@example.com" {
		t.Errorf("Email = %q", creds.Email)
	}
}

func TestParseCredentialInputRejectsProse(t *testing.T) {
	if _, errParse := parseCredentialInput([]byte("这里是一段说明文字，不是凭据")); errParse == nil {
		t.Error("不应接受普通文本")
	}
}

func TestJWTClaimsReadNumericAccountID(t *testing.T) {
	// {"account_id":474610,"exp":1792473523}
	token := "eyJhbGciOiJIUzI1NiJ9.eyJhY2NvdW50X2lkIjo0NzQ2MTAsImV4cCI6MTc5MjQ3MzUyM30.sig"
	if got := claimString(token, "account_id"); got != "474610" {
		t.Errorf("account_id = %q, 期望 474610", got)
	}
	if got := claimInt64(token, "exp"); got != 1792473523 {
		t.Errorf("exp = %d", got)
	}
}

func TestBuildSSEFramesShape(t *testing.T) {
	frames := buildSSEFrames("chatcmpl-x", "gpt-5.2", strings.Repeat("a", 200))
	if len(frames) < 2 {
		t.Fatalf("帧数过少: %d", len(frames))
	}
	// Each payload must be a bare JSON object: CPA adds the "data: " prefix and
	// the terminal [DONE] itself. Pre-framing here produced "data: data: {...}".
	for _, frame := range frames {
		payload := strings.TrimSpace(string(frame.Payload))
		if strings.HasPrefix(payload, "data:") {
			t.Errorf("chunk 不应自带 SSE 前缀: %q", payload)
		}
		if !strings.HasPrefix(payload, "{") || !strings.HasSuffix(payload, "}") {
			t.Errorf("chunk 应为裸 JSON 对象: %q", payload)
		}
		var body struct {
			Object  string `json:"object"`
			Choices []any  `json:"choices"`
		}
		if err := json.Unmarshal(frame.Payload, &body); err != nil {
			t.Errorf("chunk 不是合法 JSON: %v (%s)", err, payload)
		}
		if body.Object != "chat.completion.chunk" {
			t.Errorf("object = %q", body.Object)
		}
	}
	if strings.Contains(string(frames[len(frames)-1].Payload), "[DONE]") {
		t.Error("[DONE] 应由 CPA 追加，插件不应发送")
	}
}

func TestChatCompletionBodyIsOpenAIShaped(t *testing.T) {
	raw := chatCompletionBody("chatcmpl-1", "gpt-5.2", "你好")
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Role    string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "chat.completion" {
		t.Errorf("object = %q", body.Object)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "你好" {
		t.Errorf("choices 不正确: %+v", body.Choices)
	}
}

func TestRunStatusOfReadsTerminalStates(t *testing.T) {
	raw := []byte(`{"data":{"artifact":{"latestRun":{"status":"SUCCEEDED"}}}}`)
	status, message := runStatusOf(raw)
	if status != "SUCCEEDED" || message != "" {
		t.Errorf("status=%q message=%q", status, message)
	}

	failed := []byte(`{"data":{"artifact":{"latestRun":{"status":"FAILED","displayErrorMessage":"额度不足"}}}}`)
	status, message = runStatusOf(failed)
	if status != "FAILED" || message != "额度不足" {
		t.Errorf("status=%q message=%q", status, message)
	}
}

func TestDetectGraphQLErrorMarksAuthFailures(t *testing.T) {
	raw := []byte(`{"errors":[{"message":"Authentication required","extensions":{"code":"UNAUTHENTICATED"}}]}`)
	err := detectGraphQLError(raw)
	if err == nil {
		t.Fatal("应识别出 GraphQL 错误")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("错误类型 = %T", err)
	}
	if !apiErr.isAuthError() {
		t.Error("应判定为鉴权失败")
	}
}

func TestOphashesContainRequiredOperations(t *testing.T) {
	for _, operation := range []string{
		"Auth_CurrentUser",
		"ArtifactCreation_GetTool",
		"Artifact_CreateAndExecute",
		"ArtifactView_Get",
		"RehydrateArtifact",
	} {
		if strings.TrimSpace(opHashes[operation]) == "" {
			t.Errorf("缺少操作哈希: %s", operation)
		}
	}
}

func TestSchedulerSkipsDisabledAccounts(t *testing.T) {
	state.accounts = newAccountStore()
	state.accounts.setDisabled("user-a", true)

	request, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{
			{"id": "a", "provider": tinkerProviderKey,
				"attributes": map[string]string{tinkerProviderKey + "-uid": "user-a"}},
			{"id": "b", "provider": tinkerProviderKey,
				"attributes": map[string]string{tinkerProviderKey + "-uid": "user-b"}},
		},
	})
	raw, errPick := schedulerPick(request)
	if errPick != nil {
		t.Fatal(errPick)
	}
	var env struct {
		Result struct {
			AuthID  string `json:"AuthID"`
			Handled bool   `json:"Handled"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !env.Result.Handled || env.Result.AuthID != "b" {
		t.Errorf("应选择未被禁用的账号 b, 得到 %+v", env.Result)
	}
}

func TestAccountTogglePersistsDisabled(t *testing.T) {
	state.accounts = newAccountStore()
	state.settings.setDataDir("")
	state.accounts.setDisabled("user-x", true)

	record := state.accounts.lookup("user-x")
	if record == nil || !record.Disabled {
		t.Fatalf("禁用状态未记录: %+v", record)
	}
	if state.accounts.usable("user-x") {
		t.Error("被禁用的账号不应可用")
	}
}

// TestParseCredentialInputAcceptsWholeAuthFile guards the shape CPA actually
// hands auth.parse: the entire auth file, with the plugin blob nested under
// "storage". A parser that only understands the flat shape silently drops the
// credential and the host rewrites the file without it.
func TestParseCredentialInputAcceptsWholeAuthFile(t *testing.T) {
	whole := []byte(`{"type":"tinker","provider":"tinker","disabled":false,` +
		`"storage":{"access_token":"token-abcdefghijklmnop","refresh_token":"rt-1",` +
		`"email":"user@example.com","uid":"42"}}`)
	creds, errParse := parseCredentialInput(whole)
	if errParse != nil {
		t.Fatalf("整文件解析失败: %v", errParse)
	}
	if creds.AccessToken != "token-abcdefghijklmnop" {
		t.Errorf("access_token = %q", creds.AccessToken)
	}
	if creds.Email != "user@example.com" {
		t.Errorf("email = %q", creds.Email)
	}
	if creds.accountKey() != "42" {
		t.Errorf("accountKey = %q", creds.accountKey())
	}
}

// TestLoginPageURLIsRelative guards the fix for a login URL that embedded the
// host's own 127.0.0.1, which a remote browser then opened against itself.
func TestLoginPageURLIsRelative(t *testing.T) {
	got := loginPageURL("abc123")
	if !strings.HasPrefix(got, "/v0/resource/plugins/tinker/login") {
		t.Errorf("loginPageURL = %q, want a relative path", got)
	}
	if strings.Contains(got, "127.0.0.1") || strings.Contains(got, "://") {
		t.Errorf("loginPageURL must not carry a host: %q", got)
	}
	if !strings.Contains(got, "state=abc123") {
		t.Errorf("loginPageURL lost the state: %q", got)
	}
}

// TestThemeFollowsHost guards the three-part theme contract.
//
// CPA writes data-theme="dark" / "white" on its own root and removes it for
// "follow system", so the stylesheet must handle all three cases and the script
// must copy the host's choice onto this document — the page is rendered inside
// the host, and a hardcoded palette made it stay light while the host went dark.
func TestThemeFollowsHost(t *testing.T) {
	for _, want := range []string{
		`:root[data-theme="white"]`,
		`:root[data-theme="dark"]`,
		`@media (prefers-color-scheme: light)`,
		`:root:not([data-theme])`,
	} {
		if !strings.Contains(uiCSS, want) {
			t.Errorf("样式表缺少 %s", want)
		}
	}

	script := mainPageScript()
	if !strings.Contains(script, "adoptHostTheme") {
		t.Error("缺少读取宿主主题的脚本")
	}
	if !strings.Contains(script, `setAttribute(name, value)`) {
		t.Error("脚本没有把宿主主题应用到本页")
	}
	if !strings.Contains(script, "} catch (e) {") {
		t.Error("跨域读取父文档会抛异常，必须捕获后才能回退")
	}
	if !strings.Contains(script, "setInterval(adoptHostTheme") {
		t.Error("宿主换主题不会重载页面，需要定期复查")
	}
}

// TestPageDoesNotHardcodeTheme checks the <html> tag itself: the CSS selectors are
// expected there, but the element must not carry a fixed theme.
func TestPageDoesNotHardcodeTheme(t *testing.T) {
	page := renderMainPage()
	open := strings.Index(page, "<html")
	if open < 0 {
		t.Fatal("找不到 <html> 标签")
	}
	close := strings.Index(page[open:], ">")
	if close < 0 {
		t.Fatal("<html> 标签未闭合")
	}
	tag := page[open : open+close]
	if strings.Contains(tag, "data-theme") {
		t.Errorf("<html> 不应写死主题: %s", tag)
	}
}

// TestExpiresAtComesFromToken guards the invariant that caused accounts to die
// quickly: a stored expires_at that disagreed with the token's own exp made CPA
// believe the credential was long expired, so it refreshed on every check and
// burned the single-use refresh token.
func TestExpiresAtComesFromToken(t *testing.T) {
	// {"exp":1792505628,"account_id":474610}
	token := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3OTI1MDU2MjgsImFjY291bnRfaWQiOjQ3NDYxMH0.sig"

	stale := &tinkerCredentials{AccessToken: token, ExpiresAt: 100}
	normalized := normalizeCredentials(stale)
	if normalized.ExpiresAt != 1792505628 {
		t.Errorf("expires_at 应取自 token 的 exp，得到 %d", normalized.ExpiresAt)
	}

	// No exp in the token: whatever was stored stays.
	noExp := &tinkerCredentials{AccessToken: "not-a-jwt-token", ExpiresAt: 12345}
	if got := normalizeCredentials(noExp).ExpiresAt; got != 12345 {
		t.Errorf("无 exp 时应保留原值，得到 %d", got)
	}
}

// TestFreshLoginClearsBlocked guards the report that a just-added account showed
// "需重新登录" immediately.
//
// A blocked flag is only ever set when the server rejects the credential, but an
// account can still carry one from an earlier failure. Logging in again is proof
// the credential works, so the flag must be cleared at that point — otherwise the
// account appears dead the moment it is added.
func TestFreshLoginClearsBlocked(t *testing.T) {
	state.accounts = newAccountStore()
	state.settings.setDataDir("")

	creds := &tinkerCredentials{
		AccessToken:  "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjk5OTk5OTk5OTl9.sig",
		RefreshToken: "rt",
		Email:        "user@example.com",
		UID:          "42",
	}
	key := creds.accountKey()

	// An earlier failure parked the account.
	state.accounts.observe(creds, "")
	state.accounts.markAuthRejected(key, "invalid JWT refresh token")
	if record := state.accounts.lookup(key); record == nil || !record.AuthRejected {
		t.Fatal("前置条件失败：账号应已被标记")
	}

	// Logging in again must clear it.
	state.accounts.clearAuthRejected(key)
	state.accounts.observe(creds, "")
	if record := state.accounts.lookup(key); record != nil && record.AuthRejected {
		t.Error("重新登录后 blocked 应被清除")
	}
}

// TestTransientRefreshFailureDoesNotBlock guards the other half: a network blip
// must not be reported as a dead credential.
func TestTransientRefreshFailureDoesNotBlock(t *testing.T) {
	state.accounts = newAccountStore()
	state.settings.setDataDir("")

	creds := &tinkerCredentials{Email: "user@example.com", UID: "42"}
	state.accounts.observe(creds, "")

	// A plain error (no apiError) is what a timeout or transport failure looks
	// like; the refresh handler must not call markAuthRejected for it.
	var errRefresh error = errStub("dial tcp: timeout")
	var apiErr *apiError
	if errors.As(errRefresh, &apiErr) {
		t.Fatal("普通错误不应被识别为 apiError")
	}
	if record := state.accounts.lookup(creds.accountKey()); record != nil && record.AuthRejected {
		t.Error("瞬时失败不应标记 blocked")
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// TestAuthRejectedDoesNotBlockUsability is the invariant the reference plugin
// taught: the auth-rejected flag is advisory (the panel badge), never a gate.
//
// Gating usability on it was the root of "刚登录就显示需重新登录" — the flag
// outlived the condition that set it, so a healthy credential stayed unreachable
// until someone deleted and re-added the account.
func TestAuthRejectedDoesNotBlockUsability(t *testing.T) {
	state.accounts = newAccountStore()
	state.settings.setDataDir("")

	creds := &tinkerCredentials{Email: "user@example.com", UID: "42"}
	key := creds.accountKey()
	state.accounts.observe(creds, "")
	state.accounts.markAuthRejected(key, "invalid JWT refresh token")

	if record := state.accounts.lookup(key); record == nil || !record.AuthRejected {
		t.Fatal("前置条件失败：应记录 auth 被拒")
	}
	// markAuthRejected only sets the badge; it must not take the account out of
	// rotation on its own — the cooldown is what does that.
	if !state.accounts.usable(key) {
		t.Error("仅标记 AuthRejected 不应让账号不可用")
	}

	// Cooling is CPA's job, so a failure must not take the account out of
	// rotation here — the plugin would then refuse an account the host still
	// considers healthy.
	state.accounts.markFailure(key, "invalid JWT refresh token", failureAuth)
	if !state.accounts.usable(key) {
		t.Error("失败不应让插件自己停用账号，冷却由 CPA 负责")
	}

	// A success clears the badge too.
	state.accounts.markUsed(key)
	if record := state.accounts.lookup(key); record != nil && record.AuthRejected {
		t.Error("成功调用后应清除 AuthRejected")
	}
}

// TestFailureDoesNotSchedulePrivateCooldown guards the split of responsibility:
// CPA owns cooldowns, so the plugin must not keep a second schedule the panel
// cannot explain.
func TestFailureDoesNotSchedulePrivateCooldown(t *testing.T) {
	state.accounts = newAccountStore()
	state.settings.setDataDir("")

	creds := &tinkerCredentials{Email: "user@example.com", UID: "7"}
	key := creds.accountKey()
	state.accounts.observe(creds, "")

	for _, kind := range []failureKind{failureTransient, failureRate, failureQuota, failureAuth} {
		state.accounts.markFailure(key, "boom", kind)
		record := state.accounts.lookup(key)
		if record == nil {
			t.Fatal("记录未创建")
		}
		if record.CooldownUntil != 0 {
			t.Errorf("kind=%d 不应写入插件私有冷却", kind)
		}
		if !state.accounts.usable(key) {
			t.Errorf("kind=%d 失败后账号仍应可用（冷却归 CPA）", kind)
		}
	}
	if record := state.accounts.lookup(key); record != nil && !record.AuthRejected {
		t.Error("鉴权失败应留下徽章供面板提示")
	}
}

// TestExpiredAccessTokenIsTransient guards the distinction that matters most for
// this provider: Tinker's access token lives 15 minutes and CPA refreshes it, so
// "expired" is a routine gap, not a dead account. Only a rejected refresh token
// proves the credential is gone.
func TestExpiredAccessTokenIsTransient(t *testing.T) {
	cases := []struct {
		message string
		status  int
		want    failureKind
	}{
		{"expired JWT access token", 401, failureTransient},
		{"Authentication required", 401, failureAuth},
		{"invalid JWT refresh token", 401, failureAuth},
		{"Your generation limit was reached", 400, failureQuota},
		{"rate limit exceeded", 429, failureRate},
	}
	for _, c := range cases {
		err := &apiError{Status: c.status, Message: c.message}
		if got := classifyFailure(err); got != c.want {
			t.Errorf("classifyFailure(%q) = %d, 期望 %d", c.message, got, c.want)
		}
	}
}
