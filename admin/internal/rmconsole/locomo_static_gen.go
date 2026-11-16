// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// embeddedLocomoSnapshotGZ 内嵌预置的 LoCoMo 3D 图谱静态快照（gzip 形态随源码分发，
// 加载时解压；解压后的载荷与原 JSON 全文逐字节一致）：
// 确保在用户安装态、离线分发包或非源码工作目录下无需外部文件即可 100% 渲染预置演示图谱。
// 以压缩形态入库是导出面「单文件 8MiB」体积预算的要求。
//
//go:embed testdata/locomo_home_snapshot.json.gz
var embeddedLocomoSnapshotGZ []byte

func embeddedLocomoSnapshotJSON() []byte {
	if len(embeddedLocomoSnapshotGZ) == 0 {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(embeddedLocomoSnapshotGZ))
	if err != nil {
		return nil
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	return raw
}

// locomoSkipSnapshotOnLoad 在 GenerateLocomoStaticSnapshot 重建快照期间置 true，
// 防止重生成时误读编译期内嵌的旧快照。
var locomoSkipSnapshotOnLoad atomic.Bool

// LocomoDataDump 用于固化首屏 3D 图谱所需的完整计算状态，无需连接 RB 后端即可本地展示与召回。
type LocomoDataDump struct {
	Fingerprint string                      `json:"fingerprint"`
	ActiveScope string                      `json:"active_scope"`
	Units       map[string]locomoVaultEntry `json:"units"`
	Nodes       []GraphNodeItem             `json:"nodes"`
	Edges       []GraphEdgeItem             `json:"edges"`
	Questions   []LocomoPublicQuestion      `json:"questions"`
	AtomTotal   int                         `json:"atom_total"`
	MolTotal    int                         `json:"mol_total"`
	DimerTotal  int                         `json:"dimer_total"`
	TopAtoms    []GraphAtomParticle         `json:"top_atoms"`
	TopMols     []GraphMoleculeFragment     `json:"top_mols"`
}

func resolveLocomoSnapshotPaths(home string) []string {
	var paths []string
	if wd, err := os.Getwd(); err == nil {
		paths = append(paths,
			filepath.Join(wd, "internal", "rmconsole", "testdata", "locomo_home_snapshot.json"),
			filepath.Join(wd, "testdata", "locomo_home_snapshot.json"),
			filepath.Join(wd, "admin", "internal", "rmconsole", "testdata", "locomo_home_snapshot.json"),
		)
	}
	if home != "" {
		paths = append(paths,
			filepath.Join(home, "Rolling-Memory", "admin", "internal", "rmconsole", "testdata", "locomo_home_snapshot.json"),
			filepath.Join(home, ".local", "state", "rolling-memory", "eval", "locomo-static-snapshot.json"),
		)
	}
	return paths
}

// GenerateLocomoStaticSnapshot 构建并持久化首屏快照（同时落盘至 testdata 与本地 state）。
func GenerateLocomoStaticSnapshot() error {
	home, _ := os.UserHomeDir()
	// 生成快照前先移除旧快照，确保从真实语料完整构建
	stateOut := filepath.Join(home, ".local", "state", "rolling-memory", "eval", "locomo-static-snapshot.json")
	repoOut := filepath.Join(home, "Rolling-Memory", "admin", "internal", "rmconsole", "testdata", "locomo_home_snapshot.json.gz")
	_ = os.Remove(stateOut)
	_ = os.Remove(repoOut)
	InvalidateLocomoPublicCorpusCache()
	locomoSkipSnapshotOnLoad.Store(true)
	defer locomoSkipSnapshotOnLoad.Store(false)

	vaultPath := resolveLocomoVaultPath(home)
	fp := computeLocomoCorpusFingerprint(home, vaultPath)

	corpus, err := loadLocomoCorpus()
	if err != nil {
		return err
	}

	var nodes []GraphNodeItem
	for _, k := range corpus.keyOrder {
		if n := corpus.nodesByKey[k]; n != nil {
			nodes = append(nodes, *n)
		}
	}

	dump := LocomoDataDump{
		Fingerprint: fp,
		ActiveScope: corpus.activeScope,
		Units:       corpus.units,
		Nodes:       nodes,
		Edges:       corpus.rawEdges,
		Questions:   corpus.questions,
		AtomTotal:   corpus.atomTotal,
		MolTotal:    corpus.molTotal,
		DimerTotal:  corpus.dimerTotal,
		TopAtoms:    corpus.topAtoms,
		TopMols:     corpus.topMols,
	}

	b, err := json.Marshal(dump)
	if err != nil {
		return err
	}

	_ = os.MkdirAll(filepath.Dir(stateOut), 0o755)
	if err := os.WriteFile(stateOut, b, 0o644); err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(repoOut), 0o755)
	if f, err := os.Create(repoOut); err == nil {
		zw := gzip.NewWriter(f)
		_, _ = zw.Write(b)
		_ = zw.Close()
		_ = f.Close()
	}
	return nil
}

func buildCorpusFromSnapshot(dump *LocomoDataDump) *locomoCachedCorpus {
	units := dump.Units
	if units == nil {
		units = make(map[string]locomoVaultEntry)
	}
	nodesByKey := make(map[string]*GraphNodeItem, len(dump.Nodes))
	keyOrder := make([]string, 0, len(dump.Nodes))
	unitOrder := make([]string, 0, len(units))

	for i := range dump.Nodes {
		n := dump.Nodes[i]
		nodesByKey[n.NodeKey] = &n
		keyOrder = append(keyOrder, n.NodeKey)
		if n.Kind == "unit" && n.Layer != "noetic" {
			uid := strings.TrimPrefix(n.NodeKey, "unit:")
			if _, ok := units[uid]; !ok {
				units[uid] = locomoVaultEntry{
					DocID:      uid,
					RawText:    n.Span,
					AddressURI: n.Anchor,
				}
			}
		}
	}
	for k := range units {
		unitOrder = append(unitOrder, k)
	}
	sort.Strings(unitOrder)

	cellMembers := make(map[string][]*GraphNodeItem)
	unitCells := make(map[string][]string)
	citesTargets := make(map[string][]string)
	seenPair := make(map[string]bool)

	for _, e := range dump.Edges {
		switch e.Relation {
		case "member_of":
			uNode := nodesByKey[e.FromKey]
			cNode := nodesByKey[e.ToKey]
			if uNode != nil && uNode.Kind == "unit" && cNode != nil && cNode.Kind == "cell" {
				cellMembers[cNode.NodeKey] = append(cellMembers[cNode.NodeKey], uNode)
				pk := uNode.NodeKey + "|" + cNode.NodeKey
				if !seenPair[pk] {
					seenPair[pk] = true
					unitCells[uNode.NodeKey] = append(unitCells[uNode.NodeKey], cNode.NodeKey)
				}
			}
		case "cites":
			citesTargets[e.FromKey] = append(citesTargets[e.FromKey], e.ToKey)
		}
	}

	atomTotal, molTotal, dimerTotal := dump.AtomTotal, dump.MolTotal, dump.DimerTotal
	topAtoms, topMols := dump.TopAtoms, dump.TopMols
	if atomTotal == 0 && molTotal == 0 {
		atomTotal, molTotal, dimerTotal, topAtoms, topMols = enrichGraphWithAtomsAndMolecules(nodesByKey, keyOrder, cellMembers, unitCells)
	}

	return &locomoCachedCorpus{
		loadedAt:     time.Now(),
		fingerprint:  dump.Fingerprint,
		activeScope:  dump.ActiveScope,
		units:        units,
		unitOrder:    unitOrder,
		questions:    dump.Questions,
		nodesByKey:   nodesByKey,
		keyOrder:     keyOrder,
		rawEdges:     dump.Edges,
		unitCells:    unitCells,
		cellMembers:  cellMembers,
		citesTargets: citesTargets,
		totalCells:   len(cellMembers),
		atomTotal:    atomTotal,
		molTotal:     molTotal,
		dimerTotal:   dimerTotal,
		topAtoms:     topAtoms,
		topMols:      topMols,
	}
}
