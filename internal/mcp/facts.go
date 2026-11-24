// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
	"sync"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/text"
)

var (
	fpCacheMu sync.RWMutex
	fpCache   = make(map[string]string, 8)
)

// recordRecallFact appends one agent-initiated recall to the fact ledger: what
// the tool returned, how long it took and how large the delivery was, never what
// the agent later did with it. The ledger is evidence, not a verdict, and it
// stores only a content hash plus a byte count — the delivered text itself is
// never written. Timing is recorded by the memory server for every host, so
// latency reporting never has to read a particular host's private store.
func recordRecallFact(memory *facade.Memory, presence *text.PresenceCoordinates, mode, outcome, delivered string, elapsedMS int64) {
	if memory == nil || memory.Config == nil {
		return
	}
	cfg := memory.Config
	if strings.HasPrefix(cfg.Scope, "mem:golden:") {
		return
	}
	writeFact := func() {
		cacheKey := cfg.DB + "\x00" + cfg.Scope
		fpCacheMu.RLock()
		fp := fpCache[cacheKey]
		fpCacheMu.RUnlock()
		if fp == "" {
			if memory.Store != nil && memory.Store.DB() != nil {
				fp = efficacy.ComputeDBFingerprint(memory.Store.DB(), cfg.Scope)
			}
			if fp == "" {
				fp = efficacy.ComputeDBFingerprintFromPath(cfg.DB, cfg.Scope)
			}
			if fp != "" {
				fpCacheMu.Lock()
				fpCache[cacheKey] = fp
				fpCacheMu.Unlock()
			}
		}
		if fp == "" {
			return
		}
		path, err := efficacy.DefaultLedgerPath(cfg.DB)
		if err != nil {
			return
		}
		var random [16]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return
		}
		m := mode
		if strings.TrimSpace(m) == "" {
			m = "full"
		}
		executionID := hex.EncodeToString(random[:])
		entry := efficacy.LedgerEvent{
			EventID:       executionID,
			ExecutionID:   executionID,
			Scope:         cfg.Scope,
			Harness:       recallHarness(presence),
			Hook:          "mcp_recall:" + m,
			Action:        "recall",
			OutputOutcome: outcome,
			ObservedAt:    clock.Now().UnixNano(),
			Observation:   "unknown",
		}
		if outcome == "hits" || outcome == "empty" {
			length := int64(len(delivered))
			entry.TextSHA256 = ids.SHA256Hex(fp + "\x00" + delivered)
			entry.TextBytes = &length
			entry.Observation = "observed"
			if elapsedMS >= 0 {
				elapsed := elapsedMS
				entry.ElapsedMS = &elapsed
			}
		}
		ledger, err := efficacy.OpenBoundLedger(path, fp, efficacy.ComputeLegacyDBFingerprintFromPath(cfg.DB))
		if err != nil {
			return
		}
		defer ledger.Close()
		_ = ledger.Append(entry)
	}
	if tc, err := memory.LocalVault(); err == nil && tc != nil && tc.Vault != nil && tc.Vault.CorpusSize() >= 2048 {
		go writeFact()
		return
	}
	writeFact()
}

// recallOutcome distinguishes delivered evidence from a certified absence.
// A receipt with neither cannot establish a hit or a miss.
func recallOutcome(structured any) string {
	payload, ok := structured.(map[string]any)
	if !ok {
		return "unknown"
	}
	if absent, ok := payload["absent"].(bool); ok && absent {
		return "empty"
	}
	if bands, ok := payload["bands"].([]map[string]any); ok && len(bands) > 0 {
		return "hits"
	}
	if hits, ok := payload["hits"].([]any); ok && len(hits) > 0 {
		return "hits"
	}
	return "unknown"
}

// recallHarness reads the host channel from presence coordinates
// ("harness:<id>" -> "<id>"); an unknown channel stays empty.
func recallHarness(presence *text.PresenceCoordinates) string {
	if presence == nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(presence.Carrier), "harness:")
}
