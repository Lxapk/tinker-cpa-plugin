package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleMethod dispatches one CPA RPC call.
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {

	// ---- lifecycle ----------------------------------------------------
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
		}
		if errDecode := state.settings.decodeLifecycleConfig(req.ConfigYAML); errDecode != nil {
			return nil, errDecode
		}
		ensureDataDir()
		return okEnvelope(buildRegistration())

	case pluginabi.MethodPluginQuiesce:
		// Acknowledge: nothing to drain, CPA owns the request lifecycle.
		return okEnvelope(map[string]any{})

	case pluginabi.MethodPluginShutdown:
		shutdownPlugin()
		return okEnvelope(map[string]any{})

	// ---- login (email OTP) -------------------------------------------
	case pluginabi.MethodAuthIdentifier:
		return authIdentifier()

	case pluginabi.MethodAuthParse:
		return authParse(request)

	case pluginabi.MethodAuthLoginStart:
		return authLoginStart(request)

	case pluginabi.MethodAuthLoginPoll:
		return authLoginPoll(request)

	case pluginabi.MethodAuthRefresh:
		return authRefresh(request)

	// ---- model catalogue ---------------------------------------------
	case pluginabi.MethodModelRegister:
		return modelStatic(request)

	case pluginabi.MethodModelStatic:
		return modelStatic(request)

	case pluginabi.MethodModelForAuth:
		return modelForAuth(request)

	// ---- request routing ---------------------------------------------
	case pluginabi.MethodModelRoute:
		return modelRoute(request)

	// ---- upstream execution ------------------------------------------
	case pluginabi.MethodExecutorIdentifier:
		return executorIdentifier()

	case pluginabi.MethodExecutorExecute:
		return executorExecute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return executorExecuteStream(request)

	case pluginabi.MethodExecutorCountTokens:
		return executorCountTokens(request)

	case pluginabi.MethodExecutorHTTPRequest:
		return executorHTTPRequest(request)

	// ---- account selection strategy ----------------------------------
	case pluginabi.MethodSchedulerPick:
		return schedulerPick(request)

	// ---- management API ----------------------------------------------
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())

	case pluginabi.MethodManagementHandle:
		return handleManagementRequest(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotImplemented), nil
	}
}

// buildRegistration declares the plugin's identity and capabilities.
func buildRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepo,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "refresh_skew_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "凭据提前刷新的秒数（Tinker 的 access token 只有约 15 分钟）。"},
				{Name: "poll_interval_millis", Type: pluginapi.ConfigFieldTypeInteger, Description: "生成任务的状态轮询间隔。"},
				{Name: "generation_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "单次生成的最长等待时间，视频任务需要数分钟。"},
				{Name: "image_prompt_limit", Type: pluginapi.ConfigFieldTypeInteger, Description: "图像/视频工具的 prompt 字符上限，超限会被上游直接拒绝。"},
				{Name: "text_prompt_limit", Type: pluginapi.ConfigFieldTypeInteger, Description: "文本工具 prompt 字符上限。"},
				{Name: "max_rotate", Type: pluginapi.ConfigFieldTypeInteger, Description: "单次请求最多换几个账号。"},
				{Name: "cooldown_millis", Type: pluginapi.ConfigFieldTypeInteger, Description: "硬失败（鉴权/额度）后账号的冷却时长。"},
				{Name: "soft_cooldown_millis", Type: pluginapi.ConfigFieldTypeInteger, Description: "瞬时失败后的短暂冷却。"},
				{Name: "error_threshold", Type: pluginapi.ConfigFieldTypeInteger, Description: "连续失败多少次后暂停账号。"},
				{Name: "log_retention_days", Type: pluginapi.ConfigFieldTypeInteger, Description: "调用记录保留天数。"},
				{Name: "default_model", Type: pluginapi.ConfigFieldTypeString, Description: "客户端未指定模型时使用的模型。"},
				{Name: "debug", Type: pluginapi.ConfigFieldTypeBoolean, Description: "输出更详细的日志。"},
			},
		},
		Capabilities: registrationCaps{
			AuthProvider:  true,
			ManagementAPI: true,
			// Tinker's tools are global, and the router must see the request
			// before provider resolution so a Tinker model name is claimed here
			// rather than looked up among CPA's built-in providers.
			ModelRegistrar: true,
			ModelProvider:  true,
			ModelRouter:    true,
			Executor:       true,
			ExecutorModelScope: string(pluginapi.ExecutorModelScopeBoth),
			// Tinker speaks OpenAI chat-completions on the way in; the executor
			// builds the OpenAI-shaped response itself, so both directions stay
			// in that format.
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			Scheduler:             true,
		},
	}
}

// ensureDataDir resolves and loads the plugin's data directory.
//
// CPA changes its working directory to its own config directory, so a relative
// path lands next to config.yaml — which is exactly where an operator expects
// plugin state to live.
func ensureDataDir() {
	dir := strings.TrimSpace(state.settings.dataDir())
	if dir == "" {
		dir = filepath.Join("data", pluginName)
	}
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		logf("创建数据目录失败: %v", errMkdir)
		return
	}
	state.settings.setDataDir(dir)
	if errLoad := state.accounts.load(dir); errLoad != nil {
		logf("读取账号缓存失败: %v", errLoad)
	}
	state.accounts.save(dir)
}

// shutdownPlugin releases plugin state. CPA owns the listener and the auth
// files, so there is nothing to close beyond flushing the cache.
func shutdownPlugin() {
	dir := state.settings.dataDir()
	if dir != "" {
		state.accounts.save(dir)
	}
}

// handleManagementRequest dispatches every management/resource call.
//
// CPA mounts management routes under /v0/management/ and browser-navigable
// resources under /v0/resource/plugins/<id>/. Both arrive here, so a path that
// names the plugin's home page is served the panel HTML and everything else is
// delegated to the JSON handlers.
func handleManagementRequest(request []byte) ([]byte, error) {
	var req managementRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	path := strings.TrimSuffix(strings.TrimSpace(req.Path), "/")
	method := strings.ToUpper(strings.TrimSpace(req.Method))

	if method == http.MethodGet && isHomePath(path) {
		return htmlManagementResponse(pageHTML())
	}

	return handleManagement(request)
}

// isHomePath recognises the resource route registered as "home".
func isHomePath(path string) bool {
	if path == "" || path == "/" {
		return true
	}
	return strings.HasSuffix(path, "/home")
}
