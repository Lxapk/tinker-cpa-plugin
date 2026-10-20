package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// This file hardens the plugin's background goroutines.
//
// Why this matters
//
// CPA fuses a plugin that panics: internal/pluginhost records the panic in its
// `fused` set, and every subsequent capability call for that plugin — including
// model_router and model_provider — is skipped. The user-visible result is
// baffling: an account that worked moments ago starts failing with
// "unknown provider for model ...", with nothing in the UI explaining why.
//
// The scheduler and quota loops run outside any RPC frame, so a panic there is
// not caught by CPA's per-call recover for the call that happens to be in
// flight. Each loop therefore guards itself.

// safeGo runs fn in a goroutine, converting a panic into a diagnostic line.
func safeGo(loopName string, fn func()) {
	go func() {
		defer recoverLoop(loopName)
		fn()
	}()
}

// recoverLoop converts a panic inside a loop iteration into a stderr line.
func recoverLoop(loopName string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr,
			"tinker: recovered panic in %s: %v\n%s\n",
			loopName, r, debug.Stack())
	}
}

// guardRPC wraps an RPC handler so a panic inside the plugin cannot fuse every
// capability at once.
func guardRPC(method string, fn func() ([]byte, error)) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out = errorEnvelope("plugin_panic",
				fmt.Sprintf("%s panicked: %v", method, r), 500)
			err = nil
			fmt.Fprintf(os.Stderr,
				"tinker: recovered panic in %s: %v\n%s\n",
				method, r, debug.Stack())
		}
	}()
	return fn()
}
