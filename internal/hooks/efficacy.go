// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// recordHookDecision records a completed empty hook or a failed hook without
// treating a prepared result as output. Empty identities remain unknown.
func recordHookDecision(cfg *config.Config, event string, payload map[string]any, outcome string) {
	recordHookObservation(cfg, event, payload, "decision", outcome, "")
}

// recordHookOutput records what the writer accepted, not whether a host consumed it.
func recordHookOutput(cfg *config.Config, event string, payload map[string]any, output string, written int, writeErr, flushErr error) {
	outcome := "success"
	switch {
	case written < 0 || written > len(output):
		outcome = "unknown"
	case writeErr != nil:
		outcome = "failure"
	case written < len(output):
		outcome = "short_write"
	case flushErr != nil:
		outcome = "failure"
	}
	if written < 0 || written > len(output) {
		recordHookObservation(cfg, event, payload, "output_attempt", outcome, "")
		return
	}
	recordHookObservation(cfg, event, payload, "output_attempt", outcome, output[:written])
}

func recordHookObservation(cfg *config.Config, event string, payload map[string]any, action, outcome, text string) {
	if cfg == nil || event != "recall" || !cfg.Hooks.Ledger {
		return
	}
	if strings.HasPrefix(cfg.Scope, "mem:golden:") {
		return
	}
	fp := efficacy.ComputeDBFingerprintFromPath(cfg.DB, cfg.Scope)
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
	executionID := hex.EncodeToString(random[:])
	rawSession, _ := payload["session_id"].(string)
	if strings.TrimSpace(rawSession) == "" {
		rawSession, _ = payload["sessionId"].(string)
	}
	sessionDigest := ""
	if strings.TrimSpace(rawSession) != "" {
		sessionDigest = "s_" + ids.SHA256Hex(fp + "\x00" + strings.TrimSpace(rawSession))[:24]
	}
	entry := efficacy.LedgerEvent{
		EventID:       executionID,
		ExecutionID:   executionID,
		Session:       sessionDigest,
		Scope:         cfg.Scope,
		Hook:          event,
		Action:        action,
		OutputOutcome: outcome,
		ObservedAt:    clock.Now().UnixNano(),
		Observation:   "unknown",
	}
	if (action == "output_attempt" && outcome != "unknown") || (action == "decision" && outcome == "empty") {
		length := int64(len(text))
		entry.TextSHA256 = ids.SHA256Hex(fp + "\x00" + text)
		entry.TextBytes = &length
		entry.Observation = "observed"
	}
	ledger, err := efficacy.OpenBoundLedger(path, fp)
	if err != nil {
		return
	}
	defer ledger.Close()
	_ = ledger.Append(entry)
}
