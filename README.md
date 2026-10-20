# Tinker CPA 插件

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）v7 的原生动态插件（`.so`），把 Tinker 账号反代为 **OpenAI 兼容接口**，并在 CPA 管理面板里提供账号与调用管理。

## 功能

- **OpenAI 兼容调用**：`/v1/chat/completions`（流式 / 非流式）与 `/v1/models`，覆盖 Tinker 全部 12 个工具。
- **多种能力**：文本（Claude / Gemini / Grok / GPT）、图片（Gemini / GPT / Flux / Grok）、视频（MiniMax / Veo / Kling / Seedance）。
- **邮箱验证码登录**：在 CPA 的登录页填入 Tinker 邮箱，插件发码，回填验证码即可。
- **多账号**：每个账号独立保存凭据，失败自动换号；按最近使用时间分散负载。
- **账号启用 / 禁用**：一键写入 CPA 的 auth 文件，与 CPA 面板状态一致。
- **自动续期**：Tinker 的 access token 只有约 15 分钟，插件在到期前自动刷新。

## 安装

### 方式一：插件商店

在 CPA 配置里追加本仓库的清单源：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/Lxapk/tinker-cpa-plugin/main/registry.json"
```

打开 CPA 管理面板 → 插件 → 插件商店，找到 **Tinker** 安装后重启 CPA。

### 方式二：手动放置

把 `tinker.so` 放进 CPA 工作目录（`config.yaml` 所在目录）的 `plugins/` 下，重启 CPA。

> 插件是 `c-shared` 动态库，必须与 CPA 的 CPU 架构一致（`linux/amd64` 或 `linux/arm64`）。

## 使用

1. 在 CPA 的 OAuth 登录页选择 **Tinker**，填邮箱。
2. 收到验证码后回填，账号自动出现在插件面板。
3. 客户端按 OpenAI 格式调用：

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer <你的 CPA API Key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

4. 管理面板：CPA 管理面板 → 插件 → Tinker。

## 模型

| 类型 | 模型名 | Tinker 工具 |
|---|---|---|
| 文本 | `claude-opus-4-8` | `claude_text` |
| 文本 | `gemini-3-pro` | `gemini_text` |
| 文本 | `grok-4-1-fast-reasoning` | `grok_text` |
| 文本 | `gpt-5.2` | `openai_text` |
| 图片 | `gemini-3.1-flash-image` | `gemini_image` |
| 图片 | `gpt-image` | `gpt_image` |
| 图片 | `flux-image` | `flux_pro_kontext` |
| 图片 | `grok-image` | `grok_image` |
| 视频 | `minimax-video` | `minimax_video` |
| 视频 | `veo-video` | `veo_text_to_video` |
| 视频 | `kling-video` | `kling_image_to_video` |
| 视频 | `seedance-video` | `seedance_video` |

## 配置

在 `plugins.configs.tinker` 下填写，均为可选：

| 字段 | 默认 | 说明 |
|---|---|---|
| `refresh_skew_seconds` | 3600 | 凭据提前刷新的秒数 |
| `poll_interval_millis` | 2000 | 生成任务状态轮询间隔 |
| `generation_timeout_seconds` | 600 | 单次生成最长等待（视频需要数分钟） |
| `image_prompt_limit` | 3900 | 图像/视频 prompt 字符上限（上游硬限制） |
| `text_prompt_limit` | 30000 | 文本 prompt 字符上限 |
| `max_rotate` | 3 | 单次请求最多换几个账号 |
| `cooldown_millis` | 43200000 | 硬失败（鉴权/额度）后的冷却（12h） |
| `soft_cooldown_millis` | 60000 | 瞬时失败的冷却（60s） |
| `error_threshold` | 3 | 连续失败多少次后暂停账号 |
| `log_retention_days` | 30 | 调用记录保留天数 |
| `default_model` | `claude-opus-4-8` | 客户端未指定模型时使用 |
| `debug` | `false` | 详细日志 |

## 实现要点

Tinker 没有 OpenAI 兼容接口，所有能力都走一个 GraphQL 端点，且该端点**拒绝临时查询**：只接受构建期已登记的 sha256 哈希（否则报 `PersistedQueryRequired` / `PersistedQueryNotFound`）。插件自带哈希表（`ophashes.go`，183 个操作，从发布的 web bundle 中 `__meta__.hash` 提取）。

一次生成是三段式：

```
createAndRunArtifact(flowJson + inputs)  -> artifact id + run id
轮询 artifactRun 状态直到终态             -> SUCCEEDED / FAILED / ...
读取 artifact 内容                        -> outputContents[].pages[].content
```

两个细节容易踩坑，已在代码中处理：

- **状态查询与内容查询是两个操作**：`ArtifactView_Get` 便宜但**不含 `outputContents`**，正文必须用 `RehydrateArtifact` 取。只用前者会得到「有 tokens 但正文为空」。
- **refresh 必须带旧 access token**：Tinker 的 `/auth/refresh` 要求 `Authorization: Bearer <当前 token>`，只发 `refresh_token` 会被拒（`no JWT access token provided during refresh`）。

## 构建

依赖 Go 1.26+ 与 C 编译器（cgo，插件为 c-shared 动态库）：

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -o tinker.so .
CGO_ENABLED=0 go test ./...          # 单元测试
sh scripts/run_smoke.sh amd64        # dlopen 端到端冒烟测试
```

发布：修改 `rpc.go` 的 `pluginVersion` 与 `registry.json` 的 `version` / `release_tag`，推送 `v<版本>` tag，GitHub Actions 自动测试、打包并创建 Release。

## License

MIT
