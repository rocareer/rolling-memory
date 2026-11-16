// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

// Read-side Decision Cache:
//   - Default OFF (enabled when ROLLING_MEMORY_DECISION_CACHE="1" or "strict");
//   - Key incorporates scope, cue, mode, full Options fingerprint (including AsOf, WindowStart/End,
//     ResultUnit, Rank, AttachFiles, SearchEngine, Reader, Presence), full RecallConfig fingerprint,
//     and the scope write epoch (bumped on every graph write/governance mutation) so write-after-read
//     never returns a stale cached result.

const (
	decisionCacheMaxEntries = 256
	decisionCacheTTL        = 10 * time.Minute
	decisionEpochMapMaxCap  = 4096
)

type decisionCacheItem struct {
	result  Result
	expires time.Time
}

var (
	decisionMu          sync.RWMutex
	decisionGlobalEpoch uint64
	decisionScopeEpochs = map[string]uint64{}
	decisionEntries     = map[string]decisionCacheItem{}
	decisionOrder       = []string{}
)

// DecisionCacheOn reports whether the read-side decision cache is enabled.
func DecisionCacheOn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DECISION_CACHE")))
	return v == "1" || v == "true" || v == "strict" || v == "on"
}

// DecisionCacheBumpScope increments the write generation epoch for scope so any subsequent
// read immediately misses stale cache entries from prior graph states.
func DecisionCacheBumpScope(scope string) {
	key := strings.TrimSpace(scope)
	if key == "" {
		return
	}
	decisionMu.Lock()
	if len(decisionScopeEpochs) >= decisionEpochMapMaxCap {
		decisionScopeEpochs = map[string]uint64{}
		decisionGlobalEpoch++
	}
	decisionScopeEpochs[key]++
	decisionMu.Unlock()
}

// DecisionScopeEpoch returns the current (globalEpoch, scopeEpoch) pair for scope.
func DecisionScopeEpoch(scope string) (uint64, uint64) {
	key := strings.TrimSpace(scope)
	decisionMu.RLock()
	g := decisionGlobalEpoch
	s := decisionScopeEpochs[key]
	decisionMu.RUnlock()
	return g, s
}

// DecisionCacheKey computes the collision-free SHA-1 key over all read parameters, config, and scope epoch.
func DecisionCacheKey(cfg *config.Config, scope, query string, opts Options) string {
	g, s := DecisionScopeEpoch(scope)
	var recallCfgStr string
	if cfg != nil {
		recallCfgStr = fmt.Sprintf("%+v", cfg.Recall)
	}
	rankStr := "nil"
	if opts.Rank != nil {
		rankStr = fmt.Sprintf("%v", *opts.Rank)
	}
	attachStr := "nil"
	if opts.AttachFiles != nil {
		attachStr = fmt.Sprintf("%v", *opts.AttachFiles)
	}
	raw := strings.Join([]string{
		scope,
		query,
		opts.Mode,
		opts.Reader,
		opts.ReaderSubject,
		fmt.Sprintf("asof=%d|win=%d..%d|tr=%d", opts.AsOf, opts.WindowStart, opts.WindowEnd, opts.TimeRef),
		fmt.Sprintf("unit=%s|rank=%s|attach=%s|eng=%s", strings.ToLower(strings.TrimSpace(opts.ResultUnit)), rankStr, attachStr, strings.ToLower(strings.TrimSpace(opts.SearchEngine))),
		fmt.Sprintf("off=%d|depth=%d|raw=%d:%v|sess=%s|bms=%v", opts.Offset, opts.Depth, opts.RawLimit, derefBoolStr(opts.IncludeRaw), nullableSessionString(opts.Session), derefInt(opts.BudgetMS)),
		fmt.Sprintf("flags=%s:%s:%s:%s", derefBoolStr(opts.TokenCorrect), derefBoolStr(opts.AliasExpand), derefBoolStr(opts.RelativeTag), derefBoolStr(opts.MentionBackfill)),
		fmt.Sprintf("caps=%v:%v:%v:%v:%v", derefInt(opts.CapsBands), derefInt(opts.CapsNodes), derefInt(opts.CapsChars), derefInt(opts.CapsChains), derefInt(opts.CapsRows)),
		fmt.Sprintf("pres=%s:%+v", opts.PresenceLocus, opts.Presence),
		recallCfgStr,
		fmt.Sprintf("ep=%d:%d", g, s),
	}, "\x1f")
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// DecisionCacheGet retrieves a cached Result if present and not expired.
func DecisionCacheGet(key string) (*Result, bool) {
	decisionMu.RLock()
	item, ok := decisionEntries[key]
	decisionMu.RUnlock()
	if !ok || time.Now().After(item.expires) {
		return nil, false
	}
	cp := cloneCachedResult(item.result)
	if cp.Meta == nil {
		cp.Meta = map[string]any{}
	}
	cp.Meta["decision_cached"] = true
	return &cp, true
}

// DecisionCachePut stores a Result in the bounded FIFO decision cache.
func DecisionCachePut(key string, res *Result) {
	if res == nil || key == "" {
		return
	}
	cp := cloneCachedResult(*res)
	decisionMu.Lock()
	if _, exists := decisionEntries[key]; !exists {
		if len(decisionOrder) >= decisionCacheMaxEntries {
			oldest := decisionOrder[0]
			decisionOrder = decisionOrder[1:]
			delete(decisionEntries, oldest)
		}
		decisionOrder = append(decisionOrder, key)
	}
	decisionEntries[key] = decisionCacheItem{
		result:  cp,
		expires: time.Now().Add(decisionCacheTTL),
	}
	decisionMu.Unlock()
}

// DecisionCacheReset clears all entries and epochs (for testing).
func DecisionCacheReset() {
	decisionMu.Lock()
	decisionGlobalEpoch = 0
	decisionScopeEpochs = map[string]uint64{}
	decisionEntries = map[string]decisionCacheItem{}
	decisionOrder = nil
	decisionMu.Unlock()
}

func cloneMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		switch tv := v.(type) {
		case map[string]any:
			cp[k] = cloneMapAny(tv)
		case []map[string]any:
			s := make([]map[string]any, len(tv))
			for i, item := range tv {
				s[i] = cloneMapAny(item)
			}
			cp[k] = s
		case []any:
			s := make([]any, len(tv))
			for i, item := range tv {
				if sub, ok := item.(map[string]any); ok {
					s[i] = cloneMapAny(sub)
				} else {
					s[i] = item
				}
			}
			cp[k] = s
		default:
			cp[k] = v
		}
	}
	return cp
}

func cloneCachedResult(r Result) Result {
	out := Result{
		Scope:   r.Scope,
		Bands:   make([]map[string]any, len(r.Bands)),
		RawHits: make([]map[string]any, len(r.RawHits)),
		Gaps:    make(map[string]int, len(r.Gaps)),
		Absent:  r.Absent,
		Meta:    make(map[string]any, len(r.Meta)+1),
	}
	for i, b := range r.Bands {
		out.Bands[i] = cloneMapAny(b)
	}
	for i, h := range r.RawHits {
		out.RawHits[i] = cloneMapAny(h)
	}
	for k, v := range r.Gaps {
		out.Gaps[k] = v
	}
	for k, v := range r.Meta {
		out.Meta[k] = v
	}
	return out
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func derefBoolStr(p *bool) string {
	if p == nil {
		return "nil"
	}
	if *p {
		return "1"
	}
	return "0"
}

func nullableSessionString(p *string) string {
	if p == nil {
		return "nil"
	}
	return "value:" + *p
}
