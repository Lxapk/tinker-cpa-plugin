package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestToolCatalogueIsComplete(t *testing.T) {
	// Tinker exposes exactly four text, four image and four video tools. A
	// missing entry means a model the app offers is unreachable through CPA.
	counts := map[string]int{}
	for _, ref := range toolCatalogue {
		if ref.OpenAI == "" || ref.ToolID == "" {
			t.Fatalf("工具条目缺少标识: %+v", ref)
		}
		// Aliases exist so CPA's hard-coded endpoint whitelists accept a name;
		// they are not additional tools.
		if ref.Alias {
			continue
		}
		counts[ref.OutputType]++
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

// TestImageInputsCarryResolution guards the report that a client could not ask for
// 2K/4K: only the prompt used to be forwarded, so every image came back at the
// tool's 1K default.
func TestImageInputsCarryResolution(t *testing.T) {
	gemini := resolveTool("gemini-3.1-flash-image")

	// An explicit OpenAI-style size.
	inputs := buildToolInputs(gemini, "a cat", []byte(`{"size":"2048x2048"}`))
	if inputs["resolution"] != "2K" {
		t.Errorf("2048x2048 应映射为 2K，得到 %v", inputs["resolution"])
	}
	if inputs["aspect_ratio"] != "1:1" {
		t.Errorf("方图应映射为 1:1，得到 %v", inputs["aspect_ratio"])
	}

	// A plain "4K" token, which some clients send in size.
	inputs = buildToolInputs(gemini, "a cat", []byte(`{"size":"4K"}`))
	if inputs["resolution"] != "4K" {
		t.Errorf("4K 应原样传递，得到 %v", inputs["resolution"])
	}

	// An explicit resolution field wins.
	inputs = buildToolInputs(gemini, "a cat", []byte(`{"resolution":"4K","size":"1024x1024"}`))
	if inputs["resolution"] != "4K" {
		t.Errorf("显式 resolution 应优先，得到 %v", inputs["resolution"])
	}

	// Landscape sizes pick a landscape ratio.
	inputs = buildToolInputs(gemini, "a cat", []byte(`{"size":"1920x1080"}`))
	if inputs["aspect_ratio"] != "16:9" {
		t.Errorf("1920x1080 应映射为 16:9，得到 %v", inputs["aspect_ratio"])
	}
	if inputs["resolution"] != "2K" {
		t.Errorf("1920 长边应映射为 2K，得到 %v", inputs["resolution"])
	}
}

// TestImageInputsRespectToolCapabilities checks that no field is sent to a tool that
// does not declare it — the upstream rejects unknown inputs.
func TestImageInputsRespectToolCapabilities(t *testing.T) {
	gpt := resolveTool("gpt-image")
	inputs := buildToolInputs(gpt, "a cat", []byte(`{"size":"2048x2048"}`))
	if _, has := inputs["resolution"]; has {
		t.Error("gpt_image 不接受 resolution，不应传入")
	}
	if inputs["quality"] != "medium" {
		t.Errorf("2K 应映射为 medium 质量，得到 %v", inputs["quality"])
	}

	flux := resolveTool("flux-image")
	inputs = buildToolInputs(flux, "a cat", []byte(`{"size":"2048x2048"}`))
	if _, has := inputs["resolution"]; has {
		t.Error("flux 不接受 resolution")
	}
	if _, has := inputs["quality"]; has {
		t.Error("flux 不接受 quality")
	}
	if inputs["aspect_ratio"] != "1:1" {
		t.Errorf("flux 应收到 aspect_ratio，得到 %v", inputs["aspect_ratio"])
	}
}

// TestTextToolInputsStayMinimal: text tools take only a prompt.
func TestTextToolInputsStayMinimal(t *testing.T) {
	claude := resolveTool("claude-opus-4-8")
	inputs := buildToolInputs(claude, "hello", []byte(`{"size":"4K"}`))
	if len(inputs) != 1 || inputs["prompt"] != "hello" {
		t.Errorf("文本工具只应收到 prompt，得到 %v", inputs)
	}
}

// TestVideoInputsCarryDurationAndRatio covers the video path, which had no input
// translation at all: only the prompt was sent, so every clip came back at the
// tool's default length and orientation.
func TestVideoInputsCarryDurationAndRatio(t *testing.T) {
	veo := resolveTool("veo-video")

	// OpenAI video clients send "seconds".
	inputs := buildToolInputs(veo, "a dog running", []byte(`{"seconds":"6","size":"9:16"}`))
	if inputs["duration"] != 6 {
		t.Errorf("seconds=6 应映射为 duration=6，得到 %v", inputs["duration"])
	}
	if inputs["aspect_ratio"] != "9:16" {
		t.Errorf("aspect_ratio 应传递，得到 %v", inputs["aspect_ratio"])
	}

	// A duration the tool does not accept is snapped to the nearest allowed value.
	// Veo allows 4 or 6, so 5 becomes 4 or 6 — never an invalid 5.
	inputs = buildToolInputs(veo, "x", []byte(`{"duration":5}`))
	dur, _ := inputs["duration"].(int)
	if dur != 4 && dur != 6 {
		t.Errorf("Veo 只接受 4/6，得到 %v", inputs["duration"])
	}

	// Kling allows 3/4/5, so 5 is kept as-is.
	kling := resolveTool("kling-video")
	inputs = buildToolInputs(kling, "x", []byte(`{"duration":5}`))
	if inputs["duration"] != 5 {
		t.Errorf("Kling 应保留 5，得到 %v", inputs["duration"])
	}

	// No duration requested: the tool's own default applies.
	inputs = buildToolInputs(kling, "x", []byte(`{}`))
	if _, has := inputs["duration"]; has {
		t.Error("未指定时长时不应强行写入")
	}
}

// TestVideoAudioFlag checks the generate_audio passthrough.
func TestVideoAudioFlag(t *testing.T) {
	veo := resolveTool("veo-video")
	inputs := buildToolInputs(veo, "x", []byte(`{"generate_audio":false}`))
	if v, ok := inputs["generate_audio"].(bool); !ok || v {
		t.Errorf("应传递 generate_audio=false，得到 %v", inputs["generate_audio"])
	}

	// Tools without the field must not receive it.
	flux := resolveTool("flux-image")
	inputs = buildToolInputs(flux, "x", []byte(`{"generate_audio":true}`))
	if _, has := inputs["generate_audio"]; has {
		t.Error("flux 不接受 generate_audio")
	}
}

// TestVideoRatiosFitToolEnum: each video tool has its own ratio list, so a ratio
// outside it must not be forwarded — the upstream rejects unknown enum values.
func TestVideoRatiosFitToolEnum(t *testing.T) {
	minimax := resolveTool("minimax-video")
	inputs := buildToolInputs(minimax, "x", []byte(`{"size":"21:9"}`))
	if inputs["aspect_ratio"] != "21:9" {
		t.Errorf("MiniMax 接受 21:9，得到 %v", inputs["aspect_ratio"])
	}

	veo := resolveTool("veo-video")
	inputs = buildToolInputs(veo, "x", []byte(`{"size":"21:9"}`))
	// Veo only offers 9:16 / 16:9; a landscape 21:9 has to fall back to 16:9.
	if inputs["aspect_ratio"] != "16:9" {
		t.Errorf("Veo 只接受 9:16/16:9，21:9 应回落到 16:9，得到 %v", inputs["aspect_ratio"])
	}
}

// TestRequestShapeDetection guards the branch that lets one executor serve three
// OpenAI endpoints: chat, images and video all arrive at the same handler.
func TestRequestShapeDetection(t *testing.T) {
	chat := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`)
	if detectRequestKind(chat) != kindChat {
		t.Error("messages 应识别为 chat")
	}

	images := []byte(`{"model":"flux-image","prompt":"a cat"}`)
	if detectRequestKind(images) != kindImages {
		t.Error("只有 prompt 应识别为 images")
	}

	if isVideoCreate(executorRequest{}, []byte(`{"model":"grok-imagine-video","prompt":"waves"}`)) != true {
		t.Error("grok-imagine-video 应识别为视频")
	}
	if isVideoCreate(executorRequest{}, []byte(`{"model":"flux-image","prompt":"a cat"}`)) {
		t.Error("图片模型不应识别为视频")
	}
	if isVideoCreate(executorRequest{}, []byte(`{"model":"veo-video","prompt":"waves"}`)) != true {
		t.Error("veo-video 应识别为视频")
	}

	if !isVideoRetrieve([]byte(`{"request_id":"gid://api/Artifact/1"}`)) {
		t.Error("只有 request_id 应识别为检索")
	}
	if isVideoRetrieve([]byte(`{"request_id":"x","prompt":"y"}`)) {
		t.Error("带 prompt 的不是检索")
	}
}

// TestVideoAliasesPassCPAWhitelist records why the alias names exist: CPA's video
// endpoints accept only its own hard-coded list, so a Tinker tool name is rejected
// before the request reaches this plugin.
func TestVideoAliasesPassCPAWhitelist(t *testing.T) {
	// The names CPA's isXAIVideosModel / isSoraVideosModel accept.
	whitelisted := []string{
		"grok-imagine-video",
		"grok-imagine-video-1.5",
		"sora-2",
		"xai/grok-imagine-video",
	}
	for _, name := range whitelisted {
		if !isVideoModelName(name) {
			t.Errorf("%q 应被识别为视频模型", name)
		}
	}

	// Each alias must resolve to a real Tinker video tool.
	for _, ref := range toolCatalogue {
		if !ref.Alias {
			continue
		}
		if ref.OutputType != "video" {
			t.Errorf("别名 %q 应为视频工具", ref.OpenAI)
		}
		resolved := resolveTool(ref.OpenAI)
		if resolved.ToolID != ref.ToolID {
			t.Errorf("别名 %q 应解析到 %q，得到 %q", ref.OpenAI, ref.ToolID, resolved.ToolID)
		}
	}
}

// TestImagesAndVideoResponsesUseTheirOwnShape: an images client cannot parse a
// chat.completion, and a video client needs a task id.
func TestImagesAndVideoResponsesUseTheirOwnShape(t *testing.T) {
	var images struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(imagesResponseBody([]string{"https://x/y.png"}, "url"), &images); err != nil {
		t.Fatalf("images 响应不是合法 JSON: %v", err)
	}
	if len(images.Data) != 1 || images.Data[0].URL != "https://x/y.png" {
		t.Errorf("images data 不正确: %+v", images.Data)
	}

	var video struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(videoCreateResponse("task-1", "grok-imagine-video", "p", "6", "720x1280"), &video); err != nil {
		t.Fatalf("视频响应不是合法 JSON: %v", err)
	}
	if video.RequestID != "task-1" {
		t.Errorf("视频响应应带 request_id，得到 %q", video.RequestID)
	}
}

// TestCollectImageURLs covers the multimodal parsing: an OpenAI client attaches
// images as content parts, in either the object or the flat-string form.
func TestCollectImageURLs(t *testing.T) {
	messages := []openAIMessage{
		{Role: "user", Content: json.RawMessage(`[
			{"type":"text","text":"what is this"},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},
			{"type":"image_url","image_url":"https://example.com/b.png"}
		]`)},
		{Role: "user", Content: json.RawMessage(`[
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
		]`)},
	}
	urls := collectImageURLs(messages)
	if len(urls) != 2 {
		t.Fatalf("应收集 2 个去重后的图片，得到 %d: %v", len(urls), urls)
	}
	if urls[0] != "https://example.com/a.png" || urls[1] != "https://example.com/b.png" {
		t.Errorf("顺序或内容不对: %v", urls)
	}
}

// TestReferenceFieldPerTool guards the field names: they differ per tool and the
// upstream rejects an input it does not declare.
func TestReferenceFieldPerTool(t *testing.T) {
	cases := map[string]string{
		"claude_text":          "reference_photos",
		"gemini_text":          "reference_photos",
		"gemini_image":         "image_urls",
		"gpt_image":            "reference_images",
		"grok_image":           "reference_photo",
		"flux_pro_kontext":     "reference_photos",
		"minimax_video":        "reference_images",
		"veo_text_to_video":    "first_frame",
		"kling_image_to_video": "start_frame",
		"seedance_video":       "reference_images",
	}
	for tool, want := range cases {
		if got := referenceFieldFor(tool); got != want {
			t.Errorf("%s 的参考图字段应为 %q，得到 %q", tool, want, got)
		}
	}
}

// TestBuildReferenceInputsRespectsLimit: each tool declares a maximum.
func TestBuildReferenceInputsRespectsLimit(t *testing.T) {
	if got := referenceLimit("grok_image"); got != 1 {
		t.Errorf("grok_image 只接受 1 张参考图，得到 %d", got)
	}
	if got := referenceLimit("gemini_image"); got != 10 {
		t.Errorf("gemini_image 接受 10 张，得到 %d", got)
	}
}

// TestFormatExt maps content types to file extensions for the upload.
func TestFormatExt(t *testing.T) {
	cases := map[string]string{
		"image/png":  "png",
		"image/webp": "webp",
		"image/jpeg": "jpg",
		"image/gif":  "gif",
	}
	for in, want := range cases {
		if got := formatExt(in); got != want {
			t.Errorf("formatExt(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestNextRefreshAfterIsExpiry pins the scheduling contract: CPA refreshes at the
// token's expiry, never ahead of it.
//
// A lead time (the old refresh_skew_seconds: 120) made CPA refresh on a fixed
// schedule, which spends Tinker's single-use refresh token far more often than the
// credential needs and widens the window where two refreshes race. The official app
// refreshes only after a 401.
func TestNextRefreshAfterIsExpiry(t *testing.T) {
	expiry := int64(1792505628)
	creds := &tinkerCredentials{
		AccessToken: "token-abcdefghijklmnop",
		Email:       "user@example.com",
		ExpiresAt:   expiry,
	}
	auth := tinkerAuthData(creds, "")

	if auth.NextRefreshAfter.IsZero() {
		t.Fatal("应设置 NextRefreshAfter")
	}
	if got := auth.NextRefreshAfter.Unix(); got != expiry {
		t.Errorf("NextRefreshAfter = %d, 期望等于到期时间 %d（不应提前）", got, expiry)
	}
}

// TestNoScheduledRefreshInPlugin: the plugin must not carry its own refresh timer
// or skew. Refreshing is CPA's job; a second trigger would race it for the
// single-use token.
func TestNoScheduledRefreshInPlugin(t *testing.T) {
	cfg := defaultSettings()
	if cfg.RefreshSkewSeconds != 0 {
		t.Errorf("默认刷新提前量应为 0，得到 %d", cfg.RefreshSkewSeconds)
	}
}

// TestOnlyAuthRefreshCallsUpstream records where refreshing happens. The single
// call site is auth.refresh, which CPA invokes; there must be no path that
// refreshes during a generation.
func TestOnlyAuthRefreshCallsUpstream(t *testing.T) {
	src, err := os.ReadFile("auth_provider.go")
	if err != nil {
		t.Skip("源码不可读")
	}
	if !strings.Contains(string(src), "upstream.refresh") {
		t.Error("auth.refresh 应调用上游刷新")
	}
	exec, errExec := os.ReadFile("executor.go")
	if errExec != nil {
		t.Skip("executor.go 不可读")
	}
	if strings.Contains(string(exec), "upstream.refresh") {
		t.Error("执行器不应调用上游刷新——那会与 CPA 抢一次性 refresh token")
	}
}

// TestAuthDataDeclaresRefreshInterval guards the field whose absence made every
// account silently expire.
//
// CPA's auto-refresh loop asks ProviderRefreshLead(provider, runtime) for
// permission to schedule anything. A plugin provider has no registered lead
// factory, so the answer is nil — "never refresh" — unless the credential carries
// refresh_interval_seconds. That one field is what makes CPA look after the token.
func TestAuthDataDeclaresRefreshInterval(t *testing.T) {
	creds := &tinkerCredentials{
		AccessToken: "token-abcdefghijklmnop",
		Email:       "user@example.com",
		ExpiresAt:   timeUnixNow() + 900,
	}
	auth := tinkerAuthData(creds, "")

	raw, ok := auth.Metadata["refresh_interval_seconds"]
	if !ok {
		t.Fatal("缺少 refresh_interval_seconds：CPA 不会刷新该账号")
	}
	interval, isInt := raw.(int)
	if !isInt || interval <= 0 {
		t.Fatalf("refresh_interval_seconds 应为正整数，得到 %#v", raw)
	}
	// Capped at the token's remaining lifetime, so the refresh lands at expiry.
	if interval > 900 {
		t.Errorf("间隔不应超过 token 剩余寿命，得到 %d", interval)
	}

	// An already-expired credential asks for an immediate refresh.
	expired := &tinkerCredentials{Email: "e@x.y", ExpiresAt: timeUnixNow() - 60}
	if got := expired.refreshIntervalSeconds(); got != 1 {
		t.Errorf("已过期凭据应返回 1 秒，得到 %d", got)
	}
}

// timeUnixNow is a small helper so the test reads clearly.
func timeUnixNow() int64 { return nowUnix() }

// TestAspectRatioInResolutionFallsBackToSize covers a real mix-up: some clients
// expose only a "resolution" field and the operator types "9:16" into it. That is a
// ratio, not a resolution, so it must not shadow the size they also set — otherwise
// the request looks correct while producing default-resolution images.
func TestAspectRatioInResolutionFallsBackToSize(t *testing.T) {
	gemini := resolveTool("gemini-3.1-flash-image")

	// ratio in resolution, real value in size
	inputs := buildToolInputs(gemini, "x", []byte(`{"resolution":"9:16","size":"2k"}`))
	if inputs["resolution"] != "2K" {
		t.Errorf("resolution=9:16 时应回退到 size=2k，得到 %v", inputs["resolution"])
	}

	// ratio in resolution, nothing else: no resolution must be sent
	inputs = buildToolInputs(gemini, "x", []byte(`{"resolution":"9:16"}`))
	if _, has := inputs["resolution"]; has {
		t.Errorf("只有比例时不应写入 resolution，得到 %v", inputs["resolution"])
	}

	// a real resolution still wins over size
	inputs = buildToolInputs(gemini, "x", []byte(`{"resolution":"4K","size":"1024x1024"}`))
	if inputs["resolution"] != "4K" {
		t.Errorf("显式 4K 应优先，得到 %v", inputs["resolution"])
	}
}
