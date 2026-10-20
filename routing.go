package main

import (
	"encoding/json"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Account selection strategy.
//
// The host hands over a candidate list per request; this file decides which one to
// pin. Four strategies, each answering a different question:
//
//	round_robin  spread load evenly        — the safe default
//	fill_first   drain one account first    — keeps idle accounts idle, which is
//	                                          what you want when the first account
//	                                          has a generous quota
//	random       no ordering at all         — avoids a predictable pattern
//	by_usage     fewest recent calls first  — evens out consumption without a cursor,
//	                                          so it survives restarts
const (
	routingRoundRobin = "round_robin"
	routingFillFirst  = "fill_first"
	routingRandom     = "random"
	routingByUsage    = "by_usage"
)

// routingOptions is the panel's list of strategies, with the text shown under the
// segmented control.
var routingOptions = []struct {
	Value       string
	Label       string
	Description string
	Tradeoff    string
}{
	{
		Value:       routingRoundRobin,
		Label:       "轮巡",
		Description: "按顺序轮流使用每个账号。",
		Tradeoff:    "负载最均匀，但请求会分散到所有账号上。",
	},
	{
		Value:       routingFillFirst,
		Label:       "用满再换",
		Description: "始终使用第一个可用账号，直到它失败或进入冷却。",
		Tradeoff:    "配额集中在少数账号，账号多时其余账号长期闲置。",
	},
	{
		Value:       routingRandom,
		Label:       "随机",
		Description: "每次请求随机挑一个可用账号。",
		Tradeoff:    "没有可预测的顺序，难以复现某次调用用了哪个账号。",
	},
	{
		Value:       routingByUsage,
		Label:       "按用量",
		Description: "优先选择调用次数最少的账号。",
		Tradeoff:    "比轮巡更均衡地消耗配额，但需要记录调用次数。",
	},
}

// normalizeStrategy coerces an unknown value to the default.
func normalizeStrategy(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	for _, opt := range routingOptions {
		if opt.Value == value {
			return value
		}
	}
	return routingRoundRobin
}

// routingLabel is the human-readable name of a strategy.
func routingLabel(value string) string {
	for _, opt := range routingOptions {
		if opt.Value == value {
			return opt.Label
		}
	}
	return routingRoundRobin
}

// rotationCursor tracks the round-robin position.
//
// The host offers candidates in its own order, which is stable, so a cursor over
// the identifiers it hands over is enough. It is keyed by account key rather than
// by index so a removed account does not shift every position.
var rotation = &rotationState{last: ""}

type rotationState struct {
	mu   sync.Mutex
	last string
}

// nextAfter returns the identifier following `last` in `order`, wrapping around.
func (r *rotationState) nextAfter(order []string) string {
	if len(order) == 0 {
		return ""
	}
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()

	idx := -1
	for i, id := range order {
		if id == last {
			idx = i
			break
		}
	}
	next := order[(idx+1)%len(order)]

	r.mu.Lock()
	r.last = next
	r.mu.Unlock()
	return next
}

// remember records which account was used, for the next round-robin step.
func (r *rotationState) remember(id string) {
	r.mu.Lock()
	r.last = id
	r.mu.Unlock()
}

// resetRotation clears the cursor so the next request starts at the first account.
func resetRotation() {
	rotation.mu.Lock()
	rotation.last = ""
	rotation.mu.Unlock()
}

// relayableCandidates drops the ones the pool has taken out of rotation.
func relayableCandidates(candidates []pluginapi.SchedulerAuthCandidate) []pluginapi.SchedulerAuthCandidate {
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidateMatches(candidate) {
			continue
		}
		key := candidateKey(candidate)
		if record := state.accounts.lookup(key, candidate.ID); record != nil {
			// Only a disabled account is filtered out here; cooling is CPA's
			// decision and it will not offer a credential it has parked.
			if record.Disabled {
				continue
			}
		}
		out = append(out, candidate)
	}
	return out
}

// pickCandidate applies the configured strategy.
func pickCandidate(candidates []pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerAuthCandidate {
	cfg := state.settings.get().Routing
	strategy := normalizeStrategy(cfg.Strategy)

	if len(candidates) == 1 {
		return candidates[0]
	}

	switch strategy {
	case routingFillFirst:
		// The host's order is its own priority order, so the first entry is the
		// one to drain.
		return candidates[0]

	case routingRandom:
		return candidates[rand.Intn(len(candidates))]

	case routingByUsage:
		best := candidates[0]
		bestUsed := int64(-1)
		for _, candidate := range candidates {
			record := state.accounts.lookup(candidateKey(candidate), candidate.ID)
			used := int64(0)
			if record != nil {
				used = record.CallCount
			}
			if bestUsed < 0 || used < bestUsed {
				best = candidate
				bestUsed = used
			}
		}
		return best

	default: // routingRoundRobin
		order := make([]string, 0, len(candidates))
		byID := map[string]pluginapi.SchedulerAuthCandidate{}
		for _, candidate := range candidates {
			id := candidate.ID
			if id == "" {
				id = candidateKey(candidate)
			}
			order = append(order, id)
			byID[id] = candidate
		}
		next := rotation.nextAfter(order)
		if candidate, ok := byID[next]; ok {
			return candidate
		}
		return candidates[0]
	}
}

// routingStatusJSON describes the current strategy for the panel.
func routingStatusJSON() map[string]any {
	cfg := state.settings.get().Routing
	strategy := normalizeStrategy(cfg.Strategy)

	options := make([]map[string]any, 0, len(routingOptions))
	for _, opt := range routingOptions {
		options = append(options, map[string]any{
			"value":       opt.Value,
			"label":       opt.Label,
			"description": opt.Description,
			"tradeoff":    opt.Tradeoff,
		})
	}

	rotation.mu.Lock()
	last := rotation.last
	rotation.mu.Unlock()

	accounts := liveAccounts()
	enabled := 0
	for _, record := range accounts {
		if record.Disabled {
			continue
		}
		enabled++
	}

	return map[string]any{
		"strategy":       strategy,
		"strategy_label": routingLabel(strategy),
		"options":        options,
		"cooldown":       cfg.CooldownSeconds,
		"last":           last,
		"usable":         enabled,
	}
}

// applyRoutingConfig stores a strategy chosen in the panel.
func applyRoutingConfig(strategy string) map[string]any {
	cfg := state.settings.get()
	cfg.Routing.Strategy = normalizeStrategy(strategy)

	state.settings.mu.Lock()
	state.settings.cfg = cfg
	state.settings.mu.Unlock()

	logf("路由策略已切换为 %s（%s）", routingLabel(cfg.Routing.Strategy), cfg.Routing.Strategy)
	return routingStatusJSON()
}

/* ---------- scheduler capability ---------- */

// schedulerPick answers scheduler.pick.
//
// Returning Handled=false hands the decision back to the host, which is correct
// when there is nothing to choose from: the host then reports its own
// "no auth available" with its own wording.
func schedulerPick(request []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	candidates := relayableCandidates(req.Candidates)
	if len(candidates) == 0 {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}

	chosen := pickCandidate(candidates)
	return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: chosen.ID, Handled: true})
}

// sortAccountsByUsage orders records by call count, used by the panel.
func sortAccountsByUsage(records []accountRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].CallCount != records[j].CallCount {
			return records[i].CallCount < records[j].CallCount
		}
		return strings.ToLower(records[i].Email) < strings.ToLower(records[j].Email)
	})
}

// routingNextHint explains what the next request will do.
func routingNextHint() string {
	cfg := state.settings.get().Routing
	switch normalizeStrategy(cfg.Strategy) {
	case routingFillFirst:
		return "下一个请求会用第一个可用账号"
	case routingRandom:
		return "下一个请求随机挑一个可用账号"
	case routingByUsage:
		return "下一个请求会用调用次数最少的账号"
	default:
		return "下一个请求会用在轮巡中排在后面的账号"
	}
}

var _ = time.Now
