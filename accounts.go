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
	// Blocked marks a credential the upstream rejected (expired token, revoked
	// access). It is not the operator's doing, so it is reported separately.
	Blocked    bool   `json:"blocked"`
	BlockedWhy string `json:"blocked_reason,omitempty"`
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
		record.CallCount++
		record.SuccessCount++
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// markFailure records a failed call and applies the configured cooldown.
func (s *accountStore) markFailure(key, reason string, hard bool) {
	cfg := state.settings.get()
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
	if hard {
		record.CooldownUntil = time.Now().UnixMilli() + cfg.CooldownMillis
	} else {
		record.CooldownUntil = time.Now().UnixMilli() + cfg.SoftCooldownMillis
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// markBlocked flags a credential the upstream rejected outright.
func (s *accountStore) markBlocked(key, reason string) {
	s.mu.Lock()
	if record, ok := s.items[key]; ok {
		record.Blocked = true
		record.BlockedWhy = reason
	}
	s.mu.Unlock()
	s.save(state.settings.dataDir())
}

// clearBlocked lifts a block once a refresh succeeds.
func (s *accountStore) clearBlocked(key string) {
	s.mu.Lock()
	if record, ok := s.items[key]; ok {
		record.Blocked = false
		record.BlockedWhy = ""
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
		record.Blocked = false
		record.BlockedWhy = ""
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
func (s *accountStore) usable(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.items[key]
	if !ok {
		return true
	}
	if record.Disabled || record.Blocked {
		return false
	}
	return record.CooldownUntil <= time.Now().UnixMilli()
}
