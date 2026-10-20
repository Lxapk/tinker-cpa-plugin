package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tinkerCredentials is what gets persisted in the CPA auth file's storage JSON.
//
// Tinker issues a short-lived JWT (15 minutes) plus a long-lived refresh token.
// Both are kept because a refresh needs the *current* access token in
// Authorization; see tinkerClient.refresh.
type tinkerCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Email        string `json:"email,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	// UID is the account identity the panel groups credentials by. Tinker has no
	// separate user id on the wire, so it mirrors AccountID.
	UID       string `json:"uid,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	// AddedAt records when the account was first seen, for panel ordering.
	AddedAt int64 `json:"added_at,omitempty"`
}

// accountKey is the stable identity used to deduplicate a re-login of the same
// person into the same account record.
func (c *tinkerCredentials) accountKey() string {
	if c.UID != "" {
		return c.UID
	}
	if c.AccountID != "" {
		return c.AccountID
	}
	return strings.ToLower(strings.TrimSpace(c.Email))
}

// label is the user-facing account name.
func (c *tinkerCredentials) label() string {
	if c.Email != "" {
		return c.Email
	}
	if key := c.accountKey(); key != "" {
		return key
	}
	return "Tinker 账号"
}

// expired reports whether the access token is past (or near) its expiry.
func (c *tinkerCredentials) expired(skew time.Duration) bool {
	if c.ExpiresAt <= 0 {
		return false
	}
	return time.Now().Add(skew).Unix() >= c.ExpiresAt
}

// parseCredentials reads the persisted storage JSON.
func parseCredentials(raw []byte) (*tinkerCredentials, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, errors.New("凭据内容为空")
	}

	// Accept both the flat shape this plugin writes and a {"data":{...}}
	// envelope, because an auth file copied between installs may carry either.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 &&
		strings.HasPrefix(strings.TrimSpace(string(envelope.Data)), "{") {
		var inner tinkerCredentials
		if errInner := json.Unmarshal(envelope.Data, &inner); errInner == nil && inner.AccessToken != "" {
			return normalizeCredentials(&inner), nil
		}
	}

	var creds tinkerCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("无法解析凭据: %w", err)
	}
	if creds.AccessToken == "" {
		return nil, errors.New("凭据缺少 access_token")
	}
	return normalizeCredentials(&creds), nil
}

// parseCredentialInput accepts whatever the operator pasted: a JSON blob, a
// bare token, or an "email + token" pair pasted as two lines.
func parseCredentialInput(raw []byte) (*tinkerCredentials, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("未提供任何凭据内容")
	}

	if strings.HasPrefix(trimmed, "{") {
		if creds, err := parseCredentials([]byte(trimmed)); err == nil {
			return creds, nil
		}
	}

	// A bare token: derive what can be derived from its claims.
	if looksLikeToken(trimmed) {
		creds := &tinkerCredentials{
			AccessToken: trimmed,
			AccountID:   claimString(trimmed, "account_id"),
			ExpiresAt:   claimInt64(trimmed, "exp"),
		}
		creds.UID = creds.AccountID
		return normalizeCredentials(creds), nil
	}

	// Two lines: email then token, which is how a person tends to paste it.
	lines := strings.Split(trimmed, "\n")
	if len(lines) >= 2 {
		email := strings.TrimSpace(lines[0])
		token := strings.TrimSpace(strings.Join(lines[1:], "\n"))
		if strings.Contains(email, "@") && looksLikeToken(token) {
			creds := &tinkerCredentials{
				AccessToken: token,
				Email:       email,
				AccountID:   claimString(token, "account_id"),
				ExpiresAt:   claimInt64(token, "exp"),
			}
			creds.UID = creds.AccountID
			return normalizeCredentials(creds), nil
		}
	}

	return nil, errors.New("无法识别的凭据格式：既不是 JSON 也不是访问令牌")
}

// normalizeCredentials fills derived fields.
func normalizeCredentials(c *tinkerCredentials) *tinkerCredentials {
	if c.AccountID == "" {
		c.AccountID = claimString(c.AccessToken, "account_id")
	}
	if c.UID == "" {
		c.UID = c.AccountID
	}
	if c.ExpiresAt == 0 {
		c.ExpiresAt = claimInt64(c.AccessToken, "exp")
	}
	if c.AddedAt == 0 {
		c.AddedAt = time.Now().Unix()
	}
	c.AccessToken = strings.TrimSpace(c.AccessToken)
	c.RefreshToken = strings.TrimSpace(c.RefreshToken)
	c.Email = strings.TrimSpace(c.Email)
	return c
}

// looksLikeToken is a permissive sanity check so obviously-wrong input (prose,
// empty strings) is rejected early with a clear message.
func looksLikeToken(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '~', r == '+', r == '/', r == '=':
			continue
		default:
			return false
		}
	}
	return true
}

/* ---------- JWT claims ---------- */

// jwtClaims decodes the payload segment of a JWT without verifying it. The
// signature is the server's business; the plugin only needs the account id and
// expiry it already trusts because the token came over TLS from a login.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(raw, &claims); errUnmarshal != nil {
		return nil
	}
	return claims
}

// claimString reads a claim as a string, accepting numbers too because
// account_id arrives as a JSON number.
func claimString(token string, names ...string) string {
	claims := jwtClaims(token)
	if claims == nil {
		return ""
	}
	for _, name := range names {
		value, ok := claims[name]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if typed != "" {
				return typed
			}
		case float64:
			return fmt.Sprintf("%.0f", typed)
		case json.Number:
			return typed.String()
		}
	}
	return ""
}

// claimInt64 reads a numeric claim.
func claimInt64(token string, names ...string) int64 {
	claims := jwtClaims(token)
	if claims == nil {
		return 0
	}
	for _, name := range names {
		value, ok := claims[name]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int64(typed)
		case json.Number:
			if parsed, err := typed.Int64(); err == nil {
				return parsed
			}
		}
	}
	return 0
}
