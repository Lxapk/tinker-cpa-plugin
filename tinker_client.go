package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	// tinkerBaseURL is the only backend Tinker talks to. The Android app and the
	// web build both use it, and the GraphQL endpoint lives on the same host.
	tinkerBaseURL = "https://app.tinker.com"
	// tinkerClientName identifies the client in the headers Tinker's own app
	// sends; the server is lenient about it but the gateway logic keys some
	// behaviour off it.
	tinkerClientName = "tinker-mobile"
	// The app builds X-Client-Version as "<versionName>-<versionCode>"; these are
	// the values from the 1.0.18 APK this plugin was written against.
	tinkerAppVersion = "1.0.18"
	tinkerAppBuild   = "870"
	// Device identity. A real Pixel 8 profile: the server compares these against
	// nothing in particular, but a plausible, stable set reads as one physical
	// device rather than an anonymous script.
	tinkerOSVersion  = "14"
	tinkerDeviceModel = "Pixel 8"
	tinkerDeviceName  = "Pixel 8"
	tinkerLocale      = "zh-CN"
)

// tinkerClient is the thin GraphQL/REST client used by every capability.
type tinkerClient struct {
	http *http.Client
}

var upstream = &tinkerClient{
	http: &http.Client{Timeout: 120 * time.Second},
}

// graphqlError is the shape of one entry in Tinker's "errors" array.
type graphqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// apiError carries an upstream failure with enough detail for the caller to
// decide whether another account is worth trying.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

// isAuthError reports whether the failure means "this credential is dead".
func (e *apiError) isAuthError() bool {
	if e.Status == http.StatusUnauthorized {
		return true
	}
	switch e.Code {
	case "PERSISTED_QUERY_NOT_FOUND", "PERSISTED_QUERY_REQUIRED":
		// Not the credential's fault: the build-time hash table is stale.
		return false
	}
	lowered := strings.ToLower(e.Message)
	return strings.Contains(lowered, "authentication required") ||
		strings.Contains(lowered, "unauthorized") ||
		strings.Contains(lowered, "invalid jwt")
}

// isQuotaError reports whether the account is out of generation credits.
func (e *apiError) isQuotaError() bool {
	lowered := strings.ToLower(e.Message)
	for _, phrase := range []string{
		"generation limit", "quota", "credit", "额度", "积分", "次数已用完",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return e.Status == http.StatusPaymentRequired
}

// doJSON performs one authenticated JSON call and returns the raw body.
func (c *tinkerClient) doJSON(ctx context.Context, method, path string, body []byte, accessToken string) ([]byte, error) {
	req, errRequest := http.NewRequestWithContext(ctx, method, tinkerBaseURL+path, bytes.NewReader(body))
	if errRequest != nil {
		return nil, errRequest
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Never let the transport negotiate gzip: the sizes here are small and a
	// transparent decode failure surfaces as a JSON parse error much later.
	req.Header.Set("Accept-Encoding", "identity")
	applyClientHeaders(req, accessToken)

	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("上游请求失败: %w", errDo)
	}
	defer resp.Body.Close()

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", errRead)
	}
	if resp.StatusCode >= 400 {
		return raw, decodeAPIError(resp.StatusCode, raw)
	}
	return raw, nil
}

// decodeAPIError turns whatever the server returned into an apiError. Tinker
// answers REST failures as {"error":"..."} and GraphQL failures inside a 200
// with an "errors" array, so both shapes are handled here.
func decodeAPIError(status int, raw []byte) *apiError {
	out := &apiError{Status: status, Message: strings.TrimSpace(string(raw))}
	if out.Message == "" {
		out.Message = http.StatusText(status)
	}

	var rest struct {
		Error  string `json:"error"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(raw, &rest); err == nil && rest.Error != "" {
		out.Message = rest.Error
		return out
	}

	var gql struct {
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &gql); err == nil && len(gql.Errors) > 0 {
		out.Message = gql.Errors[0].Message
		out.Code = gql.Errors[0].Extensions.Code
		if strings.EqualFold(out.Code, "UNAUTHENTICATED") {
			out.Status = http.StatusUnauthorized
		}
		return out
	}
	return out
}

/* ---------- authentication ---------- */

// requestEmailOTP asks Tinker to mail a login code.
func (c *tinkerClient) requestEmailOTP(ctx context.Context, email string) (time.Time, error) {
	payload, _ := json.Marshal(map[string]string{"email": email})
	raw, err := c.doJSON(ctx, http.MethodPost, "/auth/request-email-otp", payload, "")
	if err != nil {
		return time.Time{}, err
	}
	var resp struct {
		Message     string `json:"message"`
		ResendAfter string `json:"resend_after"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.ResendAfter != "" {
		if when, errParse := time.Parse(time.RFC3339, resp.ResendAfter); errParse == nil {
			return when, nil
		}
	}
	return time.Now().Add(time.Minute), nil
}

// verifyEmailOTP exchanges the mailed code for an access/refresh pair.
func (c *tinkerClient) verifyEmailOTP(ctx context.Context, email, code string) (*tinkerCredentials, error) {
	payload, _ := json.Marshal(map[string]string{
		"provider": "email",
		"email":    email,
		"code":     code,
	})
	raw, err := c.doJSON(ctx, http.MethodPost, "/auth/verify", payload, "")
	if err != nil {
		return nil, err
	}
	return parseVerifyResponse(raw, email)
}

// parseVerifyResponse reads the token pair out of /auth/verify.
func parseVerifyResponse(raw []byte, email string) (*tinkerCredentials, error) {
	var resp struct {
		Error        string `json:"error"`
		UserID       any    `json:"user_id"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("无法解析登录响应: %w", err)
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.AccessToken == "" {
		return nil, errors.New("登录响应缺少 access_token")
	}
	creds := &tinkerCredentials{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		Email:        email,
		AccountID:    claimString(resp.AccessToken, "account_id"),
		ExpiresAt:    claimInt64(resp.AccessToken, "exp"),
	}
	creds.UID = creds.AccountID
	return creds, nil
}

// refresh exchanges a refresh token for a new pair.
//
// Tinker requires the *current* access token in Authorization even when it has
// already expired — sending only refresh_token is rejected with
// "no JWT access token provided during refresh".
func (c *tinkerClient) refresh(ctx context.Context, creds *tinkerCredentials) (*tinkerCredentials, error) {
	if creds == nil || creds.RefreshToken == "" {
		return nil, errors.New("缺少 refresh_token")
	}
	payload, _ := json.Marshal(map[string]string{"refresh_token": creds.RefreshToken})
	raw, err := c.doJSON(ctx, http.MethodPost, "/auth/refresh", payload, creds.AccessToken)
	if err != nil {
		return nil, err
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, fmt.Errorf("无法解析刷新响应: %w", errUnmarshal)
	}
	if resp.AccessToken == "" {
		return nil, errors.New("刷新响应缺少 access_token")
	}
	next := *creds
	next.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		next.RefreshToken = resp.RefreshToken
	}
	next.ExpiresAt = claimInt64(resp.AccessToken, "exp")
	return &next, nil
}

// currentUser resolves the account behind a credential, used to validate a
// freshly added account and to fill the panel's label.
func (c *tinkerClient) currentUser(ctx context.Context, accessToken string) (string, error) {
	raw, err := c.graphql(ctx, "Auth_CurrentUser", nil, accessToken)
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			CurrentUser *struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"currentUser"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return "", errUnmarshal
	}
	if resp.Data.CurrentUser == nil {
		return "", errors.New("登录已失效")
	}
	return resp.Data.CurrentUser.Email, nil
}

/* ---------- graphql ---------- */

// graphql issues one persisted-query call.
func (c *tinkerClient) graphql(ctx context.Context, operation string, variables map[string]any, accessToken string) ([]byte, error) {
	hash, okHash := opHashes[operation]
	if !okHash {
		return nil, fmt.Errorf("未知操作 %s", operation)
	}
	if variables == nil {
		variables = map[string]any{}
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"operationName": operation,
		"variables":     variables,
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	})
	if errMarshal != nil {
		return nil, errMarshal
	}
	raw, err := c.doJSON(ctx, http.MethodPost, "/graphql", payload, accessToken)
	if err != nil {
		return raw, err
	}
	// A GraphQL failure still arrives as HTTP 200, so the errors array has to be
	// inspected explicitly or a null payload is all the caller would see.
	if errGQL := detectGraphQLError(raw); errGQL != nil {
		return raw, errGQL
	}
	return raw, nil
}

// detectGraphQLError returns an apiError when the payload carries errors.
func detectGraphQLError(raw []byte) error {
	var resp struct {
		Errors []graphqlError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Errors) == 0 {
		return nil
	}
	first := resp.Errors[0]
	apiErr := &apiError{Status: http.StatusBadGateway, Message: first.Message, Code: first.Extensions.Code}
	if apiErr.isAuthError() {
		apiErr.Status = http.StatusUnauthorized
	}
	return apiErr
}

/* ---------- generation ---------- */

// runArtifact creates a generation and blocks until it reaches a terminal state.
type generationResult struct {
	ArtifactID string
	RunID      string
	Status     string
	Artifact   map[string]any
}

// generate performs the create -> poll -> fetch-content cycle.
func (c *tinkerClient) generate(ctx context.Context, accessToken, toolID, modelID string, inputs map[string]any) (*generationResult, error) {
	createdRaw, errCreate := c.createGeneration(ctx, accessToken, toolID, modelID, inputs)
	if errCreate != nil {
		return nil, errCreate
	}

	var created struct {
		Data struct {
			CreateAndRunArtifact *struct {
				Artifact map[string]any `json:"artifact"`
				Run      *struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"run"`
			} `json:"createAndRunArtifact"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(createdRaw, &created); errUnmarshal != nil {
		return nil, fmt.Errorf("无法解析创建响应: %w", errUnmarshal)
	}
	payload := created.Data.CreateAndRunArtifact
	if payload == nil || payload.Artifact == nil {
		return nil, errors.New("生成任务创建失败")
	}
	artifactID, _ := payload.Artifact["id"].(string)
	if artifactID == "" {
		return nil, errors.New("生成任务未返回 artifact id")
	}
	runID := ""
	if payload.Run != nil {
		runID = payload.Run.ID
	}

	status, errWait := c.waitForCompletion(ctx, accessToken, artifactID)
	if errWait != nil {
		return nil, errWait
	}

	artifact, errContent := c.fetchArtifact(ctx, accessToken, artifactID)
	if errContent != nil {
		return nil, errContent
	}
	return &generationResult{
		ArtifactID: artifactID,
		RunID:      runID,
		Status:     status,
		Artifact:   artifact,
	}, nil
}

// createGeneration issues createAndRunArtifact with the flow shape Tinker's own
// client builds: one node whose inputs are template references, plus the
// concrete input values.
func (c *tinkerClient) createGeneration(ctx context.Context, accessToken, toolID, modelID string, inputs map[string]any) ([]byte, error) {
	// Every key is both referenced by the node's template and supplied as a
	// concrete input. Tinker's tool schema exposes more than a prompt — image
	// tools take aspect_ratio / resolution / output_format — and passing only the
	// prompt silently dropped them, so a client asking for 4K got the 1K default.
	template := make(map[string]any, len(inputs))
	for name := range inputs {
		template[name] = "${inputs." + name + "}"
	}
	node := map[string]any{
		"id":     "node-1",
		"type":   toolID,
		"inputs": template,
	}
	if modelID != "" {
		node["model"] = modelID
	}
	flow := map[string]any{
		"schemaVersion": 1,
		"nodes":         []any{node},
	}
	input := map[string]any{
		"flowJson": flow,
		"inputs":   inputs,
		// The enum is upper-case on the wire; "Private" is rejected with a
		// variable-validation error that never mentions casing.
		"privacy": "PRIVATE",
	}
	return c.graphql(ctx, "Artifact_CreateAndExecute", map[string]any{"input": input}, accessToken)
}

// waitForCompletion polls the artifact until its latest run is terminal.
//
// Two queries are in play: ArtifactView_Get is cheap and carries the run status,
// while RehydrateArtifact is the only one that returns outputContents. Polling
// the heavy query every few seconds would pull the whole artifact repeatedly.
func (c *tinkerClient) waitForCompletion(ctx context.Context, accessToken, artifactID string) (string, error) {
	cfg := state.settings.get()
	deadline := time.Now().Add(time.Duration(cfg.GenerationTimeoutSeconds) * time.Second)
	interval := time.Duration(cfg.PollIntervalMillis) * time.Millisecond
	var lastStatus string

	for {
		if errCtx := ctx.Err(); errCtx != nil {
			return lastStatus, errCtx
		}
		raw, errStatus := c.graphql(ctx, "ArtifactView_Get", map[string]any{"artifactId": artifactID}, accessToken)
		if errStatus != nil {
			return lastStatus, errStatus
		}
		status, message := runStatusOf(raw)
		if status != "" && status != lastStatus {
			lastStatus = status
			debugf("artifact %s 状态 %s", artifactID, status)
		}
		switch status {
		case "SUCCEEDED", "COMPLETED":
			return status, nil
		case "FAILED", "ERROR", "CANCELLED":
			if message == "" {
				message = "生成失败"
			}
			return status, &apiError{Status: http.StatusBadGateway, Message: message}
		}

		if time.Now().After(deadline) {
			return lastStatus, &apiError{Status: http.StatusGatewayTimeout, Message: "生成超时"}
		}
		select {
		case <-ctx.Done():
			return lastStatus, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// runStatusOf pulls latestRun.status and the display error out of either query.
func runStatusOf(raw []byte) (string, string) {
	var resp struct {
		Data struct {
			Artifact *struct {
				LatestRun *struct {
					Status               string `json:"status"`
					DisplayErrorMessage  string `json:"displayErrorMessage"`
				} `json:"latestRun"`
			} `json:"artifact"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Data.Artifact == nil {
		return "", ""
	}
	if resp.Data.Artifact.LatestRun == nil {
		return "", ""
	}
	return resp.Data.Artifact.LatestRun.Status, resp.Data.Artifact.LatestRun.DisplayErrorMessage
}

// fetchArtifact reads the artifact with its content, retrying briefly because
// the content can land a moment after the run turns terminal.
func (c *tinkerClient) fetchArtifact(ctx context.Context, accessToken, artifactID string) (map[string]any, error) {
	var last map[string]any
	for attempt := 0; attempt < 5; attempt++ {
		raw, err := c.graphql(ctx, "RehydrateArtifact", map[string]any{"artifactId": artifactID}, accessToken)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data struct {
				Artifact map[string]any `json:"artifact"`
			} `json:"data"`
		}
		if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		last = resp.Data.Artifact
		if last != nil && artifactHasOutput(last) {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	if last == nil {
		return nil, errors.New("读取生成结果失败")
	}
	return last, nil
}

// uploadImage sends one image to Tinker and returns the blob reference the tool
// inputs expect.
//
// Tinker does not accept a bare URL for a reference photo: its upload UI mints a
// blob through uploadArtifactFile and then passes {image_blob_id: <signedId>,
// previewUrl, name} as the input value. So a client's image_url has to be
// downloaded here and re-uploaded.
//
// The GraphQL multipart request spec is used: operations, map and the file parts.
func (c *tinkerClient) uploadImage(ctx context.Context, accessToken string, data []byte, filename, contentType string) (map[string]any, error) {
	if len(data) == 0 {
		return nil, errors.New("图片内容为空")
	}
	operation := map[string]any{
		"operationName": "Upload_ArtifactFile",
		"variables": map[string]any{
			"input": map[string]any{"file": nil},
		},
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": opHashes["Upload_ArtifactFile"],
			},
		},
	}
	operationsJSON, errOps := json.Marshal(operation)
	if errOps != nil {
		return nil, errOps
	}
	mapJSON, _ := json.Marshal(map[string][]string{"0": {"variables.input.file"}})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errField := writer.WriteField("operations", string(operationsJSON)); errField != nil {
		return nil, errField
	}
	if errField := writer.WriteField("map", string(mapJSON)); errField != nil {
		return nil, errField
	}
	// CreateFormFile hard-codes application/octet-stream, which Tinker rejects
	// ("Invalid file type: application/octet-stream"). The part has to declare the
	// real image type, so it is built by hand.
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="0"; filename="%s"`, filename))
	header.Set("Content-Type", contentType)
	part, errPart := writer.CreatePart(header)
	if errPart != nil {
		return nil, errPart
	}
	if _, errWrite := part.Write(data); errWrite != nil {
		return nil, errWrite
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, errClose
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, tinkerBaseURL+"/graphql", &body)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	applyClientHeaders(req, accessToken)

	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("上传请求失败: %w", errDo)
	}
	defer resp.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if errRead != nil {
		return nil, fmt.Errorf("读取上传响应失败: %w", errRead)
	}
	if resp.StatusCode >= 400 {
		return nil, decodeAPIError(resp.StatusCode, raw)
	}
	if errGQL := detectGraphQLError(raw); errGQL != nil {
		return nil, errGQL
	}

	var parsed struct {
		Data struct {
			UploadArtifactFile struct {
				Blob *struct {
					SignedID  string `json:"signedId"`
					URL       string `json:"url"`
					Filename  string `json:"filename"`
					ContentTy string `json:"contentType"`
				} `json:"blob"`
			} `json:"uploadArtifactFile"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	blob := parsed.Data.UploadArtifactFile.Blob
	if blob == nil || blob.SignedID == "" {
		return nil, errors.New("上传未返回 blob")
	}
	// This is the exact shape Tinker's own UI puts in the inputs map.
	return map[string]any{
		"image_blob_id": blob.SignedID,
		"previewUrl":    blob.URL,
		"name":          firstNonEmpty(blob.Filename, filename),
	}, nil
}

// fetchImage downloads a client-supplied image URL.
func (c *tinkerClient) fetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if errReq != nil {
		return nil, "", errReq
	}
	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return nil, "", fmt.Errorf("下载图片失败: %w", errDo)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("下载图片失败: HTTP %d", resp.StatusCode)
	}
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if errRead != nil {
		return nil, "", errRead
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}

// applyClientHeaders sets the request context the app sends on every call.
//
// The server derives per-account trust from these: its risk model reports
// "Device: <status>" and "Latest class: <classification>" alongside the IP events,
// and an account that presents no device information has no positive evidence to
// weigh. Sending only the client name marked every account as an unknown client,
// which is exactly the state that gets generation restricted.
//
// Values mirror the Android build (package com.shopify.tinker, 1.0.18 / 870).
// X-Client-Version is "<versionName>-<versionCode>" — the app joins the two with a
// dash and omits either half when null, so the separator only appears when both
// exist.
func applyClientHeaders(req *http.Request, accessToken string) {
	req.Header.Set("X-Client-Name", tinkerClientName)
	req.Header.Set("X-Client-Version", tinkerAppVersion+"-"+tinkerAppBuild)
	req.Header.Set("X-Platform", "android")
	req.Header.Set("X-OS-Version", tinkerOSVersion)
	req.Header.Set("X-Device-Type", "mobile")
	req.Header.Set("X-Device-Model", tinkerDeviceModel)
	req.Header.Set("X-Device-Name", tinkerDeviceName)
	req.Header.Set("Shopify-Client-Locale", tinkerLocale)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
}
