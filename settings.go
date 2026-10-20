package main

import (
	"encoding/json"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// pluginSettings is what an operator can tune under plugins.configs.tinker.
type pluginSettings struct {
	// RefreshSkewSeconds is how far ahead of expiry credentials are refreshed.
	RefreshSkewSeconds int64 `json:"refresh_skew_seconds" yaml:"refresh_skew_seconds"`
	// PollIntervalMillis is how often a running generation is polled.
	PollIntervalMillis int64 `json:"poll_interval_millis" yaml:"poll_interval_millis"`
	// GenerationTimeoutSeconds caps how long one generation may take. Tinker
	// runs image and video jobs asynchronously, and a video job legitimately
	// needs several minutes.
	GenerationTimeoutSeconds int64 `json:"generation_timeout_seconds" yaml:"generation_timeout_seconds"`
	// ImagePromptLimit mirrors Tinker's own limit for image/video tools; a
	// longer prompt is rejected upstream with a hard error.
	ImagePromptLimit int `json:"image_prompt_limit" yaml:"image_prompt_limit"`
	// TextPromptLimit caps a text prompt; Tinker tolerates far larger input
	// here, so this is only a guard against pathological requests.
	TextPromptLimit int `json:"text_prompt_limit" yaml:"text_prompt_limit"`
	// MaxRotate is how many credentials to try per request.
	MaxRotate int `json:"max_rotate" yaml:"max_rotate"`
	// CooldownMillis is how long a credential is skipped after a hard failure
	// (auth rejection, quota exhaustion).
	CooldownMillis int64 `json:"cooldown_millis" yaml:"cooldown_millis"`
	// SoftCooldownMillis is the shorter window applied to a transient failure.
	SoftCooldownMillis int64 `json:"soft_cooldown_millis" yaml:"soft_cooldown_millis"`
	// ErrorThreshold is the consecutive-failure count that parks a credential.
	ErrorThreshold int `json:"error_threshold" yaml:"error_threshold"`
	// LogRetentionDays bounds the in-memory call log.
	LogRetentionDays int `json:"log_retention_days" yaml:"log_retention_days"`
	// DefaultModel answers requests that omit the model, or ask for "auto".
	DefaultModel string `json:"default_model" yaml:"default_model"`
	// Debug enables verbose host logging.
	Debug bool `json:"debug" yaml:"debug"`
	// Routing decides how a model call picks an account.
	Routing routingSettings `json:"routing" yaml:"routing"`
}

// routingSettings controls how the plugin spreads requests over its accounts.
type routingSettings struct {
	// Strategy is one of round_robin / fill_first / random / by_usage.
	Strategy string `json:"strategy" yaml:"strategy"`
	// CooldownSeconds is the minimum gap between two switches of the same
	// account, so a burst of requests does not thrash across the pool.
	CooldownSeconds int `json:"cooldown_seconds" yaml:"cooldown_seconds"`
}

func defaultSettings() pluginSettings {
	return pluginSettings{
		// Tinker's access token lives ~900 seconds and the refresh token is
		// single-use, so the skew must stay well under the token lifetime. A skew
		// longer than the lifetime would make every call attempt a refresh, burn
		// the one-shot refresh token, and turn a working account into a dead one.
		RefreshSkewSeconds:       120,
		PollIntervalMillis:       2000,
		GenerationTimeoutSeconds: 600,
		ImagePromptLimit:         3900,
		TextPromptLimit:          30000,
		MaxRotate:                3,
		CooldownMillis:           12 * 60 * 60 * 1000,
		SoftCooldownMillis:       60 * 1000,
		ErrorThreshold:           3,
		LogRetentionDays:         30,
		DefaultModel:             "claude-opus-4-8",
		Routing: routingSettings{
			Strategy:        routingRoundRobin,
			CooldownSeconds: 0,
		},
	}
}

// lifecycleRequest is the payload of plugin.register / plugin.reconfigure.
type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// settingsStore keeps the live configuration plus the resolved data directory.
type settingsStore struct {
	mu   sync.RWMutex
	cfg  pluginSettings
	dir  string
}

var state = &struct {
	settings settingsStore
	accounts *accountStore
	logs     *callLog
	hostMu   sync.Mutex
}{
	settings: settingsStore{cfg: defaultSettings()},
	accounts: newAccountStore(),
	logs:     newCallLog(500),
}

func (s *settingsStore) decodeLifecycleConfig(raw []byte) error {
	cfg := defaultSettings()
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	cfg.applyDefaults()
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// applyDefaults fills in every value the operator left out. A zero here is
// almost always "not configured" rather than a deliberate 0, and a zero
// timeout or poll interval would break the poll loop outright.
func (c *pluginSettings) applyDefaults() {
	def := defaultSettings()
	if c.RefreshSkewSeconds <= 0 {
		c.RefreshSkewSeconds = def.RefreshSkewSeconds
	}
	if c.PollIntervalMillis <= 0 {
		c.PollIntervalMillis = def.PollIntervalMillis
	}
	if c.GenerationTimeoutSeconds <= 0 {
		c.GenerationTimeoutSeconds = def.GenerationTimeoutSeconds
	}
	if c.ImagePromptLimit <= 0 {
		c.ImagePromptLimit = def.ImagePromptLimit
	}
	if c.TextPromptLimit <= 0 {
		c.TextPromptLimit = def.TextPromptLimit
	}
	if c.MaxRotate <= 0 {
		c.MaxRotate = def.MaxRotate
	}
	if c.CooldownMillis <= 0 {
		c.CooldownMillis = def.CooldownMillis
	}
	if c.SoftCooldownMillis <= 0 {
		c.SoftCooldownMillis = def.SoftCooldownMillis
	}
	if c.ErrorThreshold <= 0 {
		c.ErrorThreshold = def.ErrorThreshold
	}
	if c.LogRetentionDays <= 0 {
		c.LogRetentionDays = def.LogRetentionDays
	}
	if strings.TrimSpace(c.DefaultModel) == "" {
		c.DefaultModel = def.DefaultModel
	}
}

func (s *settingsStore) get() pluginSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	cfg.applyDefaults()
	return cfg
}

func (s *settingsStore) dataDir() string {
	s.mu.RLock()
	dir := s.dir
	s.mu.RUnlock()
	return dir
}

func (s *settingsStore) setDataDir(dir string) {
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
}

// logf emits a plugin log line through the host when available and always to
// stderr, so a line is never lost while debugging an install.
func logf(format string, args ...any) {
	msg := sprintf(format, args...)
	payload := map[string]any{"level": "info", "message": "tinker: " + msg}
	if _, err := callHost("host.log", payload); err != nil {
		// Host logging is best-effort; the message already reached stderr.
		_ = err
	}
	stderrln(msg)
}

func debugf(format string, args ...any) {
	if !state.settings.get().Debug {
		return
	}
	logf(format, args...)
}

// encodeJSON is a helper used by handlers that marshal host payloads.
func encodeJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}
