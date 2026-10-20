package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// accountStore is the plugin's own view of the accounts it has seen.
//
// CPA owns the authoritative auth files; this store is a cache that adds what
// the host does not track: the operator's enable/disable choice, the block
// marker, and a stable identity so a panel row survives a refresh.
//
// Every mutation is written through to <dataDir>/accounts.json so a restart
// rehydrates the same view.
type accountStore struct {
	mu    sync.RWMutex
	items map[string]*accountRecord
	dirty bool
}

type accountRecord struct {
	Key       string `json:"key"`
	UID       string `json:"uid,omitempty"`
	Email     string `json:"email,omitempty"`
	AuthIndex string `json:"auth_index,omitempty"`
	// Disabled is the operator's choice, mirrored into the CPA auth file's
	// top-level "disabled" key so the host stops routing to it.
	Disabled bool `json:"disabled"`
	// AuthRejected records that the server refused the credential outright
	// (invalid or revoked token). It is not a permanent verdict: it is set only
	// when the server says so, and it clears as soon as a refresh or a login
	// succeeds. The panel reports it, but the pool decides usability by the
	// cooldown alone — a stored "dead" flag outlived the cause and left healthy
	// accounts looking broken.
	AuthRejected bool   `json:"auth_rejected,omitempty"`
	AuthReason   string `json:"auth_reason,omitempty"`
	// ConsecutiveFailures feeds the park threshold.
	Failures  int   `json:"failures"`
	LastError string `json:"last_error,omitempty"`
	// CooldownUntil suppresses a credential until this Unix ms timestamp.
	CooldownUntil int64  `json:"cooldown_until,omitempty"`
	LastUsed      int64  `json:"last_used,omitempty"`
	AddedAt       int64  `json:"added_at,omitempty"`
	// CallCount feeds the by_usage strategy and the panel's statistics.
	CallCount int64 `json:"call_count,omitempty"`
	// SuccessCount and FailureCount split CallCount for the panel.
	SuccessCount int64 `json:"success_count,omitempty"`
	FailureCount int64 `json:"failure_count,omitempty"`
	// ExpiresAt mirrors the credential's token expiry, shown in the account table.
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

func newAccountStore() *accountStore {
	return &accountStore{items: map[string]*accountRecord{}}
}

// load rehydrates the store from disk.
func (s *accountStore) load(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	raw, errRead := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return errRead
	}
	var items map[string]*accountRecord
	if errUnmarshal := json.Unmarshal(raw, &items); errUnmarshal != nil {
		return errUnmarshal
	}
	s.mu.Lock()
	s.items = items
	if s.items == nil {
		s.items = map[string]*accountRecord{}
	}
	s.mu.Unlock()
	return nil
}

// save persists the store. It is called after every mutation.
func (s *accountStore) save(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	s.mu.RLock()
	raw, errMarshal := json.MarshalIndent(s.items, "", "  ")
	s.mu.RUnlock()
	if errMarshal != nil {
		return
	}
	path := filepath.Join(dir, "accounts.json")
	tmp, errCreate := os.CreateTemp(dir, ".accounts-*.tmp")
	if errCreate != nil {
		logf("写入账号缓存失败: %v", errCreate)
		return
	}
	tmpName := tmp.Name()
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		logf("写入账号缓存失败: %v", errWrite)
		return
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		_ = os.Remove(tmpName)
		logf("写入账号缓存失败: %v", errRename)
	}
}

// observe records a credential the plugin just used or saw.
func (s *accountStore) observe(creds *tinkerCredentials, authIndex string) {
	if creds == nil {
		return
	}
	key := creds.accountKey()
	if key == "" {
		return
	}
	s.mu.Lock()
	record, ok := s.items[key]
	if !ok {
		record = &accountRecord{Key: key, AddedAt: time.Now().Unix()}
		s.items[key] = record
	}
	if creds.UID != "" {
		record.UID = creds.UID
	}
	if creds.Email != "" {
		record.Email = creds.Email
	}
	if authIndex != "" {
		record.AuthIndex = authIndex
	}
	if creds.ExpiresAt > 0 {
		record.ExpiresAt = creds.ExpiresAt
	}
	// Seeing a credential again means it is in use; an old failure streak should
	// not decide anything about it.
	record.Failures = 0
	record.CooldownUntil = 0
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// markUsed stamps a successful call.
func (s *accountStore) markUsed(key string) {
	s.mu.Lock()
	if record, ok := s.items[key]; ok {
		record.LastUsed = time.Now().Unix()
		record.Failures = 0
		record.LastError = ""
		record.CooldownUntil = 0
		// A successful call proves the credential works, so the advisory badge
		// must go with it.
		record.AuthRejected = false
		record.AuthReason = ""
		record.CallCount++
		record.SuccessCount++
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// markFailure records a failed call and applies the configured cooldown.
// failureKind classifies a failed call so the right cooldown applies.
//
// Ported from the reference plugin's classifyUpstream: an auth rejection and a
// quota exhaustion are hard problems, a throttle is short-lived, and everything
// else is transient. The distinction matters because the cooldowns differ by
// three orders of magnitude — treating a timeout like an auth rejection parked a
// healthy account for twelve hours.
type failureKind int

const (
	failureTransient failureKind = iota
	failureAuth
	failureRate
	failureQuota
)

// markFailure records a failed call and applies the cooldown for its kind.
//
// Transient failures only park the account briefly, and only beyond a threshold;
// hard failures apply the configured long cooldown. This mirrors the reference
// implementation, where a single soft failure costs seconds and a sustained
// streak is what actually takes an account out.
func (s *accountStore) markFailure(key, reason string, kind failureKind) {
	cfg := state.settings.get()
	now := time.Now().UnixMilli()

	s.mu.Lock()
	record, ok := s.items[key]
	if !ok {
		record = &accountRecord{Key: key, AddedAt: time.Now().Unix()}
		s.items[key] = record
	}
	record.Failures++
	record.CallCount++
	record.FailureCount++
	record.LastError = reason

	switch kind {
	case failureAuth:
		// The server refused the credential. Cool it down for the long window and
		// flag it for the panel, but a later refresh or login clears both.
		record.CooldownUntil = now + cfg.CooldownMillis
		record.AuthRejected = true
		record.AuthReason = reason
	case failureQuota:
		record.CooldownUntil = now + cfg.CooldownMillis
	case failureRate:
		record.CooldownUntil = now + cfg.SoftCooldownMillis
	default:
		// Transient: park only after a streak, and briefly.
		if record.Failures >= cfg.ErrorThreshold {
			record.CooldownUntil = now + cfg.SoftCooldownMillis*10
			record.Failures = 0
		} else {
			record.CooldownUntil = now + cfg.SoftCooldownMillis
		}
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// markAuthRejected records an outright refusal by the server.
//
// This is advisory only: it drives the panel's badge and is cleared the moment the
// credential works again. Usability is decided by the cooldown, never by this flag
// alone.
func (s *accountStore) markAuthRejected(key, reason string) {
	s.mu.Lock()
	if record, ok := s.items[key]; ok {
		record.AuthRejected = true
		record.AuthReason = reason
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// clearAuthRejected lifts the advisory flag once the credential works again.
func (s *accountStore) clearAuthRejected(key string) {
	s.mu.Lock()
	if record, ok := s.items[key]; ok {
		record.AuthRejected = false
		record.AuthReason = ""
		record.Failures = 0
		record.CooldownUntil = 0
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// setDisabled stores the operator's choice.
func (s *accountStore) setDisabled(key string, disabled bool) {
	s.mu.Lock()
	record, ok := s.items[key]
	if !ok {
		record = &accountRecord{Key: key, AddedAt: time.Now().Unix()}
		s.items[key] = record
	}
	record.Disabled = disabled
	if disabled {
		record.AuthRejected = false
		record.AuthReason = ""
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// list returns the records ordered by email for a stable panel.
func (s *accountStore) list() []accountRecord {
	s.mu.RLock()
	out := make([]accountRecord, 0, len(s.items))
	for _, record := range s.items {
		out = append(out, *record)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Email) < strings.ToLower(out[j].Email)
	})
	return out
}

// lookup finds a record by any identifier the panel might send.
func (s *accountStore) lookup(keys ...string) *accountRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if record, ok := s.items[key]; ok {
			return record
		}
	}
	return nil
}

// usable reports whether a credential may be used right now.
// usable reports whether a credential may serve a request right now.
//
// Only two things decide this: the operator's disable switch, and whether the
// cooldown has passed. The AuthRejected flag is deliberately not consulted — it is
// advisory, for the panel's badge. Letting it gate usability is what made a
// healthy account unreachable after one bad refresh, because the flag outlived
// the condition that set it.
//
// This mirrors the reference plugin, whose accountUsable() is a pure function of
// disabled / expired / cooling state.
func (s *accountStore) usable(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.items[key]
	if !ok {
		return true
	}
	if record.Disabled {
		return false
	}
	return record.CooldownUntil <= time.Now().UnixMilli()
}
