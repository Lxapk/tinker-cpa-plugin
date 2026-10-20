package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const (
	// pluginName is the plugin's identifier, and it has to stay directory-safe.
	//
	// CPA derives the routes from it — /v0/management/<pluginName>,
	// /v0/resource/plugins/<pluginName> — and the plugin resolves its data
	// directory with it. Changing it would break every request the panel makes
	// and orphan the stored configuration, so it is not a label.
	pluginName = "tinker"
	// pluginDisplayName is what a person reads.
	pluginDisplayName = "Tinker"
	pluginVersion     = "0.1.0"
	pluginAuthor      = "Lxapk"
	pluginRepo        = "https://github.com/Lxapk/tinker-cpa-plugin"
)

// envelope is the CPA RPC envelope:
//
//	{"ok":true,"result":{...}}  |  {"ok":false,"error":{"code","message","http_status"}}
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// errorEnvelope builds a failure envelope. httpStatus defaults to 500 the same
// way CPA's pluginabi.NewErrorEnvelope treats a zero status.
func errorEnvelope(code, message string, httpStatus int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
	}})
	return raw
}

// okEnvelope wraps a value in the CPA success envelope.
func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(raw)})
}

// identifierResponse answers every *.identifier method.
type identifierResponse struct {
	Identifier string `json:"identifier"`
}

// registration mirrors pluginhost.rpcRegistration.
type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      metadataInfo       `json:"metadata"`
	Capabilities  registrationCaps   `json:"capabilities"`
}

// metadataInfo mirrors pluginapi.Metadata but is declared locally so the
// registration payload does not depend on the SDK's optional fields.
type metadataInfo struct {
	Name             string                `json:"name"`
	Version          string                `json:"version"`
	Author           string                `json:"author"`
	GitHubRepository string                `json:"github_repository"`
	ConfigFields     []configFieldMetadata `json:"config_fields,omitempty"`
}

type configFieldMetadata struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// registrationCaps mirrors pluginhost.rpcCapabilities. Only the fields this
// plugin sets are declared; the rest default to false/empty.
type registrationCaps struct {
	AuthProvider bool `json:"auth_provider"`

	RequestInterceptor     bool `json:"request_interceptor"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
	UsagePlugin            bool `json:"usage_plugin"`
	ManagementAPI          bool `json:"management_api"`

	// Model catalogue + execution. Both model routes are declared, as the
	// official simple example does: model.register lets a startup pass publish
	// the catalogue, model.static answers on demand, and model.for_auth serves
	// the per-credential view the auth-file page needs.
	ModelRegistrar bool `json:"model_registrar"`
	ModelProvider  bool `json:"model_provider"`
	ModelRouter    bool `json:"model_router"`
	Executor       bool `json:"executor"`

	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`

	// Scheduler lets the plugin choose which credential a request uses, which
	// is what makes round-robin / fill-first switching configurable.
	Scheduler bool `json:"scheduler"`
}

// managementRegistrationResponse mirrors pluginhost.rpcManagementRegistrationResponse.
type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description,omitempty"`
}

// statusOf is a tiny helper for building error statuses in handlers.
func statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if typed, ok := err.(errHostRPC); ok && typed.Code == "unauthorized" {
		return http.StatusUnauthorized
	}
	return http.StatusInternalServerError
}

func joinErr(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
