// Package main implements the CLIProxyAPI (CPA) dynamic plugin "tinker".
//
// Tinker (Shopify's mobile-first AI tools app, package com.shopify.tinker) has no
// OpenAI-compatible surface of its own. Everything goes through one GraphQL
// endpoint that refuses ad-hoc queries: the server only accepts operations whose
// sha256 hash was registered at build time, so the plugin carries the hash table
// (see ophashes.go) and speaks persisted queries.
//
// One generation is a three-step dance:
//
//	createAndRunArtifact(flowJson + inputs)  -> artifact id + run id
//	reload artifact status until terminal    -> SUCCEEDED / FAILED / ...
//	reload artifact content                  -> outputContents[].pages[].content
//
// CPA already owns the HTTP server, routing, the credential pool and the
// management panel, so this plugin supplies only the provider glue:
//
//	AuthProvider   email-OTP login, multi-account credentials, refresh   (auth_*.go)
//	ModelProvider  the tool catalogue                                    (models.go)
//	Executor       OpenAI request -> Tinker generation -> OpenAI response (executor.go)
//	ManagementAPI  account list + enable/disable + call log               (management.go)
package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// The host API table is handed to us by CPA at init time. cgo cannot invoke a
// function pointer held in a struct field, so every host call goes through
// these C trampolines.
static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	if host == nil {
		return 2
	}
	C.store_host_api(host)
	// Route host RPCs through the cgo implementation now that a host exists.
	hostCallFunc = callHostCgo
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	// guardRPC contains a panic to this one call. CPA recovers plugin panics, but
	// it also marks the plugin fused, which skips every later capability and
	// leaves the user with "unknown provider" for no visible reason.
	raw, errHandle := guardRPC(C.GoString(method), func() ([]byte, error) {
		return handleMethod(C.GoString(method), requestBytes)
	})
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), 500))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownPlugin()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// callHostCgo is the cgo-backed host RPC implementation. cabi.go installs it
// into hostCallFunc during init; host_rpc.go provides the cgo-free default so
// the package also builds and tests with CGO_ENABLED=0.
func callHostCgo(method string, payload any) (json.RawMessage, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var body []byte
	if payload != nil {
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return nil, errMarshal
		}
		body = raw
	}

	var reqPtr *C.uint8_t
	if len(body) > 0 {
		reqPtr = (*C.uint8_t)(C.CBytes(body))
		defer C.free(unsafe.Pointer(reqPtr))
	}

	var buf C.cliproxy_buffer
	rc := C.call_host_api(cMethod, reqPtr, C.size_t(len(body)), &buf)
	if buf.ptr != nil {
		defer C.free_host_buffer(buf.ptr, buf.len)
	}
	if rc != 0 {
		return nil, errHostUnavailable
	}
	if buf.ptr == nil || buf.len == 0 {
		return nil, nil
	}
	raw := C.GoBytes(buf.ptr, C.int(buf.len))

	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if !env.OK {
		if env.Error != nil {
			return nil, errHostRPC{Code: env.Error.Code, Message: env.Error.Message}
		}
		return nil, errHostUnavailable
	}
	return env.Result, nil
}
