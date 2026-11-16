// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"

	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/store"
)

// supplementFollowupAndBridgeRawHits appends (1) immediate follow-up turns (t+1, t+2)
// of the top-4 primary raw hits and (2) 2-hop object-bridged traces after slot 4.
// Placing them after slot 4 preserves the top-48 token window (0 dilution to head evidence_hit)
// while giving the Answerer complete multi-turn Q&A continuations and 2-hop cross-session facts.
func supplementFollowupAndBridgeRawHits(memory *store.Store, scope, query string, rawHits []map[string]any, deliverLimit int) []map[string]any {
	if memory == nil || memory.DB() == nil || strings.HasPrefix(scope, "mem:golden:") || len(rawHits) < 2 {
		return rawHits
	}
	seenHash := map[string]bool{}
	for _, h := range rawHits {
		if th := asString(h["trace"]); th != "" {
			seenHash[th] = true
		}
	}

	var followExtras []map[string]any
	var bridgeExtras []map[string]any
	topK := 8
	if len(rawHits) < topK {
		topK = len(rawHits)
	}

	// (1) Immediate adjacent turns (t+1..t+6 forward first, then t-2..t-1 for top-2) in the same session,
	// interleaved breadth-first with a dedicated cap (12) so it never crowds out (2) 2-hop bridge traces.
	followK := 6
	if len(rawHits) < followK {
		followK = len(rawHits)
	}
	type fTurn struct {
		h, s, auth, txt string
		t, occ          int64
	}
	perHitNexts := make([][]fTurn, followK)
	for i := 0; i < followK; i++ {
		sess := strings.TrimSpace(asString(rawHits[i]["session"]))
		trn := asInt64(rawHits[i]["turn"])
		if sess == "" || trn <= 0 {
			continue
		}
		var nexts []fTurn
		fRows, fErr := memory.DB().Query(
			`SELECT hash, session, turn, author, occurred_at, text
			 FROM trace
			 WHERE scope = ? AND audience != 'private' AND session = ? AND turn > ?
			 ORDER BY turn ASC LIMIT 6`,
			scope, sess, trn,
		)
		if fErr == nil {
			for fRows.Next() {
				var ft fTurn
				if fRows.Scan(&ft.h, &ft.s, &ft.t, &ft.auth, &ft.occ, &ft.txt) == nil && ft.h != "" {
					nexts = append(nexts, ft)
				}
			}
			_ = fRows.Close()
		}
		if i < 2 && trn > 2 {
			pRows, pErr := memory.DB().Query(
				`SELECT hash, session, turn, author, occurred_at, text
				 FROM trace
				 WHERE scope = ? AND audience != 'private' AND session = ? AND turn >= ? AND turn < ?
				 ORDER BY turn ASC LIMIT 2`,
				scope, sess, trn-2, trn,
			)
			if pErr == nil {
				for pRows.Next() {
					var ft fTurn
					if pRows.Scan(&ft.h, &ft.s, &ft.t, &ft.auth, &ft.occ, &ft.txt) == nil && ft.h != "" {
						nexts = append(nexts, ft)
					}
				}
				_ = pRows.Close()
			}
		}
		perHitNexts[i] = nexts
	}
	appendStep := func(i, k int) {
		if len(followExtras) >= 12 {
			return
		}
		nexts := perHitNexts[i]
		if k >= len(nexts) {
			return
		}
		n0 := nexts[k]
		if k+1 < len(nexts) {
			n1 := nexts[k+1]
			if seenHash[n0.h] && seenHash[n1.h] {
				return
			}
			seenHash[n0.h] = true
			seenHash[n1.h] = true
			t0 := n0.txt
			if n0.auth != "" && n0.auth != "user" {
				t0 = n0.auth + ": " + t0
			}
			t1 := n1.txt
			if n1.auth != "" && n1.auth != "user" {
				t1 = n1.auth + ": " + t1
			}
			followExtras = append(followExtras, map[string]any{
				"trace": n1.h, "_trace2": n0.h, "session": n1.s, "turn": n1.t,
				"author": n1.auth, "occurred_at": n1.occ, "text": t0 + "\n" + t1,
			})
		} else if !seenHash[n0.h] {
			seenHash[n0.h] = true
			t0 := n0.txt
			if n0.auth != "" && n0.auth != "user" {
				t0 = n0.auth + ": " + t0
			}
			followExtras = append(followExtras, map[string]any{
				"trace": n0.h, "session": n0.s, "turn": n0.t,
				"author": n0.auth, "occurred_at": n0.occ, "text": t0,
			})
		}
	}
	top4 := 4
	if followK < top4 {
		top4 = followK
	}
	for k := 0; k < 6; k += 2 {
		for i := 0; i < top4; i++ {
			appendStep(i, k)
		}
	}
	for k := 0; k < 4; k += 2 {
		for i := top4; i < followK; i++ {
			appendStep(i, k)
		}
	}
	for i := 0; i < 2 && i < followK; i++ {
		appendStep(i, 6)
	}

	if len(followExtras) == 0 {
		return rawHits
	}

	// (2) 2-Hop Entity-Object Bridge traces from assertions anchored in top-4 raw hits
	qLower := strings.ToLower(query)
	bridgeK := 4
	if len(rawHits) < bridgeK {
		bridgeK = len(rawHits)
	}
	topHashes := make([]string, 0, bridgeK)
	for i := 0; i < bridgeK; i++ {
		if th := asString(rawHits[i]["trace"]); th != "" {
			topHashes = append(topHashes, th)
		}
	}
	if anchored, _, aErr := memory.AssertionsByAnchor(topHashes, store.AnchorOptions{Scope: scope, Limit: 64}); aErr == nil && len(anchored) > 0 {
		bridgeObjects := []string{}
		seenObj := map[string]bool{}
		for _, c := range anchored {
			if c.Data.ObjectID == nil || *c.Data.ObjectID == "" || strings.HasPrefix(*c.Data.ObjectID, "a_") {
				continue
			}
			ol := strings.TrimSpace(c.Data.OLabel)
			olLow := strings.ToLower(ol)
			if memcore.StringShannonBits(ol) >= 6.0 && memcore.CountTokens(ol) <= memcore.TokenTier4 && !seenObj[olLow] && !strings.Contains(qLower, olLow) {
				seenObj[olLow] = true
				bridgeObjects = append(bridgeObjects, ol)
				if len(bridgeObjects) >= 5 {
					break
				}
			}
		}
		for _, bo := range bridgeObjects {
			bRows, bErr := memory.DB().Query(
				`SELECT hash, session, turn, author, occurred_at, text
				 FROM trace
				 WHERE scope = ? AND audience != 'private' AND text LIKE ?
				 ORDER BY occurred_at ASC LIMIT 4`,
				scope, "%"+bo+"%",
			)
			if bErr != nil {
				continue
			}
			for bRows.Next() {
				var h, s, auth, txt string
				var trn, occ int64
				if bRows.Scan(&h, &s, &trn, &auth, &occ, &txt) == nil && h != "" && !seenHash[h] {
					seenHash[h] = true
					bridgeExtras = append(bridgeExtras, map[string]any{
						"trace": h, "session": s, "turn": trn,
						"author": auth, "occurred_at": occ, "text": txt,
					})
				}
			}
			_ = bRows.Close()
		}
	}
	if len(bridgeExtras) > 6 {
		bridgeExtras = bridgeExtras[:6]
	}
	extras := append(bridgeExtras, followExtras...)
	if len(extras) == 0 {
		return rawHits
	}
	insertAt := topK
	if len(followExtras) == 0 && len(rawHits) > 12 {
		insertAt = 12
	}
	out := make([]map[string]any, 0, len(rawHits)+len(extras))
	out = append(out, rawHits[:insertAt]...)
	maxExtras := 18
	if len(extras) > maxExtras {
		extras = extras[:maxExtras]
	}
	out = append(out, extras...)
	out = append(out, rawHits[insertAt:]...)
	if deliverLimit > 0 && len(out) > deliverLimit {
		out = out[:deliverLimit]
	}
	return out
}
