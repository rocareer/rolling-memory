// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rocareer/rolling-memory/sdk/go/local"
)

const (
	// EngineName is the canonical unified engine name across client and evaluation lab.
	EngineName = "Rolling Autonoetic Memory Engine"
	// EngineNameAutonoetic is the canonical full name of the memory engine.
	EngineNameAutonoetic = "Rolling Autonoetic Memory Engine"
	// EngineNameZH is the canonical Chinese engine name.
	EngineNameZH = "洛灵自知记忆引擎"

	// Canary* constants delegate to the canonical SDK self-test vector.
	CanarySecret    = local.CanarySecret
	CanaryDocID     = local.CanaryDocID
	CanaryText      = local.CanaryText
	CanaryTS        = local.CanaryTS
	CanarySlotCount = local.CanarySlotCount
	CanarySentCount = local.CanarySentCount
	CanaryAtomsB64  = local.CanaryAtomsB64
)

// BrainTransport abstracts the retrieval channel of the Local Vault Client: the
// in-process `LocalVaultTransport` over the local SQLite plaintext vault is the
// production implementation (zero network egress).
type BrainTransport interface {
	IngestBlindPulse(ctx context.Context, pkt BlindPulsePacket) (IngestResponse, error)
	QueryBlindManifold(ctx context.Context, queryPkt BlindPulsePacket, topK int) ([]QueryHit, error)
}

// Client is the top-level Local Vault Client coordinating any user-chosen PlaintextStore
// (SQLite Vault by default, or JSONFileStore, DirTextStore, MySQL, PostgreSQL, etc.)
// with the local plaintext vault retrieval kernel.
type Client struct {
	Vault     *Vault
	Store     PlaintextStore
	Transport BrainTransport
	// AssistantID / IncarnationID / Locus are the persona coordinate stamped onto every
	// pulse and onto each locally saved row. Unlimited parallel incarnations therefore stay
	// attributable without widening the memory envelope; empty values simply inherit the
	// transport-level identity.
	AssistantID   string
	IncarnationID string
	Locus         string

	// recallMu guards lastRecall, the outcome of the most recent Recall call, so a caller
	// can distinguish "memory holds nothing" from "retrieval was unavailable".
	recallMu   sync.Mutex
	lastRecall RecallDiagnostics
}

// SetIdentity records the persona coordinate on the client and its local vault,
// so pulses and local rows agree.
func (c *Client) SetIdentity(assistantID, incarnationID, locus string) {
	if c == nil {
		return
	}
	c.AssistantID = strings.TrimSpace(assistantID)
	c.IncarnationID = strings.TrimSpace(incarnationID)
	c.Locus = strings.TrimSpace(locus)
	if c.Vault != nil {
		c.Vault.SetIdentity(VaultIdentity{
			AssistantID: c.AssistantID, IncarnationID: c.IncarnationID, Locus: c.Locus,
		})
	}
}

// stampIdentity fills the persona coordinate on a pulse that does not carry one yet.
func (c *Client) stampIdentity(pkt *BlindPulsePacket) {
	if strings.TrimSpace(pkt.AssistantID) == "" {
		pkt.AssistantID = c.AssistantID
	}
	if strings.TrimSpace(pkt.IncarnationID) == "" {
		pkt.IncarnationID = c.IncarnationID
	}
	if strings.TrimSpace(pkt.Locus) == "" {
		pkt.Locus = c.Locus
	}
}

// NewClient constructs a production Local Vault Client backed by the default local SQLite Vault and a BrainTransport.
func NewClient(vault *Vault, transport BrainTransport) *Client {
	return &Client{
		Vault:     vault,
		Store:     vault,
		Transport: transport,
	}
}

// NewLocalClient constructs a local_plain client backed by the local SQLite Vault
// and its in-process LocalVaultTransport (zero network egress).
func NewLocalClient(vault *Vault) *Client {
	return NewClient(vault, NewLocalVaultTransport(vault))
}

// NewClientWithStore constructs an Local Vault Client backed by ANY custom PlaintextStore
// (e.g., JSONFileStore, DirTextStore, MySQL, PostgreSQL, Redis, or custom business tables).
func NewClientWithStore(store PlaintextStore, transport BrainTransport) *Client {
	v, _ := store.(*Vault)
	return &Client{
		Vault:     v,
		Store:     store,
		Transport: transport,
	}
}

func (c *Client) activeStore() PlaintextStore {
	if c.Store != nil {
		return c.Store
	}
	return c.Vault
}

// Remember stores a user Harness dialogue turn in `LayerEpisodic` ("episodic", the default conversational stream),
// packs ONLY the blind `{h, b, f}` integer frame into the local vault, and records the
// local retrieval address & dry-sentence indices. It is the historical name of
// RememberEpisodic and forwards to it.
func (c *Client) Remember(ctx context.Context, docID string, plaintext string, occurredAt int64) (IngestResponse, error) {
	return c.RememberEpisodic(ctx, docID, plaintext, occurredAt)
}

// RememberEpisodic explicitly stores a user Harness conversation turn in `LayerEpisodic` ("episodic").
func (c *Client) RememberEpisodic(ctx context.Context, docID string, plaintext string, occurredAt int64) (IngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, plaintext, occurredAt, LayerEpisodic, nil)
}

// localIngestResponse preserves every sentence before any transport write. The
// local plaintext vault is the source of truth; the retrieval address is a
// derived index that may be backfilled on a later reconcile.
func localIngestResponse(docID, layer string, sentenceCount int) IngestResponse {
	kept := make([]int, sentenceCount)
	for i := range kept {
		kept[i] = i
	}
	return IngestResponse{UnitID: docID, Layer: layer, KeptSentenceIndices: kept}
}

// RememberEpisodicByRef ingests the blind `{h, b, f}` pulse of `livePlaintext`
// (`layer="episodic"`) into the local vault and records a verified zero-copy pointer in local SQLite.
// If the pointer cannot be verified, the supplied plaintext is preserved locally instead.
//
// `turnRef` is the provenance of the unit (a content-addressed unit can be said in
// several turns): the vault row keeps one pointer, the lineage table keeps every
// occurrence. Chunked units pass an empty refPointer, so their text is stored locally.
func (c *Client) RememberEpisodicByRef(ctx context.Context, docID, refPointer, turnRef, livePlaintext string, occurredAt int64) (IngestResponse, error) {
	if occurredAt < 0 {
		occurredAt = 0
	}
	st := c.activeStore()
	pkt, sents := st.PackBlindPacket(docID, livePlaintext, occurredAt)
	pkt.Layer = LayerEpisodic
	c.stampIdentity(&pkt)
	usePointer := c.Vault != nil && strings.HasPrefix(strings.TrimSpace(refPointer), "@ref:")
	invalidPointer := false
	saveLocal := func(resp IngestResponse) error {
		if usePointer {
			if err := c.Vault.SaveLocalEpisodicPointerIndexed(docID, occurredAt, refPointer, turnRef, livePlaintext, pkt, resp); err == nil {
				return nil
			} else if errors.Is(err, ErrEpisodicUnitDeleted) || !errors.Is(err, ErrEpisodicIndexItem) {
				return err
			} else if verifyEpisodicPointerSource(refPointer, livePlaintext) == nil {
				var sameInline int
				checkErr := c.Vault.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_units d WHERE d.doc_id = ? AND d.raw_text = ? AND (d.layer = 'episodic' OR d.layer = '') AND NOT EXISTS (SELECT 1 FROM rm_episodic_index i WHERE i.doc_id = d.doc_id) AND NOT EXISTS (SELECT 1 FROM rm_episodic_lineage l WHERE l.doc_id = d.doc_id AND l.turn_ref = d.raw_text))`, docID, livePlaintext).Scan(&sameInline)
				if checkErr != nil {
					return checkErr
				}
				if sameInline != 0 && livePlaintext != strings.TrimSpace(refPointer) {
					return nil
				}
				return err
			}
			usePointer = false
			invalidPointer = true
			turnRef = ""
		}
		if invalidPointer {
			if c.Vault != nil {
				var existing string
				if err := c.Vault.db.QueryRow(`SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id WHERE d.unit_id = ?`, docID).Scan(&existing); err == nil && existing != "" && existing != livePlaintext {
					return fmt.Errorf("%w: %s", ErrEpisodicPointerSourceConflict, docID)
				}
				resp.Layer = LayerEpisodic
			}
			if err := st.SaveLocalPlaintext(docID, occurredAt, livePlaintext, sents, resp); err != nil {
				return err
			}
			if c.Vault != nil {
				c.Vault.clearSlotCache()
			}
			return nil
		}
		if c.Vault != nil {
			resp.Layer = LayerEpisodic
		}
		if err := st.SaveLocalPlaintext(docID, occurredAt, livePlaintext, sents, resp); err != nil {
			return err
		}
		if c.Vault != nil {
			if err := c.Vault.RecordEpisodicLineage(turnRef, docID, occurredAt); err != nil {
				return err
			}
		}
		return nil
	}
	localResp := localIngestResponse(docID, LayerEpisodic, len(sents))
	if c.Transport == nil {
		if err := saveLocal(localResp); err != nil {
			return IngestResponse{}, err
		}
		return localResp, nil
	}
	resp, err := c.Transport.IngestBlindPulse(ctx, pkt)
	if err != nil {
		// The transport could not produce the kept-sentence set, so the local
		// vault keeps every sentence rather than dropping the unit.
		if saveErr := saveLocal(localResp); saveErr != nil {
			return IngestResponse{}, saveErr
		}
		return localResp, err
	}
	if strings.TrimSpace(resp.Layer) == "" {
		resp.Layer = LayerEpisodic
	}
	if err := saveLocal(resp); err != nil {
		return IngestResponse{}, err
	}
	return resp, nil
}

// RememberWithLayer stores the plaintext in the user's chosen PlaintextStore and packs the blind `{h, b, f}`
// frame into the local vault with an explicit cognitive layer
// (`"background" | "episodic" | "noetic"`).
func (c *Client) RememberWithLayer(ctx context.Context, docID string, plaintext string, occurredAt int64, layer string, citedUnitIDs []string) (IngestResponse, error) {
	st := c.activeStore()
	pkt, sents := st.PackBlindPacket(docID, plaintext, occurredAt)
	if strings.TrimSpace(layer) != "" {
		pkt.Layer = strings.TrimSpace(layer)
	} else {
		pkt.Layer = "episodic"
	}
	if len(citedUnitIDs) > 0 {
		pkt.CitedUnitIDs = citedUnitIDs
	}
	c.stampIdentity(&pkt)
	localResp := localIngestResponse(docID, pkt.Layer, len(sents))
	if c.Transport == nil {
		if err := st.SaveLocalPlaintext(docID, occurredAt, plaintext, sents, localResp); err != nil {
			return IngestResponse{}, err
		}
		return localResp, nil
	}
	resp, err := c.Transport.IngestBlindPulse(ctx, pkt)
	if err != nil {
		// The transport could not produce the kept-sentence set, so the local
		// vault keeps every sentence rather than dropping the unit.
		if saveErr := st.SaveLocalPlaintext(docID, occurredAt, plaintext, sents, localResp); saveErr != nil {
			return IngestResponse{}, saveErr
		}
		return localResp, err
	}
	if strings.TrimSpace(resp.Layer) == "" {
		resp.Layer = pkt.Layer
	}
	if err := st.SaveLocalPlaintext(docID, occurredAt, plaintext, sents, resp); err != nil {
		return IngestResponse{}, err
	}
	return resp, nil
}

// PrepareQueuedRemember derives the canonical document one queued remember will
// write without touching the canonical tables, so a worker can commit the
// document, its metadata and the queue terminal state in one transaction.
func (c *Client) PrepareQueuedRemember(ctx context.Context, docID string, plaintext string, occurredAt int64, layer string) (PreparedCanonicalIngest, error) {
	if c == nil || c.Vault == nil {
		return PreparedCanonicalIngest{}, errors.New("localvault: queued remember requires a local vault")
	}
	st := c.activeStore()
	pkt, sents := st.PackBlindPacket(docID, plaintext, occurredAt)
	if strings.TrimSpace(layer) != "" {
		pkt.Layer = strings.TrimSpace(layer)
	} else {
		pkt.Layer = "episodic"
	}
	c.stampIdentity(&pkt)
	resp := localIngestResponse(docID, pkt.Layer, len(sents))
	if c.Transport != nil {
		got, err := c.Transport.IngestBlindPulse(ctx, pkt)
		if err != nil {
			return PreparedCanonicalIngest{}, err
		}
		if strings.TrimSpace(got.Layer) == "" {
			got.Layer = pkt.Layer
		}
		resp = got
	}
	meta, err := canonicalMetaForResponse(sents, resp, c.Vault.Identity(), time.Now().Unix())
	if err != nil {
		return PreparedCanonicalIngest{}, err
	}
	return c.Vault.prepareCanonicalIngest(docID, occurredAt, plaintext, meta)
}

// RecallDiagnostics reports how the last Recall on this client was served. Degraded means
// retrieval was unavailable and the answer came from the local lexical fallback,
// so a caller must never read an empty result as "memory holds nothing".
type RecallDiagnostics struct {
	Degraded bool
	Reason   string
}

// RecallDiagnostics returns the outcome of the most recent Recall call.
func (c *Client) RecallDiagnostics() RecallDiagnostics {
	if c == nil {
		return RecallDiagnostics{}
	}
	c.recallMu.Lock()
	defer c.recallMu.Unlock()
	return c.lastRecall
}

func (c *Client) setRecallDiagnostics(d RecallDiagnostics) {
	if c == nil {
		return
	}
	c.recallMu.Lock()
	c.lastRecall = d
	c.recallMu.Unlock()
}

// Recall converts the query into a `{h, b, f}` frame, queries ranked UnitIDs
// and weights, merges the dual-cluster kernel ranking (`SearchClientSDKDefault`),
// and resolves the plaintext locally from the user's PlaintextStore.
func (c *Client) Recall(ctx context.Context, queryPlaintext string, topK int) ([]LocalResolvedHit, error) {
	return c.RecallWithOptions(ctx, queryPlaintext, BlindQueryOptions{TopK: topK})
}

// RecallWithOptions executes the delivery recall: the blind-local candidates are
// hydrated and the dual-cluster kernel ranking is authoritative, with explicit
// temporal horizon (AsOf / TimeRef) and distillation options forwarded.
func (c *Client) RecallWithOptions(ctx context.Context, queryPlaintext string, opts BlindQueryOptions) ([]LocalResolvedHit, error) {
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}
	opts.TopK = topK
	st := c.activeStore()
	if c.Transport == nil {
		c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "no_transport"})
		return c.RecallDefault(queryPlaintext, topK)
	}
	qPkt, _ := st.PackBlindPacket("QUERY", queryPlaintext, opts.TimeRef)
	var rankedHits []QueryHit
	var err error
	maxProbes := topK * 4
	if maxProbes < 64 {
		maxProbes = 64
	}
	if localTr, ok := c.Transport.(*LocalVaultTransport); ok && c.Vault != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := localTr.Vault.verifyTransportScope(qPkt.Scope); err != nil {
			return nil, err
		}
		c.setRecallDiagnostics(RecallDiagnostics{})
		rankedHits, err = c.Vault.SearchClientSDKDefault(queryPlaintext, maxProbes)
		if err != nil {
			return nil, err
		}
	} else if localTr, ok := c.Transport.(*LocalVaultTransport); ok {
		localQPkt, _ := st.PackBlindPacket("QUERY", queryPlaintext, 0)
		localQPkt.Scope = qPkt.Scope
		rankedHits, err = localTr.QueryBlindManifold(ctx, localQPkt, 0)
	} else {
		rankedHits, err = c.Transport.QueryBlindManifold(ctx, qPkt, topK)
	}
	if err != nil {
		if _, local := c.Transport.(*LocalVaultTransport); local {
			return nil, err
		}
		if c.Vault != nil {
			if fallback, fErr := c.RecallDefault(queryPlaintext, topK); fErr == nil && len(fallback) > 0 {
				c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "engine_error_fallback_local"})
				return fallback, nil
			}
		}
		c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "engine_error"})
		return nil, err
	}
	c.setRecallDiagnostics(RecallDiagnostics{})
	_, local := c.Transport.(*LocalVaultTransport)
	if local && c.Vault != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(rankedHits) > maxProbes {
			maxProbes = len(rankedHits)
		}
		resolved := make([]LocalResolvedHit, 0, topK+8)
		usableLocal := 0
		seenResolved := make(map[string]bool, len(rankedHits))
		// 交付列表必须仅由金库与查询决定：不设墙钟断流；确定性上界 = maxProbes 与
		// 「已获 topK 条可用命中」两个条件。需要限时的调用方经 ctx 取消，循环按其保已得结果退出。
		for i := 0; i < len(rankedHits) && i < maxProbes; i++ {
			ch := rankedHits[i]
			if seenResolved[ch.UnitID] {
				continue
			}
			seenResolved[ch.UnitID] = true
			if err := ctx.Err(); err != nil {
				if usableLocal > 0 {
					break
				}
				return nil, err
			}
			rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(ch.UnitID)
			if err != nil {
				resolved = append(resolved, LocalResolvedHit{QueryHit: ch, SourceUnavailable: true})
				continue
			}
			usableLocal++
			if len(ch.KeptSentenceIndices) > 0 {
				allSents := SplitSentences(rawTxt)
				if len(allSents) > len(ch.KeptSentenceIndices) {
					distTxt, keptSents = ReconstructDistilled(rawTxt, allSents, ch.KeptSentenceIndices)
				}
			}
			resolved = append(resolved, LocalResolvedHit{
				QueryHit:           ch,
				RawPlaintext:       rawTxt,
				DistilledPlaintext: distTxt,
				KeptSentences:      keptSents,
				Authors:            RoleTaggedAuthors(rawTxt),
			})
			if i+1 >= topK && usableLocal >= topK {
				break
			}
		}
		return limitHydratedLocalHits(resolved, topK), nil
	}

	seen := make(map[string]bool, len(rankedHits)*2)
	resolved := make([]LocalResolvedHit, 0, len(rankedHits)*2)
	rankByID := make(map[string]int, len(rankedHits))
	maxRankedWeight := 1.0
	usableLocal := 0
	// Missing sources remain in the ranked prefix for gap reporting, but do not
	// consume its usable-hit budget. The transport has already sorted all matches.
	for i, ch := range rankedHits {
		if local {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(ch.UnitID)
		if err != nil {
			resolved = append(resolved, LocalResolvedHit{QueryHit: ch, SourceUnavailable: true})
			seen[ch.UnitID] = true
			continue
		}
		if local {
			usableLocal++
		}
		if len(ch.KeptSentenceIndices) > 0 {
			allSents := SplitSentences(rawTxt)
			if len(allSents) > len(ch.KeptSentenceIndices) {
				distTxt, keptSents = ReconstructDistilled(rawTxt, allSents, ch.KeptSentenceIndices)
			}
		}
		seen[ch.UnitID] = true
		rankByID[ch.UnitID] = len(resolved) + 1
		if ch.FinalWeight > maxRankedWeight {
			maxRankedWeight = ch.FinalWeight
		}
		resolved = append(resolved, LocalResolvedHit{
			QueryHit:           ch,
			RawPlaintext:       rawTxt,
			DistilledPlaintext: distTxt,
			KeptSentences:      keptSents,
			Authors:            RoleTaggedAuthors(rawTxt),
		})
		if local && i+1 >= topK && usableLocal >= topK {
			break
		}
	}
	if c.Vault != nil {
		localCap := topK
		if !local && localCap > 6 {
			localCap = 6
		} else if local && localCap < 20 {
			localCap = 20
		}
		if local {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if localHits, lErr := c.Vault.SearchClientSDKDefault(queryPlaintext, localCap); lErr == nil && len(localHits) > 0 {
			if local {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			localWeightByID := make(map[string]float64, len(localHits))
			topLocalScore := localHits[0].FinalWeight
			if topLocalScore <= 0 {
				topLocalScore = 1.0
			}
			addedLocalOnly := 0
			maxLocalAdmit := 2
			if local {
				maxLocalAdmit = topK
			}
			for _, lh := range localHits {
				if local {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				localWeightByID[lh.UnitID] = lh.FinalWeight
				if seen[lh.UnitID] {
					continue
				}
				// When transport hits exist, only admit up to 2 high-confidence local-only L1/L2 units
				// (within 75% of the top local score) so local background chunks supplement rather than flood.
				// In local_plain mode, SearchClientSDKDefault (dual-cluster kernel + L3 crystallization merge)
				// is the authoritative ranker.
				if !local && len(rankByID) > 0 && (addedLocalOnly >= maxLocalAdmit || lh.FinalWeight < topLocalScore*0.75) {
					continue
				}
				if local && addedLocalOnly >= maxLocalAdmit {
					continue
				}
				rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(lh.UnitID)
				if err != nil {
					resolved = append(resolved, LocalResolvedHit{QueryHit: lh, SourceUnavailable: true})
					seen[lh.UnitID] = true
					continue
				}
				seen[lh.UnitID] = true
				addedLocalOnly++
				resolved = append(resolved, LocalResolvedHit{
					QueryHit:           lh,
					RawPlaintext:       rawTxt,
					DistilledPlaintext: distTxt,
					KeptSentences:      keptSents,
					Authors:            RoleTaggedAuthors(rawTxt),
				})
			}
			// Delivery ordering: in local_plain mode (`local == true`) the hits ranked by
			// SearchClientSDKDefault (dual-cluster kernel + L3 crystallization merge) are
			// the authoritative order, carried through a monotone weight transform; hits
			// seen only by the exhaustive candidate pass (`QueryBlindManifold`) rank below
			// the authority set. The non-local transport branch keeps the legacy consensus
			// scaling for a custom transport surface.
			for i := range resolved {
				uid := resolved[i].UnitID
				_, inRanked := rankByID[uid]
				_, inLocal := localWeightByID[uid]
				if local {
					if inLocal {
						sdkNorm := localWeightByID[uid] / topLocalScore
						energyBoost := 1.0 + 0.368*sdkNorm
						resolved[i].FinalWeight = maxRankedWeight * (1.0 + sdkNorm) * energyBoost
					} else {
						resolved[i].FinalWeight *= 0.65
					}
				} else if inRanked && inLocal {
					sdkNorm := localWeightByID[uid] / topLocalScore
					resolved[i].FinalWeight *= (1.0 + 0.368*sdkNorm)
				} else if !inRanked && inLocal {
					resolved[i].FinalWeight = maxRankedWeight * (0.368 * (localWeightByID[uid] / topLocalScore))
				}
			}
			sort.SliceStable(resolved, func(i, j int) bool {
				if resolved[i].FinalWeight != resolved[j].FinalWeight {
					return resolved[i].FinalWeight > resolved[j].FinalWeight
				}
				return resolved[i].UnitID < resolved[j].UnitID
			})
			if local {
				resolved = limitHydratedLocalHits(resolved, topK)
			} else if len(resolved) > topK {
				resolved = resolved[:topK]
			}
		}
	}
	if _, local := c.Transport.(*LocalVaultTransport); local {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// RecallDefault executes the built-in Client SDK Default Recall (`SearchClientSDKDefault`:
// the dual-cluster kernel ranking over the local plaintext vault plus the L3
// crystallization merge) directly against the local Vault without any transport round trip.
func (c *Client) RecallDefault(queryPlaintext string, topK int) ([]LocalResolvedHit, error) {
	if c.Vault == nil {
		return nil, fmt.Errorf("localvault: RecallDefault requires a local SQLite Vault")
	}
	if topK <= 0 {
		topK = 10
	}
	hits, err := c.Vault.SearchClientSDKDefault(queryPlaintext, topK)
	if err != nil {
		return nil, err
	}
	st := c.activeStore()
	out := make([]LocalResolvedHit, 0, len(hits))
	for _, h := range hits {
		rawTxt, distTxt, keptSents, lErr := st.LoadLocalDocument(h.UnitID)
		if lErr != nil {
			out = append(out, LocalResolvedHit{QueryHit: h, SourceUnavailable: true})
			continue
		}
		out = append(out, LocalResolvedHit{
			QueryHit:           h,
			RawPlaintext:       rawTxt,
			DistilledPlaintext: distTxt,
			KeptSentences:      keptSents,
			Authors:            RoleTaggedAuthors(rawTxt),
		})
	}
	return out, nil
}

// RememberNoetic stores an L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// directly in the client's own local SQLite table (`rm_noetic`).
func (c *Client) RememberNoetic(noeticID string, queryText string, citedUnitIDs []string, citedSentences []string, answerText string, occurredAt int64) error {
	_, err := c.RememberNoeticTrace(noeticID, queryText, citedUnitIDs, citedSentences, answerText, occurredAt)
	return err
}

// RememberNoeticTrace is RememberNoetic returning the row that owns the
// evidence set: a cue that cites an already-known set merges into that trace.
func (c *Client) RememberNoeticTrace(noeticID string, queryText string, citedUnitIDs []string, citedSentences []string, answerText string, occurredAt int64) (string, error) {
	return c.Vault.SaveLocalNoeticTrace(LocalNoeticRecord{
		NoeticID:       noeticID,
		OccurredAt:     occurredAt,
		Status:         "active",
		QueryText:      queryText,
		AnswerText:     answerText,
		CitedUnitIDs:   citedUnitIDs,
		CitedSentences: citedSentences,
		AssistantID:    c.AssistantID,
		IncarnationID:  c.IncarnationID,
	})
}

// blindPacketDice scores the weighted unique-atom overlap of two blind query
// packets. Only the original retrieval cues participate: a long cited answer
// must never manufacture a match from an incidental token deep in its evidence.
func blindPacketDice(a, b BlindPulsePacket) float64 {
	mass := func(slots []BlindTokenAtom) (map[uint64]uint16, uint64) {
		byHash := make(map[uint64]uint16, len(slots))
		for _, slot := range slots {
			if slot.RawBitsX10 > byHash[slot.H] {
				byHash[slot.H] = slot.RawBitsX10
			}
		}
		var total uint64
		for _, width := range byHash {
			total += uint64(width)
		}
		return byHash, total
	}
	aMass, aTotal := mass(a.Slots)
	bMass, bTotal := mass(b.Slots)
	if aTotal == 0 || bTotal == 0 {
		return 0
	}
	var intersection uint64
	for h, width := range aMass {
		other, ok := bMass[h]
		if !ok {
			continue
		}
		if other < width {
			width = other
		}
		intersection += uint64(width)
	}
	return float64(intersection) / float64(aTotal+bTotal) * 2
}

func (v *Vault) loadActiveNoeticRecords() ([]LocalNoeticRecord, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	v.checkExternalDBMutation()
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		ready := v.snapRef.noeticReady
		hasLargeSnap := v.snapRef.snap != nil && len(v.snapRef.snap.docs) >= 2048
		cached := v.snapRef.activeNoetics
		lastTime := v.snapRef.lastNoeticTime
		refreshing := v.snapRef.noeticRefreshing
		v.slotCacheMu.RUnlock()
		if ready && hasLargeSnap {
			if time.Since(lastTime) >= 5*time.Minute && !refreshing {
				v.slotCacheMu.Lock()
				if !v.snapRef.noeticRefreshing {
					v.snapRef.noeticRefreshing = true
					v.snapRef.lastNoeticTime = time.Now()
					go func() {
						defer func() {
							v.slotCacheMu.Lock()
							v.snapRef.noeticRefreshing = false
							v.slotCacheMu.Unlock()
						}()
						_, _ = v.queryAndCacheActiveNoeticRecords()
					}()
				}
				v.slotCacheMu.Unlock()
			}
			return cached, nil
		}
	}
	return v.queryAndCacheActiveNoeticRecords()
}

func (v *Vault) queryAndCacheActiveNoeticRecords() ([]LocalNoeticRecord, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	var cnt, maxRowID, occSum int64
	hasFP := false
	if v.slotCacheMu != nil && v.snapRef != nil {
		if v.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(rowid), 0), COALESCE(SUM(occurred_at), 0) FROM rm_noetic WHERE status = 'active'`).Scan(&cnt, &maxRowID, &occSum) == nil {
			hasFP = true
			v.slotCacheMu.RLock()
			if v.snapRef.noeticReady &&
				v.snapRef.noeticCnt == cnt &&
				v.snapRef.noeticMaxRowID == maxRowID &&
				v.snapRef.noeticOccSum == occSum {
				cached := v.snapRef.activeNoetics
				v.slotCacheMu.RUnlock()
				v.slotCacheMu.Lock()
				v.snapRef.lastNoeticTime = time.Now()
				v.slotCacheMu.Unlock()
				return cached, nil
			}
			v.slotCacheMu.RUnlock()
			if cnt == 0 {
				v.slotCacheMu.Lock()
				v.snapRef.noeticReady = true
				v.snapRef.lastNoeticTime = time.Now()
				v.snapRef.noeticCnt = 0
				v.snapRef.noeticMaxRowID = 0
				v.snapRef.noeticOccSum = 0
				v.snapRef.activeNoetics = nil
				v.snapRef.noeticCueIndex = nil
				v.slotCacheMu.Unlock()
				return nil, nil
			}
		}
	}
	rows, err := v.db.Query(`
				SELECT noetic_id, occurred_at, COALESCE(inserted_at, occurred_at), status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json,
				       COALESCE(address_uri, ''), COALESCE(coord64, 0), COALESCE(waveform_bar, ''),
				       COALESCE(assistant_id, ''), COALESCE(incarnation_id, ''),
				       COALESCE(activated_at, 0), COALESCE(activation_count, 0),
				       COALESCE(trace_key, ''), COALESCE(query_variants_json, '[]'), COALESCE(superseded_by, '')
				FROM rm_noetic
				WHERE status = 'active'
				ORDER BY occurred_at DESC
			`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []LocalNoeticRecord
	idxByID := make(map[string]int, 64)
	inCite := make(map[string]int, 64)
	for rows.Next() {
		var rec LocalNoeticRecord
		var uidsJSON, sentsJSON, variantsJSON string
		if err := rows.Scan(&rec.NoeticID, &rec.OccurredAt, &rec.InsertedAt, &rec.Status, &rec.QueryText, &rec.AnswerText,
			&uidsJSON, &sentsJSON, &rec.AddressURI, &rec.Coord64, &rec.WaveformBar, &rec.AssistantID, &rec.IncarnationID,
			&rec.ActivatedAt, &rec.ActivationCount, &rec.TraceKey, &variantsJSON, &rec.SupersededBy); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(uidsJSON), &rec.CitedUnitIDs)
		_ = json.Unmarshal([]byte(sentsJSON), &rec.CitedSentences)
		_ = json.Unmarshal([]byte(variantsJSON), &rec.QueryVariants)
		idxByID[rec.NoeticID] = len(list)
		for _, cid := range rec.CitedUnitIDs {
			cid = strings.TrimSpace(cid)
			if cid != "" && cid != rec.NoeticID {
				inCite[cid]++
			}
		}
		list = append(list, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		rec := &list[i]
		reuse := inCite[rec.NoeticID]
		if rec.InsertedAt > 0 && rec.OccurredAt > rec.InsertedAt {
			reuse++
		}
		rec.ReuseCount = reuse
		if len(rec.CitedUnitIDs) > 0 {
			seen := make(map[string]bool, len(rec.CitedUnitIDs)+4)
			expanded := make([]string, 0, len(rec.CitedUnitIDs)+4)
			for _, cid := range rec.CitedUnitIDs {
				cid = strings.TrimSpace(cid)
				if cid == "" || seen[cid] {
					continue
				}
				seen[cid] = true
				expanded = append(expanded, cid)
				if childIdx, ok := idxByID[cid]; ok && childIdx != i {
					for _, leafID := range list[childIdx].CitedUnitIDs {
						leafID = strings.TrimSpace(leafID)
						if leafID != "" && leafID != rec.NoeticID && !seen[leafID] {
							seen[leafID] = true
							expanded = append(expanded, leafID)
						}
					}
				}
			}
			rec.CitedUnitIDs = expanded
		}
	}
	if hasFP && v.slotCacheMu != nil && v.snapRef != nil {
		// Build the index outside the cache lock: packing cues is the expensive
		// part and must not block concurrent recall readers.
		cueIndex := buildNoeticCueIndex(v, list)
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = true
		v.snapRef.lastNoeticTime = time.Now()
		v.snapRef.noeticCnt = cnt
		v.snapRef.noeticMaxRowID = maxRowID
		v.snapRef.noeticOccSum = occSum
		v.snapRef.activeNoetics = list
		v.snapRef.noeticCueIndex = cueIndex
		v.slotCacheMu.Unlock()
	}
	return list, nil
}

// RecallNoetic searches the client's local `rm_noetic` table by matching the
// query against every cue handle of each recollection trace.
func (c *Client) RecallNoetic(queryPlaintext string, topK int) ([]LocalNoeticRecord, error) {
	if c.Vault == nil || c.Vault.db == nil {
		return nil, nil
	}
	qPkt, _ := c.Vault.PackBlindPacket("Q-NOETIC", queryPlaintext, 0)
	if len(qPkt.Slots) == 0 {
		return nil, nil
	}

	records, err := c.Vault.loadActiveNoeticRecords()
	if err != nil || len(records) == 0 {
		return nil, err
	}
	// Records and index must come from the same cache generation: a background
	// refresh may replace both while the loader is returning.
	var cueIndex map[uint64][]int32
	if cached, index, ok := c.Vault.noeticCacheSnapshot(); ok {
		records = cached
		cueIndex = index
	}
	// The cue inverted index narrows the scan to traces sharing at least one
	// cue atom; a trace with no shared atom has Dice zero by construction.
	candidates := make([]int32, 0, len(records))
	if cueIndex != nil {
		seen := make([]bool, len(records))
		for _, atom := range qPkt.Slots {
			for _, idx := range cueIndex[atom.H] {
				if int(idx) < 0 || int(idx) >= len(records) || seen[idx] {
					continue
				}
				seen[idx] = true
				candidates = append(candidates, idx)
			}
		}
	} else {
		for i := range records {
			candidates = append(candidates, int32(i))
		}
	}
	var hits []LocalNoeticRecord
	for _, idx := range candidates {
		rec := records[idx]
		// A trace keeps several handles for the same evidence set. The strongest
		// handle is the one this recall used, and it is the cue the delivery
		// renders — the recollection itself stays singular.
		best := 0.0
		matched := ""
		for _, cue := range rec.CueTexts() {
			nPkt := c.Vault.noeticBlindPacket(rec.NoeticID, cue, rec.OccurredAt)
			if d := blindPacketDice(qPkt, nPkt); d > best {
				best = d
				matched = cue
			}
			// Dice cannot exceed 1: an exact handle match already proves the
			// maximum, so a repeatedly asked question pays for one packet only.
			if best >= 1.0 {
				break
			}
		}
		if best <= 0 {
			continue
		}
		rec.MatchScore = best
		rec.MatchedCue = matched
		hits = append(hits, rec)
	}
	effScore := func(r LocalNoeticRecord) float64 {
		return r.MatchScore * r.ActivationGain()
	}
	sort.SliceStable(hits, func(i, j int) bool {
		ei, ej := effScore(hits[i]), effScore(hits[j])
		if ei != ej {
			return ei > ej
		}
		if hits[i].MatchScore != hits[j].MatchScore {
			return hits[i].MatchScore > hits[j].MatchScore
		}
		return hits[i].NoeticID < hits[j].NoeticID
	})
	// Deliver the strongest handle plateau plus any verified handle whose
	// reinforced score stays within 1/e of the peak.
	if len(hits) > 0 {
		bestEff := effScore(hits[0])
		bestMatch := hits[0].MatchScore
		kept := hits[:1]
		for i := 1; i < len(hits); i++ {
			if hits[i].MatchScore == bestMatch || (hits[i].ReuseCount > 0 && withinNaturalEnergyEnvelope(effScore(hits[i]), bestEff)) {
				kept = append(kept, hits[i])
			}
		}
		hits = kept
	}
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// LocalVaultTransport provides an offline/local BrainTransport over the local SQLite Vault
// using strictly integer token-commitment matching (zero floating-point transcendental math).
type LocalVaultTransport struct {
	Vault *Vault
}

// NewLocalVaultTransport creates a local fallback transport backed by the SQLite Vault.
func NewLocalVaultTransport(vault *Vault) *LocalVaultTransport {
	return &LocalVaultTransport{Vault: vault}
}

func (v *Vault) verifyTransportScope(scope string) error {
	if v == nil || v.db == nil {
		return nil
	}
	reqScope := strings.TrimSpace(scope)
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		bound := v.snapRef.boundScope
		hasLargeSnap := v.snapRef.snap != nil && len(v.snapRef.snap.docs) >= 2048
		v.slotCacheMu.RUnlock()
		if bound != "" && hasLargeSnap {
			if reqScope != "" && reqScope != bound && reqScope != strings.ReplaceAll(bound, ":", "-") {
				return fmt.Errorf("localvault: requested scope %q does not match local_plain vault binding %q", reqScope, bound)
			}
			return nil
		}
	}
	var bound, storageType string
	err := v.db.QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
	if err == nil {
		bound = strings.TrimSpace(bound)
		if storageType != "text" || bound == "" {
			return fmt.Errorf("localvault: invalid local_plain vault scope binding")
		}
		if v.slotCacheMu != nil && v.snapRef != nil {
			v.slotCacheMu.Lock()
			v.snapRef.boundScope = bound
			v.slotCacheMu.Unlock()
		}
		if reqScope != "" && reqScope != bound && reqScope != strings.ReplaceAll(bound, ":", "-") {
			return fmt.Errorf("localvault: requested scope %q does not match local_plain vault binding %q", reqScope, bound)
		}
		return nil
	}
	if reqScope != "" {
		var populated int
		_ = v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_units LIMIT 1) OR EXISTS(SELECT 1 FROM rm_noetic LIMIT 1)`).Scan(&populated)
		if populated != 0 {
			return fmt.Errorf("localvault: local_plain vault scope is unbound; cannot serve explicit scope %q", reqScope)
		}
	}
	return nil
}

func (l *LocalVaultTransport) IngestBlindPulse(_ context.Context, pkt BlindPulsePacket) (IngestResponse, error) {
	if l.Vault != nil {
		if err := l.Vault.verifyTransportScope(pkt.Scope); err != nil {
			return IngestResponse{}, err
		}
	}
	var keptIdx []int
	seenSentSig := make(map[uint64]int, len(pkt.SentenceFrames))
	for sIdx, sf := range pkt.SentenceFrames {
		var sig uint64
		for _, a := range sf {
			sig = (sig * 1099511628211) ^ a.H
		}
		seenSentSig[sig]++
		if seenSentSig[sig] <= 2 {
			keptIdx = append(keptIdx, sIdx)
		}
	}
	return IngestResponse{
		UnitID:              pkt.UnitID,
		Epoch:               1,
		WaveformHex:         "F",
		WaveformBar:         "█",
		ConservedMass:       1.0,
		QualityMult:         1.0,
		AddressURI:          "C6:L1:LOCAL#" + pkt.UnitID,
		AddressCoord64:      0x1000000000000001,
		ContainerID:         0x1100000000000001,
		KeptSentenceIndices: keptIdx,
	}, nil
}

// DeleteUnits is a local no-op because LocalVaultTransport keeps every unit in the local vault.
func (l *LocalVaultTransport) DeleteUnits(_ context.Context, _ []string) error {
	return nil
}

func (l *LocalVaultTransport) QueryBlindManifold(ctx context.Context, queryPkt BlindPulsePacket, topK int) ([]QueryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.Vault == nil || l.Vault.db == nil {
		return nil, nil
	}
	if err := l.Vault.verifyTransportScope(queryPkt.Scope); err != nil {
		return nil, err
	}
	snap, release, snapErr := l.Vault.acquireCorpusSnapshot(ctx)
	if snapErr != nil {
		return nil, snapErr
	}
	if release != nil {
		defer release()
	}
	if snap == nil || len(snap.docs) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	zHits := snap.searchSnapshotCoreEngines(l.Vault, "", &queryPkt, topK)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]QueryHit, 0, len(zHits))
	for _, zh := range zHits {
		if zh.Energy <= 0 {
			continue
		}
		if di, ok := snap.docIdxByID[zh.UnitID]; ok && di >= 0 && di < len(snap.docs) {
			d := snap.docs[di]
			out = append(out, QueryHit{
				UnitID:         d.docID,
				Layer:          d.layer,
				OccurredAt:     d.occ,
				AddressURI:     d.addrURI,
				AddressCoord64: d.coord64,
				WaveformBar:    d.waveBar,
				WaveScore:      zh.Energy,
				FinalWeight:    zh.Energy,
				DirectWeight:   zh.Direct,
			})
		} else {
			out = append(out, QueryHit{
				UnitID:       zh.UnitID,
				Layer:        LayerEpisodic,
				WaveScore:    zh.Energy,
				FinalWeight:  zh.Energy,
				DirectWeight: zh.Direct,
			})
		}
	}
	return out, nil
}
