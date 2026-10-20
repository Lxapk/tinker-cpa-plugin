package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// callLog is a bounded in-memory ring of recent requests, surfaced on the
// management page. CPA keeps its own request log; this one records what the
// plugin did with each generation (which account, which tool, how long).
type callLog struct {
	mu    sync.RWMutex
	limit int
	items []callEntry
}

type callEntry struct {
	Time      string `json:"time"`
	Account   string `json:"account"`
	Model     string `json:"model"`
	Tool      string `json:"tool"`
	Status    string `json:"status"`
	Duration  int64  `json:"duration_ms"`
	Detail    string `json:"detail,omitempty"`
}

func newCallLog(limit int) *callLog {
	if limit <= 0 {
		limit = 500
	}
	return &callLog{limit: limit}
}

func (l *callLog) add(entry callEntry) {
	if entry.Time == "" {
		entry.Time = time.Now().Format("2006-01-02 15:04:05")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, entry)
	if len(l.items) > l.limit {
		l.items = l.items[len(l.items)-l.limit:]
	}
}

// recent returns the newest entries first.
func (l *callLog) recent(max int) []callEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if max <= 0 || max > len(l.items) {
		max = len(l.items)
	}
	out := make([]callEntry, 0, max)
	for i := len(l.items) - 1; i >= 0 && len(out) < max; i-- {
		out = append(out, l.items[i])
	}
	return out
}

func (l *callLog) clear() {
	l.mu.Lock()
	l.items = nil
	l.mu.Unlock()
}

// sprintf / stderrln keep the logging helpers free of an fmt/os import in every
// file that logs.
func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func stderrln(msg string) {
	fmt.Fprintln(os.Stderr, "tinker: "+msg)
}

// nowUnix keeps response builders free of a time import.
func nowUnix() int64 { return time.Now().Unix() }

// newCompletionID returns an id shaped like OpenAI's.
func newCompletionID() string { return "chatcmpl-" + randomToken() }
