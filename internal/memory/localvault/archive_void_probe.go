// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"strings"

	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

// ArchiveVoidProbe 返回查询相对全档倒排的确定性空洞凭证（C1/C2/C3）与整查询
// 完备度报告。凭证是关于「表面词形零记载」的精确事实（全档穷举、非候选集
// 抽样、非语义判断），供交付面在证据不足时据实拒答；它们只作事实陈述，
// 不触发任何截断或能量修改。只读：不写库、不改变排序。
func (v *Vault) ArchiveVoidProbe(text string) ([]plain.VoidCertificate, plain.CoverageReport, bool) {
	if v == nil || v.db == nil || strings.TrimSpace(text) == "" {
		return nil, plain.CoverageReport{}, false
	}
	snap, release, err := v.acquireCorpusSnapshot(context.Background())
	if err != nil {
		return nil, plain.CoverageReport{}, false
	}
	if release != nil {
		defer release()
	}
	if snap == nil || len(snap.docs) == 0 {
		return nil, plain.CoverageReport{}, false
	}
	eng := snap.ensureSnapshotCoreEngine(v)
	if eng == nil || eng.Index == nil {
		return nil, plain.CoverageReport{}, false
	}
	te := v.newVaultTermExtractor()
	snap.coreMu.Lock()
	defer snap.coreMu.Unlock()
	certs := te.ArchiveVoidProbe(eng.Index, text)
	report := te.QueryCoverage(eng.Index, text, nil)
	return certs, report, true
}
