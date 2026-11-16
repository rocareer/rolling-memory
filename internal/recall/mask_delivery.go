// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/store"
)

// applyDeliverableMask implements reader-facing entity surface masking:
//   - Modes: "off" (default, zero-op), "shadow" (computes surface dictionary and records dose in meta["mask_delivery"] without modifying text),
//     "on" (replaces non-reader referent labels and aliases with deterministic `[PERSON_<8hex>]` tokens).
//   - Reader exemption: if opts.ReaderSubject matches a referent node (by label or alias), that person's canonical label and all aliases are exempted.
//   - World exemption: spatial/world nodes (origin='world') are never masked.
func applyDeliverableMask(memory *store.Store, cfg *config.Config, scope string, opts Options, bands []map[string]any, rawHits []map[string]any, meta map[string]any) {
	if cfg == nil || memory == nil || memory.DB() == nil || strings.HasPrefix(scope, "mem:golden:") {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Recall.MaskDeliverable))
	switch mode {
	case "shadow", "on":
	default:
		return
	}
	if opts.Reader == "owner" {
		return
	}
	_ = db.EnsureMemcoreSchema(memory.DB())

	exemptNodeID := ""
	if rs := strings.TrimSpace(opts.ReaderSubject); rs != "" {
		if n, found, err := memory.NodeByLabel(scope, rs, "referent"); err == nil && found {
			exemptNodeID = n.ID
		}
	}

	rows, err := memory.DB().Query(
		`SELECT n.id, n.label, COALESCE(ne.origin, '') AS origin
		 FROM node n
		 LEFT JOIN node_ext ne ON ne.node_id = n.id AND ne.scope = n.scope
		 WHERE n.scope = ? AND n.kind = 'referent' AND n.status IN ('provisional', 'active')
		 LIMIT 5000`,
		scope,
	)
	if err != nil {
		return
	}
	type maskPair struct {
		surface string
		token   string
	}
	seenSurface := map[string]bool{}
	var pairs []maskPair
	addSurface := func(nodeID, surface string) {
		sf := strings.TrimSpace(surface)
		if sf == "" || memcore.StringShannonBits(sf) < 4.0 {
			return
		}
		lo := strings.ToLower(sf)
		if seenSurface[lo] {
			return
		}
		seenSurface[lo] = true
		sum := sha1.Sum([]byte("rm-deliver|" + scope + "|" + nodeID))
		tok := "[PERSON_" + hex.EncodeToString(sum[:])[:8] + "]"
		pairs = append(pairs, maskPair{surface: sf, token: tok})
	}

	var nodeIDs []string
	for rows.Next() {
		var nid, label, origin string
		if err := rows.Scan(&nid, &label, &origin); err != nil {
			continue
		}
		if nid == "" || nid == exemptNodeID || origin == "world" {
			continue
		}
		// Only mask roster/person or unannotated referent nodes that are not structural paths
		if strings.Contains(label, "/") {
			continue
		}
		addSurface(nid, label)
		nodeIDs = append(nodeIDs, nid)
	}
	_ = rows.Close()

	for _, nid := range nodeIDs {
		if aliases, aErr := memory.Aliases(nid); aErr == nil {
			for _, al := range aliases {
				addSurface(nid, al)
			}
		}
	}

	// Highest information mass surface first to prevent partial prefix corruption
	sort.SliceStable(pairs, func(i, j int) bool {
		bi, bj := memcore.StringShannonBits(pairs[i].surface), memcore.StringShannonBits(pairs[j].surface)
		if bi != bj {
			return bi > bj
		}
		return pairs[i].surface < pairs[j].surface
	})

	replacements := 0
	maskString := func(s string) string {
		if s == "" || len(pairs) == 0 {
			return s
		}
		out := s
		for _, p := range pairs {
			if strings.Contains(out, p.surface) {
				if mode == "on" {
					out = strings.ReplaceAll(out, p.surface, p.token)
				}
				replacements++
			}
		}
		return out
	}

	for _, b := range bands {
		if title, ok := b["title"].(string); ok {
			b["title"] = maskString(title)
		}
		if subj, ok := b["subject"].(string); ok {
			b["subject"] = maskString(subj)
		}
		nodes, _ := b["nodes"].([]map[string]any)
		for _, n := range nodes {
			for _, k := range []string{"label", "content"} {
				if v, ok := n[k].(string); ok {
					n[k] = maskString(v)
				}
			}
		}
		chains, _ := b["chains"].([]any)
		for _, chAny := range chains {
			if ch, ok := chAny.(map[string]any); ok {
				if lb, ok := ch["label"].(string); ok {
					ch["label"] = maskString(lb)
				}
				cNodes, _ := ch["nodes"].([]map[string]any)
				for _, cn := range cNodes {
					for _, k := range []string{"label", "content"} {
						if v, ok := cn[k].(string); ok {
							cn[k] = maskString(v)
						}
					}
				}
			}
		}
	}
	for _, h := range rawHits {
		if txt, ok := h["text"].(string); ok {
			h["text"] = maskString(txt)
		}
	}

	if meta != nil {
		meta["mask_delivery"] = map[string]any{
			"mode":         mode,
			"surfaces":     len(pairs),
			"replacements": replacements,
		}
	}
}

// AssistantNameEvidence holds an explicit assistant name alias and its source basis.
type AssistantNameEvidence struct {
	Alias  string
	Source string
}

// UniqueAssistantNameFromEvidence selects a unique explicit assistant name from "self" or "explicit"
// alias evidence; if multiple distinct names exist at the same rank or only referral/role placeholders exist,
// it returns "" so delivery safely falls back to "assistant".
func UniqueAssistantNameFromEvidence(rows []AssistantNameEvidence) string {
	for _, basis := range []string{"self", "explicit"} {
		seen := map[string]string{}
		for _, row := range rows {
			src := strings.TrimSpace(row.Source)
			if src != "remember:"+basis && src != basis {
				continue
			}
			alias := strings.TrimSpace(row.Alias)
			if alias == "" || memcore.IsSelfDesignator(alias) {
				continue
			}
			key := strings.ToLower(alias)
			if seen[key] == "" {
				seen[key] = alias
			}
		}
		if len(seen) == 1 {
			for _, alias := range seen {
				return alias
			}
		}
		if len(seen) > 1 {
			return ""
		}
	}
	return ""
}

// ResolveAssistantDisplayName resolves the consumer-facing display name for the canonical "self" node
// in EgressViewNeutral / EgressViewPersona modes using explicit policy/config or unique alias evidence,
// falling back to memcore.FallbackAssistantDesignator ("assistant").
func ResolveAssistantDisplayName(memory *store.Store, cfg *config.Config, scope string, pol memcore.EgressViewPolicy) string {
	if explicit := strings.TrimSpace(pol.SelfDisplayName); explicit != "" && !strings.EqualFold(explicit, memcore.CanonicalSelfDesignator) {
		return explicit
	}
	if pol.Mode != memcore.EgressViewRedacted {
		if cfg != nil {
			if name := strings.TrimSpace(cfg.Identity.Assistant); name != "" && !strings.EqualFold(name, memcore.CanonicalSelfDesignator) {
				return name
			}
		}
		if memory != nil && memory.DB() != nil {
			rows, err := memory.DB().Query(
				`SELECT al.alias, COALESCE(al.created_by, '')
				 FROM node_alias al
				 JOIN node n ON n.id = al.node_id
				 LEFT JOIN node_ext ne ON ne.node_id = n.id AND ne.scope = n.scope
				 WHERE n.scope = ? AND (COALESCE(ne.origin, '') = 'self' OR lower(trim(n.label)) IN ('self', 'assistant', '助手'))
				 ORDER BY n.id ASC LIMIT 128`,
				scope,
			)
			if err == nil {
				var ev []AssistantNameEvidence
				for rows.Next() {
					var al, src string
					if rows.Scan(&al, &src) == nil {
						ev = append(ev, AssistantNameEvidence{Alias: al, Source: src})
					}
				}
				_ = rows.Close()
				if name := UniqueAssistantNameFromEvidence(ev); name != "" {
					return name
				}
			}
		}
	}
	return memcore.FallbackAssistantDesignator
}

// ApplyEgressViewProjection projects node labels and raw evidence texts according to memcore.EgressViewPolicy:
//   - EgressViewNeutral (default): projects "self" to the unique assistant real name or "assistant" in structured labels while keeping raw evidence verbatim;
//   - EgressViewPersona: projects "self" to SelfDisplayName across structured labels and evidence;
//   - EgressViewRedacted: projects "self" to "assistant" and masks roster nodes into Person_1, Person_2...
func ApplyEgressViewProjection(memory *store.Store, cfg *config.Config, scope string, pol memcore.EgressViewPolicy, labels map[uint64]string, texts map[string]string) map[uint64]string {
	if pol.Mode != memcore.EgressViewPersona && pol.Mode != memcore.EgressViewRedacted {
		pol.Mode = memcore.EgressViewNeutral
	}
	pol.SelfDisplayName = ResolveAssistantDisplayName(memory, cfg, scope, pol)
	projected, glossary := memcore.ProjectNodeLabelsView(labels, pol)
	if len(glossary) > 0 && len(texts) > 0 {
		for k, v := range texts {
			texts[k] = memcore.RedactEvidenceTextWithGlossary(v, glossary)
		}
	}
	return projected
}
