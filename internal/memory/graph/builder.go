// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package graph projects the local retrieval engine into a read-only memory
// graph: word families (word), sentence-local primary dyads (phrase), and
// engine-derived containers (cluster: episode / entity / document), with units
// that fall outside every container marked free.
package graph

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/graphmodel"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

// Builder caches one immutable graph snapshot per corpus generation.
type Builder struct {
	vault *localvault.Vault

	mu   sync.Mutex
	snap *Snapshot
	key  string

	viewMu   sync.Mutex
	viewKey  string
	viewAt   time.Time
	viewResp *graphmodel.Response

	// lexMu guards the per-snapshot unit hydration cache. Resolving a unit's
	// plaintext can read a harness transcript through a @ref pointer, so view
	// shape changes (capacity, layers, keyword) must not re-read the same unit.
	lexMu    sync.Mutex
	lexKey   string
	lexCache map[string]*hydratedUnit
	lexOrder []string
}

// snapshotReuseWindow bounds how often a full engine projection rebuild can run.
// The vault changes continuously while a session is active; without a reuse
// window every auto-refresh would rescan the whole corpus and rebuild every
// cluster. A short window keeps the graph responsive and still converges after
// new writes on the next request past the window.
const snapshotReuseWindow = 30 * time.Second

// hydratedCacheLimit bounds the per-snapshot hydration cache. Every entry holds
// one unit's projected span and lexical aggregates (no source text), so the cap
// stays well under the graph body budget while covering full-domain views.
const hydratedCacheLimit = 4096

// NewBuilder returns a graph builder bound to one local vault.
func NewBuilder(vault *localvault.Vault) *Builder {
	return &Builder{vault: vault}
}

// Invalidate drops the cached snapshot (called on daemon cache invalidation).
func (b *Builder) Invalidate() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.snap != nil {
		b.snap.retire()
	}
	b.snap = nil
	b.key = ""
	b.mu.Unlock()
	b.viewMu.Lock()
	b.viewKey = ""
	b.viewAt = time.Time{}
	b.viewResp = nil
	b.viewMu.Unlock()
	b.lexMu.Lock()
	b.lexKey = ""
	b.lexCache = nil
	b.lexOrder = nil
	b.lexMu.Unlock()
}

// replaceSnapshotLocked installs a freshly built snapshot and retires the
// previous one; in-flight renders keep the old engine alive through their
// leases until they finish. Caller holds b.mu.
func (b *Builder) replaceSnapshotLocked(snap *Snapshot) {
	if b.snap != nil {
		b.snap.retire()
	}
	b.snap = snap
}

// Snapshot returns the cached snapshot, rebuilding it when the corpus
// generation or the vault metadata changed.
func (b *Builder) Snapshot(ctx context.Context) (*Snapshot, error) {
	if b == nil || b.vault == nil {
		return nil, fmt.Errorf("graph: nil builder")
	}
	b.mu.Lock()
	// Fast path: a just-built snapshot is served without rescanning the vault.
	// Auto-refresh during active writes would otherwise rebuild every few
	// seconds; one rebuild per window is enough for a local graph view.
	if b.snap != nil && time.Since(b.snap.BuiltAt) < snapshotReuseWindow {
		snap := b.snap
		b.mu.Unlock()
		return snap, nil
	}
	defer b.mu.Unlock()

	units, err := b.vault.GraphUnits(ctx)
	if err != nil {
		return nil, err
	}
	citations, err := b.vault.GraphCitations(ctx)
	if err != nil {
		return nil, err
	}
	if len(units) == 0 {
		key := snapshotKey(units, citations, 0)
		if b.snap != nil && b.key == key {
			// The verification pass just revalidated the cached projection;
			// renew the window so the next request can take the cheap path.
			b.snap.BuiltAt = time.Now()
			return b.snap, nil
		}
		b.replaceSnapshotLocked(&Snapshot{
			Key:            key,
			UnitByID:       map[string]*Unit{},
			Episodes:       map[int32]*Cluster{},
			EntityClusters: map[uint64]*Cluster{},
			SourceClusters: map[string]*Cluster{},
			ClusterByKey:   map[string]*Cluster{},
			Citations:      citations,
			CitedBy:        map[string][]string{},
			GroupEnergy:    map[uint64]float64{},
			TermGroup:      map[uint64]uint64{},
			BuiltAt:        time.Now(),
		})
		b.key = key
		return b.snap, nil
	}

	var engineGen uint64
	if err := b.vault.WithCoreEngine(ctx, func(eng *core.Engine) error {
		if eng != nil && eng.Index != nil {
			engineGen = eng.Index.StateGeneration()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	key := snapshotKey(units, citations, engineGen)
	if b.snap != nil && b.key == key {
		// The verification pass just revalidated the cached projection; renew
		// the window so the next request can take the cheap path.
		b.snap.BuiltAt = time.Now()
		return b.snap, nil
	}

	started := time.Now()
	snap, err := b.build(ctx, units, citations, key, engineGen)
	if err != nil {
		return nil, err
	}
	snap.BuildMS = time.Since(started).Milliseconds()
	b.replaceSnapshotLocked(snap)
	b.key = key
	return snap, nil
}

// Snapshot is one immutable engine projection.
type Snapshot struct {
	Key              string
	EngineGeneration uint64
	Units            []*Unit
	UnitByID         map[string]*Unit
	Episodes         map[int32]*Cluster
	EntityClusters   map[uint64]*Cluster
	SourceClusters   map[string]*Cluster
	ClusterByKey     map[string]*Cluster
	Citations        map[string][]string
	CitedBy          map[string][]string
	GroupEnergy      map[uint64]float64
	GroupMean        float64
	// TermGroup is the canonical word-family container per indexed term: the
	// minimum parent group observed across every unit, exactly the identity the
	// retrieval engine's cluster state uses. Building it once per snapshot keeps
	// per-view label projection O(1) per term instead of a full partner scan.
	TermGroup map[uint64]uint64
	BuiltAt   time.Time
	BuildMS   int64

	// engine is the immutable corpus engine this snapshot was projected from.
	// View renders share it through Engine(): the corpus lease (engRelease)
	// keeps the backing mmap alive until the owner retires the snapshot and
	// every active render returns its lease.
	engine     *core.Engine
	engMu      sync.Mutex
	engReaders int
	engClosed  bool
	engRelease func()
}

// Engine leases the cached engine for one render pass. The returned release
// func must be called when the pass ends; retirement cannot unmap the backing
// segment until every active lease is returned. ok is false when the snapshot
// carries no engine (empty vault) or was already retired.
func (s *Snapshot) Engine() (*core.Engine, func(), bool) {
	if s == nil || s.engine == nil {
		return nil, func() {}, false
	}
	s.engMu.Lock()
	if s.engClosed {
		s.engMu.Unlock()
		return nil, func() {}, false
	}
	s.engReaders++
	s.engMu.Unlock()
	return s.engine, s.releaseEngineReader, true
}

func (s *Snapshot) releaseEngineReader() {
	if s == nil {
		return
	}
	s.engMu.Lock()
	if s.engReaders > 0 {
		s.engReaders--
	}
	drop := s.engClosed && s.engReaders == 0
	release := s.engRelease
	s.engMu.Unlock()
	if drop && release != nil {
		release()
	}
}

// retire marks the snapshot dropped by its owner (builder replacement or
// invalidation). The corpus lease is released immediately when no render pass
// is active, otherwise when the last active pass returns its lease.
func (s *Snapshot) retire() {
	if s == nil {
		return
	}
	s.engMu.Lock()
	if s.engClosed {
		s.engMu.Unlock()
		return
	}
	s.engClosed = true
	drop := s.engReaders == 0
	release := s.engRelease
	s.engMu.Unlock()
	if drop && release != nil {
		release()
	}
}

// Unit is one memory record with its engine-derived grouping.
type Unit struct {
	Row              graphmodel.UnitRow
	EngineIndex      int32
	HasEngine        bool
	DocHash          uint64
	BlockHash        uint64
	GroupWeights     map[uint64]float64
	DominantGroup    uint64
	DominantWeight   float64
	EpisodeID        int32
	EntityClusterKey string
	SourceCluster    string
	PrimaryCluster   string
	Free             bool
	Summary          string
	Anchor           string
	Span             string
}

// Cluster is one graph container.
type Cluster struct {
	Key         string
	Type        string
	Label       string
	Summary     string
	Layer       string
	LayerMix    map[string]int
	MemberIDs   []string
	MemberCount int
	EnergyNats  float64
	Anchor      string
	Span        string
	Group       uint64
	RelPath     string
	EpisodeID   int32
}

func (b *Builder) build(ctx context.Context, rows []graphmodel.UnitRow, citations map[string][]string, key string, engineGen uint64) (*Snapshot, error) {
	snap := &Snapshot{
		Key:              key,
		EngineGeneration: engineGen,
		UnitByID:         make(map[string]*Unit, len(rows)),
		Episodes:         make(map[int32]*Cluster),
		EntityClusters:   make(map[uint64]*Cluster),
		SourceClusters:   make(map[string]*Cluster),
		ClusterByKey:     make(map[string]*Cluster),
		Citations:        citations,
		CitedBy:          make(map[string][]string, len(citations)),
		GroupEnergy:      make(map[uint64]float64, 1024),
		TermGroup:        make(map[uint64]uint64, 1<<16),
		BuiltAt:          time.Now(),
	}
	for noeticID, cited := range citations {
		for _, id := range cited {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			snap.CitedBy[id] = append(snap.CitedBy[id], noeticID)
		}
	}
	for id := range snap.CitedBy {
		sort.Strings(snap.CitedBy[id])
	}

	// The projection caches the engine pointer across view renders, so it
	// leases the corpus snapshot for the snapshot's lifetime instead of the
	// callback-scoped WithCoreEngine lease; Snapshot.retire returns it.
	eng, release, err := b.vault.AcquireCoreEngine(ctx)
	if err != nil {
		return nil, err
	}
	buildErr := func() error {
		snap.engine = eng
		snap.engRelease = release
		units := make([]*Unit, 0, len(rows))
		for i := range rows {
			row := rows[i]
			u := &Unit{
				Row:          row,
				EngineIndex:  -1,
				EpisodeID:    -1,
				GroupWeights: map[uint64]float64{},
				Summary:      unitSummary(row),
				Anchor:       unitAnchor(row),
				Span:         unitSpan(row),
			}
			if idx, ok := eng.Index.LookupUnit(row.UnitID); ok {
				u.EngineIndex = idx
				u.HasEngine = true
				if gv, ok := eng.UnitGroupVector(row.UnitID); ok {
					for _, entry := range gv.Entries {
						if entry.ID == 0 || entry.WeightNats <= 0 {
							continue
						}
						u.GroupWeights[entry.ID] += entry.WeightNats
					}
				}
				u.EpisodeID = eng.EpisodeIDOf(idx)
				if int(idx) >= 0 && int(idx) < len(eng.Index.UnitMeta) {
					meta := eng.Index.UnitMeta[idx]
					u.DocHash = meta.DocHash
					u.BlockHash = meta.BlockHash
				}
			}
			for group, weight := range u.GroupWeights {
				snap.GroupEnergy[group] += weight
			}
			units = append(units, u)
		}
		sort.Slice(units, func(i, j int) bool { return units[i].Row.UnitID < units[j].Row.UnitID })

		if len(snap.GroupEnergy) > 0 {
			var sum float64
			for _, mass := range snap.GroupEnergy {
				sum += mass
			}
			snap.GroupMean = sum / float64(len(snap.GroupEnergy))
		}

		for _, u := range units {
			snap.UnitByID[u.Row.UnitID] = u
			u.DominantGroup, u.DominantWeight = dominantGroup(u.GroupWeights)
			if u.DocHash != 0 {
				u.SourceCluster = sourceKey(u.DocHash)
			}
			if u.DominantGroup != 0 && snap.GroupEnergy[u.DominantGroup] >= snap.GroupMean {
				u.EntityClusterKey = entityKey(u.DominantGroup)
			}
		}

		// Time clusters: engine episode rosters, restricted to graph units.
		rosters := eng.EpisodeRosters()
		for epID, roster := range rosters {
			if len(roster) == 0 {
				continue
			}
			var members []string
			for _, idx := range roster {
				if int(idx) < 0 || int(idx) >= len(eng.Index.UnitIDs) {
					continue
				}
				id := eng.Index.UnitIDs[idx]
				if _, ok := snap.UnitByID[id]; ok {
					members = append(members, id)
				}
			}
			if len(members) == 0 {
				continue
			}
			c := &Cluster{
				Key:         episodeKey(int32(epID)),
				Type:        graphmodel.ClusterEpisode,
				LayerMix:    map[string]int{},
				MemberIDs:   members,
				MemberCount: len(members),
				Anchor:      episodeKey(int32(epID)),
				EpisodeID:   int32(epID),
			}
			c.LayerMix = layerMixOf(snap, members)
			c.Layer = dominantLayer(c.LayerMix)
			c.Label, c.Summary = episodeLabel(snap, members)
			c.Span = fmt.Sprintf("时间簇 · %d 个单元", len(members))
			if profile, ok := eng.EpisodeEnergyVector(int32(epID)); ok {
				c.EnergyNats = profile.ContractionNats
			}
			snap.Episodes[int32(epID)] = c
		}

		// Entity clusters: dominant word-family containers above the mean.
		for _, u := range units {
			if u.EntityClusterKey == "" {
				continue
			}
			c := snap.EntityClusters[u.DominantGroup]
			if c == nil {
				c = &Cluster{
					Key:        u.EntityClusterKey,
					Type:       graphmodel.ClusterEntity,
					LayerMix:   map[string]int{},
					Group:      u.DominantGroup,
					Anchor:     u.EntityClusterKey,
					EnergyNats: snap.GroupEnergy[u.DominantGroup],
				}
				snap.EntityClusters[u.DominantGroup] = c
			}
			c.MemberIDs = append(c.MemberIDs, u.Row.UnitID)
		}
		for group, c := range snap.EntityClusters {
			sort.Strings(c.MemberIDs)
			c.MemberCount = len(c.MemberIDs)
			c.LayerMix = layerMixOf(snap, c.MemberIDs)
			c.Layer = dominantLayer(c.LayerMix)
			c.Label, c.Summary = entityLabel(group, c.MemberCount)
			c.Span = fmt.Sprintf("实体簇 · %d 个单元", c.MemberCount)
		}

		// Source clusters: units sharing one engine document provenance
		// (DocHash). The physical shape is irrelevant — a multi-chunk file, a
		// PDF, an email thread or any other chunked source groups identically.
		for _, u := range units {
			if u.SourceCluster == "" {
				continue
			}
			c := snap.SourceClusters[u.SourceCluster]
			if c == nil {
				label := ""
				if u.Row.Source == graphmodel.SourceBG {
					label = u.Row.RelPath
				}
				c = &Cluster{
					Key:      u.SourceCluster,
					Type:     graphmodel.ClusterSource,
					LayerMix: map[string]int{},
					RelPath:  label,
					Anchor:   u.SourceCluster,
				}
				snap.SourceClusters[u.SourceCluster] = c
			}
			c.MemberIDs = append(c.MemberIDs, u.Row.UnitID)
		}
		for _, c := range snap.SourceClusters {
			sort.Strings(c.MemberIDs)
			c.MemberCount = len(c.MemberIDs)
			c.LayerMix = layerMixOf(snap, c.MemberIDs)
			c.Layer = dominantLayer(c.LayerMix)
			if c.RelPath == "" {
				sourceID := strings.TrimPrefix(c.Key, "cluster:source:")
				if len(sourceID) > 8 {
					sourceID = sourceID[:8]
				}
				c.RelPath = "来源 #" + sourceID
			}
			c.Label = c.RelPath
			c.Summary = fmt.Sprintf("来源簇 · %s · %d 个单元", c.RelPath, c.MemberCount)
			c.Span = c.Summary
		}

		// Free units: no time/document/entity container and no citation either way.
		for _, c := range snap.Episodes {
			snap.ClusterByKey[c.Key] = c
		}
		for _, c := range snap.EntityClusters {
			snap.ClusterByKey[c.Key] = c
		}
		for _, c := range snap.SourceClusters {
			snap.ClusterByKey[c.Key] = c
		}
		for _, u := range units {
			u.PrimaryCluster = ""
			if ep := snap.Episodes[u.EpisodeID]; u.EpisodeID >= 0 && ep != nil && ep.MemberCount > 1 {
				u.PrimaryCluster = ep.Key
			}
			if u.PrimaryCluster == "" && u.SourceCluster != "" {
				u.PrimaryCluster = u.SourceCluster
			}
			if u.PrimaryCluster == "" && u.EntityClusterKey != "" {
				u.PrimaryCluster = u.EntityClusterKey
			}
			u.Free = u.PrimaryCluster == "" &&
				len(snap.Citations[u.Row.UnitID]) == 0 &&
				len(snap.CitedBy[u.Row.UnitID]) == 0
		}

		snap.Units = units

		// Canonical term → word-family container for the view projection. The
		// engine's cluster state resolves a term's parent as the minimum group
		// observed across its document entries; reproducing that pass once per
		// snapshot keeps label projection O(1) per term instead of issuing a
		// full energy-profile partner scan for every uncached term.
		for u := 0; u < eng.Index.N; u++ {
			eng.ForEachDocEntry(u, func(entry core.DocEntryView) {
				if entry.ID == 0 {
					return
				}
				group := entry.Group
				if group == 0 {
					group = entry.ID
				}
				if prev, ok := snap.TermGroup[entry.ID]; !ok || group < prev {
					snap.TermGroup[entry.ID] = group
				}
			})
		}
		return nil
	}()
	if buildErr != nil {
		snap.retire()
		return nil, buildErr
	}
	return snap, nil
}

func snapshotKey(rows []graphmodel.UnitRow, citations map[string][]string, engineGen uint64) string {
	var maxOcc, maxIns int64
	for _, r := range rows {
		if r.OccurredAt > maxOcc {
			maxOcc = r.OccurredAt
		}
		if r.InsertedAt > maxIns {
			maxIns = r.InsertedAt
		}
	}
	var citationLines []string
	for id, cited := range citations {
		citationLines = append(citationLines, id+":"+strings.Join(cited, ","))
	}
	sort.Strings(citationLines)
	sum := sha256.Sum256([]byte(strings.Join(citationLines, "|")))
	return fmt.Sprintf("gen=%d|rows=%d|occ=%d|ins=%d|cites=%x", engineGen, len(rows), maxOcc, maxIns, sum[:8])
}

func dominantGroup(weights map[uint64]float64) (uint64, float64) {
	var best uint64
	var bestWeight float64
	for group, weight := range weights {
		if weight > bestWeight || (weight == bestWeight && group != 0 && (best == 0 || group < best)) {
			best = group
			bestWeight = weight
		}
	}
	return best, bestWeight
}

func layerMixOf(snap *Snapshot, ids []string) map[string]int {
	mix := map[string]int{}
	for _, id := range ids {
		if u := snap.UnitByID[id]; u != nil {
			mix[u.Row.Layer]++
		}
	}
	return mix
}

func dominantLayer(mix map[string]int) string {
	order := []string{localvault.LayerBackground, localvault.LayerEpisodic, localvault.LayerNoetic}
	best := localvault.LayerEpisodic
	bestCount := -1
	for _, layer := range order {
		if mix[layer] > bestCount {
			best = layer
			bestCount = mix[layer]
		}
	}
	return best
}

func unitAnchor(row graphmodel.UnitRow) string {
	if addr := strings.TrimSpace(row.AddressURI); addr != "" {
		return addr
	}
	return row.UnitID
}

func unitSummary(row graphmodel.UnitRow) string {
	switch row.Source {
	case graphmodel.SourceNoetic:
		if q := strings.TrimSpace(row.QueryText); q != "" {
			return clipText("Q: "+q, 54)
		}
	case graphmodel.SourceBG:
		if row.RelPath != "" {
			return clipText(row.RelPath+" #"+row.Anchor, 54)
		}
	}
	if strings.HasPrefix(row.RefPointer, "@ref:") {
		return clipText(row.RefPointer, 54)
	}
	return clipText(row.UnitID, 54)
}

func unitSpan(row graphmodel.UnitRow) string {
	switch row.Source {
	case graphmodel.SourceNoetic:
		q := strings.TrimSpace(row.QueryText)
		a := strings.TrimSpace(row.AnswerText)
		if q == "" && a == "" {
			return ""
		}
		return clipText("Q: "+q+"\nA: "+a, 4000)
	case graphmodel.SourceBG:
		if row.RelPath == "" && row.AbsPath == "" {
			return ""
		}
		return clipText(row.RelPath+"#"+row.Anchor+"\n"+row.AbsPath, 4000)
	}
	if strings.HasPrefix(row.RefPointer, "@ref:") {
		return row.RefPointer
	}
	return ""
}

func episodeKey(id int32) string {
	return fmt.Sprintf("cluster:episode:%d", id)
}

func entityKey(group uint64) string {
	return "cluster:entity:" + hex16(group)
}

func sourceKey(docHash uint64) string {
	return "cluster:source:" + hex16(docHash)
}

func episodeLabel(snap *Snapshot, ids []string) (string, string) {
	var minTS, maxTS int64
	for _, id := range ids {
		u := snap.UnitByID[id]
		if u == nil {
			continue
		}
		ts := u.Row.OccurredAt
		if ts <= 0 {
			continue
		}
		if minTS == 0 || ts < minTS {
			minTS = ts
		}
		if ts > maxTS {
			maxTS = ts
		}
	}
	if minTS == 0 {
		label := fmt.Sprintf("时间簇 · %d 个单元", len(ids))
		return label, label
	}
	start := time.Unix(minTS, 0).UTC().Format("2006-01-02 15:04")
	end := time.Unix(maxTS, 0).UTC().Format("2006-01-02 15:04")
	label := start
	if end != start {
		label = start + " ~ " + end
	}
	return label, fmt.Sprintf("时间簇 · %s · %d 个单元", label, len(ids))
}

func entityLabel(group uint64, members int) (string, string) {
	label := "词族 " + hex16(group)
	return label, fmt.Sprintf("实体簇 · %s · %d 个单元", label, members)
}

func hex16(v uint64) string {
	return fmt.Sprintf("%016x", v)
}

func clipText(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
