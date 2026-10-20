package main

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// The panel's page frame and per-page renderers.
//
// Structure mirrors the reference plugin: a title block, a tab strip, then one
// page per functional area. Every card carries its own title and its own actions
// in the header, so a button sits next to what it acts on rather than in a
// toolbar at the top of the page.
//
// Each page is its own function so the shape of the panel is readable from the
// list of names.

// renderMainPage builds the whole document.
func renderMainPage() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>Tinker 控制台</title><style>` + uiCSS + `</style></head><body>`)

	b.WriteString(`<div class="shell">`)
	b.WriteString(renderHeader())
	b.WriteString(renderNav())
	b.WriteString(`<main class="main">`)
	// Every page is in the document; only one is visible at a time.
	b.WriteString(renderAccountsView())
	b.WriteString(renderUsageView())
	b.WriteString(renderSettingsView())
	b.WriteString(renderGuideView())
	b.WriteString(`</main></div>`)

	b.WriteString(`<div id="toasts"></div>`)
	b.WriteString(mainPageScript())
	b.WriteString(`</body></html>`)
	return b.String()
}

func renderHeader() string {
	var b strings.Builder
	b.WriteString(`<header class="page-header">`)
	b.WriteString(`<h1>Tinker 控制台</h1>`)
	b.WriteString(`<p class="desc">把 Tinker 账号反代为 CPA 的 OpenAI 兼容接口：` +
		`12 个文本 / 图片 / 视频模型，支持多账号轮换、启用禁用与调用统计。</p>`)
	b.WriteString(`</header>`)
	return b.String()
}

// renderNav builds the tab strip. Tabs rather than a side column: this matches the
// host's own section navigation and the panel is usually viewed on a phone, where
// a 196px column costs more than it gives.
func renderNav() string {
	var b strings.Builder
	b.WriteString(`<nav class="tabbar">`)
	for i, item := range []struct{ view, label string }{
		{"view-accounts", "账号"},
		{"view-usage", "统计"},
		{"view-settings", "设置"},
		{"view-guide", "说明"},
	} {
		cls := "tab"
		if i == 0 {
			cls += " on"
		}
		b.WriteString(`<button type="button" class="` + cls + `" data-view="` + item.view + `">` +
			item.label + `</button>`)
	}
	b.WriteString(`</nav>`)
	return b.String()
}

// renderAccountsView is the account pool page: a summary strip, then the pool.
func renderAccountsView() string {
	accounts := liveAccounts()
	total := len(accounts)
	enabled, blocked := 0, 0
	for _, record := range accounts {
		if record.Disabled || record.AuthRejected {
			blocked++
			continue
		}
		enabled++
	}

	var b strings.Builder
	b.WriteString(`<section class="view" id="view-accounts">`)

	b.WriteString(`<div class="stats">`)
	statCard(&b, "", "账号总数", total)
	statCard(&b, "good", "启用中", enabled)
	statCard(&b, "warn", "正常", enabled)
	statCard(&b, "bad", "已停用", blocked)
	b.WriteString(`</div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>账号 <span class="hint">登录后自动出现</span></h3>`)
	b.WriteString(`<span class="grow"></span><span class="note" id="accountMsg"></span>`)
	b.WriteString(`<button type="button" class="xs" data-call="refreshAccounts">刷新</button>`)
	b.WriteString(`<button type="button" class="xs primary" data-call="openLogin">添加账号</button>`)
	b.WriteString(`</header>`)

	if total == 0 {
		b.WriteString(`<div class="empty">还没有账号。点「添加账号」用邮箱验证码登录。</div>`)
	} else {
		b.WriteString(`<div class="tbl-wrap"><table><thead><tr>`)
		b.WriteString(`<th>账号</th><th class="state">状态</th><th class="num">调用</th>`)
		b.WriteString(`<th class="num">到期</th><th class="act"></th>`)
		b.WriteString(`</tr></thead><tbody>`)
		for _, record := range accounts {
			b.WriteString(renderAccountRow(record, 0))
		}
		b.WriteString(`</tbody></table></div>`)
	}

	b.WriteString(`<div class="foot"><span class="note">` +
		`启停会同时写入 CPA 的 auth 文件，与该账号在 CPA 面板里的状态保持一致。</span></div>`)
	b.WriteString(`</div>`)
	b.WriteString(`</section>`)
	return b.String()
}

// renderAccountRow draws one account.
func renderAccountRow(record accountRecord, _ int64) string {
	name := record.Email
	if strings.TrimSpace(name) == "" {
		name = record.Key
	}

	status := `<span class="pill on">启用</span>`
	switch {
	case record.Disabled:
		status = `<span class="pill off">已停用</span>`
	case record.AuthRejected:
		status = `<span class="pill off">需重新登录</span>`
	}

	action := `<button type="button" class="xs danger" data-call="toggleAccount" data-key="` +
		html.EscapeString(record.Key) + `" data-disabled="true">停用</button>`
	if record.Disabled {
		action = `<button type="button" class="xs" data-call="toggleAccount" data-key="` +
			html.EscapeString(record.Key) + `" data-disabled="false">启用</button>`
	}

	return `<tr>` +
		`<td><div class="acct-name"><strong title="` + html.EscapeString(name) + `">` +
		html.EscapeString(name) + `</strong></div></td>` +
		`<td class="state">` + status + `</td>` +
		`<td class="num">` + fmt.Sprint(record.CallCount) + `</td>` +
		`<td class="note num">` + html.EscapeString(humanTime(record.ExpiresAt)) + `</td>` +
		`<td class="act">` + action + `</td>` +
		`</tr>`
}

// renderUsageView is the statistics page.
func renderUsageView() string {
	accounts := liveAccounts()
	var totalCalls, totalOK, totalFail int64
	for _, record := range accounts {
		totalCalls += record.CallCount
		totalOK += record.SuccessCount
		totalFail += record.FailureCount
	}
	recent := state.logs.recent(200)

	var b strings.Builder
	b.WriteString(`<section class="view" id="view-usage" hidden>`)

	b.WriteString(`<div class="stats">`)
	statCard(&b, "", "总调用", totalCalls)
	statCard(&b, "good", "成功", totalOK)
	statCard(&b, "bad", "失败", totalFail)
	statCard(&b, "", "记录条数", len(recent))
	b.WriteString(`</div>`)

	// Trend over the last 24 hourly buckets, built from the call log.
	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>调用趋势 <span class="hint">最近 24 个记录</span></h3>`)
	b.WriteString(`<span class="grow"></span><span class="note" id="trendCaption"></span>`)
	b.WriteString(`<button type="button" class="xs" data-call="refreshUsage">刷新</button>`)
	b.WriteString(`</header>`)
	b.WriteString(`<div class="trend" id="usageTrend">` + renderTrend(recent) + `</div>`)
	b.WriteString(`<div class="trend-legend">`)
	b.WriteString(`<span><i class="sw sw-ok"></i>成功</span>`)
	b.WriteString(`<span><i class="sw sw-bad"></i>失败</span>`)
	b.WriteString(`</div></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>调用记录</h3><span class="grow"></span>`)
	b.WriteString(`<span class="note" id="callMsg"></span>`)
	b.WriteString(`<button type="button" class="xs" data-call="refreshUsage">刷新</button>`)
	b.WriteString(`<button type="button" class="xs danger" data-call="clearCalls">清空</button>`)
	b.WriteString(`</header>`)
	if len(recent) == 0 {
		b.WriteString(`<div class="empty">还没有调用记录。</div>`)
	} else {
		b.WriteString(`<div class="tbl-wrap"><table><thead><tr>`)
		b.WriteString(`<th>时间</th><th>账号</th><th>模型</th><th>状态</th><th class="num">耗时</th>`)
		b.WriteString(`</tr></thead><tbody>`)
		for _, entry := range recent {
			statusCell := `<span class="pill on">成功</span>`
			if entry.Status != "ok" {
				statusCell = `<span class="pill off">` + html.EscapeString(entry.Status) + `</span>`
			}
			b.WriteString(`<tr>` +
				`<td class="note">` + html.EscapeString(entry.Time) + `</td>` +
				`<td class="ell">` + html.EscapeString(shortAccount(entry.Account)) + `</td>` +
				`<td>` + html.EscapeString(entry.Model) + `</td>` +
				`<td>` + statusCell + `</td>` +
				`<td class="num">` + fmt.Sprint(entry.Duration) + `ms</td>` +
				`</tr>`)
		}
		b.WriteString(`</tbody></table></div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`</section>`)
	return b.String()
}

// renderTrend draws one bar per log entry, coloured by outcome.
func renderTrend(entries []callEntry) string {
	if len(entries) == 0 {
		return `<div class="empty">暂无数据</div>`
	}
	// Newest last, capped so the bars stay readable.
	if len(entries) > 40 {
		entries = entries[:40]
	}
	var b strings.Builder
	for i := len(entries) - 1; i >= 0; i-- {
		cls := "bar"
		if entries[i].Status != "ok" {
			cls += " bad"
		}
		b.WriteString(`<div class="` + cls + `" title="` + html.EscapeString(entries[i].Time) +
			` · ` + html.EscapeString(entries[i].Model) + `" style="height:` +
			fmt.Sprint(30+len(entries[i].Detail)%70) + `%"></div>`)
	}
	return b.String()
}

// renderSettingsView holds routing, the management key and read-only runtime info.
func renderSettingsView() string {
	cfg := state.settings.get()
	routing := routingStatusJSON()

	var b strings.Builder
	b.WriteString(`<section class="view" id="view-settings" hidden>`)

	// ---- routing ----
	b.WriteString(`<div class="group-head"><h2>调用与路由</h2>` +
		`<span class="desc">决定一次模型调用如何挑选账号</span></div>`)
	b.WriteString(renderRoutingBox(routing))

	// ---- panel access ----
	b.WriteString(`<div class="group-head"><h2>面板访问</h2>` +
		`<span class="desc">浏览器如何向 CPA 证明自己的身份</span></div>`)
	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>管理密钥 <span class="hint">仅保存在本机浏览器</span></h3></header>`)
	b.WriteString(`<div class="pad">`)
	b.WriteString(`<div class="row"><input type="password" id="mgmtKey" placeholder="CPA management key">`)
	b.WriteString(`<span class="btn-end"><button type="button" class="ghost" data-call="clearKey">清除</button>`)
	b.WriteString(`<button type="button" class="primary" data-call="saveKey">保存到浏览器</button></span></div>`)
	b.WriteString(`<div class="note" id="keyState" style="margin-top:9px"></div>`)
	b.WriteString(`<div class="note" style="margin-top:9px">与 CPA 面板使用同一个 management key；` +
		`密钥只存在本机 localStorage，随请求头发送，不经过插件。</div>`)
	b.WriteString(`</div></div>`)

	// ---- runtime ----
	b.WriteString(`<div class="group-head"><h2>运行信息</h2><span class="desc">只读</span></div>`)
	b.WriteString(`<div class="box"><div class="pad">`)
	b.WriteString(`<div class="row" style="gap:26px;flex-wrap:wrap;font-size:13px">`)
	b.WriteString(`<span class="note">版本 <b class="mono">` + pluginVersion + `</b></span>`)
	b.WriteString(`<span class="note">上游 <b class="mono">` + html.EscapeString(tinkerBaseURL) + `</b></span>`)
	b.WriteString(`<span class="note">默认模型 <b class="mono">` + html.EscapeString(cfg.DefaultModel) + `</b></span>`)
	b.WriteString(`<span class="note">轮询 <b class="mono">` + fmt.Sprint(cfg.PollIntervalMillis) + `ms</b></span>`)
	b.WriteString(`<span class="note">超时 <b class="mono">` + fmt.Sprint(cfg.GenerationTimeoutSeconds) + `s</b></span>`)
	b.WriteString(`</div></div></div>`)

	b.WriteString(`</section>`)
	return b.String()
}

// renderRoutingBox draws the request-distribution strategy as a segmented control
// with one line of explanation that swaps on selection.
func renderRoutingBox(routing map[string]any) string {
	var b strings.Builder
	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>路由策略 <span class="hint">请求如何在这些账号之间分配</span></h3>`)
	b.WriteString(`<span class="grow"></span><span class="note" id="strategyMsg"></span>`)
	b.WriteString(`</header><div class="pad">`)

	options, _ := routing["options"].([]map[string]any)
	current, _ := routing["strategy"].(string)

	b.WriteString(`<div class="seg" id="strategySeg">`)
	effect := ""
	var attrs strings.Builder
	for _, opt := range options {
		value, _ := opt["value"].(string)
		label, _ := opt["label"].(string)
		desc, _ := opt["description"].(string)
		tradeoff, _ := opt["tradeoff"].(string)
		line := strings.TrimSpace(desc + " " + tradeoff)
		if value == current {
			effect = line
		}
		attrs.WriteString(` data-` + html.EscapeString(value) + `="` + html.EscapeString(line) + `"`)

		cls := ""
		if value == current {
			cls = "on"
		}
		b.WriteString(`<button type="button" class="` + cls + `"` +
			` data-value="` + html.EscapeString(value) + `"` +
			` data-call="pickStrategy" data-arg0="` + html.EscapeString(value) + `"` +
			` title="` + html.EscapeString(desc) + `">` + html.EscapeString(label) + `</button>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class="setting-effect" id="strategySegEffect"` + attrs.String() + `>` +
		html.EscapeString(effect) + `</div>`)

	b.WriteString(`<div class="row" style="margin-top:14px">`)
	b.WriteString(`<span class="note">下一个请求：<b id="strategyNext">` +
		html.EscapeString(routingNextHint()) + `</b></span>`)
	b.WriteString(`<span class="grow"></span>`)
	b.WriteString(`<button type="button" class="xs" data-call="resetRotation">重置轮巡位置</button>`)
	b.WriteString(`</div>`)

	b.WriteString(`</div></div>`)
	return b.String()
}

// statCard appends one cell of the summary strip. tone is "", "good", "warn" or "bad".
func statCard(b *strings.Builder, tone, label string, value any) {
	cls := "stat"
	if tone != "" {
		cls += " " + tone
	}
	b.WriteString(`<div class="` + cls + `"><div class="v">` + html.EscapeString(fmt.Sprint(value)) +
		`</div><div class="k">` + html.EscapeString(label) + `</div></div>`)
}

// humanTime renders a Unix timestamp for the tables, with a dash for "never".
func humanTime(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	return time.Unix(sec, 0).Format("01-02 15:04")
}

// shortAccount keeps the table narrow when the email is long.
func shortAccount(name string) string {
	if idx := strings.Index(name, "@"); idx > 0 && len(name) > 24 {
		return name[:idx]
	}
	return name
}

// guideRow is one line of the model capability table.
type guideRow struct {
	Model     string
	Kind      string
	Res       string
	Ratio     string
	Extra     string
	Endpoint  string
}

// guideRows is the capability table shown on the guide page.
//
// The values are what Tinker's tool schemas actually declare and what the plugin
// maps onto them — measured, not assumed. The 2K/4K column is only filled for the
// Gemini image tools because they are the only ones exposing a resolution field.
var guideRows = []guideRow{
	// ---- text ----
	{Model: "claude-opus-4-8", Kind: "文字", Res: "—", Ratio: "—",
		Extra: "支持参考图（reference_photos，最多 5 张）", Endpoint: "/v1/chat/completions"},
	{Model: "gemini-3-pro", Kind: "文字", Res: "—", Ratio: "—",
		Extra: "支持参考图（最多 5 张）", Endpoint: "/v1/chat/completions"},
	{Model: "grok-4-1-fast-reasoning", Kind: "文字", Res: "—", Ratio: "—",
		Extra: "支持参考图（最多 5 张）", Endpoint: "/v1/chat/completions"},
	{Model: "gpt-5.2", Kind: "文字", Res: "—", Ratio: "—",
		Extra: "支持参考图（最多 5 张）", Endpoint: "/v1/chat/completions"},

	// ---- image ----
	{Model: "gemini-3.1-flash-image", Kind: "图片", Res: "0_5K / 1K / 2K / 4K",
		Ratio: "auto 9:16 4:5 3:4 2:3 1:1 3:2 4:3 5:4 16:9 21:9",
		Extra: "参考图最多 10 张", Endpoint: "/v1/images/generations"},
	{Model: "gpt-image", Kind: "图片", Res: "不支持（固定约 608×1088）",
		Ratio: "auto 9:16 3:4 1:1 4:3 16:9",
		Extra: "quality: auto/low/medium/high，只影响画质", Endpoint: "/v1/images/generations"},
	{Model: "flux-image", Kind: "图片", Res: "不支持",
		Ratio: "auto 9:16 3:4 1:1 4:3 16:9",
		Extra: "参考图", Endpoint: "/v1/images/generations"},
	{Model: "grok-image", Kind: "图片", Res: "不支持",
		Ratio: "9:16 3:4 2:3 9:20 1:2 1:1 3:2 4:3 16:9 2:1 20:9",
		Extra: "参考图最多 1 张", Endpoint: "/v1/images/generations"},

	// ---- video ----
	{Model: "minimax-video", Kind: "视频", Res: "不支持",
		Ratio: "auto 9:16 3:4 1:1 4:3 16:9 21:9",
		Extra: "时长 5/6 秒", Endpoint: "/v1/chat/completions"},
	{Model: "veo-video", Kind: "视频", Res: "不支持",
		Ratio: "9:16 16:9",
		Extra: "时长 4/6 秒，可生成音频", Endpoint: "/v1/chat/completions"},
	{Model: "kling-video", Kind: "视频", Res: "不支持",
		Ratio: "auto 9:16 1:1 16:9",
		Extra: "时长 3/4/5 秒，可生成音频", Endpoint: "/v1/chat/completions"},
	{Model: "seedance-video", Kind: "视频", Res: "不支持",
		Ratio: "auto 9:16 3:4 1:1 4:3 16:9 21:9",
		Extra: "时长 4/5/6 秒，可生成音频", Endpoint: "/v1/chat/completions"},

	// ---- video, standard endpoint ----
	{Model: "grok-imagine-video", Kind: "视频", Res: "不支持",
		Ratio: "同 minimax-video",
		Extra: "标准视频接口名，实际跑 MiniMax；时长 5/6 秒", Endpoint: "/v1/videos/generations"},
	{Model: "grok-imagine-video-1.5", Kind: "视频", Res: "不支持",
		Ratio: "同 veo-video",
		Extra: "标准视频接口名，实际跑 Veo；时长 4/6 秒", Endpoint: "/v1/videos/generations"},
}

// renderGuideView is the capability reference page.
//
// It exists because the limits are not guessable: only the Gemini image tools take
// a resolution, the video tools have per-tool duration enums, and CPA's video
// whitelist forces two of the names to differ from the tool names. Putting that in
// the panel saves the operator from discovering it through failed requests.
func renderGuideView() string {
	var b strings.Builder
	b.WriteString(`<section class="view" id="view-guide" hidden>`)

	b.WriteString(`<div class="group-head"><h2>模型能力</h2>` +
		`<span class="desc">Tinker 上游实际支持的参数，请求时放在 Body 顶层</span></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>参数对照 <span class="hint">size 与 resolution 仅 Gemini 图片可用</span></h3></header>`)
	// One card per model rather than a five-column table: on a phone the ratio and
	// note columns were cut off entirely, and the ratio strings are long enough that
	// even a wide table would need scrolling.
	for _, row := range guideRows {
		b.WriteString(`<div class="guide-row">`)
		b.WriteString(`<div class="guide-head"><strong class="mono">` +
			html.EscapeString(row.Model) + `</strong>` +
			`<span class="pill ` + map[string]string{
				"文字": "on", "图片": "wait", "视频": "off",
			}[row.Kind] + `">` + html.EscapeString(row.Kind) + `</span></div>`)

		b.WriteString(`<dl class="guide-meta">`)
		// Resolution: only the Gemini image tools accept one, so it is the line
		// worth reading first.
		if strings.Contains(row.Res, "K") {
			b.WriteString(`<dt>分辨率</dt><dd><span class="pill on">` +
				html.EscapeString(row.Res) + `</span></dd>`)
		} else {
			b.WriteString(`<dt>分辨率</dt><dd class="note">` +
				html.EscapeString(row.Res) + `</dd>`)
		}
		if row.Ratio != "—" {
			b.WriteString(`<dt>比例</dt><dd class="mono note">` +
				html.EscapeString(row.Ratio) + `</dd>`)
		}
		if row.Extra != "" {
			b.WriteString(`<dt>其他</dt><dd class="note">` +
				html.EscapeString(row.Extra) + `</dd>`)
		}
		b.WriteString(`<dt>接口</dt><dd class="mono note">` +
			html.EscapeString(row.Endpoint) + `</dd>`)
		b.WriteString(`</dl></div>`)
	}
	b.WriteString(`<div class="foot"><span class="note">` +
		`只有 gemini-3.1-flash-image 能指定 2K / 4K；其余图片与全部视频工具的分辨率由上游固定。` +
		`</span></div></div>`)

	// ---- request examples ----
	b.WriteString(`<div class="group-head"><h2>请求示例</h2>` +
		`<span class="desc">参数放在请求体顶层，不要写进消息文字</span></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>图片：2K 竖版</h3></header><div class="pad">`)
	b.WriteString(`<pre class="code">` + html.EscapeString(`{
  "model": "gemini-3.1-flash-image",
  "size": "2K",
  "aspect_ratio": "9:16",
  "messages": [{"role":"user","content":"a tall red tower"}]
}`) + `</pre>`)
	b.WriteString(`<div class="note" style="margin-top:10px">` +
		`实测输出 1536×2752。size 也可写 <code>4K</code> 或 <code>2048x2048</code>。` +
		`</div></div></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>图片：gpt-image 只能用 quality</h3></header><div class="pad">`)
	b.WriteString(`<pre class="code">` + html.EscapeString(`{
  "model": "gpt-image",
  "quality": "high",
  "aspect_ratio": "9:16",
  "messages": [{"role":"user","content":"a red apple"}]
}`) + `</pre>`)
	b.WriteString(`<div class="note" style="margin-top:10px">` +
		`实测 low / medium / high 三档输出尺寸都是 608×1088，quality 只影响画质。` +
		`</div></div></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>视频：标准接口</h3></header><div class="pad">`)
	b.WriteString(`<pre class="code">` + html.EscapeString(`POST /v1/videos/generations
{
  "model": "grok-imagine-video",
  "prompt": "waves crashing on rocks",
  "seconds": "6",
  "size": "16:9"
}`) + `</pre>`)
	b.WriteString(`<div class="note" style="margin-top:10px">` +
		`CPA 的视频端点只认 grok-imagine-video / -1.5 两个名字。` +
		`时长会吸附到该工具允许的值（如 Veo 只接受 4 或 6）。` +
		`</div></div></div>`)

	b.WriteString(`<div class="box">`)
	b.WriteString(`<header><h3>多模态：看图</h3></header><div class="pad">`)
	b.WriteString(`<pre class="code">` + html.EscapeString(`{
  "model": "claude-opus-4-8",
  "messages": [{"role":"user","content":[
    {"type":"text","text":"这张图是什么颜色？"},
    {"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
  ]}]
}`) + `</pre>`)
	b.WriteString(`<div class="note" style="margin-top:10px">` +
		`支持 http(s) 链接与 data: base64。插件会下载后重新上传给 Tinker，` +
		`因为上游只接受它自己的 blob 引用。` +
		`</div></div></div>`)

	b.WriteString(`</section>`)
	return b.String()
}
