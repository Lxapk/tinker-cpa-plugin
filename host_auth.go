package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The host's per-credential "disabled" flag is written on the auth file itself.
//
// CPA builds its candidate list from the top-level "disabled" in each auth file
// and reloads a file when its watcher sees it change. That flag is therefore the
// one lever this plugin has over the host's routing without touching the host,
// and the file on disk — not the host's listing, which describes the copy it
// last loaded — is the current truth.

// hostAuthEntry is one credential as the host reports it.
type hostAuthEntry struct {
	ID        string `json:"id"`
	AuthIndex string `json:"auth_index"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Email     string `json:"email"`
	Disabled  bool   `json:"disabled"`
}

// hostAuthListResponse is the shape of host.auth.list.
type hostAuthListResponse struct {
	Files []hostAuthEntry `json:"files"`
	Auths []hostAuthEntry `json:"auths"`
}

// listHostAuthEntries returns the host's auth files.
func listHostAuthEntries() []hostAuthEntry {
	raw, errList := callHost("host.auth.list", map[string]any{})
	if errList != nil {
		debugf("host.auth.list 失败: %v", errList)
		return nil
	}
	return decodeAuthEntries(raw)
}

func decodeAuthEntries(raw []byte) []hostAuthEntry {
	if len(raw) == 0 {
		return nil
	}
	var wrapper hostAuthListResponse
	if errUnmarshal := json.Unmarshal(raw, &wrapper); errUnmarshal == nil {
		if len(wrapper.Files) > 0 {
			return wrapper.Files
		}
		if len(wrapper.Auths) > 0 {
			return wrapper.Auths
		}
	}
	// Some host builds answer with a bare array.
	var list []hostAuthEntry
	if errUnmarshal := json.Unmarshal(raw, &list); errUnmarshal == nil {
		return list
	}
	return nil
}

// tinkerAuthEntries filters the host listing down to this provider.
func tinkerAuthEntries() []hostAuthEntry {
	all := listHostAuthEntries()
	out := make([]hostAuthEntry, 0, len(all))
	for _, entry := range all {
		provider := strings.TrimSpace(entry.Provider)
		if provider == "" {
			provider = strings.TrimSpace(entry.Type)
		}
		if provider != "" && !strings.EqualFold(provider, tinkerProviderKey) {
			continue
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// findAuthEntriesForAccount returns the auth files that belong to one account.
//
// One person can own several credentials (a re-login, or an imported copy), and
// leaving any of them enabled keeps the account routable at the host.
func findAuthEntriesForAccount(keys ...string) []hostAuthEntry {
	wanted := map[string]bool{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			wanted[strings.ToLower(key)] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	var out []hostAuthEntry
	for _, entry := range tinkerAuthEntries() {
		if wanted[strings.ToLower(strings.TrimSpace(entry.ID))] ||
			wanted[strings.ToLower(strings.TrimSpace(entry.AuthIndex))] ||
			wanted[strings.ToLower(strings.TrimSpace(entry.Email))] ||
			wanted[strings.ToLower(strings.TrimSpace(entry.Label))] ||
			wanted[strings.ToLower(strings.TrimSpace(entry.Name))] {
			out = append(out, entry)
			continue
		}
		// Fall back to the identity stored inside the credential, which is the
		// only reliable match for a file named by the host.
		if creds := readAuthFileCredentials(entry.Path); creds != nil {
			if wanted[strings.ToLower(creds.accountKey())] ||
				wanted[strings.ToLower(creds.Email)] {
				out = append(out, entry)
			}
		}
	}
	return out
}

// readAuthFileCredentials reads the credential a host auth file points at.
func readAuthFileCredentials(path string) *tinkerCredentials {
	raw, ok := readAuthFile(path)
	if !ok {
		return nil
	}
	// The auth file wraps the plugin storage in "storage"; older shapes keep the
	// token at the top level.
	if creds, errParse := parseCredentials(unwrapAuthFile(raw)); errParse == nil {
		return creds
	}
	if creds, errParse := parseCredentials(raw); errParse == nil {
		return creds
	}
	return nil
}

// readAuthFile reads a credential file at the location the host reported.
//
// The host reports it relative to its own working directory ("auths/x.json").
// The plugin is loaded into the host process, so that is this process's working
// directory as well and the path opens as given.
func readAuthFile(path string) ([]byte, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// setAuthFileDisabled sets the top-level "disabled" in a credential file,
// changing nothing else. It reports whether the file was rewritten.
func setAuthFileDisabled(path string, disabled bool) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("宿主没有报告凭据文件位置")
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return false, errStat
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return false, errRead
	}
	var doc map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil || doc == nil {
		return false, fmt.Errorf("%s 不是 JSON 对象", filepath.Base(path))
	}
	if current, known := disabledInPayload(raw); known && current == disabled {
		return false, nil
	}
	doc["disabled"] = json.RawMessage(strconv.FormatBool(disabled))
	encoded, errMarshal := json.Marshal(doc)
	if errMarshal != nil {
		return false, errMarshal
	}

	// Write through a temp file in the same directory so the host's watcher
	// never observes a half-written credential.
	tmp, errTemp := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if errTemp != nil {
		return false, errTemp
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, errWrite := tmp.Write(encoded); errWrite != nil {
		_ = tmp.Close()
		cleanup()
		return false, errWrite
	}
	if errChmod := tmp.Chmod(info.Mode().Perm()); errChmod != nil {
		_ = tmp.Close()
		cleanup()
		return false, errChmod
	}
	if errClose := tmp.Close(); errClose != nil {
		cleanup()
		return false, errClose
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		cleanup()
		return false, errRename
	}
	return true, nil
}

// disabledInPayload extracts the top-level "disabled" from a credential payload.
func disabledInPayload(raw []byte) (disabled, known bool) {
	if len(raw) == 0 {
		return false, false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return false, false
	}
	value, has := doc["disabled"]
	if !has {
		return false, false
	}
	if json.Unmarshal(value, &disabled) != nil {
		return false, false
	}
	return disabled, true
}

// syncAccountDisabledToHost writes the operator's enable/disable onto every auth
// file that belongs to the account.
func syncAccountDisabledToHost(keys []string, disabled bool) (int, error) {
	entries := findAuthEntriesForAccount(keys...)
	if len(entries) == 0 {
		return 0, fmt.Errorf("没有找到对应的凭据文件")
	}
	changed := 0
	var lastErr error
	for _, entry := range entries {
		if strings.TrimSpace(entry.Path) == "" {
			lastErr = fmt.Errorf("宿主没有报告 %s 的文件路径", entry.Name)
			continue
		}
		_, errWrite := setAuthFileDisabled(entry.Path, disabled)
		if errWrite != nil {
			lastErr = errWrite
			continue
		}
		changed++
	}
	return changed, lastErr
}

// saveAuthToHost persists a credential through the host, which is what makes a
// new login show up in CPA's own auth list.
func saveAuthToHost(name string, storage []byte) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("缺少 auth 文件名")
	}
	_, errSave := callHost("host.auth.save", map[string]any{
		"name": name,
		"json": map[string]any{
			"type":    tinkerProviderKey,
			"storage": json.RawMessage(storage),
		},
	})
	return errSave
}

// persistRefreshedCredential writes a rotated token pair back to the auth file
// it came from.
//
// This is the step that keeps an account alive. Tinker's refresh token is
// single-use: the moment a refresh succeeds, the old one is dead. If the new
// pair only lived in memory, the next call would refresh again with the spent
// token, the server would answer "invalid JWT refresh token", and a perfectly
// good account would be marked blocked — which is exactly what happened before
// this was written.
func persistRefreshedCredential(authID string, creds *tinkerCredentials) {
	if creds == nil {
		return
	}
	storage, errMarshal := json.Marshal(creds)
	if errMarshal != nil {
		return
	}

	// Prefer the file the host reported for this credential: writing the same
	// name back keeps the disabled flag and any host metadata intact.
	for _, entry := range tinkerAuthEntries() {
		if !entryMatches(entry, authID) {
			continue
		}
		raw, okRead := readAuthFile(entry.Path)
		if !okRead {
			continue
		}
		var doc map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil || doc == nil {
			continue
		}
		doc["storage"] = json.RawMessage(storage)
		encoded, errEncode := json.Marshal(doc)
		if errEncode != nil {
			continue
		}
		if err := writeFileAtomic(entry.Path, encoded); err != nil {
			logf("回写刷新凭据失败 %s: %v", entry.Name, err)
			continue
		}
		debugf("已回写刷新后的凭据到 %s", entry.Name)
		return
	}

	// No listing matched. The identifier CPA passes is usually the auth file's
	// name or index, so try it as a path relative to the working directory
	// before falling back to the host API — a refresh must never leave the
	// spent refresh token on disk.
	if path := resolveAuthPath(authID); path != "" {
		raw, okRead := readAuthFile(path)
		if okRead {
			var doc map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal == nil && doc != nil {
				doc["storage"] = json.RawMessage(storage)
				if encoded, errEncode := json.Marshal(doc); errEncode == nil {
					if err := writeFileAtomic(path, encoded); err == nil {
						debugf("已按路径回写刷新凭据到 %s", path)
						return
					}
				}
			}
		}
	}

	// Last resort: let the host write it.
	name := firstNonEmpty(authID, "")
	if name != "" && !strings.HasSuffix(name, ".json") {
		name += ".json"
	}
	if name == "" {
		name = credsFileName(creds)
	}
	if err := saveAuthToHost(name, storage); err != nil {
		debugf("host.auth.save 失败: %v", err)
	}
}

// resolveAuthPath turns an auth identifier into a readable file path.
//
// The identifier can be a plain file name, a relative path, or an absolute one,
// depending on which of the host's fields CPA forwards.
func resolveAuthPath(authID string) string {
	id := strings.TrimSpace(authID)
	if id == "" {
		return ""
	}
	candidates := []string{id}
	if !strings.HasSuffix(id, ".json") {
		candidates = append(candidates, id+".json")
	}
	for _, candidate := range candidates {
		if info, errStat := os.Stat(candidate); errStat == nil && !info.IsDir() {
			return candidate
		}
		for _, dir := range []string{"auths", filepath.Join("data", pluginName)} {
			joined := filepath.Join(dir, candidate)
			if info, errStat := os.Stat(joined); errStat == nil && !info.IsDir() {
				return joined
			}
		}
	}
	return ""
}

// entryMatches reports whether a host auth entry is the credential with authID.
func entryMatches(entry hostAuthEntry, authID string) bool {
	if strings.TrimSpace(authID) == "" {
		return false
	}
	wanted := strings.ToLower(strings.TrimSpace(authID))
	return strings.ToLower(strings.TrimSpace(entry.ID)) == wanted ||
		strings.ToLower(strings.TrimSpace(entry.AuthIndex)) == wanted ||
		strings.ToLower(strings.TrimSpace(entry.Name)) == wanted
}

// writeFileAtomic replaces a file through a temp file in the same directory.
func writeFileAtomic(path string, data []byte) error {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return errStat
	}
	tmp, errTemp := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if errTemp != nil {
		return errTemp
	}
	tmpName := tmp.Name()
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errWrite
	}
	if errChmod := tmp.Chmod(info.Mode().Perm()); errChmod != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errChmod
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return errClose
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		_ = os.Remove(tmpName)
		return errRename
	}
	return nil
}

// randomToken returns a state token for a login flow.
func randomToken() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 24)
	seed := timeNowNano()
	for i := range buf {
		seed = seed*6364136223846793005 + 1442695040888963407
		buf[i] = alphabet[(seed>>33)%uint64(len(alphabet))]
	}
	return string(buf)
}

// timeNowNano keeps the token generator free of a time import cycle.
func timeNowNano() uint64 {
	return uint64(time.Now().UnixNano())
}

// liveAccounts returns the accounts that currently exist on disk.
//
// The auth files are the inventory; the plugin's cache only annotates them with
// call counts and cooldowns. Everything user-facing goes through this function so
// a credential deleted in CPA disappears from the panel instead of lingering as a
// cached row.
func liveAccounts() []accountRecord {
	rows := make([]accountRecord, 0)
	seen := map[string]bool{}
	now := nowUnixMilli()

	for _, entry := range tinkerAuthEntries() {
		creds := readAuthFileCredentials(entry.Path)
		key := ""
		email := strings.TrimSpace(entry.Email)
		if creds != nil {
			key = creds.accountKey()
			if creds.Email != "" {
				email = creds.Email
			}
		}
		if key == "" {
			key = strings.ToLower(email)
		}
		if key == "" {
			key = strings.TrimSpace(entry.ID)
		}
		if key == "" || seen[strings.ToLower(key)] {
			continue
		}
		seen[strings.ToLower(key)] = true

		record := accountRecord{
			Key:       key,
			Email:     email,
			AuthIndex: firstNonEmpty(entry.AuthIndex, entry.ID),
			// The host's own flag wins: a credential disabled in CPA must read as
			// disabled here even if the plugin never wrote that itself.
			Disabled: entry.Disabled,
		}
		if cached := state.accounts.lookup(key, email, entry.ID, entry.AuthIndex); cached != nil {
			record.CallCount = cached.CallCount
			record.SuccessCount = cached.SuccessCount
			record.FailureCount = cached.FailureCount
			record.LastUsed = cached.LastUsed
			record.LastError = cached.LastError
			record.Failures = cached.Failures
			record.CooldownUntil = cached.CooldownUntil
			record.AddedAt = cached.AddedAt
			if cached.Disabled {
				record.Disabled = true
			}
			// Trust the flag as stored. markBlocked is only set when the server
			// rejected the credential outright, so a stale one cannot linger from a
			// transient failure any more.
			if cached.Blocked {
				record.Blocked = true
				record.BlockedWhy = cached.BlockedWhy
			}
		}
		if creds != nil && creds.ExpiresAt > 0 {
			record.ExpiresAt = creds.ExpiresAt
		}
		if record.Email == "" {
			record.Email = key
		}
		_ = now
		rows = append(rows, record)
	}

	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Email) < strings.ToLower(rows[j].Email)
	})
	return rows
}
