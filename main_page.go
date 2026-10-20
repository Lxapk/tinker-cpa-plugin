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
	enabled, cooling, blocked := 0, 0, 0
	now := nowUnixMilli()
	for _, record := range accounts {
		if record.Disabled || record.Blocked {
			blocked++
			continue
		}
		enabled++
		if record.CooldownUntil > now {
			cooling++
		}
	}

	var b strings.Builder
	b.WriteString(`<section class="view" id="view-accounts">`)

	b.WriteString(`<div class="stats">`)
	statCard(&b, "", "账号总数", total)
	statCard(&b, "good", "启用中", enabled)
	statCard(&b, "warn", "冷却中", cooling)
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
			b.WriteString(renderAccountRow(record, now))
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
func renderAccountRow(record accountRecord, now int64) string {
	name := record.Email
	if strings.TrimSpace(name) == "" {
		name = record.Key
	}

	status := `<span class="pill on">启用</span>`
	switch {
	case record.Disabled:
		status = `<span class="pill off">已停用</span>`
	case record.Blocked:
		status = `<span class="pill off">需重新登录</span>`
	case record.CooldownUntil > now:
		status = `<span class="pill wait">冷却中</span>`
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
