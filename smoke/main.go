// Command smoke loads the built tinker.so through the same C ABI surface
// CLIProxyAPI's pluginhost uses (dlopen + cliproxy_plugin_init) and replays a
// real RPC conversation against it.
//
// It is the end-to-end proof that the produced shared library is loadable and
// speaks the host protocol, independent of the Go unit tests.
package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

typedef int (*init_fn)(cliproxy_host_api*, cliproxy_plugin_api*);

// C-side trampolines. cgo cannot invoke a function pointer held in a struct
// field, so every indirect call goes through these.
static int call_init(void* fn, cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return ((init_fn)fn)(host, plugin);
}
static int call_plugin(cliproxy_plugin_api* api, char* method, uint8_t* req, size_t reqLen, cliproxy_buffer* resp) {
	return api->call(method, req, reqLen, resp);
}
static void free_plugin_buffer(cliproxy_plugin_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}
static void call_shutdown(cliproxy_plugin_api* api) {
	api->shutdown();
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"unsafe"
)

func main() {
	libPath := "dist/tinker.so"
	if len(os.Args) > 1 {
		libPath = os.Args[1]
	}

	cPath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cPath))

	handle := C.dlopen(cPath, C.RTLD_NOW|C.RTLD_LOCAL)
	if handle == nil {
		die("dlopen failed: %s", C.GoString(C.dlerror()))
	}
	defer C.dlclose(handle)
	ok("dlopen %s", libPath)

	initSym := C.CString("cliproxy_plugin_init")
	defer C.free(unsafe.Pointer(initSym))
	initPtr := C.dlsym(handle, initSym)
	if initPtr == nil {
		die("dlsym cliproxy_plugin_init failed: %s", C.GoString(C.dlerror()))
	}

	// None of the exercised code paths require a host callback, so a
	// callback-less host table is sufficient.
	var host C.cliproxy_host_api
	host.abi_version = 1

	var plugin C.cliproxy_plugin_api
	if rc := C.call_init(initPtr, &host, &plugin); rc != 0 {
		die("cliproxy_plugin_init returned %d", int(rc))
	}
	ok("cliproxy_plugin_init -> 0 (abi_version=%d)", uint32(plugin.abi_version))
	if plugin.call == nil || plugin.free_buffer == nil || plugin.shutdown == nil {
		die("plugin ABI table incomplete")
	}

	// --- 1. register --------------------------------------------------
	regResp := call(plugin, "plugin.register", json.RawMessage(`{"schema_version":6,"config_yaml":""}`))
	assertOK(regResp, "plugin.register")
	var reg struct {
		SchemaVersion uint32 `json:"schema_version"`
		// The SDK's pluginapi.Metadata carries no json tags, so the host and this
		// smoke test both read PascalCase field names.
		Metadata struct {
			Name         string `json:"Name"`
			Version      string `json:"Version"`
			Author       string `json:"Author"`
			Repo         string `json:"GitHubRepository"`
			ConfigFields []any  `json:"ConfigFields"`
		} `json:"metadata"`
		Capabilities map[string]any `json:"capabilities"`
	}
	mustUnmarshal(regResp.Result, &reg)
	if reg.Metadata.Name != "Tinker" {
		die("plugin name = %q", reg.Metadata.Name)
	}
	if len(reg.Metadata.ConfigFields) == 0 {
		die("expected config fields, got none")
	}
	// The host refuses a plugin whose metadata is incomplete, so prove it here
	// rather than after an install.
	if reg.Metadata.Name == "" || reg.Metadata.Version == "" ||
		reg.Metadata.Author == "" || reg.Metadata.Repo == "" {
		die("metadata incomplete: name=%q version=%q author=%q repo=%q",
			reg.Metadata.Name, reg.Metadata.Version, reg.Metadata.Author, reg.Metadata.Repo)
	}
	ok("registered %s v%s (schema=%d, config_fields=%d)",
		reg.Metadata.Name, reg.Metadata.Version, reg.SchemaVersion, len(reg.Metadata.ConfigFields))
	for _, cap := range []string{
		"auth_provider", "management_api", "model_registrar",
		"model_provider", "model_router", "executor", "scheduler",
	} {
		if v, present := reg.Capabilities[cap]; !present || v != true {
			die("capability %q not declared: %v", cap, reg.Capabilities)
		}
	}
	ok("all expected capabilities declared")

	// --- 2. auth.identifier must be the provider key -------------------
	identResp := call(plugin, "auth.identifier", nil)
	assertOK(identResp, "auth.identifier")
	var ident struct {
		Identifier string `json:"identifier"`
	}
	mustUnmarshal(identResp.Result, &ident)
	// CPA compares this against auth.Provider before asking for models, so a
	// display spelling here would blank the account list and /v1/models.
	if ident.Identifier != "tinker" {
		die("auth identifier = %q, want the provider key \"tinker\"", ident.Identifier)
	}
	ok("auth.identifier -> %s", ident.Identifier)

	// --- 3. executor.identifier ---------------------------------------
	execIdent := call(plugin, "executor.identifier", nil)
	assertOK(execIdent, "executor.identifier")
	var execIdentOut struct {
		Identifier string `json:"identifier"`
	}
	mustUnmarshal(execIdent.Result, &execIdentOut)
	if execIdentOut.Identifier != "tinker" {
		die("executor identifier = %q", execIdentOut.Identifier)
	}
	ok("executor.identifier -> %s", execIdentOut.Identifier)

	// --- 4. model catalogue -------------------------------------------
	modelsResp := call(plugin, "model.static", json.RawMessage(`{}`))
	assertOK(modelsResp, "model.static")
	var models struct {
		Provider string `json:"Provider"`
		Models   []struct {
			ID   string `json:"ID"`
			Type string `json:"Type"`
		} `json:"Models"`
	}
	mustUnmarshal(modelsResp.Result, &models)
	if models.Provider != "tinker" {
		die("model provider = %q", models.Provider)
	}
	if len(models.Models) != 12 {
		die("expected 12 models, got %d", len(models.Models))
	}
	counts := map[string]int{}
	for _, m := range models.Models {
		counts[m.Type]++
	}
	ok("model.static -> %d models (text=%d image=%d video=%d)",
		len(models.Models), counts["chat"], counts["image"], counts["video"])

	// --- 5. model.route claims only Tinker models ---------------------
	routeIn := call(plugin, "model.route", json.RawMessage(`{"requested_model":"gpt-5.2"}`))
	assertOK(routeIn, "model.route(tinker)")
	var routed struct {
		Handled bool   `json:"handled"`
		Target  string `json:"target"`
	}
	mustUnmarshal(routeIn.Result, &routed)
	if !routed.Handled || routed.Target != "tinker" {
		die("tinker model not routed here: %s", routeIn.Result)
	}
	ok("model.route -> handled=%v target=%s", routed.Handled, routed.Target)

	routeOut := call(plugin, "model.route", json.RawMessage(`{"requested_model":"claude-3-5-sonnet-latest"}`))
	assertOK(routeOut, "model.route(foreign)")
	mustUnmarshal(routeOut.Result, &routed)
	if routed.Handled {
		die("foreign model must not be claimed: %s", routeOut.Result)
	}
	ok("foreign model left to CPA")

	// --- 6. auth.parse accepts a credential blob ---------------------
	parseBlob := `{"Provider":"tinker","RawJSON":"eyJhY2Nlc3NfdG9rZW4iOiJleUpoYkdjaU9pSklVekkxTmlKOS5leUpoWTJOdmRXNTBYMmxrSWpvME56UTJNVEFzSW1WNGNDSTZNVGM1TWpRM016VXlNMzAuc2lnIiwicmVmcmVzaF90b2tlbiI6InJ0LXNtb2tlIiwiZW1haWwiOiJzbW9rZUBleGFtcGxlLmNvbSJ9"}`
	parseResp := call(plugin, "auth.parse", json.RawMessage(parseBlob))
	assertOK(parseResp, "auth.parse")
	var parsed struct {
		Handled bool `json:"Handled"`
		Auth    struct {
			Provider string `json:"Provider"`
			Label    string `json:"Label"`
		} `json:"Auth"`
	}
	mustUnmarshal(parseResp.Result, &parsed)
	if !parsed.Handled {
		die("auth.parse did not handle our own credential: %s", parseResp.Result)
	}
	if parsed.Auth.Provider != "tinker" {
		die("parsed provider = %q", parsed.Auth.Provider)
	}
	ok("auth.parse -> provider=%s label=%s", parsed.Auth.Provider, parsed.Auth.Label)

	// --- 7. management home page -------------------------------------
	mgmtResp := call(plugin, "management.handle", json.RawMessage(`{"Method":"GET","Path":"/v0/resource/plugins/tinker/home"}`))
	assertOK(mgmtResp, "management.handle(home)")
	var mgmt struct {
		StatusCode int    `json:"StatusCode"`
		Body       []byte `json:"Body"`
	}
	mustUnmarshal(mgmtResp.Result, &mgmt)
	if mgmt.StatusCode != 200 {
		die("management status code = %d", mgmt.StatusCode)
	}
	if len(mgmt.Body) == 0 {
		die("management home page is empty")
	}
	ok("management home -> %d bytes", len(mgmt.Body))

	// --- 8. management account list ----------------------------------
	accResp := call(plugin, "management.handle", json.RawMessage(`{"Method":"GET","Path":"/v0/management/tinker/accounts"}`))
	assertOK(accResp, "management.handle(accounts)")
	mustUnmarshal(accResp.Result, &mgmt)
	if mgmt.StatusCode != 200 {
		die("accounts status code = %d", mgmt.StatusCode)
	}
	var accounts struct {
		Total int `json:"total"`
	}
	mustUnmarshal(mgmt.Body, &accounts)
	ok("management accounts -> total=%d", accounts.Total)

	// --- 9. scheduler defers when it has no candidates ---------------
	schedResp := call(plugin, "scheduler.pick", json.RawMessage(`{"candidates":[]}`))
	assertOK(schedResp, "scheduler.pick")
	var picked struct {
		Handled bool `json:"Handled"`
	}
	mustUnmarshal(schedResp.Result, &picked)
	if picked.Handled {
		die("scheduler must defer when there is nothing to choose from")
	}
	ok("scheduler.pick deferred to CPA")

	// --- 10. shutdown -------------------------------------------------
	C.call_shutdown(&plugin)
	ok("shutdown clean")
	fmt.Println("\nsmoke: PASS")
}

type rpcResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func call(api C.cliproxy_plugin_api, method string, body []byte) rpcResult {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var reqPtr *C.uint8_t
	if len(body) > 0 {
		reqPtr = (*C.uint8_t)(C.CBytes(body))
		defer C.free(unsafe.Pointer(reqPtr))
	}

	var buf C.cliproxy_buffer
	rc := C.call_plugin(&api, cMethod, reqPtr, C.size_t(len(body)), &buf)
	if rc != 0 && buf.ptr == nil {
		die("%s: plugin call failed rc=%d", method, int(rc))
	}
	if buf.ptr == nil {
		die("%s: empty response", method)
	}
	raw := C.GoBytes(buf.ptr, C.int(buf.len))
	C.free_plugin_buffer(&api, buf.ptr, buf.len)

	var parsed rpcResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		die("%s: response is not JSON: %v (%s)", method, err, raw)
	}
	return parsed
}

func assertOK(res rpcResult, what string) {
	if res.OK {
		return
	}
	if res.Error != nil {
		die("%s failed: %s (%s)", what, res.Error.Message, res.Error.Code)
	}
	die("%s returned no result", what)
}

func mustUnmarshal(raw json.RawMessage, target any) {
	if err := json.Unmarshal(raw, target); err != nil {
		die("decode failed: %v (%s)", err, raw)
	}
}

func ok(format string, args ...any) {
	fmt.Printf("  ok   "+format+"\n", args...)
}

func die(format string, args ...any) {
	fmt.Printf("  FAIL "+format+"\n", args...)
	os.Exit(1)
}
